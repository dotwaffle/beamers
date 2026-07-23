package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/display"
	"github.com/dotwaffle/beamers/ent/displayassignment"
	"github.com/dotwaffle/beamers/ent/displaycredential"
	"github.com/dotwaffle/beamers/ent/displayenrollment"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/location"
	"github.com/dotwaffle/beamers/ent/locationpublishedversion"
	"github.com/dotwaffle/beamers/ent/rundown"
)

var (
	// ErrDisplayEnrollmentConflict means generated enrollment material collided.
	ErrDisplayEnrollmentConflict = errors.New("Display Enrollment credential conflict")
	// ErrDisplayEnrollmentUnavailable means a claim code is unknown, expired, or used.
	ErrDisplayEnrollmentUnavailable = errors.New("Display Enrollment is unavailable")
	// ErrDisplayCredential means a token does not identify an enrolled Display.
	ErrDisplayCredential = errors.New("Display authentication required")
	// ErrDisplayNotFound means Assignment targeted no enrolled Display.
	ErrDisplayNotFound = errors.New("Display not found")
	// ErrDisplayAssignmentReference means Event or Location routing is invalid.
	ErrDisplayAssignmentReference = errors.New("invalid Display Assignment reference")
)

// DisplayEnrollmentParams contains hashed short-lived enrollment material.
type DisplayEnrollmentParams struct {
	CodeHash       string
	CredentialHash string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

// Display is the durable projection of one enrolled screen identity.
type Display struct {
	ID         int       `json:"id"`
	Name       string    `json:"name"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

// DisplayAssignment is one Event-specific normal route.
type DisplayAssignment struct {
	DisplayID        int      `json:"display_id"`
	EventID          int      `json:"event_id"`
	LocationID       int      `json:"location_id"`
	ViewKey          string   `json:"view_key"`
	DisplayGroupKeys []string `json:"display_group_keys,omitempty"`
}

// DisplayStatus is one crew-visible current routing summary.
type DisplayStatus struct {
	ID                                   int
	Name                                 string
	ActiveEventID                        int
	ActivationGeneration                 int
	PublishedRevision                    int
	Standby                              bool
	EventName                            string
	LocationID                           int
	LocationName                         string
	ViewKey                              string
	ProgramChannelID                     int
	AppliedProtocolVersion               string
	AppliedAssetVersion                  string
	AppliedStreamID                      string
	AppliedStreamPosition                int64
	AppliedActiveEventID                 int
	AppliedActivationGeneration          int
	AppliedPublishedRevision             int
	AppliedStageMessageID                int
	AppliedStageMessageRevision          int
	AppliedTechnicalDifficultiesID       int
	AppliedTechnicalDifficultiesRevision int
	AppliedStandby                       bool
	AppliedAt                            *time.Time
	ClockOffsetMilliseconds              int64
	ClockUncertaintyMilliseconds         int64
	RendererUnstable                     bool
}

// IssueDisplayEnrollment stores one short-lived, single-use enrollment offer.
func (installation *SQLite) IssueDisplayEnrollment(
	ctx context.Context,
	params DisplayEnrollmentParams,
) error {
	_, err := installation.client.DisplayEnrollment.Create().
		SetCodeHash(params.CodeHash).
		SetCredentialHash(params.CredentialHash).
		SetCreatedAt(params.CreatedAt).
		SetExpiresAt(params.ExpiresAt).
		Save(systemContext(ctx))
	if ent.IsConstraintError(err) {
		return ErrDisplayEnrollmentConflict
	}
	if err != nil {
		return opaqueError("issue Display Enrollment", err)
	}
	return nil
}

// ClaimDisplayEnrollment consumes one code and creates its Display identity and credential.
func (transaction *CommandTx) ClaimDisplayEnrollment(
	ctx context.Context,
	codeHash string,
	name string,
	now time.Time,
) (Display, error) {
	internalContext := systemContext(ctx)
	enrollment, err := transaction.transaction.DisplayEnrollment.Query().Where(
		displayenrollment.CodeHashEQ(codeHash),
		displayenrollment.UsedAtIsNil(),
		displayenrollment.ExpiresAtGT(now),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return Display{}, ErrDisplayEnrollmentUnavailable
	}
	if err != nil {
		return Display{}, opaqueError("load Display Enrollment claim", err)
	}
	created, err := transaction.transaction.Display.Create().
		SetName(name).
		SetCreatedAt(now).
		SetEnrolledAt(now).
		Save(internalContext)
	if err != nil {
		return Display{}, opaqueError("create enrolled Display", err)
	}
	if _, credentialErr := transaction.transaction.DisplayCredential.Create().
		SetDisplayID(created.ID).
		SetTokenHash(enrollment.CredentialHash).
		SetCreatedAt(now).
		Save(internalContext); credentialErr != nil {
		return Display{}, opaqueError("create Display credential", credentialErr)
	}
	updated, err := transaction.transaction.DisplayEnrollment.Update().Where(
		displayenrollment.IDEQ(enrollment.ID),
		displayenrollment.UsedAtIsNil(),
	).SetUsedAt(now).Save(internalContext)
	if err != nil {
		return Display{}, opaqueError("consume Display Enrollment", err)
	}
	if updated != 1 {
		return Display{}, ErrDisplayEnrollmentUnavailable
	}
	return Display{ID: created.ID, Name: created.Name, EnrolledAt: created.EnrolledAt}, nil
}

// FindDisplayByCredential authenticates one persistent Display token hash.
func (installation *SQLite) FindDisplayByCredential(
	ctx context.Context,
	tokenHash string,
) (Display, error) {
	credential, err := installation.client.DisplayCredential.Query().Where(
		displaycredential.TokenHashEQ(tokenHash),
		displaycredential.RevokedAtIsNil(),
	).WithDisplay().Only(systemContext(ctx))
	if ent.IsNotFound(err) {
		return Display{}, ErrDisplayCredential
	}
	if err != nil {
		return Display{}, opaqueError("authenticate Display credential", err)
	}
	found := credential.Edges.Display
	if found == nil {
		return Display{}, opaqueError("load Display credential owner", errors.New("missing Display"))
	}
	return Display{ID: found.ID, Name: found.Name, EnrolledAt: found.EnrolledAt}, nil
}

// AssignDisplay creates or replaces one Event-specific Assignment.
func (transaction *CommandTx) AssignDisplay(
	ctx context.Context,
	assignment DisplayAssignment,
	now time.Time,
) (DisplayAssignment, error) {
	internalContext := systemContext(ctx)
	if exists, err := transaction.transaction.Display.Query().Where(
		display.IDEQ(assignment.DisplayID),
	).Exist(internalContext); err != nil {
		return DisplayAssignment{}, opaqueError("find Display for Assignment", err)
	} else if !exists {
		return DisplayAssignment{}, ErrDisplayNotFound
	}
	if exists, err := transaction.transaction.Event.Query().Where(
		event.IDEQ(assignment.EventID),
	).Exist(internalContext); err != nil {
		return DisplayAssignment{}, opaqueError("find Event for Display Assignment", err)
	} else if !exists {
		return DisplayAssignment{}, ErrDisplayAssignmentReference
	}
	if exists, err := transaction.transaction.Location.Query().Where(
		location.IDEQ(assignment.LocationID),
		location.EventIDEQ(assignment.EventID),
	).Exist(internalContext); err != nil {
		return DisplayAssignment{}, opaqueError("find Location for Display Assignment", err)
	} else if !exists {
		return DisplayAssignment{}, ErrDisplayAssignmentReference
	}
	published, err := transaction.transaction.LocationPublishedVersion.Query().Where(
		locationpublishedversion.LocationIDEQ(assignment.LocationID),
	).Order(ent.Desc(locationpublishedversion.FieldPublishedRevision)).First(internalContext)
	if ent.IsNotFound(err) || err == nil && published.Retired {
		return DisplayAssignment{}, ErrDisplayAssignmentReference
	}
	if err != nil {
		return DisplayAssignment{}, opaqueError("load Published Location for Display Assignment", err)
	}
	existing, err := transaction.transaction.DisplayAssignment.Query().Where(
		displayassignment.DisplayIDEQ(assignment.DisplayID),
		displayassignment.EventIDEQ(assignment.EventID),
	).Only(internalContext)
	switch {
	case ent.IsNotFound(err):
		_, err = transaction.transaction.DisplayAssignment.Create().
			SetDisplayID(assignment.DisplayID).
			SetEventID(assignment.EventID).
			SetLocationID(assignment.LocationID).
			SetViewKey(assignment.ViewKey).
			SetDisplayGroupKeys(assignment.DisplayGroupKeys).
			SetCreatedAt(now).
			SetUpdatedAt(now).
			Save(internalContext)
	case err == nil:
		_, err = transaction.transaction.DisplayAssignment.UpdateOneID(existing.ID).
			SetLocationID(assignment.LocationID).
			SetViewKey(assignment.ViewKey).
			SetDisplayGroupKeys(assignment.DisplayGroupKeys).
			SetUpdatedAt(now).
			Save(internalContext)
	}
	if err != nil {
		return DisplayAssignment{}, opaqueError("save Display Assignment", err)
	}
	if syncErr := transaction.syncDisplayOverridesForAssignment(ctx, assignment, now); syncErr != nil {
		return DisplayAssignment{}, syncErr
	}
	return assignment, nil
}

// LoadDisplayStatus returns one Display's Assignment for only the Active Event.
func (installation *SQLite) LoadDisplayStatus(
	ctx context.Context,
	displayID int,
) (DisplayStatus, error) {
	internalContext := systemContext(ctx)
	found, err := installation.client.Display.Get(internalContext, displayID)
	if ent.IsNotFound(err) {
		return DisplayStatus{}, ErrDisplayNotFound
	}
	if err != nil {
		return DisplayStatus{}, opaqueError("load Display status", err)
	}
	routing, err := loadDisplayRouting(internalContext, installation.client)
	if err != nil {
		return DisplayStatus{}, err
	}
	return loadDisplayStatus(internalContext, installation.client, found, routing)
}

// ListDisplayStatuses returns one snapshot's Active Event and crew-visible Assignment summaries.
func (installation *SQLite) ListDisplayStatuses(ctx context.Context) (int, []DisplayStatus, error) {
	internalContext := systemContext(ctx)
	transaction, err := installation.client.Tx(internalContext)
	if err != nil {
		return 0, nil, opaqueError("begin Display status snapshot", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	client := transaction.Client()
	routing, err := loadDisplayRouting(internalContext, client)
	if err != nil {
		return 0, nil, err
	}
	found, err := client.Display.Query().Order(ent.Asc(display.FieldID)).All(internalContext)
	if err != nil {
		return 0, nil, opaqueError("list Displays", err)
	}
	result := make([]DisplayStatus, 0, len(found))
	for _, item := range found {
		status, statusErr := loadDisplayStatus(internalContext, client, item, routing)
		if statusErr != nil {
			return 0, nil, statusErr
		}
		result = append(result, status)
	}
	return routing.ActiveEventID, result, nil
}

type displayRouting struct {
	ActiveEventID        int
	EventName            string
	ActivationGeneration int
	PublishedRevision    int
}

func loadDisplayRouting(ctx context.Context, client *ent.Client) (displayRouting, error) {
	routing, err := client.Installation.Query().Only(ctx)
	if err != nil {
		return displayRouting{}, opaqueError("load Active Event for Display", err)
	}
	result := displayRouting{ActivationGeneration: routing.ActivationGeneration}
	if routing.ActiveEventID == nil {
		return result, nil
	}
	activeEvent, err := client.Event.Get(ctx, *routing.ActiveEventID)
	if err != nil {
		return displayRouting{}, opaqueError("load Active Event Display projection", err)
	}
	activeRundown, err := client.Rundown.Query().Where(
		rundown.EventIDEQ(activeEvent.ID),
	).Only(ctx)
	if err != nil {
		return displayRouting{}, opaqueError("load Active Event Rundown for Display", err)
	}
	result.ActiveEventID = activeEvent.ID
	result.EventName = activeEvent.Name
	result.PublishedRevision = activeRundown.PublishedRevision
	return result, nil
}

func loadDisplayStatus(
	ctx context.Context,
	client *ent.Client,
	found *ent.Display,
	routing displayRouting,
) (DisplayStatus, error) {
	status := DisplayStatus{
		ID: found.ID, Name: found.Name, Standby: true,
		ActiveEventID: routing.ActiveEventID, EventName: routing.EventName,
		ActivationGeneration:                 routing.ActivationGeneration,
		PublishedRevision:                    routing.PublishedRevision,
		AppliedProtocolVersion:               found.AppliedProtocolVersion,
		AppliedAssetVersion:                  found.AppliedAssetVersion,
		AppliedStreamID:                      found.AppliedStreamID,
		AppliedStreamPosition:                found.AppliedStreamPosition,
		AppliedActiveEventID:                 found.AppliedActiveEventID,
		AppliedActivationGeneration:          found.AppliedActivationGeneration,
		AppliedPublishedRevision:             found.AppliedPublishedRevision,
		AppliedStageMessageID:                found.AppliedStageMessageID,
		AppliedStageMessageRevision:          found.AppliedStageMessageRevision,
		AppliedTechnicalDifficultiesID:       found.AppliedTechnicalDifficultiesID,
		AppliedTechnicalDifficultiesRevision: found.AppliedTechnicalDifficultiesRevision,
		AppliedStandby:                       found.AppliedStandby,
		AppliedAt:                            found.AppliedAt,
		ClockOffsetMilliseconds:              found.ClockOffsetMilliseconds,
		ClockUncertaintyMilliseconds:         found.ClockUncertaintyMilliseconds,
		RendererUnstable:                     found.RendererUnstable,
	}
	if routing.ActiveEventID == 0 {
		return status, nil
	}
	assignment, err := client.DisplayAssignment.Query().Where(
		displayassignment.DisplayIDEQ(found.ID),
		displayassignment.EventIDEQ(routing.ActiveEventID),
	).WithLocation().Only(ctx)
	if ent.IsNotFound(err) {
		return status, nil
	}
	if err != nil {
		return DisplayStatus{}, opaqueError("load Active Event Display Assignment", err)
	}
	assignedLocation := assignment.Edges.Location
	if assignedLocation == nil {
		return DisplayStatus{}, opaqueError("load Display Assignment Location", errors.New("missing Location"))
	}
	published, err := assignedLocation.QueryPublishedVersions().
		Order(ent.Desc(locationpublishedversion.FieldPublishedRevision)).
		First(ctx)
	if err != nil {
		return DisplayStatus{}, opaqueError("load Published Display Assignment Location name", err)
	}
	if published.Retired {
		return status, nil
	}
	status.Standby = false
	status.LocationID = assignment.LocationID
	status.LocationName = published.Name
	status.ViewKey = assignment.ViewKey
	if status.ViewKey == "competition-output" {
		status.ProgramChannelID, err = competitionOutputProgramChannelID(
			ctx, client, routing.ActiveEventID, assignment.LocationID,
		)
		if err != nil {
			return DisplayStatus{}, err
		}
	}
	return status, nil
}

// DisplayTargetID formats one stable Display command target.
func DisplayTargetID(displayID int) string { return strconv.Itoa(displayID) }

// PendingDisplayEnrollment reports whether exact enrollment material remains usable.
func (installation *SQLite) PendingDisplayEnrollment(
	ctx context.Context,
	codeHash string,
	credentialHash string,
	now time.Time,
) (time.Time, bool, error) {
	found, err := installation.client.DisplayEnrollment.Query().Where(
		displayenrollment.CodeHashEQ(codeHash),
		displayenrollment.CredentialHashEQ(credentialHash),
		displayenrollment.UsedAtIsNil(),
		displayenrollment.ExpiresAtGT(now),
	).Only(systemContext(ctx))
	if ent.IsNotFound(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, opaqueError("find pending Display Enrollment", err)
	}
	return found.ExpiresAt, true, nil
}

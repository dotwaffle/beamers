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
	// ErrDisplayAlreadyEnrolled means recovery targeted a Display with a live credential.
	ErrDisplayAlreadyEnrolled = errors.New("Display already has an active credential")
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
	DisplayGroupKeys                     []string
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
	AppliedUrgentNoticeID                int
	AppliedUrgentNoticeRevision          int
	AppliedEmergencyAlertID              int
	AppliedEmergencyAlertRevision        int
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
	displayID int,
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
	var claimed *ent.Display
	if displayID == 0 {
		claimed, err = transaction.transaction.Display.Create().
			SetName(name).
			SetCreatedAt(now).
			SetEnrolledAt(now).
			Save(internalContext)
		if err != nil {
			return Display{}, opaqueError("create enrolled Display", err)
		}
	} else {
		claimed, err = transaction.transaction.Display.Get(internalContext, displayID)
		if ent.IsNotFound(err) {
			return Display{}, ErrDisplayNotFound
		}
		if err != nil {
			return Display{}, opaqueError("load Display for re-Enrollment", err)
		}
		active, credentialErr := transaction.transaction.DisplayCredential.Query().Where(
			displaycredential.DisplayIDEQ(displayID),
			displaycredential.RevokedAtIsNil(),
		).Exist(internalContext)
		if credentialErr != nil {
			return Display{}, opaqueError("inspect Display credentials", credentialErr)
		}
		if active {
			return Display{}, ErrDisplayAlreadyEnrolled
		}
	}
	if _, credentialErr := transaction.transaction.DisplayCredential.Create().
		SetDisplayID(claimed.ID).
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
	return Display{ID: claimed.ID, Name: claimed.Name, EnrolledAt: claimed.EnrolledAt}, nil
}

// FindDisplayByCredential authenticates one persistent Display token hash.
func (installation *SQLite) FindDisplayByCredential(
	ctx context.Context,
	tokenHash string,
) (Display, error) {
	credential, err := installation.reader.DisplayCredential.Query().Where(
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
	if pauseErr := transaction.reconcilePrizegivingRevealPauses(
		ctx,
		assignment.EventID,
		now,
	); pauseErr != nil {
		return DisplayAssignment{}, pauseErr
	}
	return assignment, nil
}

// LoadDisplayStatus returns one Display's Assignment for only the Active Event.
func (installation *SQLite) LoadDisplayStatus(
	ctx context.Context,
	displayID int,
) (DisplayStatus, error) {
	internalContext := systemContext(ctx)
	found, err := installation.readClient().Display.Get(internalContext, displayID)
	if ent.IsNotFound(err) {
		return DisplayStatus{}, ErrDisplayNotFound
	}
	if err != nil {
		return DisplayStatus{}, opaqueError("load Display status", err)
	}
	routing, err := loadDisplayRouting(internalContext, installation.readClient())
	if err != nil {
		return DisplayStatus{}, err
	}
	inputs, err := loadDisplayStatusInputs(
		internalContext, installation.readClient(), routing, []*ent.Display{found},
	)
	if err != nil {
		return DisplayStatus{}, err
	}
	return displayStatus(found, inputs)
}

// ListDisplayStatuses returns one snapshot's Active Event and crew-visible Assignment summaries.
func (installation *SQLite) ListDisplayStatuses(ctx context.Context) (int, []DisplayStatus, error) {
	internalContext := systemContext(ctx)
	type snapshot struct {
		activeEventID int
		statuses      []DisplayStatus
	}
	found, err := withReadTx(internalContext, installation.readClient(), "Display status snapshot", func(transaction *ent.Tx) (snapshot, error) {
		client := transaction.Client()
		routing, err := loadDisplayRouting(internalContext, client)
		if err != nil {
			return snapshot{}, err
		}
		displays, err := client.Display.Query().Order(ent.Asc(display.FieldID)).All(internalContext)
		if err != nil {
			return snapshot{}, opaqueError("list Displays", err)
		}
		inputs, err := loadDisplayStatusInputs(internalContext, client, routing, displays)
		if err != nil {
			return snapshot{}, err
		}
		result := make([]DisplayStatus, 0, len(displays))
		for _, item := range displays {
			status, statusErr := displayStatus(item, inputs)
			if statusErr != nil {
				return snapshot{}, statusErr
			}
			result = append(result, status)
		}
		return snapshot{activeEventID: routing.ActiveEventID, statuses: result}, nil
	})
	if err != nil {
		return 0, nil, err
	}
	return found.activeEventID, found.statuses, nil
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

// displayStatusInputs are the Active Event facts every Display status row
// shares. Loading them once per snapshot is what keeps the crew Displays list
// from re-reading the Event for every enrolled Display.
type displayStatusInputs struct {
	Routing         displayRouting
	Assignments     map[int]*ent.DisplayAssignment
	LocationNames   map[int]*ent.LocationPublishedVersion
	ProgramChannels map[int]int
}

func loadDisplayStatusInputs(
	ctx context.Context,
	client *ent.Client,
	routing displayRouting,
	displays []*ent.Display,
) (displayStatusInputs, error) {
	inputs := displayStatusInputs{Routing: routing}
	if routing.ActiveEventID == 0 || len(displays) == 0 {
		return inputs, nil
	}
	displayIDs := make([]int, 0, len(displays))
	for _, item := range displays {
		displayIDs = append(displayIDs, item.ID)
	}
	assignments, err := client.DisplayAssignment.Query().
		Where(
			displayassignment.DisplayIDIn(displayIDs...),
			displayassignment.EventIDEQ(routing.ActiveEventID),
		).
		All(ctx)
	if err != nil {
		return displayStatusInputs{}, opaqueError("load Active Event Display Assignment", err)
	}
	inputs.Assignments = make(map[int]*ent.DisplayAssignment, len(assignments))
	locationIDs := make([]int, 0, len(assignments))
	for _, assignment := range assignments {
		inputs.Assignments[assignment.DisplayID] = assignment
		locationIDs = append(locationIDs, assignment.LocationID)
	}
	if len(locationIDs) == 0 {
		return inputs, nil
	}
	published, err := client.LocationPublishedVersion.Query().
		Where(
			locationpublishedversion.LocationIDIn(locationIDs...),
			latestPublishedVersion(
				locationpublishedversion.Table,
				locationpublishedversion.FieldLocationID,
			),
		).
		All(ctx)
	if err != nil {
		return displayStatusInputs{}, opaqueError(
			"load Published Display Assignment Location name", err,
		)
	}
	inputs.LocationNames = make(map[int]*ent.LocationPublishedVersion, len(published))
	for _, version := range published {
		inputs.LocationNames[version.LocationID] = version
	}
	// One Program Channel is routed per Location, not per Display, so a
	// Location with twenty Displays resolves it once.
	inputs.ProgramChannels, err = loadDisplayStatusProgramChannels(ctx, client, inputs)
	if err != nil {
		return displayStatusInputs{}, err
	}
	return inputs, nil
}

func loadDisplayStatusProgramChannels(
	ctx context.Context,
	client *ent.Client,
	inputs displayStatusInputs,
) (map[int]int, error) {
	routed := make(map[int]struct{})
	for _, assignment := range inputs.Assignments {
		version := inputs.LocationNames[assignment.LocationID]
		if assignment.ViewKey != "competition-output" || version == nil || version.Retired {
			continue
		}
		routed[assignment.LocationID] = struct{}{}
	}
	if len(routed) == 0 {
		return nil, nil
	}
	routing, err := loadProgramChannelRouting(ctx, client, inputs.Routing.ActiveEventID)
	if err != nil {
		return nil, err
	}
	result := make(map[int]int, len(routed))
	for locationID := range routed {
		result[locationID] = routing.channelAt(locationID)
	}
	return result, nil
}

func displayStatus(
	found *ent.Display,
	inputs displayStatusInputs,
) (DisplayStatus, error) {
	routing := inputs.Routing
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
		AppliedUrgentNoticeID:                found.AppliedUrgentNoticeID,
		AppliedUrgentNoticeRevision:          found.AppliedUrgentNoticeRevision,
		AppliedEmergencyAlertID:              found.AppliedEmergencyAlertID,
		AppliedEmergencyAlertRevision:        found.AppliedEmergencyAlertRevision,
		AppliedStandby:                       found.AppliedStandby,
		AppliedAt:                            found.AppliedAt,
		ClockOffsetMilliseconds:              found.ClockOffsetMilliseconds,
		ClockUncertaintyMilliseconds:         found.ClockUncertaintyMilliseconds,
		RendererUnstable:                     found.RendererUnstable,
	}
	if routing.ActiveEventID == 0 {
		return status, nil
	}
	assignment := inputs.Assignments[found.ID]
	if assignment == nil {
		return status, nil
	}
	published := inputs.LocationNames[assignment.LocationID]
	if published == nil {
		return DisplayStatus{}, opaqueError(
			"load Published Display Assignment Location name",
			errors.New("missing Published Location"),
		)
	}
	if published.Retired {
		return status, nil
	}
	status.Standby = false
	status.LocationID = assignment.LocationID
	status.LocationName = published.Name
	status.ViewKey = assignment.ViewKey
	status.DisplayGroupKeys = assignment.DisplayGroupKeys
	if status.ViewKey == "competition-output" {
		status.ProgramChannelID = inputs.ProgramChannels[assignment.LocationID]
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

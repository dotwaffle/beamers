package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/account"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/sessionrun"
)

var (
	// ErrPresentationNotFound hides unknown, unpublished, and non-Presentation Sessions.
	ErrPresentationNotFound = errors.New("presentation not found")
	// ErrPresentationSubmitterRequired hides unknown and differently owned Presentations.
	ErrPresentationSubmitterRequired = errors.New("presentation Submitter required")
	// ErrPresentationSubmissionRevision means the caller used stale Presentation state.
	ErrPresentationSubmissionRevision = errors.New("presentation submission revision conflict")
	// ErrPresentationSubmissionClosed means the Presentation cutoff has arrived.
	ErrPresentationSubmissionClosed = errors.New("presentation submission access is closed")
)

// PresentationSubmission is current approved Presentation content and ownership.
type PresentationSubmission struct {
	EventID            int       `json:"event_id"`
	SessionID          int       `json:"session_id"`
	SubmitterAccountID int       `json:"submitter_account_id"`
	Revision           int       `json:"revision"`
	EventName          string    `json:"event_name"`
	SessionTitle       string    `json:"session_title"`
	Timezone           string    `json:"timezone"`
	EventLocale        string    `json:"event_locale"`
	Speaker            string    `json:"speaker"`
	PublicDetails      string    `json:"public_details"`
	UploadDeadline     time.Time `json:"upload_deadline"`
}

// AssignPresentationSubmitterParams assigns or replaces one Submitter Account.
type AssignPresentationSubmitterParams struct {
	EventID, SessionID, AccountID int
	ExpectedRevision              int
}

// UpdatePresentationSubmissionParams changes Account-maintained public content.
type UpdatePresentationSubmissionParams struct {
	EventID, SessionID, AccountID int
	ExpectedRevision              int
	Speaker, PublicDetails        string
	Now                           time.Time
}

// LoadPresentationSubmission returns one current published Presentation.
func (installation *SQLite) LoadPresentationSubmission(
	ctx context.Context,
	eventID, sessionID int,
) (PresentationSubmission, error) {
	found, version, foundEvent, err := loadPresentationSubmission(
		ctx,
		installation.client,
		eventID,
		sessionID,
	)
	if err != nil {
		return PresentationSubmission{}, err
	}
	return presentationSubmission(found, version, foundEvent), nil
}

// LoadAccountPresentationSubmissions returns only one Account's assigned Presentations.
func (installation *SQLite) LoadAccountPresentationSubmissions(
	ctx context.Context,
	accountID int,
) ([]PresentationSubmission, error) {
	enabled, err := installation.client.Account.Query().
		Where(account.IDEQ(accountID), account.DisabledAtIsNil()).
		Exist(ctx)
	if err != nil {
		return nil, opaqueError("load Presentation Submitter Account", err)
	}
	if !enabled {
		return nil, ErrPresentationSubmitterRequired
	}
	found, err := installation.client.Session.Query().
		Where(session.SubmitterAccountIDEQ(accountID)).
		WithEvent().
		Order(ent.Asc(session.FieldID)).
		All(ctx)
	if err != nil {
		return nil, opaqueError("load Account Presentations", err)
	}
	result := make([]PresentationSubmission, 0, len(found))
	for _, identity := range found {
		version, versionErr := identity.QueryPublishedVersions().
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			First(ctx)
		if ent.IsNotFound(versionErr) ||
			(versionErr == nil && version.Type != sessionpublishedversion.TypePresentation) {
			continue
		}
		if versionErr != nil {
			return nil, opaqueError("load Account Presentation version", versionErr)
		}
		foundEvent, edgeErr := identity.Edges.EventOrErr()
		if edgeErr != nil {
			return nil, opaqueError("load Account Presentation Event", edgeErr)
		}
		result = append(result, presentationSubmission(identity, version, foundEvent))
	}
	return result, nil
}

// AssignPresentationSubmitter assigns or replaces one enabled Account.
func (transaction *CommandTx) AssignPresentationSubmitter(
	ctx context.Context,
	params AssignPresentationSubmitterParams,
) (PresentationSubmission, error) {
	found, version, foundEvent, err := loadPresentationSubmission(
		ctx,
		transaction.transaction.Client(),
		params.EventID,
		params.SessionID,
	)
	if err != nil {
		return PresentationSubmission{}, err
	}
	current := presentationSubmission(found, version, foundEvent)
	if found.PresentationSubmissionRevision != params.ExpectedRevision {
		return current, ErrPresentationSubmissionRevision
	}
	enabled, err := transaction.transaction.Account.Query().
		Where(account.IDEQ(params.AccountID), account.DisabledAtIsNil()).
		Exist(ctx)
	if err != nil {
		return PresentationSubmission{}, opaqueError("load assigned Presentation Submitter", err)
	}
	if !enabled {
		return current, ErrPresentationSubmitterRequired
	}
	updated, err := found.Update().
		SetSubmitterAccountID(params.AccountID).
		AddPresentationSubmissionRevision(1).
		Save(ctx)
	if err != nil {
		return PresentationSubmission{}, opaqueError("assign Presentation Submitter", err)
	}
	return presentationSubmission(updated, version, foundEvent), nil
}

// UpdatePresentationSubmission changes approved content during current access.
func (transaction *CommandTx) UpdatePresentationSubmission(
	ctx context.Context,
	params UpdatePresentationSubmissionParams,
) (PresentationSubmission, error) {
	found, version, foundEvent, err := loadPresentationSubmission(
		ctx,
		transaction.transaction.Client(),
		params.EventID,
		params.SessionID,
	)
	if err != nil {
		return PresentationSubmission{}, err
	}
	current := presentationSubmission(found, version, foundEvent)
	if found.SubmitterAccountID == nil || *found.SubmitterAccountID != params.AccountID {
		return current, ErrPresentationSubmitterRequired
	}
	if found.PresentationSubmissionRevision != params.ExpectedRevision {
		return current, ErrPresentationSubmissionRevision
	}
	started, err := transaction.transaction.SessionRun.Query().
		Where(sessionrun.SessionIDEQ(params.SessionID)).
		Exist(ctx)
	if err != nil {
		return PresentationSubmission{}, opaqueError("load Presentation start", err)
	}
	if started {
		return current, ErrPresentationSubmissionClosed
	}
	open, err := uploadTargetOpen(
		ctx,
		transaction.transaction.Client(),
		UploadAuthorization{
			EventID: params.EventID, TargetType: UploadTargetPresentation, TargetID: params.SessionID,
		},
		params.Now,
	)
	if err != nil {
		return PresentationSubmission{}, err
	}
	if !open {
		return current, ErrPresentationSubmissionClosed
	}
	updated, err := found.Update().
		SetCorrectedSpeaker(params.Speaker).
		SetCorrectedPublicDetails(params.PublicDetails).
		AddPresentationSubmissionRevision(1).
		Save(ctx)
	if err != nil {
		return PresentationSubmission{}, opaqueError("update Presentation submission", err)
	}
	return presentationSubmission(updated, version, foundEvent), nil
}

func loadPresentationSubmission(
	ctx context.Context,
	client *ent.Client,
	eventID, sessionID int,
) (*ent.Session, *ent.SessionPublishedVersion, *ent.Event, error) {
	found, err := client.Session.Query().
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil, nil, ErrPresentationNotFound
	}
	if err != nil {
		return nil, nil, nil, opaqueError("load Presentation", err)
	}
	version, err := found.QueryPublishedVersions().
		Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
		First(ctx)
	if ent.IsNotFound(err) ||
		(err == nil && version.Type != sessionpublishedversion.TypePresentation) {
		return nil, nil, nil, ErrPresentationNotFound
	}
	if err != nil {
		return nil, nil, nil, opaqueError("load Presentation version", err)
	}
	foundEvent, err := client.Event.Query().Where(event.IDEQ(eventID)).Only(ctx)
	if err != nil {
		return nil, nil, nil, opaqueError("load Presentation Event", err)
	}
	return found, version, foundEvent, nil
}

func presentationSubmission(
	found *ent.Session,
	version *ent.SessionPublishedVersion,
	foundEvent *ent.Event,
) PresentationSubmission {
	speaker, publicDetails := version.Speaker, version.PublicDetails
	if found.CorrectedSpeaker != nil {
		speaker = *found.CorrectedSpeaker
	}
	if found.CorrectedPublicDetails != nil {
		publicDetails = *found.CorrectedPublicDetails
	}
	submitterAccountID := 0
	if found.SubmitterAccountID != nil {
		submitterAccountID = *found.SubmitterAccountID
	}
	return PresentationSubmission{
		EventID: found.EventID, SessionID: found.ID,
		SubmitterAccountID: submitterAccountID,
		Revision:           found.PresentationSubmissionRevision,
		EventName:          foundEvent.Name, SessionTitle: version.Title,
		Timezone: foundEvent.Timezone, EventLocale: foundEvent.EventLocale,
		Speaker: speaker, PublicDetails: publicDetails, UploadDeadline: version.UploadDeadline,
	}
}

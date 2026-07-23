package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/attachment"
	"github.com/dotwaffle/beamers/ent/attachmentversion"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
)

var (
	// ErrAttachmentReleaseRevision means release configuration changed after observation.
	ErrAttachmentReleaseRevision = errors.New("attachment release revision conflict")
	// ErrAttachmentReleasePolicy means a release policy is unsupported.
	ErrAttachmentReleasePolicy = errors.New("invalid Attachment Release Policy")
	// ErrAttachmentReleaseCueBlocked means unresolved Entries prevent the Event cue.
	ErrAttachmentReleaseCueBlocked = errors.New("Event Release Cue is blocked by unresolved Entries")
	// ErrAttachmentNotReleased hides unknown and unavailable public files.
	ErrAttachmentNotReleased = errors.New("attachment version not released")
)

// AttachmentReleasePolicy selects the durable public-release trigger.
type AttachmentReleasePolicy string

const (
	// AttachmentReleaseOnLive releases once the owning Session becomes Live.
	AttachmentReleaseOnLive AttachmentReleasePolicy = "OnLive"
	// AttachmentReleaseOnEnded releases once the owning Session becomes Ended.
	AttachmentReleaseOnEnded AttachmentReleasePolicy = "OnEnded"
	// AttachmentReleaseOnEventCue releases after the Producer fires the Event cue.
	AttachmentReleaseOnEventCue AttachmentReleasePolicy = "OnEventReleaseCue"
)

// AttachmentReleaseConfiguration is one Event policy and cue state.
type AttachmentReleaseConfiguration struct {
	EventID      int                     `json:"event_id"`
	Policy       AttachmentReleasePolicy `json:"policy"`
	CueSessionID int                     `json:"cue_session_id,omitempty"`
	CueAt        time.Time               `json:"cue_at,omitzero"`
	Revision     int                     `json:"revision"`
}

// CompetitionAttachmentReleaseConfiguration is one optional Competition override.
type CompetitionAttachmentReleaseConfiguration struct {
	EventID   int                     `json:"event_id"`
	SessionID int                     `json:"session_id"`
	Policy    AttachmentReleasePolicy `json:"policy,omitempty"`
	Override  bool                    `json:"override"`
	Revision  int                     `json:"revision"`
}

// ConfigureEventAttachmentReleaseParams changes an Event's default trigger.
type ConfigureEventAttachmentReleaseParams struct {
	EventID, ExpectedRevision int
	Policy                    AttachmentReleasePolicy
	CueSessionID              int
}

// ConfigureCompetitionAttachmentReleaseParams changes one Competition override.
type ConfigureCompetitionAttachmentReleaseParams struct {
	EventID, SessionID, ExpectedRevision int
	Policy                               AttachmentReleasePolicy
	Override                             bool
}

// SetAttachmentVersionReleaseParams changes eligibility and a Producer hold independently.
type SetAttachmentVersionReleaseParams struct {
	EventID, VersionID, ExpectedRevision int
	Hold                                 bool
}

// ConfigureEventAttachmentRelease changes the default release trigger.
func (transaction *CommandTx) ConfigureEventAttachmentRelease(
	ctx context.Context,
	params ConfigureEventAttachmentReleaseParams,
) (AttachmentReleaseConfiguration, error) {
	if !validAttachmentReleasePolicy(params.Policy) {
		return AttachmentReleaseConfiguration{}, ErrAttachmentReleasePolicy
	}
	if params.CueSessionID > 0 && params.Policy != AttachmentReleaseOnEventCue {
		return AttachmentReleaseConfiguration{}, ErrAttachmentReleasePolicy
	}
	found, err := transaction.transaction.Event.Query().
		Where(event.IDEQ(params.EventID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return AttachmentReleaseConfiguration{}, ErrUploadTargetNotFound
	}
	if err != nil {
		return AttachmentReleaseConfiguration{}, opaqueError("load Event Attachment Release Policy", err)
	}
	if found.AttachmentReleaseRevision != params.ExpectedRevision {
		return eventAttachmentRelease(found), ErrAttachmentReleaseRevision
	}
	if params.CueSessionID > 0 {
		cueSession, queryErr := transaction.transaction.Session.Query().
			Where(
				session.IDEQ(params.CueSessionID),
				session.EventIDEQ(params.EventID),
			).
			Only(ctx)
		if ent.IsNotFound(queryErr) {
			return AttachmentReleaseConfiguration{}, ErrUploadTargetNotFound
		}
		if queryErr != nil {
			return AttachmentReleaseConfiguration{}, opaqueError(
				"load Attachment Release Cue Session", queryErr,
			)
		}
		published, queryErr := cueSession.QueryPublishedVersions().
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			First(ctx)
		if ent.IsNotFound(queryErr) ||
			(queryErr == nil && published.Type != sessionpublishedversion.TypeCeremony) {
			return AttachmentReleaseConfiguration{}, ErrUploadTargetNotFound
		}
		if queryErr != nil {
			return AttachmentReleaseConfiguration{}, opaqueError(
				"load Attachment Release Cue Session type", queryErr,
			)
		}
	}
	update := found.Update().
		SetAttachmentReleasePolicy(event.AttachmentReleasePolicy(params.Policy)).
		AddAttachmentReleaseRevision(1)
	if params.CueSessionID > 0 {
		update.SetAttachmentReleaseCueSessionID(params.CueSessionID)
	} else {
		update.ClearAttachmentReleaseCueSessionID()
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return AttachmentReleaseConfiguration{}, opaqueError("configure Event Attachment Release Policy", err)
	}
	return eventAttachmentRelease(updated), nil
}

// ConfigureCompetitionAttachmentRelease changes one Competition override.
func (transaction *CommandTx) ConfigureCompetitionAttachmentRelease(
	ctx context.Context,
	params ConfigureCompetitionAttachmentReleaseParams,
) (CompetitionAttachmentReleaseConfiguration, error) {
	if params.Override && !validAttachmentReleasePolicy(params.Policy) {
		return CompetitionAttachmentReleaseConfiguration{}, ErrAttachmentReleasePolicy
	}
	found, err := transaction.transaction.Session.Query().
		Where(session.IDEQ(params.SessionID), session.EventIDEQ(params.EventID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return CompetitionAttachmentReleaseConfiguration{}, ErrCompetitionNotFound
	}
	if err != nil {
		return CompetitionAttachmentReleaseConfiguration{}, opaqueError(
			"load Competition Attachment Release Policy", err,
		)
	}
	version, err := found.QueryPublishedVersions().
		Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
		First(ctx)
	if ent.IsNotFound(err) || (err == nil && version.Type != sessionpublishedversion.TypeCompetition) {
		return CompetitionAttachmentReleaseConfiguration{}, ErrCompetitionNotFound
	}
	if err != nil {
		return CompetitionAttachmentReleaseConfiguration{}, opaqueError(
			"load Competition type for Attachment Release Policy", err,
		)
	}
	if found.AttachmentReleaseRevision != params.ExpectedRevision {
		return competitionAttachmentRelease(found), ErrAttachmentReleaseRevision
	}
	update := found.Update().AddAttachmentReleaseRevision(1)
	if params.Override {
		update.SetAttachmentReleasePolicyOverride(
			session.AttachmentReleasePolicyOverride(params.Policy),
		)
	} else {
		update.ClearAttachmentReleasePolicyOverride()
	}
	updated, err := update.Save(ctx)
	if err != nil {
		return CompetitionAttachmentReleaseConfiguration{}, opaqueError(
			"configure Competition Attachment Release Policy", err,
		)
	}
	return competitionAttachmentRelease(updated), nil
}

// FireEventAttachmentReleaseCue releases cue-governed files after resolution.
func (transaction *CommandTx) FireEventAttachmentReleaseCue(
	ctx context.Context,
	eventID, expectedRevision int,
	now time.Time,
) (AttachmentReleaseConfiguration, error) {
	found, err := transaction.transaction.Event.Query().Where(event.IDEQ(eventID)).Only(ctx)
	if ent.IsNotFound(err) {
		return AttachmentReleaseConfiguration{}, ErrUploadTargetNotFound
	}
	if err != nil {
		return AttachmentReleaseConfiguration{}, opaqueError("load Event Release Cue", err)
	}
	if found.AttachmentReleaseRevision != expectedRevision {
		return eventAttachmentRelease(found), ErrAttachmentReleaseRevision
	}
	unresolved, err := transaction.transaction.CompetitionEntry.Query().
		Where(
			competitionentry.EventIDEQ(eventID),
			competitionentry.ResolutionRequiredEQ(true),
		).
		Exist(ctx)
	if err != nil {
		return AttachmentReleaseConfiguration{}, opaqueError("load Event Release Cue blockers", err)
	}
	if unresolved {
		return eventAttachmentRelease(found), ErrAttachmentReleaseCueBlocked
	}
	updated, err := found.Update().
		SetAttachmentReleaseCueAt(now).
		AddAttachmentReleaseRevision(1).
		Save(ctx)
	if err != nil {
		return AttachmentReleaseConfiguration{}, opaqueError("fire Event Release Cue", err)
	}
	return eventAttachmentRelease(updated), nil
}

func (transaction *CommandTx) fireBoundAttachmentReleaseCue(
	ctx context.Context,
	eventID, sessionID int,
	now time.Time,
) error {
	found, err := transaction.transaction.Event.Query().
		Where(
			event.IDEQ(eventID),
			event.AttachmentReleaseCueSessionIDEQ(sessionID),
		).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return opaqueError("load bound Event Release Cue", err)
	}
	unresolved, err := transaction.transaction.CompetitionEntry.Query().
		Where(
			competitionentry.EventIDEQ(eventID),
			competitionentry.ResolutionRequiredEQ(true),
		).
		Exist(ctx)
	if err != nil {
		return opaqueError("load bound Event Release Cue blockers", err)
	}
	if unresolved || !found.AttachmentReleaseCueAt.IsZero() {
		return nil
	}
	if _, err = found.Update().
		SetAttachmentReleaseCueAt(now).
		AddAttachmentReleaseRevision(1).
		Save(ctx); err != nil {
		return opaqueError("fire bound Event Release Cue", err)
	}
	return nil
}

// SetAttachmentVersionRelease changes a hold without changing eligibility or Final.
func (transaction *CommandTx) SetAttachmentVersionRelease(
	ctx context.Context,
	params SetAttachmentVersionReleaseParams,
) (AttachmentVersion, error) {
	version, err := transaction.transaction.AttachmentVersion.Query().
		Where(attachmentversion.IDEQ(params.VersionID)).
		WithAttachment().
		Only(systemContext(ctx))
	if ent.IsNotFound(err) {
		return AttachmentVersion{}, ErrUploadTargetNotFound
	}
	if err != nil {
		return AttachmentVersion{}, opaqueError("load Attachment Version release control", err)
	}
	logical, err := version.Edges.AttachmentOrErr()
	if err != nil || logical.EventID != params.EventID {
		return AttachmentVersion{}, ErrUploadTargetNotFound
	}
	if version.ReleaseRevision != params.ExpectedRevision {
		return attachmentVersion(logical, version), ErrAttachmentReleaseRevision
	}
	updated, err := version.Update().
		SetReleaseHold(params.Hold).
		AddReleaseRevision(1).
		Save(systemContext(ctx))
	if err != nil {
		return AttachmentVersion{}, opaqueError("set Attachment Version release control", err)
	}
	return attachmentVersion(logical, updated), nil
}

// LoadReleasedAttachmentVersions lists Active Event files currently safe for attendees.
func (installationStore *SQLite) LoadReleasedAttachmentVersions(
	ctx context.Context,
) ([]AttachmentVersion, error) {
	internalContext := systemContext(ctx)
	active, err := installationStore.client.Installation.Query().
		Where(installation.ActiveEventIDNotNil()).
		Only(internalContext)
	if ent.IsNotFound(err) || active.ActiveEventID == nil {
		return nil, nil
	}
	if err != nil {
		return nil, opaqueError("load Active Event Attachment releases", err)
	}
	eventID := *active.ActiveEventID
	foundEvent, err := installationStore.client.Event.Get(internalContext, eventID)
	if err != nil {
		return nil, opaqueError("load Attachment Release Event", err)
	}
	versions, err := installationStore.client.AttachmentVersion.Query().
		Where(
			attachmentversion.FinalEQ(true),
			attachmentversion.ReleaseEligibilityEQ(attachmentversion.ReleaseEligibilityPublic),
			attachmentversion.ReleaseHoldEQ(false),
			attachmentversion.HasAttachmentWith(attachment.EventIDEQ(eventID)),
		).
		WithAttachment().
		Order(ent.Asc(attachmentversion.FieldID)).
		All(internalContext)
	if err != nil {
		return nil, opaqueError("load eligible Attachment Versions", err)
	}
	released := make([]AttachmentVersion, 0, len(versions))
	for _, version := range versions {
		logical, edgeErr := version.Edges.AttachmentOrErr()
		if edgeErr != nil {
			return nil, opaqueError("load released Attachment owner", edgeErr)
		}
		sessionID, eligible, ownerErr := installationStore.publicAttachmentOwner(
			internalContext, logical,
		)
		if ownerErr != nil {
			return nil, ownerErr
		}
		if !eligible {
			continue
		}
		ownerSession, queryErr := installationStore.client.Session.Get(internalContext, sessionID)
		if queryErr != nil {
			return nil, opaqueError("load released Attachment Session", queryErr)
		}
		policy := AttachmentReleasePolicy(foundEvent.AttachmentReleasePolicy.String())
		if ownerSession.AttachmentReleasePolicyOverride != nil {
			policy = AttachmentReleasePolicy(ownerSession.AttachmentReleasePolicyOverride.String())
		}
		if !attachmentReleaseTriggered(policy, ownerSession.Lifecycle, foundEvent.AttachmentReleaseCueAt) {
			continue
		}
		released = append(released, attachmentVersion(logical, version))
	}
	return released, nil
}

// LoadReleasedAttachmentVersion returns one exact attendee-safe immutable version.
func (installationStore *SQLite) LoadReleasedAttachmentVersion(
	ctx context.Context,
	versionID int,
) (AttachmentVersion, error) {
	versions, err := installationStore.LoadReleasedAttachmentVersions(ctx)
	if err != nil {
		return AttachmentVersion{}, err
	}
	for _, version := range versions {
		if version.ID == versionID {
			return version, nil
		}
	}
	return AttachmentVersion{}, ErrAttachmentNotReleased
}

func (installationStore *SQLite) publicAttachmentOwner(
	ctx context.Context,
	logical *ent.Attachment,
) (sessionID int, eligible bool, err error) {
	switch logical.OwnerType {
	case attachment.OwnerTypePresentation:
		found, queryErr := installationStore.client.Session.Query().
			Where(session.IDEQ(logical.OwnerID), session.EventIDEQ(logical.EventID)).
			Only(ctx)
		if ent.IsNotFound(queryErr) {
			return 0, false, nil
		}
		if queryErr != nil {
			return 0, false, opaqueError("load Presentation Attachment owner", queryErr)
		}
		return found.ID, true, nil
	case attachment.OwnerTypeEntry:
		entry, queryErr := installationStore.client.CompetitionEntry.Query().
			Where(
				competitionentry.IDEQ(logical.OwnerID),
				competitionentry.EventIDEQ(logical.EventID),
			).
			Only(ctx)
		if ent.IsNotFound(queryErr) {
			return 0, false, nil
		}
		if queryErr != nil {
			return 0, false, opaqueError("load Entry Attachment owner", queryErr)
		}
		eligible := entry.Disposition == competitionentry.DispositionIncluded &&
			entry.ResultDisposition != competitionentry.ResultDispositionWithheld &&
			!entry.ReleaseHold
		return entry.CompetitionSessionID, eligible, nil
	default:
		return 0, false, nil
	}
}

func validAttachmentReleasePolicy(policy AttachmentReleasePolicy) bool {
	return policy == AttachmentReleaseOnLive ||
		policy == AttachmentReleaseOnEnded ||
		policy == AttachmentReleaseOnEventCue
}

func attachmentReleaseTriggered(
	policy AttachmentReleasePolicy,
	lifecycle session.Lifecycle,
	cueAt time.Time,
) bool {
	switch policy {
	case AttachmentReleaseOnLive:
		return lifecycle == session.LifecycleLive || lifecycle == session.LifecycleEnded
	case AttachmentReleaseOnEnded:
		return lifecycle == session.LifecycleEnded
	case AttachmentReleaseOnEventCue:
		return !cueAt.IsZero()
	default:
		return false
	}
}

func eventAttachmentRelease(found *ent.Event) AttachmentReleaseConfiguration {
	result := AttachmentReleaseConfiguration{
		EventID:  found.ID,
		Policy:   AttachmentReleasePolicy(found.AttachmentReleasePolicy.String()),
		CueAt:    found.AttachmentReleaseCueAt,
		Revision: found.AttachmentReleaseRevision,
	}
	if found.AttachmentReleaseCueSessionID != nil {
		result.CueSessionID = *found.AttachmentReleaseCueSessionID
	}
	return result
}

func competitionAttachmentRelease(found *ent.Session) CompetitionAttachmentReleaseConfiguration {
	result := CompetitionAttachmentReleaseConfiguration{
		EventID: found.EventID, SessionID: found.ID,
		Revision: found.AttachmentReleaseRevision,
	}
	if found.AttachmentReleasePolicyOverride != nil {
		result.Override = true
		result.Policy = AttachmentReleasePolicy(found.AttachmentReleasePolicyOverride.String())
	}
	return result
}

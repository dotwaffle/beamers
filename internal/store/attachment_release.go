package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/attachment"
	"github.com/dotwaffle/beamers/ent/attachmentversion"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/event"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/trackpublishedversion"
)

var (
	// ErrAttachmentReleaseRevision means release configuration changed after observation.
	ErrAttachmentReleaseRevision = errors.New("attachment release revision conflict")
	// ErrAttachmentReleasePolicy means a release policy is unsupported.
	ErrAttachmentReleasePolicy = errors.New("invalid Attachment Release Policy")
	// ErrAttachmentReleaseCueBlocked means unresolved Entries prevent the Event cue.
	ErrAttachmentReleaseCueBlocked = errors.New("Event Release Cue is blocked by unresolved Entries")
	// ErrAttachmentReleaseCuePreviewChanged means release impact changed after review.
	ErrAttachmentReleaseCuePreviewChanged = errors.New("Event Release Cue preview changed")
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

// AttachmentReleaseCuePreview is the exact Event cue impact reviewed by a Producer.
type AttachmentReleaseCuePreview struct {
	Configuration              AttachmentReleaseConfiguration `json:"configuration"`
	Eligible                   int                            `json:"eligible"`
	Held                       int                            `json:"held"`
	Blocked                    int                            `json:"blocked"`
	BlockedByUnresolvedEntries bool                           `json:"blocked_by_unresolved_entries"`
	Fingerprint                string                         `json:"fingerprint"`
}

// CompetitionAttachmentReleaseConfiguration is one optional Competition override.
type CompetitionAttachmentReleaseConfiguration struct {
	EventID   int                     `json:"event_id"`
	SessionID int                     `json:"session_id"`
	Policy    AttachmentReleasePolicy `json:"policy,omitempty"`
	Override  bool                    `json:"override"`
	Revision  int                     `json:"revision"`
}

// FinalFileTrack identifies one published Track used by an export path.
type FinalFileTrack struct {
	ID   int
	Name string
}

// FinalFileVersion is one public Final Version with human-facing owner metadata.
type FinalFileVersion struct {
	AttachmentVersion
	EventName    string
	SessionID    int
	SessionTitle string
	SessionType  string
	EntryID      int
	EntryName    string
	Tracks       []FinalFileTrack
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
	if err := requireActor(ctx, "CommandTx.ConfigureEventAttachmentRelease"); err != nil {
		return AttachmentReleaseConfiguration{}, err
	}

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
	if err := requireActor(ctx, "CommandTx.ConfigureCompetitionAttachmentRelease"); err != nil {
		return CompetitionAttachmentReleaseConfiguration{}, err
	}

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
	previewFingerprint string,
	now time.Time,
) (AttachmentReleaseConfiguration, error) {
	if err := requireActor(ctx, "CommandTx.FireEventAttachmentReleaseCue"); err != nil {
		return AttachmentReleaseConfiguration{}, err
	}

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
	preview, err := attachmentReleaseCuePreview(
		ctx, transaction.transaction.Client(), eventID,
	)
	if err != nil {
		return AttachmentReleaseConfiguration{}, err
	}
	if preview.Fingerprint != previewFingerprint {
		return eventAttachmentRelease(found), ErrAttachmentReleaseCuePreviewChanged
	}
	if preview.BlockedByUnresolvedEntries {
		return eventAttachmentRelease(found), ErrAttachmentReleaseCueBlocked
	}
	if !found.AttachmentReleaseCueAt.IsZero() {
		return eventAttachmentRelease(found), nil
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

// PreviewEventAttachmentReleaseCue returns the exact current manual-cue impact.
func (installationStore *SQLite) PreviewEventAttachmentReleaseCue(
	ctx context.Context,
	eventID int,
) (AttachmentReleaseCuePreview, error) {
	if err := requireActor(ctx, "SQLite.PreviewEventAttachmentReleaseCue"); err != nil {
		return AttachmentReleaseCuePreview{}, err
	}

	return attachmentReleaseCuePreview(ctx, installationStore.client, eventID)
}

type attachmentReleaseCuePreviewVersion struct {
	ID        int                     `json:"id"`
	State     string                  `json:"state"`
	SessionID int                     `json:"session_id"`
	Policy    AttachmentReleasePolicy `json:"policy"`
}

func attachmentReleaseCuePreview(
	ctx context.Context,
	client *ent.Client,
	eventID int,
) (AttachmentReleaseCuePreview, error) {
	foundEvent, err := client.Event.Get(ctx, eventID)
	if ent.IsNotFound(err) {
		return AttachmentReleaseCuePreview{}, ErrUploadTargetNotFound
	}
	if err != nil {
		return AttachmentReleaseCuePreview{}, opaqueError("load Event Release Cue preview", err)
	}
	unresolved, err := client.CompetitionEntry.Query().
		Where(
			competitionentry.EventIDEQ(eventID),
			competitionentry.ResolutionRequiredEQ(true),
		).
		Exist(ctx)
	if err != nil {
		return AttachmentReleaseCuePreview{}, opaqueError("load Event Release Cue blockers", err)
	}
	versions, err := client.AttachmentVersion.Query().
		Where(
			attachmentversion.FinalEQ(true),
			attachmentversion.HasAttachmentWith(attachment.EventIDEQ(eventID)),
		).
		WithAttachment().
		Order(ent.Asc(attachmentversion.FieldID)).
		All(ctx)
	if err != nil {
		return AttachmentReleaseCuePreview{}, opaqueError("load Event Release Cue Final Versions", err)
	}
	preview := AttachmentReleaseCuePreview{
		Configuration:              eventAttachmentRelease(foundEvent),
		BlockedByUnresolvedEntries: unresolved,
	}
	material := make([]attachmentReleaseCuePreviewVersion, 0, len(versions))
	for _, version := range versions {
		item := attachmentReleaseCuePreviewVersion{ID: version.ID, State: "blocked"}
		if version.ReleaseEligibility == attachmentversion.ReleaseEligibilityPublic {
			logical, edgeErr := version.Edges.AttachmentOrErr()
			if edgeErr != nil {
				return AttachmentReleaseCuePreview{}, opaqueError(
					"load Event Release Cue Attachment owner", edgeErr,
				)
			}
			sessionID, eligible, held, ownerErr := attachmentReleaseOwner(ctx, client, logical)
			if ownerErr != nil {
				return AttachmentReleaseCuePreview{}, ownerErr
			}
			item.SessionID = sessionID
			if held || (eligible && version.ReleaseHold) {
				item.State = "held"
			} else if eligible {
				ownerSession, queryErr := client.Session.Get(ctx, sessionID)
				if queryErr != nil {
					return AttachmentReleaseCuePreview{}, opaqueError(
						"load Event Release Cue Session", queryErr,
					)
				}
				item.Policy = AttachmentReleasePolicy(foundEvent.AttachmentReleasePolicy.String())
				if ownerSession.AttachmentReleasePolicyOverride != nil {
					item.Policy = AttachmentReleasePolicy(
						ownerSession.AttachmentReleasePolicyOverride.String(),
					)
				}
				if item.Policy == AttachmentReleaseOnEventCue {
					item.State = "eligible"
				}
			}
		}
		switch item.State {
		case "eligible":
			preview.Eligible++
		case "held":
			preview.Held++
		default:
			preview.Blocked++
		}
		material = append(material, item)
	}
	encoded, err := json.Marshal(struct {
		Configuration AttachmentReleaseConfiguration       `json:"configuration"`
		Unresolved    bool                                 `json:"unresolved"`
		Versions      []attachmentReleaseCuePreviewVersion `json:"versions"`
	}{preview.Configuration, unresolved, material})
	if err != nil {
		return AttachmentReleaseCuePreview{}, opaqueError("encode Event Release Cue preview", err)
	}
	sum := sha256.Sum256(encoded)
	preview.Fingerprint = hex.EncodeToString(sum[:])
	return preview, nil
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
	if err := requireActor(ctx, "CommandTx.SetAttachmentVersionRelease"); err != nil {
		return AttachmentVersion{}, err
	}

	version, err := transaction.transaction.AttachmentVersion.Query().
		Where(attachmentversion.IDEQ(params.VersionID)).
		WithAttachment().
		Only(ctx)
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
		Save(ctx)
	if err != nil {
		return AttachmentVersion{}, opaqueError("set Attachment Version release control", err)
	}
	return attachmentVersion(logical, updated), nil
}

// LoadReleasedAttachmentVersions lists Active Event files currently safe for attendees.
func (installationStore *SQLite) LoadReleasedAttachmentVersions(
	ctx context.Context,
) ([]AttachmentVersion, error) {
	if err := requireActor(ctx, "SQLite.LoadReleasedAttachmentVersions"); err != nil {
		return []AttachmentVersion(nil), err
	}

	active, err := installationStore.client.Installation.Query().
		Where(installation.ActiveEventIDNotNil()).
		Only(ctx)
	if ent.IsNotFound(err) || active.ActiveEventID == nil {
		return nil, nil
	}
	if err != nil {
		return nil, opaqueError("load Active Event Attachment releases", err)
	}
	eventID := *active.ActiveEventID
	foundEvent, err := installationStore.client.Event.Get(ctx, eventID)
	if err != nil {
		return nil, opaqueError("load Attachment Release Event", err)
	}
	return installationStore.loadReleasedAttachmentVersions(ctx, eventID, foundEvent)
}

func (installationStore *SQLite) loadReleasedAttachmentVersions(
	ctx context.Context,
	eventID int,
	foundEvent *ent.Event,
) ([]AttachmentVersion, error) {
	versions, err := installationStore.client.AttachmentVersion.Query().
		Where(
			attachmentversion.FinalEQ(true),
			attachmentversion.ReleaseEligibilityEQ(attachmentversion.ReleaseEligibilityPublic),
			attachmentversion.ReleaseHoldEQ(false),
			attachmentversion.HasAttachmentWith(attachment.EventIDEQ(eventID)),
		).
		WithAttachment().
		Order(ent.Asc(attachmentversion.FieldID)).
		All(ctx)
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
			ctx, logical,
		)
		if ownerErr != nil {
			return nil, ownerErr
		}
		if !eligible {
			continue
		}
		ownerSession, queryErr := installationStore.client.Session.Get(ctx, sessionID)
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

// LoadFinalFileVersions lists one Event's public Final Versions with export labels.
func (installationStore *SQLite) LoadFinalFileVersions(
	ctx context.Context,
	eventID int,
) ([]FinalFileVersion, error) {
	if err := requireActor(ctx, "SQLite.LoadFinalFileVersions"); err != nil {
		return []FinalFileVersion(nil), err
	}

	foundEvent, err := installationStore.client.Event.Get(ctx, eventID)
	if ent.IsNotFound(err) {
		return nil, ErrUploadTargetNotFound
	}
	if err != nil {
		return nil, opaqueError("load Final Files Export Event", err)
	}
	released, err := installationStore.loadReleasedAttachmentVersions(
		ctx, eventID, foundEvent,
	)
	if err != nil {
		return nil, err
	}
	result := make([]FinalFileVersion, 0, len(released))
	for _, version := range released {
		sessionID := version.OwnerID
		entryID, entryName := 0, ""
		if version.OwnerType == UploadTargetEntry {
			entry, queryErr := installationStore.client.CompetitionEntry.Get(
				ctx, version.OwnerID,
			)
			if queryErr != nil {
				return nil, opaqueError("load Final Files Export Entry", queryErr)
			}
			sessionID, entryID, entryName = entry.CompetitionSessionID, entry.ID, entry.Name
		}
		published, queryErr := installationStore.client.SessionPublishedVersion.Query().
			Where(sessionpublishedversion.SessionIDEQ(sessionID)).
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			WithTracks().
			First(ctx)
		if queryErr != nil {
			return nil, opaqueError("load Final Files Export Session", queryErr)
		}
		tracks := make([]FinalFileTrack, 0, len(published.Edges.Tracks))
		// ponytail: exports are infrequent; batch Track labels if large Events make this measurable.
		for _, identity := range published.Edges.Tracks {
			label, labelErr := installationStore.client.TrackPublishedVersion.Query().
				Where(trackpublishedversion.TrackIDEQ(identity.ID)).
				Order(ent.Desc(trackpublishedversion.FieldPublishedRevision)).
				First(ctx)
			if labelErr != nil {
				return nil, opaqueError("load Final Files Export Track", labelErr)
			}
			tracks = append(tracks, FinalFileTrack{ID: identity.ID, Name: label.Name})
		}
		sort.Slice(tracks, func(left, right int) bool {
			if tracks[left].Name != tracks[right].Name {
				return tracks[left].Name < tracks[right].Name
			}
			return tracks[left].ID < tracks[right].ID
		})
		result = append(result, FinalFileVersion{
			AttachmentVersion: version,
			EventName:         foundEvent.Name,
			SessionID:         sessionID,
			SessionTitle:      published.Title,
			SessionType:       published.Type.String(),
			EntryID:           entryID,
			EntryName:         entryName,
			Tracks:            tracks,
		})
	}
	return result, nil
}

// LoadReleasedAttachmentVersion returns one exact attendee-safe immutable version.
func (installationStore *SQLite) LoadReleasedAttachmentVersion(
	ctx context.Context,
	versionID int,
) (AttachmentVersion, error) {
	if err := requireActor(ctx, "SQLite.LoadReleasedAttachmentVersion"); err != nil {
		return AttachmentVersion{}, err
	}

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
	sessionID, eligible, _, err = attachmentReleaseOwner(
		ctx, installationStore.client, logical,
	)
	return sessionID, eligible, err
}

func attachmentReleaseOwner(
	ctx context.Context,
	client *ent.Client,
	logical *ent.Attachment,
) (sessionID int, eligible, held bool, err error) {
	switch logical.OwnerType {
	case attachment.OwnerTypePresentation:
		found, queryErr := client.Session.Query().
			Where(session.IDEQ(logical.OwnerID), session.EventIDEQ(logical.EventID)).
			Only(ctx)
		if ent.IsNotFound(queryErr) {
			return 0, false, false, nil
		}
		if queryErr != nil {
			return 0, false, false, opaqueError("load Presentation Attachment owner", queryErr)
		}
		public, queryErr := publicPublishedSession(
			ctx, client, found.ID, sessionpublishedversion.TypePresentation,
		)
		return found.ID, public, false, queryErr
	case attachment.OwnerTypeEntry:
		entry, queryErr := client.CompetitionEntry.Query().
			Where(
				competitionentry.IDEQ(logical.OwnerID),
				competitionentry.EventIDEQ(logical.EventID),
			).
			Only(ctx)
		if ent.IsNotFound(queryErr) {
			return 0, false, false, nil
		}
		if queryErr != nil {
			return 0, false, false, opaqueError("load Entry Attachment owner", queryErr)
		}
		eligible := entry.Disposition == competitionentry.DispositionIncluded &&
			entry.ResultDisposition != competitionentry.ResultDispositionWithheld
		if !eligible {
			return entry.CompetitionSessionID, false, false, nil
		}
		if entry.ReleaseHold {
			return entry.CompetitionSessionID, false, true, nil
		}
		public, queryErr := publicPublishedSession(
			ctx, client, entry.CompetitionSessionID, sessionpublishedversion.TypeCompetition,
		)
		if queryErr != nil {
			return 0, false, false, queryErr
		}
		return entry.CompetitionSessionID, public, false, nil
	default:
		return 0, false, false, nil
	}
}

func publicPublishedSession(
	ctx context.Context,
	client *ent.Client,
	sessionID int,
	sessionType sessionpublishedversion.Type,
) (bool, error) {
	published, err := client.SessionPublishedVersion.Query().
		Where(sessionpublishedversion.SessionIDEQ(sessionID)).
		Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
		First(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, opaqueError("load public Attachment Session", err)
	}
	return published.Type == sessionType &&
		published.AudienceVisibility == sessionpublishedversion.AudienceVisibilityPublic, nil
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

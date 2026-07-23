package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/attachment"
	"github.com/dotwaffle/beamers/ent/attachmentversion"
	"github.com/dotwaffle/beamers/ent/competitionentry"
	"github.com/dotwaffle/beamers/ent/reopenwindow"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/sessionrun"
	"github.com/dotwaffle/beamers/ent/uploadlink"
)

var (
	// ErrUploadTargetNotFound hides unknown and cross-Event upload owners.
	ErrUploadTargetNotFound = errors.New("upload target not found")
	// ErrUploadLinkInvalid hides unknown, revoked, and malformed credentials.
	ErrUploadLinkInvalid = errors.New("upload link is invalid")
	// ErrUploadClosed means the fixed cutoff has arrived without an active exception.
	ErrUploadClosed = errors.New("attachment uploads are closed")
	// ErrReopenWindowRevision means an update used stale window state.
	ErrReopenWindowRevision = errors.New("reopen window revision conflict")
	// ErrReopenWindowExtension means an update did not extend the existing expiry.
	ErrReopenWindowExtension = errors.New("reopen window extension must increase expiry")
)

// UploadTargetKind is the closed owner vocabulary for scoped attachments.
type UploadTargetKind string

const (
	// UploadTargetPresentation scopes access to one published Presentation.
	UploadTargetPresentation UploadTargetKind = "Presentation"
	// UploadTargetEntry scopes access to one Competition Entry.
	UploadTargetEntry UploadTargetKind = "Entry"
)

// AttachmentReleaseEligibility is the uploader-selected public-release choice.
type AttachmentReleaseEligibility string

const (
	// AttachmentReleasePublic permits policy-governed public release.
	AttachmentReleasePublic AttachmentReleaseEligibility = "Public"
	// AttachmentReleaseCrewOnly permanently excludes a version from public release.
	AttachmentReleaseCrewOnly AttachmentReleaseEligibility = "CrewOnly"
)

// UploadLink is safe crew-visible Upload Link metadata without its credential.
type UploadLink struct {
	ID         int              `json:"id"`
	EventID    int              `json:"event_id"`
	TargetType UploadTargetKind `json:"target_type"`
	TargetID   int              `json:"target_id"`
	RevokedAt  time.Time        `json:"revoked_at,omitzero"`
	CreatedAt  time.Time        `json:"created_at"`
}

// IssueUploadLinkParams rotates the credential for one exact owner.
type IssueUploadLinkParams struct {
	EventID    int
	TargetType UploadTargetKind
	TargetID   int
	TokenHash  string
	Now        time.Time
}

// UploadAuthorization is one current target-scoped credential.
type UploadAuthorization struct {
	LinkID, EventID, TargetID int
	TargetType                UploadTargetKind
}

// AttachmentVersion is immutable uploaded file metadata.
type AttachmentVersion struct {
	ID, AttachmentID, Version   int
	EventID, OwnerID            int
	OwnerType                   UploadTargetKind
	Name                        string
	OriginalFilename, MediaType string
	SizeBytes                   int64
	SHA256, StorageKey          string
	UploaderType                string
	UploaderID                  int
	Primary, Final              bool
	ReadinessRevision           int
	ReleaseEligibility          AttachmentReleaseEligibility
	ReleaseHold                 bool
	ReleaseRevision             int
	CreatedAt                   time.Time
}

// SaveAttachmentVersionParams appends one immutable version.
type SaveAttachmentVersionParams struct {
	Authorization                     UploadAuthorization
	Name, OriginalFilename, MediaType string
	SizeBytes                         int64
	SHA256, StorageKey                string
	UploaderType                      string
	UploaderID                        int
	ReleaseEligibility                AttachmentReleaseEligibility
	Now                               time.Time
}

// ReopenWindow is one bounded target-specific upload exception.
type ReopenWindow struct {
	ID                 int              `json:"id"`
	EventID            int              `json:"event_id"`
	TargetID           int              `json:"target_id"`
	TargetType         UploadTargetKind `json:"target_type"`
	Reason             string           `json:"reason"`
	ExpiresAt          time.Time        `json:"expires_at"`
	ClosedAt           time.Time        `json:"closed_at,omitzero"`
	CreatedAt          time.Time        `json:"created_at"`
	CreatedByAccountID int              `json:"created_by_account_id"`
	Revision           int              `json:"revision"`
}

// IssueUploadLink revokes prior target credentials and creates a replacement.
func (transaction *CommandTx) IssueUploadLink(
	ctx context.Context,
	params IssueUploadLinkParams,
) (UploadLink, error) {
	if err := transaction.validateUploadTarget(ctx, params.EventID, params.TargetType, params.TargetID); err != nil {
		return UploadLink{}, err
	}
	if params.TargetType == UploadTargetEntry {
		entry, err := transaction.transaction.CompetitionEntry.Query().Where(
			competitionentry.IDEQ(params.TargetID),
			competitionentry.EventIDEQ(params.EventID),
		).Only(ctx)
		if err != nil {
			return UploadLink{}, opaqueError("load Entry Upload Link target", err)
		}
		if !entry.UploadClosedAt.IsZero() {
			if _, err = entry.Update().ClearUploadClosedAt().Save(ctx); err != nil {
				return UploadLink{}, opaqueError("reissue rejected Entry Upload Link", err)
			}
		}
	}
	active, err := transaction.transaction.UploadLink.Query().
		Where(
			uploadlink.EventIDEQ(params.EventID),
			uploadlink.TargetTypeEQ(uploadlink.TargetType(params.TargetType)),
			uploadlink.TargetIDEQ(params.TargetID),
			uploadlink.RevokedAtIsNil(),
		).
		All(ctx)
	if err != nil {
		return UploadLink{}, opaqueError("load active Upload Links", err)
	}
	for _, link := range active {
		if _, saveErr := link.Update().SetRevokedAt(params.Now).Save(ctx); saveErr != nil {
			return UploadLink{}, opaqueError("rotate Upload Link", saveErr)
		}
	}
	created, err := transaction.transaction.UploadLink.Create().
		SetEventID(params.EventID).
		SetTargetType(uploadlink.TargetType(params.TargetType)).
		SetTargetID(params.TargetID).
		SetTokenHash(params.TokenHash).
		SetCreatedAt(params.Now).
		Save(ctx)
	if err != nil {
		return UploadLink{}, opaqueError("create Upload Link", err)
	}
	return uploadLink(created), nil
}

func (transaction *CommandTx) validateUploadTarget(
	ctx context.Context,
	eventID int,
	targetType UploadTargetKind,
	targetID int,
) error {
	return validateUploadTarget(ctx, transaction.transaction.Client(), eventID, targetType, targetID)
}

func validateUploadTarget(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	targetType UploadTargetKind,
	targetID int,
) error {
	switch targetType {
	case UploadTargetEntry:
		exists, err := client.CompetitionEntry.Query().Where(
			competitionentry.IDEQ(targetID),
			competitionentry.EventIDEQ(eventID),
		).Exist(ctx)
		if err != nil {
			return opaqueError("validate Entry Upload Link target", err)
		}
		if !exists {
			return ErrUploadTargetNotFound
		}
	case UploadTargetPresentation:
		found, err := client.Session.Query().Where(
			session.IDEQ(targetID), session.EventIDEQ(eventID),
		).Only(ctx)
		if ent.IsNotFound(err) {
			return ErrUploadTargetNotFound
		}
		if err != nil {
			return opaqueError("validate Presentation Upload Link target", err)
		}
		version, err := found.QueryPublishedVersions().
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			First(ctx)
		if ent.IsNotFound(err) ||
			(err == nil && version.Type != sessionpublishedversion.TypePresentation) {
			return ErrUploadTargetNotFound
		}
		if err != nil {
			return opaqueError("validate published Presentation Upload Link target", err)
		}
	default:
		return ErrUploadTargetNotFound
	}
	return nil
}

func uploadLink(stored *ent.UploadLink) UploadLink {
	return UploadLink{
		ID: stored.ID, EventID: stored.EventID,
		TargetType: UploadTargetKind(stored.TargetType.String()),
		TargetID:   stored.TargetID, RevokedAt: stored.RevokedAt, CreatedAt: stored.CreatedAt,
	}
}

// ResolveUploadLink identifies a credential for receipt replay before mutable-state checks.
func (installation *SQLite) ResolveUploadLink(
	ctx context.Context,
	tokenHash string,
) (UploadAuthorization, error) {
	internalContext := systemContext(ctx)
	link, err := installation.client.UploadLink.Query().Where(
		uploadlink.TokenHashEQ(tokenHash),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return UploadAuthorization{}, ErrUploadLinkInvalid
	}
	if err != nil {
		return UploadAuthorization{}, opaqueError("authorize Upload Link", err)
	}
	authorization := UploadAuthorization{
		LinkID: link.ID, EventID: link.EventID,
		TargetType: UploadTargetKind(link.TargetType.String()), TargetID: link.TargetID,
	}
	return authorization, nil
}

func uploadTargetOpen(
	ctx context.Context,
	client *ent.Client,
	authorization UploadAuthorization,
	now time.Time,
) (bool, error) {
	windows, err := client.ReopenWindow.Query().Where(
		reopenwindow.EventIDEQ(authorization.EventID),
		reopenwindow.TargetTypeEQ(reopenwindow.TargetType(authorization.TargetType)),
		reopenwindow.TargetIDEQ(authorization.TargetID),
	).All(ctx)
	if err != nil {
		return false, opaqueError("load active Reopen Window", err)
	}
	switch authorization.TargetType {
	case UploadTargetEntry:
		entry, queryErr := client.CompetitionEntry.Query().Where(
			competitionentry.IDEQ(authorization.TargetID),
			competitionentry.EventIDEQ(authorization.EventID),
		).Only(ctx)
		if ent.IsNotFound(queryErr) {
			return false, ErrUploadLinkInvalid
		}
		if queryErr != nil {
			return false, opaqueError("load Entry upload owner", queryErr)
		}
		for _, window := range windows {
			if entryReopenWindowActive(
				window.ExpiresAt, window.ClosedAt, window.CreatedAt, entry.UploadClosedAt, now,
			) {
				return true, nil
			}
		}
		if entry.Disposition == competitionentry.DispositionRejected ||
			!entry.UploadClosedAt.IsZero() {
			return false, nil
		}
		version, queryErr := client.SessionPublishedVersion.Query().Where(
			sessionpublishedversion.SessionIDEQ(entry.CompetitionSessionID),
		).Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).First(ctx)
		if ent.IsNotFound(queryErr) {
			return false, ErrUploadLinkInvalid
		}
		if queryErr != nil {
			return false, opaqueError("load Competition upload cutoff", queryErr)
		}
		return now.Before(version.SubmissionDeadline), nil
	case UploadTargetPresentation:
		for _, window := range windows {
			if reopenWindowActive(window.ExpiresAt, window.ClosedAt, now) {
				return true, nil
			}
		}
		version, queryErr := client.SessionPublishedVersion.Query().Where(
			sessionpublishedversion.SessionIDEQ(authorization.TargetID),
			sessionpublishedversion.TypeEQ(sessionpublishedversion.TypePresentation),
		).Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).First(ctx)
		if ent.IsNotFound(queryErr) {
			return false, ErrUploadLinkInvalid
		}
		if queryErr != nil {
			return false, opaqueError("load Presentation upload cutoff", queryErr)
		}
		started, queryErr := client.SessionRun.Query().Where(
			sessionrun.SessionIDEQ(authorization.TargetID),
		).Exist(ctx)
		if queryErr != nil {
			return false, opaqueError("load Presentation start", queryErr)
		}
		return !started && (version.UploadDeadline.IsZero() || now.Before(version.UploadDeadline)), nil
	default:
		return false, ErrUploadLinkInvalid
	}
}

func reopenWindowActive(expiresAt, closedAt, now time.Time) bool {
	return closedAt.IsZero() && now.Before(expiresAt)
}

func entryReopenWindowActive(
	expiresAt, closedAt, createdAt, uploadClosedAt, now time.Time,
) bool {
	return reopenWindowActive(expiresAt, closedAt, now) &&
		(uploadClosedAt.IsZero() || createdAt.After(uploadClosedAt))
}

// SaveAttachmentVersion appends metadata and its credential-attributed Audit Entry.
func (transaction *CommandTx) SaveAttachmentVersion(
	ctx context.Context,
	params SaveAttachmentVersionParams,
) (AttachmentVersion, error) {
	internalContext := systemContext(ctx)
	if params.UploaderType == "UploadLink" {
		link, err := transaction.transaction.UploadLink.Query().Where(
			uploadlink.IDEQ(params.UploaderID),
			uploadlink.EventIDEQ(params.Authorization.EventID),
			uploadlink.TargetTypeEQ(uploadlink.TargetType(params.Authorization.TargetType)),
			uploadlink.TargetIDEQ(params.Authorization.TargetID),
			uploadlink.RevokedAtIsNil(),
		).Only(internalContext)
		if ent.IsNotFound(err) {
			return AttachmentVersion{}, ErrUploadLinkInvalid
		}
		if err != nil {
			return AttachmentVersion{}, opaqueError("recheck Upload Link", err)
		}
		open, err := uploadTargetOpen(
			internalContext, transaction.transaction.Client(), UploadAuthorization{
				LinkID: link.ID, EventID: link.EventID,
				TargetType: UploadTargetKind(link.TargetType.String()), TargetID: link.TargetID,
			}, params.Now,
		)
		if err != nil {
			return AttachmentVersion{}, err
		}
		if !open {
			return AttachmentVersion{}, ErrUploadClosed
		}
	} else if err := transaction.validateUploadTarget(
		internalContext, params.Authorization.EventID,
		params.Authorization.TargetType, params.Authorization.TargetID,
	); err != nil {
		return AttachmentVersion{}, err
	}
	logical, err := transaction.transaction.Attachment.Query().Where(
		attachment.EventIDEQ(params.Authorization.EventID),
		attachment.OwnerTypeEQ(attachment.OwnerType(params.Authorization.TargetType)),
		attachment.OwnerIDEQ(params.Authorization.TargetID),
		attachment.NameEQ(params.Name),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		logical, err = transaction.transaction.Attachment.Create().
			SetEventID(params.Authorization.EventID).
			SetOwnerType(attachment.OwnerType(params.Authorization.TargetType)).
			SetOwnerID(params.Authorization.TargetID).
			SetName(params.Name).
			SetCreatedAt(params.Now).
			Save(internalContext)
	}
	if err != nil {
		return AttachmentVersion{}, opaqueError("load Attachment", err)
	}
	latest, err := transaction.transaction.AttachmentVersion.Query().Where(
		attachmentversion.AttachmentIDEQ(logical.ID),
	).Order(ent.Desc(attachmentversion.FieldVersion)).First(internalContext)
	version := 1
	if err == nil {
		version = latest.Version + 1
	} else if !ent.IsNotFound(err) {
		return AttachmentVersion{}, opaqueError("load latest Attachment Version", err)
	}
	existingVersions, err := transaction.transaction.AttachmentVersion.Query().
		Where(attachmentversion.HasAttachmentWith(
			attachment.EventIDEQ(params.Authorization.EventID),
			attachment.OwnerTypeEQ(attachment.OwnerType(params.Authorization.TargetType)),
			attachment.OwnerIDEQ(params.Authorization.TargetID),
		)).
		Count(internalContext)
	if err != nil {
		return AttachmentVersion{}, opaqueError("count owner Attachment Versions", err)
	}
	primary := existingVersions == 0
	create := transaction.transaction.AttachmentVersion.Create().
		SetAttachmentID(logical.ID).
		SetVersion(version).
		SetOriginalFilename(params.OriginalFilename).
		SetMediaType(params.MediaType).
		SetSizeBytes(params.SizeBytes).
		SetSha256(params.SHA256).
		SetStorageKey(params.StorageKey).
		SetUploaderType(attachmentversion.UploaderType(params.UploaderType)).
		SetUploaderID(params.UploaderID).
		SetReleaseEligibility(attachmentversion.ReleaseEligibility(params.ReleaseEligibility)).
		SetPrimary(primary).
		SetCreatedAt(params.Now)
	created, err := create.Save(internalContext)
	if err != nil {
		return AttachmentVersion{}, opaqueError("create Attachment Version", err)
	}
	if params.Authorization.TargetType == UploadTargetEntry {
		if _, updateErr := transaction.transaction.CompetitionEntry.UpdateOneID(params.Authorization.TargetID).
			AddContentRevision(1).
			AddRevision(1).
			Save(internalContext); updateErr != nil {
			return AttachmentVersion{}, opaqueError("invalidate Entry review after upload", updateErr)
		}
	}
	return attachmentVersion(logical, created), nil
}

// LoadAttachmentVersion returns one exact immutable version for its Event.
func (installation *SQLite) LoadAttachmentVersion(
	ctx context.Context,
	eventID, versionID int,
) (AttachmentVersion, error) {
	internalContext := systemContext(ctx)
	version, err := installation.client.AttachmentVersion.Query().
		Where(attachmentversion.IDEQ(versionID)).
		WithAttachment().
		Only(internalContext)
	if ent.IsNotFound(err) {
		return AttachmentVersion{}, ErrUploadTargetNotFound
	}
	if err != nil {
		return AttachmentVersion{}, opaqueError("load Attachment Version", err)
	}
	logical, err := version.Edges.AttachmentOrErr()
	if err != nil || logical.EventID != eventID {
		return AttachmentVersion{}, ErrUploadTargetNotFound
	}
	return attachmentVersion(logical, version), nil
}

func attachmentVersion(logical *ent.Attachment, version *ent.AttachmentVersion) AttachmentVersion {
	return AttachmentVersion{
		ID: version.ID, AttachmentID: logical.ID, Version: version.Version,
		EventID:   logical.EventID,
		OwnerType: UploadTargetKind(logical.OwnerType.String()), OwnerID: logical.OwnerID,
		Name: logical.Name, OriginalFilename: version.OriginalFilename, MediaType: version.MediaType,
		SizeBytes: version.SizeBytes, SHA256: version.Sha256, StorageKey: version.StorageKey,
		UploaderType: version.UploaderType.String(), UploaderID: version.UploaderID,
		Primary: version.Primary, Final: version.Final,
		ReadinessRevision:  version.ReadinessRevision,
		ReleaseEligibility: AttachmentReleaseEligibility(version.ReleaseEligibility.String()),
		ReleaseHold:        version.ReleaseHold,
		ReleaseRevision:    version.ReleaseRevision,
		CreatedAt:          version.CreatedAt,
	}
}

// RevokeUploadLink immediately invalidates one Event-owned credential.
func (transaction *CommandTx) RevokeUploadLink(
	ctx context.Context,
	eventID, linkID int,
	now time.Time,
) error {
	internalContext := systemContext(ctx)
	link, err := transaction.transaction.UploadLink.Query().Where(
		uploadlink.IDEQ(linkID), uploadlink.EventIDEQ(eventID),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return ErrUploadTargetNotFound
	}
	if err != nil {
		return opaqueError("load Upload Link", err)
	}
	if link.RevokedAt.IsZero() {
		if _, err = link.Update().SetRevokedAt(now).Save(internalContext); err != nil {
			return opaqueError("revoke Upload Link", err)
		}
	}
	return nil
}

// CreateReopenWindow grants one existing target a bounded upload exception.
func (transaction *CommandTx) CreateReopenWindow(
	ctx context.Context,
	eventID int,
	targetType UploadTargetKind,
	targetID int,
	reason string,
	expiresAt time.Time,
	actorAccountID int,
	now time.Time,
) (ReopenWindow, error) {
	internalContext := systemContext(ctx)
	if err := transaction.validateUploadTarget(internalContext, eventID, targetType, targetID); err != nil {
		return ReopenWindow{}, err
	}
	created, err := transaction.transaction.ReopenWindow.Create().
		SetEventID(eventID).
		SetTargetType(reopenwindow.TargetType(targetType)).
		SetTargetID(targetID).
		SetReason(reason).
		SetExpiresAt(expiresAt).
		SetCreatedByAccountID(actorAccountID).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		Save(internalContext)
	if err != nil {
		return ReopenWindow{}, opaqueError("create Reopen Window", err)
	}
	return reopenWindow(created), nil
}

// UpdateReopenWindow closes early or extends one bounded exception.
func (transaction *CommandTx) UpdateReopenWindow(
	ctx context.Context,
	eventID, windowID, expectedRevision int,
	expiresAt time.Time,
	closeWindow bool,
	now time.Time,
) (ReopenWindow, error) {
	internalContext := systemContext(ctx)
	window, err := transaction.transaction.ReopenWindow.Query().Where(
		reopenwindow.IDEQ(windowID), reopenwindow.EventIDEQ(eventID),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return ReopenWindow{}, ErrUploadTargetNotFound
	}
	if err != nil {
		return ReopenWindow{}, opaqueError("load Reopen Window", err)
	}
	if window.Revision != expectedRevision {
		return ReopenWindow{}, ErrReopenWindowRevision
	}
	if !closeWindow && !expiresAt.After(window.ExpiresAt) {
		return ReopenWindow{}, ErrReopenWindowExtension
	}
	update := window.Update().AddRevision(1).SetUpdatedAt(now)
	if closeWindow {
		update.SetClosedAt(now)
	} else {
		update.SetExpiresAt(expiresAt)
	}
	updated, err := update.Save(internalContext)
	if err != nil {
		return ReopenWindow{}, opaqueError("update Reopen Window", err)
	}
	return reopenWindow(updated), nil
}

func reopenWindow(window *ent.ReopenWindow) ReopenWindow {
	return ReopenWindow{
		ID: window.ID, EventID: window.EventID,
		TargetType: UploadTargetKind(window.TargetType.String()),
		TargetID:   window.TargetID, Reason: window.Reason, ExpiresAt: window.ExpiresAt,
		ClosedAt: window.ClosedAt, CreatedAt: window.CreatedAt,
		CreatedByAccountID: window.CreatedByAccountID, Revision: window.Revision,
	}
}

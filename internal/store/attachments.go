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
	"github.com/dotwaffle/beamers/ent/predicate"
	"github.com/dotwaffle/beamers/ent/reopenwindow"
	"github.com/dotwaffle/beamers/ent/session"
	"github.com/dotwaffle/beamers/ent/sessionpublishedversion"
	"github.com/dotwaffle/beamers/ent/sessionrun"
)

var (
	// ErrUploadTargetNotFound hides unknown and cross-Event upload owners.
	ErrUploadTargetNotFound = errors.New("upload target not found")
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

// UploadAuthorization identifies one exact submitted-content owner.
type UploadAuthorization struct {
	EventID, TargetID int
	TargetType        UploadTargetKind
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

// AccountOwnsUploadTarget reports exact Account ownership without exposing the target.
func (installation *SQLite) AccountOwnsUploadTarget(
	ctx context.Context,
	eventID int,
	targetType UploadTargetKind,
	targetID, accountID int,
) (bool, error) {
	return accountOwnsUploadTarget(
		systemContext(ctx),
		installation.client,
		eventID,
		targetType,
		targetID,
		accountID,
	)
}

func accountOwnsUploadTarget(
	ctx context.Context,
	client *ent.Client,
	eventID int,
	targetType UploadTargetKind,
	targetID, accountID int,
) (bool, error) {
	switch targetType {
	case UploadTargetEntry:
		found, err := client.CompetitionEntry.Query().
			Where(
				competitionentry.IDEQ(targetID),
				competitionentry.EventIDEQ(eventID),
				competitionentry.SubmitterAccountIDEQ(accountID),
			).
			Exist(ctx)
		if err != nil {
			return false, opaqueError("authorize Account Attachment target", err)
		}
		return found, nil
	case UploadTargetPresentation:
		found, err := client.Session.Query().
			Where(
				session.IDEQ(targetID),
				session.EventIDEQ(eventID),
				session.SubmitterAccountIDEQ(accountID),
			).
			Exist(ctx)
		if err != nil {
			return false, opaqueError("authorize Account Attachment target", err)
		}
		if !found {
			return false, nil
		}
		_, _, _, err = loadPresentationSubmission(
			ctx,
			client,
			eventID,
			targetID,
		)
		if errors.Is(err, ErrPresentationNotFound) {
			return false, nil
		}
		return err == nil, err
	default:
		return false, nil
	}
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

// CompetitionAttachmentCrewState is private Entry file and release state.
type CompetitionAttachmentCrewState struct {
	Versions           []AttachmentVersion
	ReopenWindows      []ReopenWindow
	EventRelease       AttachmentReleaseConfiguration
	CompetitionRelease CompetitionAttachmentReleaseConfiguration
}

// AccountAttachmentState contains only submitter-visible file metadata.
type AccountAttachmentState struct {
	Versions      []AccountAttachmentVersion
	ReopenWindows []AccountReopenWindow
}

// AccountAttachmentVersion is submitter-visible immutable file metadata.
type AccountAttachmentVersion struct {
	OwnerID, Version       int
	OwnerType              UploadTargetKind
	Name, OriginalFilename string
	SHA256                 string
}

// AccountReopenWindow is submitter-visible access state without Crew reasons.
type AccountReopenWindow struct {
	TargetID            int
	TargetType          UploadTargetKind
	ExpiresAt, ClosedAt time.Time
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
			return opaqueError("validate Entry upload target", err)
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
			return opaqueError("validate Presentation upload target", err)
		}
		version, err := found.QueryPublishedVersions().
			Order(ent.Desc(sessionpublishedversion.FieldPublishedRevision)).
			First(ctx)
		if ent.IsNotFound(err) ||
			(err == nil && version.Type != sessionpublishedversion.TypePresentation) {
			return ErrUploadTargetNotFound
		}
		if err != nil {
			return opaqueError("validate published Presentation upload target", err)
		}
	default:
		return ErrUploadTargetNotFound
	}
	return nil
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
			return false, ErrUploadTargetNotFound
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
			return false, ErrUploadTargetNotFound
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
			return false, ErrUploadTargetNotFound
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
		return false, ErrUploadTargetNotFound
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
	switch params.UploaderType {
	case "Account":
		owned, err := accountOwnsUploadTarget(
			internalContext,
			transaction.transaction.Client(),
			params.Authorization.EventID,
			params.Authorization.TargetType,
			params.Authorization.TargetID,
			params.UploaderID,
		)
		if err != nil {
			return AttachmentVersion{}, err
		}
		if !owned {
			return AttachmentVersion{}, ErrUploadTargetNotFound
		}
		open, err := uploadTargetOpen(
			internalContext,
			transaction.transaction.Client(),
			params.Authorization,
			params.Now,
		)
		if err != nil {
			return AttachmentVersion{}, err
		}
		if !open {
			return AttachmentVersion{}, ErrUploadClosed
		}
	case "Crew":
		if err := transaction.validateUploadTarget(
			internalContext, params.Authorization.EventID,
			params.Authorization.TargetType, params.Authorization.TargetID,
		); err != nil {
			return AttachmentVersion{}, err
		}
	default:
		return AttachmentVersion{}, ErrUploadTargetNotFound
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

// LoadCompetitionAttachmentCrewState returns private file state without file bytes.
func (installation *SQLite) LoadCompetitionAttachmentCrewState(
	ctx context.Context,
	eventID, sessionID int,
) (CompetitionAttachmentCrewState, error) {
	internalContext := systemContext(ctx)
	foundEvent, err := installation.client.Event.Query().
		Where(event.IDEQ(eventID)).
		Only(internalContext)
	if ent.IsNotFound(err) {
		return CompetitionAttachmentCrewState{}, ErrUploadTargetNotFound
	}
	if err != nil {
		return CompetitionAttachmentCrewState{},
			opaqueError("load Event Attachment crew state", err)
	}
	foundSession, err := installation.client.Session.Query().
		Where(session.IDEQ(sessionID), session.EventIDEQ(eventID)).
		Only(internalContext)
	if ent.IsNotFound(err) {
		return CompetitionAttachmentCrewState{}, ErrUploadTargetNotFound
	}
	if err != nil {
		return CompetitionAttachmentCrewState{},
			opaqueError("load Competition Attachment crew state", err)
	}
	entries, err := installation.client.CompetitionEntry.Query().
		Where(
			competitionentry.EventIDEQ(eventID),
			competitionentry.CompetitionSessionIDEQ(sessionID),
		).
		All(internalContext)
	if err != nil {
		return CompetitionAttachmentCrewState{},
			opaqueError("load Competition Attachment owners", err)
	}
	entryIDs := make([]int, 0, len(entries))
	for _, entry := range entries {
		entryIDs = append(entryIDs, entry.ID)
	}
	result := CompetitionAttachmentCrewState{
		EventRelease:       eventAttachmentRelease(foundEvent),
		CompetitionRelease: competitionAttachmentRelease(foundSession),
	}
	if len(entryIDs) == 0 {
		return result, nil
	}
	versions, err := installation.client.AttachmentVersion.Query().
		Where(attachmentversion.HasAttachmentWith(
			attachment.EventIDEQ(eventID),
			attachment.OwnerTypeEQ(attachment.OwnerTypeEntry),
			attachment.OwnerIDIn(entryIDs...),
		)).
		WithAttachment().
		Order(ent.Asc(attachmentversion.FieldID)).
		All(internalContext)
	if err != nil {
		return CompetitionAttachmentCrewState{},
			opaqueError("load Competition Attachment Versions", err)
	}
	result.Versions = make([]AttachmentVersion, 0, len(versions))
	for _, version := range versions {
		logical, edgeErr := version.Edges.AttachmentOrErr()
		if edgeErr != nil {
			return CompetitionAttachmentCrewState{},
				opaqueError("load Competition Attachment owner", edgeErr)
		}
		result.Versions = append(result.Versions, attachmentVersion(logical, version))
	}
	windows, err := installation.client.ReopenWindow.Query().
		Where(
			reopenwindow.EventIDEQ(eventID),
			reopenwindow.TargetTypeEQ(reopenwindow.TargetTypeEntry),
			reopenwindow.TargetIDIn(entryIDs...),
		).
		Order(ent.Asc(reopenwindow.FieldID)).
		All(internalContext)
	if err != nil {
		return CompetitionAttachmentCrewState{},
			opaqueError("load Competition Reopen Windows", err)
	}
	result.ReopenWindows = make([]ReopenWindow, 0, len(windows))
	for _, window := range windows {
		result.ReopenWindows = append(result.ReopenWindows, reopenWindow(window))
	}
	return result, nil
}

// LoadAccountAttachmentState returns file metadata for only one Account's submitted content.
func (installation *SQLite) LoadAccountAttachmentState(
	ctx context.Context,
	accountID int,
) (AccountAttachmentState, error) {
	internalContext := systemContext(ctx)
	entries, err := installation.client.CompetitionEntry.Query().
		Where(competitionentry.SubmitterAccountIDEQ(accountID)).
		Order(ent.Asc(competitionentry.FieldID)).
		All(internalContext)
	if err != nil {
		return AccountAttachmentState{}, opaqueError(
			"load Account Attachment owners",
			err,
		)
	}
	entryIDs := make([]int, 0, len(entries))
	for _, entry := range entries {
		entryIDs = append(entryIDs, entry.ID)
	}
	presentations, err := installation.LoadAccountPresentationSubmissions(
		internalContext,
		accountID,
	)
	if err != nil {
		return AccountAttachmentState{}, err
	}
	presentationIDs := make([]int, 0, len(presentations))
	for _, found := range presentations {
		presentationIDs = append(presentationIDs, found.SessionID)
	}
	if len(entryIDs) == 0 && len(presentationIDs) == 0 {
		return AccountAttachmentState{}, nil
	}
	attachmentOwners := make([]predicate.Attachment, 0, 2)
	if len(entryIDs) != 0 {
		attachmentOwners = append(attachmentOwners, attachment.And(
			attachment.OwnerTypeEQ(attachment.OwnerTypeEntry),
			attachment.OwnerIDIn(entryIDs...),
		))
	}
	if len(presentationIDs) != 0 {
		attachmentOwners = append(attachmentOwners, attachment.And(
			attachment.OwnerTypeEQ(attachment.OwnerTypePresentation),
			attachment.OwnerIDIn(presentationIDs...),
		))
	}
	versions, err := installation.client.AttachmentVersion.Query().
		Where(attachmentversion.HasAttachmentWith(
			attachment.Or(attachmentOwners...),
		)).
		WithAttachment().
		Order(ent.Asc(attachmentversion.FieldID)).
		All(internalContext)
	if err != nil {
		return AccountAttachmentState{}, opaqueError(
			"load Account Attachment Versions",
			err,
		)
	}
	result := AccountAttachmentState{
		Versions: make([]AccountAttachmentVersion, 0, len(versions)),
	}
	for _, version := range versions {
		logical, edgeErr := version.Edges.AttachmentOrErr()
		if edgeErr != nil {
			return AccountAttachmentState{}, opaqueError(
				"load Account Attachment owner",
				edgeErr,
			)
		}
		result.Versions = append(result.Versions, AccountAttachmentVersion{
			OwnerID: logical.OwnerID, Version: version.Version,
			OwnerType: UploadTargetKind(logical.OwnerType.String()),
			Name:      logical.Name, OriginalFilename: version.OriginalFilename,
			SHA256: version.Sha256,
		})
	}
	windowOwners := make([]predicate.ReopenWindow, 0, 2)
	if len(entryIDs) != 0 {
		windowOwners = append(windowOwners, reopenwindow.And(
			reopenwindow.TargetTypeEQ(reopenwindow.TargetTypeEntry),
			reopenwindow.TargetIDIn(entryIDs...),
		))
	}
	if len(presentationIDs) != 0 {
		windowOwners = append(windowOwners, reopenwindow.And(
			reopenwindow.TargetTypeEQ(reopenwindow.TargetTypePresentation),
			reopenwindow.TargetIDIn(presentationIDs...),
		))
	}
	windows, err := installation.client.ReopenWindow.Query().
		Where(reopenwindow.Or(windowOwners...)).
		Order(ent.Asc(reopenwindow.FieldID)).
		All(internalContext)
	if err != nil {
		return AccountAttachmentState{}, opaqueError(
			"load Account Reopen Windows",
			err,
		)
	}
	result.ReopenWindows = make([]AccountReopenWindow, 0, len(windows))
	for _, window := range windows {
		result.ReopenWindows = append(result.ReopenWindows, AccountReopenWindow{
			TargetID: window.TargetID, TargetType: UploadTargetKind(window.TargetType.String()),
			ExpiresAt: window.ExpiresAt, ClosedAt: window.ClosedAt,
		})
	}
	return result, nil
}

// ReferencedAttachmentStorageKeys lists every committed immutable file key.
func (installation *SQLite) ReferencedAttachmentStorageKeys(
	ctx context.Context,
) ([]string, error) {
	var keys []string
	if err := installation.client.AttachmentVersion.Query().
		Unique(true).
		Select(attachmentversion.FieldStorageKey).
		Scan(systemContext(ctx), &keys); err != nil {
		return nil, opaqueError("list referenced Attachment storage keys", err)
	}
	return keys, nil
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

// Package attachments owns Account and Crew uploads and immutable files.
package attachments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
)

var (
	// ErrProducerRequired means an Attachment command lacked Producer authority.
	ErrProducerRequired = errors.New("producer authority required")
	// ErrUploadTargetNotFound hides unknown and cross-Event upload owners.
	ErrUploadTargetNotFound = store.ErrUploadTargetNotFound
	// ErrInvalidInput means an Attachment request contains unsafe values.
	ErrInvalidInput = errors.New("invalid Attachment input")
	// ErrInvalidName identifies an invalid user-editable Attachment name.
	ErrInvalidName = errors.New("invalid Attachment name")
	// ErrInvalidFilename identifies an invalid uploaded filename.
	ErrInvalidFilename = errors.New("invalid Attachment filename")
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrUploadClosed means the fixed cutoff has arrived.
	ErrUploadClosed = store.ErrUploadClosed
	// ErrAttachmentTooLarge protects installation storage from unbounded requests.
	ErrAttachmentTooLarge = errors.New("attachment exceeds size limit")
	// ErrReopenWindowRevision means an update used stale window state.
	ErrReopenWindowRevision = store.ErrReopenWindowRevision
	// ErrReopenWindowExtension means a requested expiry did not extend the window.
	ErrReopenWindowExtension = store.ErrReopenWindowExtension
	// ErrReopenWindowExpiry means an extension is not future-bounded.
	ErrReopenWindowExpiry = errors.New("reopen window expiry must be future-bounded")
	// ErrReopenWindowInactive means historical window state is immutable.
	ErrReopenWindowInactive = store.ErrReopenWindowInactive
	// ErrReleaseRevision means Attachment release state changed after observation.
	ErrReleaseRevision = store.ErrAttachmentReleaseRevision
	// ErrReleasePolicy means an Attachment Release Policy is invalid.
	ErrReleasePolicy = store.ErrAttachmentReleasePolicy
	// ErrReleaseCueBlocked means unresolved Entries block the Event cue.
	ErrReleaseCueBlocked = store.ErrAttachmentReleaseCueBlocked
	// ErrReleaseCuePreviewChanged means release impact changed after review.
	ErrReleaseCuePreviewChanged = store.ErrAttachmentReleaseCuePreviewChanged
	// ErrNotReleased hides unknown and unavailable attendee files.
	ErrNotReleased = store.ErrAttachmentNotReleased
)

const (
	maxAttachmentBytes = 64 << 20
	maxReopenDuration  = 7 * 24 * time.Hour
)

// TargetKind is the closed owner vocabulary for scoped attachments.
type TargetKind = store.UploadTargetKind

// ReleaseEligibility is the uploader-selected public-release choice.
type ReleaseEligibility = store.AttachmentReleaseEligibility

const (
	// TargetPresentation scopes an upload to one Presentation.
	TargetPresentation = store.UploadTargetPresentation
	// TargetEntry scopes an upload to one Competition Entry.
	TargetEntry = store.UploadTargetEntry
	// ReleasePublic permits policy-governed public release.
	ReleasePublic = store.AttachmentReleasePublic
	// ReleaseCrewOnly permanently excludes a version from public release.
	ReleaseCrewOnly = store.AttachmentReleaseCrewOnly
)

// Version is one immutable uploaded file revision.
type Version struct {
	ID                 int                `json:"id"`
	AttachmentID       int                `json:"attachment_id"`
	Version            int                `json:"version"`
	EventID            int                `json:"event_id"`
	OwnerID            int                `json:"owner_id"`
	OwnerType          TargetKind         `json:"owner_type"`
	Name               string             `json:"name"`
	OriginalFilename   string             `json:"original_filename"`
	MediaType          string             `json:"media_type,omitempty"`
	SizeBytes          int64              `json:"size_bytes"`
	SHA256             string             `json:"sha256"`
	UploaderType       string             `json:"uploader_type"`
	UploaderID         int                `json:"uploader_id"`
	Primary            bool               `json:"primary"`
	Final              bool               `json:"final"`
	ReadinessRevision  int                `json:"readiness_revision"`
	ReleaseEligibility ReleaseEligibility `json:"release_eligibility"`
	ReleaseHold        bool               `json:"release_hold"`
	ReleaseRevision    int                `json:"release_revision"`
	CreatedAt          time.Time          `json:"created_at"`
}

// CrewState is private Competition Attachment state safe for Backstage rendering.
type CrewState struct {
	Versions           []Version
	ReopenWindows      []store.ReopenWindow
	EventRelease       store.AttachmentReleaseConfiguration
	CompetitionRelease store.CompetitionAttachmentReleaseConfiguration
}

// AccountState is private metadata for only one Account's submitted content.
type AccountState struct {
	Versions      []AccountVersion
	ReopenWindows []AccountReopenWindow
}

// AccountVersion is submitter-visible immutable file metadata.
type AccountVersion struct {
	OwnerID, Version       int
	OwnerType              TargetKind
	Name, OriginalFilename string
	SHA256                 string
}

// AccountReopenWindow is submitter-visible access state without Crew reasons.
type AccountReopenWindow = store.AccountReopenWindow

// CrewUploadInput identifies an on-behalf-of upload.
type CrewUploadInput struct {
	EventID, TargetID                            int
	TargetType                                   TargetKind
	CommandID, Name, OriginalFilename, MediaType string
	Body                                         io.Reader
	CrewOnly                                     bool
}

// AccountUploadInput identifies one upload owned by the signed-in Submitter Account.
type AccountUploadInput struct {
	EventID, TargetID                            int
	TargetType                                   TargetKind
	CommandID, Name, OriginalFilename, MediaType string
	Body                                         io.Reader
	CrewOnly                                     bool
}

// ReopenInput creates one bounded target-specific exception.
type ReopenInput struct {
	EventID    int        `json:"event_id"`
	TargetID   int        `json:"target_id"`
	TargetType TargetKind `json:"target_type"`
	Reason     string     `json:"reason"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CommandID  string     `json:"command_id"`
}

// UpdateReopenInput closes early or extends one existing window.
type UpdateReopenInput struct {
	EventID          int       `json:"event_id"`
	WindowID         int       `json:"window_id"`
	ExpectedRevision int       `json:"expected_revision"`
	ExpiresAt        time.Time `json:"expires_at"`
	Close            bool      `json:"close"`
	CommandID        string    `json:"command_id"`
}

// ReleasePolicy selects when eligible Final Versions become public.
type ReleasePolicy = store.AttachmentReleasePolicy

const (
	// ReleaseOnLive releases after the owning Session starts.
	ReleaseOnLive = store.AttachmentReleaseOnLive
	// ReleaseOnEnded releases after the owning Session ends.
	ReleaseOnEnded = store.AttachmentReleaseOnEnded
	// ReleaseOnEventCue releases after the Producer fires the Event cue.
	ReleaseOnEventCue = store.AttachmentReleaseOnEventCue
)

// ReleaseCuePreview is the exact Event cue impact reviewed by a Producer.
type ReleaseCuePreview = store.AttachmentReleaseCuePreview

// ConfigureEventReleaseInput changes the Event default.
type ConfigureEventReleaseInput struct {
	EventID          int           `json:"event_id"`
	ExpectedRevision int           `json:"expected_revision"`
	Policy           ReleasePolicy `json:"policy"`
	CueSessionID     int           `json:"cue_session_id"`
	CommandID        string        `json:"command_id"`
}

// ConfigureCompetitionReleaseInput changes one optional Competition override.
type ConfigureCompetitionReleaseInput struct {
	EventID          int           `json:"event_id"`
	SessionID        int           `json:"session_id"`
	ExpectedRevision int           `json:"expected_revision"`
	Policy           ReleasePolicy `json:"policy"`
	Override         bool          `json:"override"`
	CommandID        string        `json:"command_id"`
}

// SetVersionReleaseInput changes a Producer hold without changing uploader eligibility.
type SetVersionReleaseInput struct {
	EventID          int    `json:"event_id"`
	VersionID        int    `json:"version_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Hold             bool   `json:"hold"`
	CommandID        string `json:"command_id"`
}

// FireReleaseCueInput fires one Event-wide release cue.
type FireReleaseCueInput struct {
	EventID            int    `json:"event_id"`
	ExpectedRevision   int    `json:"expected_revision"`
	PreviewFingerprint string `json:"preview_fingerprint"`
	Confirmed          bool   `json:"confirmed"`
	CommandID          string `json:"command_id"`
}

// ReleasedVersion is attendee-safe immutable file metadata.
type ReleasedVersion struct {
	ID               int        `json:"id"`
	AttachmentID     int        `json:"attachment_id"`
	Version          int        `json:"version"`
	OwnerID          int        `json:"owner_id"`
	OwnerType        TargetKind `json:"owner_type"`
	Name             string     `json:"name"`
	OriginalFilename string     `json:"original_filename"`
	MediaType        string     `json:"media_type,omitempty"`
	SizeBytes        int64      `json:"size_bytes"`
	SHA256           string     `json:"sha256"`
}

// Service owns Attachment commands.
type Service struct {
	storage        *store.SQLite
	root           string
	now            func() time.Time
	notifySchedule func()
	uploads        sync.Mutex
}

// New creates an Attachment Service with explicit dependencies.
func New(
	ctx context.Context,
	storage *store.SQLite,
	root string,
	now func() time.Time,
	notifySchedule func(),
) (*Service, error) {
	if storage == nil {
		return nil, errors.New("attachment storage is required")
	}
	if root == "" {
		return nil, errors.New("attachment store root is required")
	}
	if now == nil {
		return nil, errors.New("attachment clock is required")
	}
	service := &Service{
		storage: storage, root: root, now: now,
		notifySchedule: notifySchedule,
	}
	if err := service.reconcileFiles(ctx); err != nil {
		return nil, err
	}
	return service, nil
}

// AuthorizeAccountUpload confirms exact ownership before a large body is parsed.
func (service *Service) AuthorizeAccountUpload(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	targetType TargetKind,
	targetID int,
) error {
	if actor.ID <= 0 || eventID <= 0 || targetID <= 0 ||
		(targetType != TargetEntry && targetType != TargetPresentation) {
		return ErrUploadTargetNotFound
	}
	owned, err := service.storage.AccountOwnsUploadTarget(
		actor.Context(ctx),
		eventID,
		targetType,
		targetID,
		actor.ID,
	)
	if err != nil {
		return err
	}
	if !owned {
		return ErrUploadTargetNotFound
	}
	return nil
}

// UploadForCrew stores one file while preserving the authenticated Account actor.
func (service *Service) UploadForCrew(
	ctx context.Context,
	actor auth.Account,
	input CrewUploadInput,
) (Version, error) {
	if input.EventID <= 0 || input.TargetID <= 0 ||
		(input.TargetType != TargetPresentation && input.TargetType != TargetEntry) {
		return Version{}, ErrInvalidInput
	}
	if err := validateAttachmentInput(input.Name, input.OriginalFilename, input.Body); err != nil {
		return Version{}, err
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return Version{}, err
	}
	authorization := store.UploadAuthorization{
		EventID: input.EventID, TargetType: input.TargetType, TargetID: input.TargetID,
	}
	return service.storeVersion(
		actor.Context(ctx),
		authz.Event(input.EventID).Demanding(authz.ManageAttachments),
		authorization, input.CommandID, input.Name, input.OriginalFilename,
		input.MediaType, input.Body, "Crew", actor.ID, input.CrewOnly,
	)
}

// UploadForAccount stores one file for Account-owned submitted content.
func (service *Service) UploadForAccount(
	ctx context.Context,
	actor auth.Account,
	input AccountUploadInput,
) (Version, error) {
	if actor.ID <= 0 || input.EventID <= 0 || input.TargetID <= 0 ||
		(input.TargetType != TargetEntry && input.TargetType != TargetPresentation) {
		return Version{}, ErrInvalidInput
	}
	if err := validateAttachmentInput(input.Name, input.OriginalFilename, input.Body); err != nil {
		return Version{}, err
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return Version{}, err
	}
	return service.storeVersion(
		actor.Context(ctx),
		authz.Event(input.EventID),
		store.UploadAuthorization{
			EventID: input.EventID, TargetType: input.TargetType, TargetID: input.TargetID,
		},
		input.CommandID,
		input.Name,
		input.OriginalFilename,
		input.MediaType,
		input.Body,
		"Account",
		actor.ID,
		input.CrewOnly,
	)
}

func (service *Service) storeVersion(
	ctx context.Context,
	facts authz.Facts,
	authorization store.UploadAuthorization,
	commandID, name, originalFilename, mediaType string,
	body io.Reader,
	uploaderType string,
	uploaderID int,
	crewOnly bool,
) (Version, error) {
	// ponytail: one lock serializes uploads; shard by digest if upload throughput matters.
	service.uploads.Lock()
	defer service.uploads.Unlock()

	storedFile, err := service.storeFile(body)
	if err != nil {
		return Version{}, err
	}
	now := service.now().UTC()
	params := store.SaveAttachmentVersionParams{
		Authorization: authorization,
		Name:          strings.TrimSpace(name), OriginalFilename: filepath.Base(originalFilename),
		MediaType: strings.TrimSpace(mediaType), SizeBytes: storedFile.size,
		SHA256: storedFile.digest, StorageKey: storedFile.key,
		UploaderType: uploaderType, UploaderID: uploaderID, Now: now,
	}
	params.ReleaseEligibility = ReleasePublic
	if crewOnly {
		params.ReleaseEligibility = ReleaseCrewOnly
	}
	payload, err := json.Marshal(struct {
		EventID      int    `json:"event_id"`
		TargetID     int    `json:"target_id"`
		TargetType   string `json:"target_type"`
		Name         string `json:"name"`
		Filename     string `json:"filename"`
		MediaType    string `json:"media_type,omitempty"`
		SizeBytes    int64  `json:"size_bytes"`
		SHA256       string `json:"sha256"`
		UploaderType string `json:"uploader_type"`
		UploaderID   int    `json:"uploader_id"`
		CrewOnly     bool   `json:"crew_only"`
	}{
		EventID: authorization.EventID, TargetID: authorization.TargetID,
		TargetType: string(authorization.TargetType),
		Name:       params.Name, Filename: params.OriginalFilename, MediaType: params.MediaType,
		SizeBytes: params.SizeBytes, SHA256: params.SHA256,
		UploaderType: params.UploaderType, UploaderID: params.UploaderID,
		CrewOnly: crewOnly,
	})
	if err != nil {
		return Version{}, errors.New("encode Attachment upload command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: uploaderID,
		CommandID:      commandID, PayloadHash: command.PayloadHash(string(payload)),
		Action: "UploadAttachment", TargetType: string(authorization.TargetType),
		TargetID: strconv.Itoa(authorization.TargetID), Now: now,
	}
	created, executeErr := command.Execute(ctx, command.Plan[Version]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			// The Crew caller demands ManageAttachments through the row's
			// TargetCapabilities; the Account caller supplies plain Event
			// facts and is admitted here because its rule is upload-target
			// ownership, which SaveAttachmentVersion enforces.
			Facts: facts, Refusals: attachmentUploadRejections,
		},
		Replay: func(outcome string) (Version, error) {
			var stored Version
			if err := decodeAttachmentUploadReceipt(outcome, &stored); err != nil {
				return Version{}, err
			}
			return stored, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Version], error) {
			// Re-sample the clock at the transactional authorization boundary so a
			// slow upload cannot commit across its fixed cutoff.
			params.Now = service.now().UTC()
			stored, saveErr := transaction.SaveAttachmentVersion(ctx, params)
			if saveErr != nil {
				rejection, rejected := attachmentUploadRejection(saveErr)
				if rejected {
					return command.Reject(Version{}, rejection, saveErr), nil
				}
				return command.Execution[Version]{}, saveErr
			}
			result := version(stored)
			outcome, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[Version]{}, errors.New("encode Attachment upload outcome")
			}
			return command.Success(result, string(outcome)).
				WithTargetID(strconv.Itoa(stored.ID)).
				WithAudit(store.AuditDetails{
					Note: string(authorization.TargetType) + " " + strconv.Itoa(authorization.TargetID),
				}), nil
		},
	})
	if executeErr == nil || !storedFile.installed {
		return created, executeErr
	}
	cleanupErr := service.removeIfUnreferenced(context.WithoutCancel(ctx), storedFile.key)
	return created, errors.Join(executeErr, cleanupErr)
}

// attachmentUploadRejections is the single source for Attachment upload
// rejection codes in both directions.
var attachmentUploadRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrProducerRequired, Code: "producer_required"},
		{Err: ErrUploadTargetNotFound, Code: "attachment_target_not_found"},
		{Err: ErrUploadClosed, Code: "upload_closed"},
	},
}

func attachmentUploadRejection(err error) (store.CommandRejection, bool) {
	return attachmentUploadRejections.Rejection(err)
}

func decodeAttachmentUploadReceipt(outcome string, target any) error {
	return attachmentUploadRejections.Restore(store.DecodeCommandReceipt(outcome, target))
}

type storedFile struct {
	key, digest string
	size        int64
	installed   bool
}

func (service *Service) storeFile(body io.Reader) (storedFile, error) {
	root := service.root
	temporaryDirectory := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(temporaryDirectory, 0o700); err != nil {
		return storedFile{}, errors.New("prepare Attachment storage")
	}
	temporary, err := os.CreateTemp(temporaryDirectory, "upload-*")
	if err != nil {
		return storedFile{}, errors.New("create temporary Attachment")
	}
	temporaryName := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(body, maxAttachmentBytes+1))
	if err != nil {
		_ = temporary.Close()
		return storedFile{}, errors.New("store Attachment bytes")
	}
	if size > maxAttachmentBytes {
		_ = temporary.Close()
		return storedFile{}, ErrAttachmentTooLarge
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return storedFile{}, errors.New("sync Attachment bytes")
	}
	if err = temporary.Close(); err != nil {
		return storedFile{}, errors.New("close Attachment bytes")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	key := filepath.Join("sha256", digest[:2], digest)
	destinationDirectory := filepath.Join(root, "sha256", digest[:2])
	destination := filepath.Join(root, key)
	if err = os.MkdirAll(destinationDirectory, 0o700); err != nil {
		return storedFile{}, errors.New("prepare content-addressed Attachment storage")
	}
	if _, statErr := os.Stat(destination); errors.Is(statErr, os.ErrNotExist) {
		if err = os.Rename(temporaryName, destination); err != nil {
			return storedFile{}, errors.New("commit Attachment bytes")
		}
		keepTemporary = true
		if err = syncDirectory(destinationDirectory); err != nil {
			return storedFile{}, errors.New("sync Attachment storage")
		}
	} else if statErr != nil {
		return storedFile{}, errors.New("inspect Attachment storage")
	}
	return storedFile{key: key, digest: digest, size: size, installed: keepTemporary}, nil
}

func (service *Service) reconcileFiles(ctx context.Context) (returnErr error) {
	ctx = systemactor.NewContext(ctx, systemactor.HostMaintenance)
	referenced, err := service.storage.ReferencedAttachmentStorageKeys(ctx)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(service.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("open Attachment storage for reconciliation")
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	if _, statErr := root.Lstat(".tmp"); statErr == nil {
		if err = root.RemoveAll(".tmp"); err != nil {
			return errors.New("remove interrupted Attachment uploads")
		}
		if err = syncRootDirectory(root, "."); err != nil {
			return errors.New("sync reconciled Attachment storage")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("inspect interrupted Attachment uploads")
	}
	err = fs.WalkDir(root.FS(), "sha256", func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return fs.SkipDir
		}
		if walkErr != nil {
			return walkErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		key := filepath.FromSlash(path)
		if slices.Contains(referenced, key) {
			return nil
		}
		if removeErr := root.Remove(key); removeErr != nil {
			return removeErr
		}
		return syncRootDirectory(root, filepath.Dir(key))
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reconcile Attachment storage: %w", err)
	}
	return nil
}

func (service *Service) removeIfUnreferenced(
	ctx context.Context,
	key string,
) (returnErr error) {
	ctx = systemactor.NewContext(ctx, systemactor.HostMaintenance)
	referenced, err := service.storage.ReferencedAttachmentStorageKeys(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(referenced, key) {
		return nil
	}
	root, err := os.OpenRoot(service.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("open Attachment storage for cleanup")
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	if err = root.Remove(key); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errors.New("remove unreferenced Attachment")
	}
	if err = syncRootDirectory(root, filepath.Dir(key)); err != nil {
		return errors.New("sync Attachment cleanup")
	}
	return nil
}

func syncRootDirectory(root *os.Root, name string) (returnErr error) {
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, directory.Close())
	}()
	return directory.Sync()
}

// ReadVersion returns verified immutable bytes to authorized crew.
func (service *Service) ReadVersion(
	ctx context.Context,
	actor auth.Account,
	eventID, versionID int,
) (Version, []byte, error) {
	if eventID <= 0 || versionID <= 0 || actor.EventRoles[eventID] == "" {
		return Version{}, nil, ErrUploadTargetNotFound
	}
	stored, err := service.storage.LoadAttachmentVersion(actor.Context(ctx), eventID, versionID)
	if err != nil {
		return Version{}, nil, err
	}
	content, err := os.ReadFile(filepath.Join(service.root, stored.StorageKey))
	if err != nil {
		return Version{}, nil, errors.New("read Attachment bytes")
	}
	digest := sha256.Sum256(content)
	if fmt.Sprintf("%x", digest) != stored.SHA256 {
		return Version{}, nil, errors.New("attachment integrity check failed")
	}
	return version(stored), content, nil
}

// CompetitionCrewState returns private metadata without storage paths or file bytes.
func (service *Service) CompetitionCrewState(
	ctx context.Context,
	actor auth.Account,
	eventID, sessionID int,
) (CrewState, error) {
	if eventID <= 0 || sessionID <= 0 {
		return CrewState{}, ErrInvalidInput
	}
	if actor.EventRoles[eventID] == "" {
		return CrewState{}, ErrProducerRequired
	}
	stored, err := service.storage.LoadCompetitionAttachmentCrewState(
		actor.Context(ctx), eventID, sessionID,
	)
	if err != nil {
		return CrewState{}, err
	}
	result := CrewState{
		Versions:           make([]Version, 0, len(stored.Versions)),
		ReopenWindows:      stored.ReopenWindows,
		EventRelease:       stored.EventRelease,
		CompetitionRelease: stored.CompetitionRelease,
	}
	for _, found := range stored.Versions {
		result.Versions = append(result.Versions, version(found))
	}
	return result, nil
}

// CrewReopenWindows returns retained private windows for one exact owner.
func (service *Service) CrewReopenWindows(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	targetType TargetKind,
	targetID int,
) ([]store.ReopenWindow, error) {
	if eventID <= 0 || targetID <= 0 ||
		(targetType != TargetPresentation && targetType != TargetEntry) {
		return nil, ErrInvalidInput
	}
	if actor.EventRoles[eventID] == "" {
		return nil, ErrProducerRequired
	}
	return service.storage.LoadReopenWindows(
		actor.Context(ctx),
		eventID,
		targetType,
		targetID,
	)
}

// SubmittedState returns only the signed-in Account's private file metadata.
func (service *Service) SubmittedState(
	ctx context.Context,
	actor auth.Account,
) (AccountState, error) {
	if actor.ID <= 0 {
		return AccountState{}, ErrUploadTargetNotFound
	}
	stored, err := service.storage.LoadAccountAttachmentState(actor.Context(ctx), actor.ID)
	if err != nil {
		return AccountState{}, err
	}
	result := AccountState{
		Versions:      make([]AccountVersion, 0, len(stored.Versions)),
		ReopenWindows: stored.ReopenWindows,
	}
	for _, found := range stored.Versions {
		result.Versions = append(result.Versions, AccountVersion{
			OwnerID: found.OwnerID, Version: found.Version,
			OwnerType: found.OwnerType,
			Name:      found.Name, OriginalFilename: found.OriginalFilename,
			SHA256: found.SHA256,
		})
	}
	return result, nil
}

// ConfigureEventRelease changes the Event's default Attachment trigger.
func (service *Service) ConfigureEventRelease(
	ctx context.Context,
	actor auth.Account,
	input ConfigureEventReleaseInput,
) (store.AttachmentReleaseConfiguration, error) {
	if input.EventID <= 0 || input.ExpectedRevision < 0 || input.CueSessionID < 0 {
		return store.AttachmentReleaseConfiguration{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return store.AttachmentReleaseConfiguration{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return store.AttachmentReleaseConfiguration{}, errors.New("encode Event Attachment release command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)),
		Action:      "ConfigureEventAttachmentRelease", TargetType: "Event",
		TargetID: strconv.Itoa(input.EventID), Now: service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[store.AttachmentReleaseConfiguration]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: attachmentReleaseRejections,
		},
		Replay: decodeReleaseReceipt[store.AttachmentReleaseConfiguration],
		Notify: service.notifySchedule,
		Apply: auditReleaseRejections(func(transaction *store.CommandTx) (
			command.Execution[store.AttachmentReleaseConfiguration], error,
		) {
			configured, configureErr := transaction.ConfigureEventAttachmentRelease(
				actor.Context(ctx), store.ConfigureEventAttachmentReleaseParams{
					EventID: input.EventID, ExpectedRevision: input.ExpectedRevision,
					Policy: input.Policy, CueSessionID: input.CueSessionID,
				},
			)
			return releaseSuccess(configured, configureErr)
		}),
	})
}

// ConfigureCompetitionRelease changes one optional Competition override.
func (service *Service) ConfigureCompetitionRelease(
	ctx context.Context,
	actor auth.Account,
	input ConfigureCompetitionReleaseInput,
) (store.CompetitionAttachmentReleaseConfiguration, error) {
	if input.EventID <= 0 || input.SessionID <= 0 || input.ExpectedRevision < 0 {
		return store.CompetitionAttachmentReleaseConfiguration{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return store.CompetitionAttachmentReleaseConfiguration{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return store.CompetitionAttachmentReleaseConfiguration{},
			errors.New("encode Competition Attachment release command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)),
		Action:      "ConfigureCompetitionAttachmentRelease", TargetType: "Competition",
		TargetID: strconv.Itoa(input.SessionID), Now: service.now().UTC(),
	}
	return command.Execute(
		actor.Context(ctx),
		command.Plan[store.CompetitionAttachmentReleaseConfiguration]{
			Storage: service.storage, Identity: identity,
			Authorization: command.Authorization{
				Facts: authz.Event(input.EventID), Refusals: attachmentReleaseRejections,
			},
			Replay: decodeReleaseReceipt[store.CompetitionAttachmentReleaseConfiguration],
			Notify: service.notifySchedule,
			Apply: auditReleaseRejections(func(transaction *store.CommandTx) (
				command.Execution[store.CompetitionAttachmentReleaseConfiguration], error,
			) {
				configured, configureErr := transaction.ConfigureCompetitionAttachmentRelease(
					actor.Context(ctx), store.ConfigureCompetitionAttachmentReleaseParams{
						EventID: input.EventID, SessionID: input.SessionID,
						ExpectedRevision: input.ExpectedRevision,
						Policy:           input.Policy, Override: input.Override,
					},
				)
				return releaseSuccess(configured, configureErr)
			}),
		},
	)
}

// SetVersionRelease changes eligibility and hold without changing Final state.
func (service *Service) SetVersionRelease(
	ctx context.Context,
	actor auth.Account,
	input SetVersionReleaseInput,
) (Version, error) {
	if input.EventID <= 0 || input.VersionID <= 0 || input.ExpectedRevision < 0 {
		return Version{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return Version{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Version{}, errors.New("encode Attachment Version release command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)),
		Action:      "SetAttachmentVersionRelease", TargetType: "AttachmentVersion",
		TargetID: strconv.Itoa(input.VersionID), Now: service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Version]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: attachmentReleaseRejections,
		},
		Replay: decodeReleaseReceipt[Version],
		Notify: service.notifySchedule,
		Apply: auditReleaseRejections(func(
			transaction *store.CommandTx,
		) (command.Execution[Version], error) {
			updated, updateErr := transaction.SetAttachmentVersionRelease(
				actor.Context(ctx), store.SetAttachmentVersionReleaseParams{
					EventID: input.EventID, VersionID: input.VersionID,
					ExpectedRevision: input.ExpectedRevision,
					Hold:             input.Hold,
				},
			)
			if updateErr != nil {
				return command.Execution[Version]{}, updateErr
			}
			result := version(updated)
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[Version]{}, errors.New("encode Attachment Version release outcome")
			}
			return command.Success(result, string(encoded)), nil
		}),
	})
}

// PreviewReleaseCue returns the exact current impact for Producer review.
func (service *Service) PreviewReleaseCue(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) (store.AttachmentReleaseCuePreview, error) {
	if eventID <= 0 {
		return store.AttachmentReleaseCuePreview{}, ErrInvalidInput
	}
	if !authz.Holds(actor.Identity(), eventID, authz.ManageAttachments) {
		return store.AttachmentReleaseCuePreview{}, ErrProducerRequired
	}
	return service.storage.PreviewEventAttachmentReleaseCue(actor.Context(ctx), eventID)
}

// FireReleaseCue releases cue-governed files without changing Results state.
func (service *Service) FireReleaseCue(
	ctx context.Context,
	actor auth.Account,
	input FireReleaseCueInput,
) (store.AttachmentReleaseConfiguration, error) {
	if input.EventID <= 0 || input.ExpectedRevision < 0 ||
		input.PreviewFingerprint == "" || !input.Confirmed {
		return store.AttachmentReleaseConfiguration{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return store.AttachmentReleaseConfiguration{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return store.AttachmentReleaseConfiguration{}, errors.New("encode Event Release Cue command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)),
		Action:      "FireEventAttachmentReleaseCue", TargetType: "Event",
		TargetID: strconv.Itoa(input.EventID), Now: service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[store.AttachmentReleaseConfiguration]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: attachmentReleaseRejections,
		},
		Replay: decodeReleaseReceipt[store.AttachmentReleaseConfiguration],
		Notify: service.notifySchedule,
		Apply: auditReleaseRejections(func(transaction *store.CommandTx) (
			command.Execution[store.AttachmentReleaseConfiguration], error,
		) {
			fired, fireErr := transaction.FireEventAttachmentReleaseCue(
				actor.Context(ctx),
				input.EventID,
				input.ExpectedRevision,
				input.PreviewFingerprint,
				identity.Now,
			)
			return releaseSuccess(fired, fireErr)
		}),
	})
}

// ReleasedVersions lists attendee-safe Active Event files.
func (service *Service) ReleasedVersions(ctx context.Context) ([]ReleasedVersion, error) {
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
	stored, err := service.storage.LoadReleasedAttachmentVersions(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ReleasedVersion, 0, len(stored))
	for _, found := range stored {
		result = append(result, releasedVersion(found))
	}
	return result, nil
}

// ReadReleasedVersion returns verified bytes only while policy permits access.
func (service *Service) ReadReleasedVersion(
	ctx context.Context,
	versionID int,
) (ReleasedVersion, []byte, error) {
	if versionID <= 0 {
		return ReleasedVersion{}, nil, ErrNotReleased
	}
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
	stored, err := service.storage.LoadReleasedAttachmentVersion(ctx, versionID)
	if err != nil {
		return ReleasedVersion{}, nil, err
	}
	content, err := service.readStoredVersion(stored)
	if err != nil {
		return ReleasedVersion{}, nil, err
	}
	return releasedVersion(stored), content, nil
}

func (service *Service) readStoredVersion(stored store.AttachmentVersion) ([]byte, error) {
	content, err := os.ReadFile(filepath.Join(service.root, stored.StorageKey))
	if err != nil {
		return nil, errors.New("read Attachment bytes")
	}
	digest := sha256.Sum256(content)
	if fmt.Sprintf("%x", digest) != stored.SHA256 {
		return nil, errors.New("attachment integrity check failed")
	}
	return content, nil
}

// attachmentReleaseRejections is the single source for Attachment release
// rejection codes in both directions.
var attachmentReleaseRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrProducerRequired, Code: "producer_required"},
		{Err: ErrReleaseRevision, Code: "stale_revision"},
		{Err: ErrReleasePolicy, Code: "invalid_release_policy"},
		{Err: ErrReleaseCueBlocked, Code: "release_cue_blocked"},
		{Err: ErrReleaseCuePreviewChanged, Code: "release_cue_preview_changed"},
		{Err: ErrUploadTargetNotFound, Code: "attachment_target_not_found"},
		{Err: store.ErrCompetitionNotFound, Code: "competition_not_found"},
	},
}

func decodeReleaseReceipt[T any](outcome string) (T, error) {
	return command.ReplayKnown[T](attachmentReleaseRejections, outcome)
}

func auditReleaseRejections[T any](
	apply func(*store.CommandTx) (command.Execution[T], error),
) func(*store.CommandTx) (command.Execution[T], error) {
	return command.RejectKnown(attachmentReleaseRejections, apply)
}

func releaseSuccess[T any](result T, resultErr error) (command.Execution[T], error) {
	if resultErr != nil {
		return command.Execution[T]{}, resultErr
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return command.Execution[T]{}, errors.New("encode Attachment release outcome")
	}
	return command.Success(result, string(encoded)), nil
}

func releasedVersion(stored store.AttachmentVersion) ReleasedVersion {
	return ReleasedVersion{
		ID: stored.ID, AttachmentID: stored.AttachmentID, Version: stored.Version,
		OwnerID: stored.OwnerID, OwnerType: stored.OwnerType, Name: stored.Name,
		OriginalFilename: stored.OriginalFilename, MediaType: stored.MediaType,
		SizeBytes: stored.SizeBytes, SHA256: stored.SHA256,
	}
}

// CreateReopenWindow restores uploads only for one target and bounded lifetime.
func (service *Service) CreateReopenWindow(
	ctx context.Context,
	actor auth.Account,
	input ReopenInput,
) (store.ReopenWindow, error) {
	now := service.now().UTC()
	if input.EventID <= 0 || input.TargetID <= 0 ||
		(input.TargetType != TargetPresentation && input.TargetType != TargetEntry) ||
		strings.TrimSpace(input.Reason) == "" ||
		!input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(maxReopenDuration)) {
		return store.ReopenWindow{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return store.ReopenWindow{}, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return store.ReopenWindow{}, errors.New("encode Reopen Window command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(encoded)), Action: "CreateReopenWindow",
		TargetType: string(input.TargetType), TargetID: strconv.Itoa(input.TargetID), Now: now,
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[store.ReopenWindow]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: attachmentReleaseRejections,
		},
		Replay: func(outcome string) (store.ReopenWindow, error) {
			var window store.ReopenWindow
			decodeErr := store.DecodeCommandReceipt(outcome, &window)
			return window, decodeErr
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[store.ReopenWindow], error) {
			window, createErr := transaction.CreateReopenWindow(
				actor.Context(ctx), input.EventID, input.TargetType, input.TargetID,
				strings.TrimSpace(input.Reason), input.ExpiresAt.UTC(), actor.ID, now,
			)
			if createErr != nil {
				return command.Execution[store.ReopenWindow]{}, createErr
			}
			outcome, encodeErr := json.Marshal(window)
			if encodeErr != nil {
				return command.Execution[store.ReopenWindow]{}, errors.New("encode Reopen Window outcome")
			}
			return command.Success(window, string(outcome)).
				WithTargetID(strconv.Itoa(window.ID)).
				WithAudit(store.AuditDetails{
					Reason: window.Reason, Note: window.ExpiresAt.Format(time.RFC3339),
				}), nil
		},
	})
}

// UpdateReopenWindow closes early or extends one bounded exception.
func (service *Service) UpdateReopenWindow(
	ctx context.Context,
	actor auth.Account,
	input UpdateReopenInput,
) (store.ReopenWindow, error) {
	now := service.now().UTC()
	if input.EventID <= 0 || input.WindowID <= 0 || input.ExpectedRevision <= 0 ||
		(input.Close != input.ExpiresAt.IsZero()) {
		return store.ReopenWindow{}, ErrInvalidInput
	}
	if !input.Close &&
		(!input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(maxReopenDuration))) {
		return store.ReopenWindow{}, ErrReopenWindowExpiry
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return store.ReopenWindow{}, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return store.ReopenWindow{}, errors.New("encode Reopen Window update command")
	}
	action := "ExtendReopenWindow"
	if input.Close {
		action = "CloseReopenWindow"
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(encoded)), Action: action,
		TargetType: "ReopenWindow", TargetID: strconv.Itoa(input.WindowID), Now: now,
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[store.ReopenWindow]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: attachmentReleaseRejections,
		},
		Replay: func(outcome string) (store.ReopenWindow, error) {
			var window store.ReopenWindow
			decodeErr := store.DecodeCommandReceipt(outcome, &window)
			return window, decodeErr
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[store.ReopenWindow], error) {
			window, updateErr := transaction.UpdateReopenWindow(
				actor.Context(ctx), input.EventID, input.WindowID, input.ExpectedRevision,
				input.ExpiresAt.UTC(), input.Close, now,
			)
			if updateErr != nil {
				return command.Execution[store.ReopenWindow]{}, updateErr
			}
			outcome, encodeErr := json.Marshal(window)
			if encodeErr != nil {
				return command.Execution[store.ReopenWindow]{}, errors.New("encode Reopen Window update outcome")
			}
			return command.Success(window, string(outcome)), nil
		},
	})
}

func validateAttachmentInput(name, originalFilename string, body io.Reader) error {
	name = strings.TrimSpace(name)
	originalFilename = filepath.Base(strings.TrimSpace(originalFilename))
	var fieldErrors []error
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 200 {
		fieldErrors = append(fieldErrors, ErrInvalidName)
	}
	if originalFilename == "." || originalFilename == "" || !utf8.ValidString(originalFilename) ||
		utf8.RuneCountInString(originalFilename) > 255 {
		fieldErrors = append(fieldErrors, ErrInvalidFilename)
	}
	for _, value := range name {
		if unicode.IsControl(value) {
			fieldErrors = append(fieldErrors, ErrInvalidName)
			break
		}
	}
	for _, value := range originalFilename {
		if unicode.IsControl(value) {
			fieldErrors = append(fieldErrors, ErrInvalidFilename)
			break
		}
	}
	if body == nil || len(fieldErrors) > 0 {
		return errors.Join(append([]error{ErrInvalidInput}, fieldErrors...)...)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path) //nolint:gosec // Path is an internally constructed Attachment directory.
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func version(stored store.AttachmentVersion) Version {
	return Version{
		ID: stored.ID, AttachmentID: stored.AttachmentID, Version: stored.Version,
		EventID: stored.EventID, OwnerType: stored.OwnerType, OwnerID: stored.OwnerID,
		Name: stored.Name, OriginalFilename: stored.OriginalFilename, MediaType: stored.MediaType,
		SizeBytes: stored.SizeBytes, SHA256: stored.SHA256,
		UploaderType: stored.UploaderType, UploaderID: stored.UploaderID,
		Primary: stored.Primary, Final: stored.Final,
		ReadinessRevision:  stored.ReadinessRevision,
		ReleaseEligibility: stored.ReleaseEligibility,
		ReleaseHold:        stored.ReleaseHold, ReleaseRevision: stored.ReleaseRevision,
		CreatedAt: stored.CreatedAt,
	}
}

// Package attachments owns scoped upload credentials and immutable files.
package attachments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

var (
	// ErrProducerRequired means an Attachment command lacked Producer authority.
	ErrProducerRequired = errors.New("producer authority required")
	// ErrUploadTargetNotFound hides unknown and cross-Event upload owners.
	ErrUploadTargetNotFound = store.ErrUploadTargetNotFound
	// ErrInvalidInput means an Attachment request contains unsafe values.
	ErrInvalidInput = errors.New("invalid Attachment input")
	// ErrCommandConflict means a Command ID was reused for different work.
	ErrCommandConflict = store.ErrCommandConflict
	// ErrUploadLinkInvalid hides unknown, revoked, and malformed credentials.
	ErrUploadLinkInvalid = store.ErrUploadLinkInvalid
	// ErrUploadClosed means the fixed cutoff has arrived.
	ErrUploadClosed = store.ErrUploadClosed
	// ErrAttachmentTooLarge protects installation storage from unbounded requests.
	ErrAttachmentTooLarge = errors.New("attachment exceeds size limit")
	// ErrReopenWindowRevision means an update used stale window state.
	ErrReopenWindowRevision = store.ErrReopenWindowRevision
	// ErrReopenWindowExtension means a requested expiry did not extend the window.
	ErrReopenWindowExtension = store.ErrReopenWindowExtension
	// ErrReleaseRevision means Attachment release state changed after observation.
	ErrReleaseRevision = store.ErrAttachmentReleaseRevision
	// ErrReleasePolicy means an Attachment Release Policy is invalid.
	ErrReleasePolicy = store.ErrAttachmentReleasePolicy
	// ErrReleaseCueBlocked means unresolved Entries block the Event cue.
	ErrReleaseCueBlocked = store.ErrAttachmentReleaseCueBlocked
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

// IssueLinkInput identifies one scoped upload owner.
type IssueLinkInput struct {
	EventID    int        `json:"event_id"`
	TargetType TargetKind `json:"target_type"`
	TargetID   int        `json:"target_id"`
	CommandID  string     `json:"command_id"`
}

// UploadLink is crew-visible metadata plus a credential only on initial issuance.
type UploadLink struct {
	ID               int        `json:"id"`
	EventID          int        `json:"event_id"`
	TargetType       TargetKind `json:"target_type"`
	TargetID         int        `json:"target_id"`
	Token            string     `json:"token,omitempty"`
	CredentialStatus string     `json:"credential_status"`
	RevokedAt        time.Time  `json:"revoked_at,omitzero"`
	CreatedAt        time.Time  `json:"created_at"`
}

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

// UploadInput contains one bounded file stream and logical owner.
type UploadInput struct {
	Token, CommandID, Name, OriginalFilename, MediaType string
	Body                                                io.Reader
	CrewOnly                                            bool
}

// CrewUploadInput identifies an on-behalf-of upload.
type CrewUploadInput struct {
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
	EventID          int    `json:"event_id"`
	ExpectedRevision int    `json:"expected_revision"`
	CommandID        string `json:"command_id"`
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
	storage *store.SQLite
	root    string
	now     func() time.Time
}

// New creates an Attachment Service with explicit dependencies.
func New(storage *store.SQLite, root string, now func() time.Time) (*Service, error) {
	if storage == nil {
		return nil, errors.New("attachment storage is required")
	}
	if root == "" {
		return nil, errors.New("attachment store root is required")
	}
	if now == nil {
		return nil, errors.New("attachment clock is required")
	}
	return &Service{storage: storage, root: root, now: now}, nil
}

// IssueUploadLink rotates and returns one target-scoped credential.
func (service *Service) IssueUploadLink(
	ctx context.Context,
	actor auth.Account,
	input IssueLinkInput,
) (UploadLink, error) {
	if input.EventID <= 0 || input.TargetID <= 0 ||
		(input.TargetType != TargetPresentation && input.TargetType != TargetEntry) {
		return UploadLink{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return UploadLink{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return UploadLink{}, errors.New("encode Upload Link command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(string(payload)), Action: "IssueUploadLink",
		TargetType: string(input.TargetType), TargetID: strconv.Itoa(input.TargetID), Now: service.now().UTC(),
	}
	issuedToken := ""
	stored, err := command.Execute(actor.Context(ctx), command.Plan[store.UploadLink]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (store.UploadLink, error) {
			var stored store.UploadLink
			if decodeErr := store.DecodeCommandReceipt(outcome, &stored); decodeErr != nil {
				return store.UploadLink{}, decodeErr
			}
			return stored, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[store.UploadLink], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[store.UploadLink]{}, ErrProducerRequired
			}
			token, tokenHash, tokenErr := newToken()
			if tokenErr != nil {
				return command.Execution[store.UploadLink]{}, tokenErr
			}
			created, issueErr := transaction.IssueUploadLink(actor.Context(ctx), store.IssueUploadLinkParams{
				EventID: input.EventID, TargetType: input.TargetType, TargetID: input.TargetID,
				TokenHash: tokenHash, Now: identity.Now,
			})
			if issueErr != nil {
				return command.Execution[store.UploadLink]{}, issueErr
			}
			issuedToken = token
			outcome, encodeErr := json.Marshal(created)
			if encodeErr != nil {
				return command.Execution[store.UploadLink]{}, errors.New("encode Upload Link outcome")
			}
			return command.Success(created, string(outcome)).
				WithTargetID(strconv.Itoa(created.ID)), nil
		},
	})
	if err != nil {
		return UploadLink{}, err
	}
	status := "AlreadyIssued"
	if issuedToken != "" {
		status = "Issued"
	}
	return uploadLink(stored, issuedToken, status), nil
}

// Upload accepts one file through a current scoped credential.
func (service *Service) Upload(ctx context.Context, input UploadInput) (Version, error) {
	if !validToken(input.Token) || !validAttachmentInput(input.Name, input.OriginalFilename, input.Body) {
		return Version{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return Version{}, err
	}
	authorization, err := service.storage.ResolveUploadLink(ctx, tokenHash(input.Token))
	if err != nil {
		return Version{}, err
	}
	return service.storeVersion(
		ctx, authorization, input.CommandID, input.Name, input.OriginalFilename,
		input.MediaType, input.Body, "UploadLink", authorization.LinkID, input.CrewOnly,
	)
}

// UploadForCrew stores one file while preserving the authenticated Account actor.
func (service *Service) UploadForCrew(
	ctx context.Context,
	actor auth.Account,
	input CrewUploadInput,
) (Version, error) {
	if input.EventID <= 0 || input.TargetID <= 0 ||
		(input.TargetType != TargetPresentation && input.TargetType != TargetEntry) ||
		!validAttachmentInput(input.Name, input.OriginalFilename, input.Body) {
		return Version{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return Version{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return Version{}, ErrProducerRequired
	}
	authorization := store.UploadAuthorization{
		EventID: input.EventID, TargetType: input.TargetType, TargetID: input.TargetID,
	}
	return service.storeVersion(
		actor.Context(ctx), authorization, input.CommandID, input.Name, input.OriginalFilename,
		input.MediaType, input.Body, "Crew", actor.ID, input.CrewOnly,
	)
}

func (service *Service) storeVersion(
	ctx context.Context,
	authorization store.UploadAuthorization,
	commandID, name, originalFilename, mediaType string,
	body io.Reader,
	uploaderType string,
	uploaderID int,
	crewOnly bool,
) (Version, error) {
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
		LinkID       int    `json:"link_id,omitempty"`
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
		LinkID: authorization.LinkID, TargetType: string(authorization.TargetType),
		Name: params.Name, Filename: params.OriginalFilename, MediaType: params.MediaType,
		SizeBytes: params.SizeBytes, SHA256: params.SHA256,
		UploaderType: params.UploaderType, UploaderID: params.UploaderID,
		CrewOnly: crewOnly,
	})
	if err != nil {
		return Version{}, errors.New("encode Attachment upload command")
	}
	identity := store.CommandIdentity{
		CommandID: commandID, PayloadHash: command.PayloadHash(string(payload)),
		Action: "UploadAttachment", TargetType: string(authorization.TargetType),
		TargetID: strconv.Itoa(authorization.TargetID), Now: now,
	}
	if uploaderType == "UploadLink" {
		identity.ActorKind = "UploadLink"
		identity.ActorUploadLinkID = uploaderID
	} else {
		identity.ActorAccountID = uploaderID
	}
	return command.Execute(ctx, command.Plan[Version]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (Version, error) {
			var stored Version
			if err := store.DecodeCommandReceipt(outcome, &stored); err != nil {
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
}

type storedFile struct {
	key, digest string
	size        int64
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
	return storedFile{key: key, digest: digest, size: size}, nil
}

// ReadVersion returns verified immutable bytes to authorized crew.
func (service *Service) ReadVersion(
	ctx context.Context,
	actor auth.Account,
	eventID, versionID int,
) (Version, []byte, error) {
	if eventID <= 0 || versionID <= 0 ||
		(!actor.Administrator && actor.EventRoles[eventID] == "") {
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
	return command.Execute(actor.Context(ctx), command.Plan[store.AttachmentReleaseConfiguration]{
		Storage: service.storage, Identity: identity,
		Replay: decodeReleaseReceipt[store.AttachmentReleaseConfiguration],
		Apply: func(transaction *store.CommandTx) (
			command.Execution[store.AttachmentReleaseConfiguration], error,
		) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[store.AttachmentReleaseConfiguration]{}, ErrProducerRequired
			}
			configured, configureErr := transaction.ConfigureEventAttachmentRelease(
				actor.Context(ctx), store.ConfigureEventAttachmentReleaseParams{
					EventID: input.EventID, ExpectedRevision: input.ExpectedRevision,
					Policy: input.Policy, CueSessionID: input.CueSessionID,
				},
			)
			return releaseSuccess(configured, configureErr)
		},
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
			Replay: decodeReleaseReceipt[store.CompetitionAttachmentReleaseConfiguration],
			Apply: func(transaction *store.CommandTx) (
				command.Execution[store.CompetitionAttachmentReleaseConfiguration], error,
			) {
				if !actor.CanProduceEvent(input.EventID) {
					return command.Execution[store.CompetitionAttachmentReleaseConfiguration]{},
						ErrProducerRequired
				}
				configured, configureErr := transaction.ConfigureCompetitionAttachmentRelease(
					actor.Context(ctx), store.ConfigureCompetitionAttachmentReleaseParams{
						EventID: input.EventID, SessionID: input.SessionID,
						ExpectedRevision: input.ExpectedRevision,
						Policy:           input.Policy, Override: input.Override,
					},
				)
				return releaseSuccess(configured, configureErr)
			},
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
	return command.Execute(actor.Context(ctx), command.Plan[Version]{
		Storage: service.storage, Identity: identity,
		Replay: func(outcome string) (Version, error) {
			var replayed Version
			err := store.DecodeCommandReceipt(outcome, &replayed)
			return replayed, err
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Version], error) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[Version]{}, ErrProducerRequired
			}
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
		},
	})
}

// FireReleaseCue releases cue-governed files without changing Results state.
func (service *Service) FireReleaseCue(
	ctx context.Context,
	actor auth.Account,
	input FireReleaseCueInput,
) (store.AttachmentReleaseConfiguration, error) {
	if input.EventID <= 0 || input.ExpectedRevision < 0 {
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
	return command.Execute(actor.Context(ctx), command.Plan[store.AttachmentReleaseConfiguration]{
		Storage: service.storage, Identity: identity,
		Replay: decodeReleaseReceipt[store.AttachmentReleaseConfiguration],
		Apply: func(transaction *store.CommandTx) (
			command.Execution[store.AttachmentReleaseConfiguration], error,
		) {
			if !actor.CanProduceEvent(input.EventID) {
				return command.Execution[store.AttachmentReleaseConfiguration]{}, ErrProducerRequired
			}
			fired, fireErr := transaction.FireEventAttachmentReleaseCue(
				actor.Context(ctx), input.EventID, input.ExpectedRevision, identity.Now,
			)
			return releaseSuccess(fired, fireErr)
		},
	})
}

// ReleasedVersions lists attendee-safe Active Event files.
func (service *Service) ReleasedVersions(ctx context.Context) ([]ReleasedVersion, error) {
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

func decodeReleaseReceipt[T any](outcome string) (T, error) {
	var replayed T
	err := store.DecodeCommandReceipt(outcome, &replayed)
	return replayed, err
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

// RevokeUploadLink immediately invalidates one credential.
func (service *Service) RevokeUploadLink(
	ctx context.Context,
	actor auth.Account,
	eventID, linkID int,
	commandID string,
) error {
	if eventID <= 0 || linkID <= 0 {
		return ErrInvalidInput
	}
	if err := command.ValidateID(commandID); err != nil {
		return err
	}
	if !actor.CanProduceEvent(eventID) {
		return ErrProducerRequired
	}
	now := service.now().UTC()
	payload := strconv.Itoa(eventID) + ":" + strconv.Itoa(linkID)
	_, err := command.Execute(actor.Context(ctx), command.Plan[struct{}]{
		Storage: service.storage,
		Identity: store.CommandIdentity{
			ActorAccountID: actor.ID, CommandID: commandID,
			PayloadHash: command.PayloadHash(payload), Action: "RevokeUploadLink",
			TargetType: "UploadLink", TargetID: strconv.Itoa(linkID), Now: now,
		},
		Replay: func(outcome string) (struct{}, error) {
			var result struct{}
			decodeErr := store.DecodeCommandReceipt(outcome, &result)
			return result, decodeErr
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[struct{}], error) {
			if revokeErr := transaction.RevokeUploadLink(actor.Context(ctx), eventID, linkID, now); revokeErr != nil {
				return command.Execution[struct{}]{}, revokeErr
			}
			return command.Success(struct{}{}, "{}"), nil
		},
	})
	return err
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
	if !actor.CanProduceEvent(input.EventID) {
		return store.ReopenWindow{}, ErrProducerRequired
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
	return command.Execute(actor.Context(ctx), command.Plan[store.ReopenWindow]{
		Storage: service.storage, Identity: identity,
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
		(input.Close != input.ExpiresAt.IsZero()) ||
		(!input.Close && (!input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(maxReopenDuration)))) {
		return store.ReopenWindow{}, ErrInvalidInput
	}
	if err := command.ValidateID(input.CommandID); err != nil {
		return store.ReopenWindow{}, err
	}
	if !actor.CanProduceEvent(input.EventID) {
		return store.ReopenWindow{}, ErrProducerRequired
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
	return command.Execute(actor.Context(ctx), command.Plan[store.ReopenWindow]{
		Storage: service.storage, Identity: identity,
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

func validAttachmentInput(name, originalFilename string, body io.Reader) bool {
	name = strings.TrimSpace(name)
	originalFilename = filepath.Base(strings.TrimSpace(originalFilename))
	if body == nil || name == "" || originalFilename == "." || originalFilename == "" ||
		utf8.RuneCountInString(name) > 200 || utf8.RuneCountInString(originalFilename) > 255 {
		return false
	}
	for _, value := range name + originalFilename {
		if unicode.IsControl(value) {
			return false
		}
	}
	return true
}

func validToken(token string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == 32 &&
		base64.RawURLEncoding.EncodeToString(decoded) == token
}

func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
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

func newToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", errors.New("generate Upload Link credential")
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(digest[:]), nil
}

func uploadLink(stored store.UploadLink, token, credentialStatus string) UploadLink {
	return UploadLink{
		ID: stored.ID, EventID: stored.EventID, TargetType: stored.TargetType,
		TargetID: stored.TargetID, Token: token, CredentialStatus: credentialStatus,
		RevokedAt: stored.RevokedAt, CreatedAt: stored.CreatedAt,
	}
}

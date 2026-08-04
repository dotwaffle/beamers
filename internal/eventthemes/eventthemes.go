// Package eventthemes owns inherited immutable Event Theme Revisions.
package eventthemes

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
	"github.com/dotwaffle/beamers/internal/themes"
	"github.com/dotwaffle/beamers/internal/themevalue"
	"github.com/dotwaffle/beamers/internal/viewer"
)

var (
	// ErrProducerRequired means Event Theme work lacked Producer authority.
	ErrProducerRequired = errors.New("producer authority required")
	// ErrRevisionNotFound means the requested Event Theme Revision does not exist.
	ErrRevisionNotFound = store.ErrEventThemeRevisionNotFound
	// ErrActivationBlocked means inherited accessibility failures prevent activation.
	ErrActivationBlocked = errors.New("event Theme activation is blocked")
	// ErrStaleActiveRevision means active Event Theme state changed after rendering.
	ErrStaleActiveRevision = store.ErrActiveEventThemeRevisionConflict
	// ErrCommandConflict means a Command ID was reused for different Theme work.
	ErrCommandConflict = store.ErrCommandConflict
)

// Revision is one immutable partial Event Theme configuration.
type Revision struct {
	ID                 int                    `json:"id"`
	EventID            int                    `json:"event_id"`
	Config             themevalue.EventConfig `json:"config"`
	CreatedByAccountID int                    `json:"created_by_account_id,omitempty"`
	CreatedAt          time.Time              `json:"created_at,omitzero"`
}

// ActiveTheme is one Revision resolved over the active Installation Theme.
type ActiveTheme struct {
	Revision Revision          `json:"revision"`
	Resolved themevalue.Config `json:"resolved"`
}

// Preview is a side-effect-free inherited Revision rendering decision.
type Preview struct {
	Revision Revision                     `json:"revision"`
	Resolved themevalue.Config            `json:"resolved"`
	Variants map[string]themevalue.Config `json:"variants"`
	Findings []themevalue.Finding         `json:"findings,omitempty"`
}

// CreateDraftInput creates one immutable Event Theme Revision.
type CreateDraftInput struct {
	Config    themevalue.EventConfig `json:"config"`
	CommandID string                 `json:"command_id"`
}

// ActivateInput selects one exact Revision, including full inheritance at zero.
type ActivateInput struct {
	RevisionID               int    `json:"revision_id"`
	ExpectedActiveRevisionID int    `json:"expected_active_revision_id"`
	CommandID                string `json:"command_id"`
}

// Service owns Event Theme queries and commands.
type Service struct {
	storage           *store.SQLite
	installation      *themes.Service
	now               func() time.Time
	notifyThemeChange func()
}

// New creates an Event Theme service with explicit dependencies.
// notifyThemeChange is optional and runs after a successful activation commit or replay.
func New(
	storage *store.SQLite,
	installation *themes.Service,
	now func() time.Time,
	notifyThemeChange func(),
) (*Service, error) {
	if storage == nil {
		return nil, errors.New("event Theme storage is required")
	}
	if installation == nil {
		return nil, errors.New("installation Theme service is required")
	}
	if now == nil {
		return nil, errors.New("event Theme clock is required")
	}
	return &Service{
		storage: storage, installation: installation, now: now,
		notifyThemeChange: notifyThemeChange,
	}, nil
}

// Active resolves the public active Event Theme and optional Display variant.
func (service *Service) Active(
	ctx context.Context,
	eventID int,
	variant string,
) (ActiveTheme, error) {
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
	base, err := service.installation.Active(ctx)
	if err != nil {
		return ActiveTheme{}, err
	}
	found, err := service.storage.LoadActiveEventThemeRevision(ctx, eventID)
	if err != nil {
		return ActiveTheme{}, err
	}
	revision, err := validateStoredRevision(revision(found))
	if err != nil {
		return ActiveTheme{}, err
	}
	resolved, err := themevalue.ResolveEvent(base.Config, revision.Config, variant)
	if err != nil {
		return ActiveTheme{}, errors.New("stored Event Theme Revision is invalid")
	}
	if len(themevalue.ActivationFindings(resolved)) != 0 {
		return ActiveTheme{}, errors.New("active Event Theme Revision fails accessibility checks")
	}
	return ActiveTheme{Revision: revision, Resolved: resolved}, nil
}

// List returns every immutable Event Draft plus full inheritance Revision zero.
func (service *Service) List(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) ([]Revision, error) {
	if !producer(actor, eventID) {
		return nil, ErrProducerRequired
	}
	found, err := service.storage.ListEventThemeRevisions(actor.Context(ctx), eventID)
	if err != nil {
		return nil, err
	}
	revisions := make([]Revision, 0, len(found)+1)
	for _, item := range found {
		validated, validationErr := validateStoredRevision(revision(item))
		if validationErr != nil {
			return nil, validationErr
		}
		revisions = append(revisions, validated)
	}
	return append(revisions, inheritedRevision(eventID)), nil
}

// Preview resolves one exact Revision without changing active state.
func (service *Service) Preview(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	revisionID int,
) (Preview, error) {
	if !producer(actor, eventID) {
		return Preview{}, ErrProducerRequired
	}
	ctx = actor.Context(ctx)
	found, err := service.loadRevision(ctx, eventID, revisionID)
	if err != nil {
		return Preview{}, err
	}
	base, err := service.installation.Active(ctx)
	if err != nil {
		return Preview{}, err
	}
	resolved, err := themevalue.ResolveEvent(base.Config, found.Config, "")
	if err != nil {
		return Preview{}, err
	}
	eventVariants := themevalue.EventVariants()
	variants := make(map[string]themevalue.Config, len(eventVariants))
	for _, variant := range eventVariants {
		variants[variant], err = themevalue.ResolveEvent(base.Config, found.Config, variant)
		if err != nil {
			return Preview{}, err
		}
	}
	return Preview{
		Revision: found,
		Resolved: resolved,
		Variants: variants,
		Findings: themevalue.EventActivationFindings(base.Config, found.Config),
	}, nil
}

// CreateDraft appends one immutable controlled Event Theme Revision.
func (service *Service) CreateDraft(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	input CreateDraftInput,
) (Revision, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return Revision{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Revision{}, errors.New("encode Create Event Theme Revision command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(strconv.Itoa(eventID), string(payload)),
		Action:      "CreateEventThemeRevision", TargetType: "EventThemeRevision",
		TargetID: strconv.Itoa(eventID) + ":pending", Now: service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Revision]{
		Storage: service.storage, Identity: identity, Replay: replayRevision,
		Authorization: command.Authorization{
			Facts: authz.Event(eventID), Refusals: eventThemeAuthorizationRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Revision], error) {
			if !producer(actor, eventID) {
				return rejectRevision("producer_required", ErrProducerRequired), nil
			}
			if validationErr := themevalue.ValidateEventDraft(input.Config); validationErr != nil {
				return rejectRevision("invalid_theme", validationErr), nil
			}
			created, createErr := transaction.CreateEventThemeRevision(
				actor.Context(ctx), eventID, input.Config, actor.ID, identity.Now,
			)
			if createErr != nil {
				return command.Execution[Revision]{}, createErr
			}
			result := revision(created)
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[Revision]{}, errors.New("encode Event Theme outcome")
			}
			return command.Success(result, string(encoded)).
				WithTargetID(strconv.Itoa(result.ID)), nil
		},
	})
}

// Activate selects one exact accessible Revision; zero restores full inheritance.
func (service *Service) Activate(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	input ActivateInput,
) (Revision, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return Revision{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Revision{}, errors.New("encode Activate Event Theme Revision command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: input.CommandID,
		PayloadHash: command.PayloadHash(strconv.Itoa(eventID), string(payload)),
		Action:      "ActivateEventThemeRevision", TargetType: "EventThemeRevision",
		TargetID: strconv.Itoa(input.RevisionID), Now: service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Revision]{
		Storage: service.storage, Identity: identity, Replay: replayRevision,
		Notify: service.notifyThemeChange,
		Authorization: command.Authorization{
			Facts: authz.Event(eventID), Refusals: eventThemeAuthorizationRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Revision], error) {
			if !producer(actor, eventID) {
				return rejectRevision("producer_required", ErrProducerRequired), nil
			}
			selected := inheritedRevision(eventID)
			if input.RevisionID > 0 {
				stored, loadErr := transaction.LoadEventThemeRevision(
					actor.Context(ctx), eventID, input.RevisionID,
				)
				if errors.Is(loadErr, ErrRevisionNotFound) {
					return rejectRevision("theme_revision_not_found", ErrRevisionNotFound), nil
				}
				if loadErr != nil {
					return command.Execution[Revision]{}, loadErr
				}
				selected = revision(stored)
			}
			base, loadErr := transaction.LoadActiveInstallationThemeRevision(actor.Context(ctx))
			if loadErr != nil {
				return command.Execution[Revision]{}, loadErr
			}
			baseConfig := base.Config
			if base.ID == 0 {
				baseConfig = themevalue.DefaultConfig()
			}
			if len(themevalue.EventActivationFindings(baseConfig, selected.Config)) > 0 {
				return rejectRevision("activation_blocked", ErrActivationBlocked), nil
			}
			stored, activateErr := transaction.ActivateEventThemeRevision(
				actor.Context(ctx),
				eventID,
				input.RevisionID,
				input.ExpectedActiveRevisionID,
			)
			if errors.Is(activateErr, ErrStaleActiveRevision) {
				return rejectRevision("stale_active_theme", ErrStaleActiveRevision), nil
			}
			if errors.Is(activateErr, ErrRevisionNotFound) {
				return rejectRevision("theme_revision_not_found", ErrRevisionNotFound), nil
			}
			if activateErr != nil {
				return command.Execution[Revision]{}, activateErr
			}
			result := revision(stored)
			if result.ID == 0 {
				result = inheritedRevision(eventID)
			}
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[Revision]{}, errors.New("encode Event Theme activation")
			}
			return command.Success(result, string(encoded)), nil
		},
	})
}

func (service *Service) loadRevision(
	ctx context.Context,
	eventID int,
	revisionID int,
) (Revision, error) {
	if revisionID == 0 {
		return inheritedRevision(eventID), nil
	}
	found, err := service.storage.LoadEventThemeRevision(ctx, eventID, revisionID)
	if err != nil {
		return Revision{}, err
	}
	return validateStoredRevision(revision(found))
}

func producer(actor auth.Account, eventID int) bool {
	return actor.EventRoles[eventID] == viewer.Producer
}

func revision(found store.EventThemeRevision) Revision {
	return Revision{
		ID: found.ID, EventID: found.EventID, Config: found.Config,
		CreatedByAccountID: found.CreatedByAccountID, CreatedAt: found.CreatedAt,
	}
}

func inheritedRevision(eventID int) Revision {
	return Revision{EventID: eventID}
}

func validateStoredRevision(found Revision) (Revision, error) {
	if err := themevalue.ValidateEventDraft(found.Config); err != nil {
		return Revision{}, errors.New("stored Event Theme Revision is invalid")
	}
	return found, nil
}

func replayRevision(original string) (Revision, error) {
	var result Revision
	if err := store.DecodeCommandReceipt(original, &result); err != nil {
		return Revision{}, err
	}
	return result, nil
}

func rejectRevision(code string, reason error) command.Execution[Revision] {
	return command.Reject(
		Revision{},
		store.CommandRejection{Code: code, Message: reason.Error()},
		reason,
	)
}

// eventThemeAuthorizationRejections maps the Capability Table's refusal for
// Event Theme actions back to this package's sentinel, so an evaluated
// refusal returns the error the imperative check returns today.
var eventThemeAuthorizationRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrProducerRequired, Code: "producer_required"},
	},
}

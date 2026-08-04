// Package themes owns immutable Installation Theme Revisions and activation.
package themes

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
	"github.com/dotwaffle/beamers/internal/themevalue"
)

var (
	// ErrAdministratorRequired means Theme administration lacked installation authority.
	ErrAdministratorRequired = errors.New("administrator authority required")
	// ErrRevisionNotFound means the requested Theme Revision does not exist.
	ErrRevisionNotFound = store.ErrInstallationThemeRevisionNotFound
	// ErrActivationBlocked means known accessibility failures prevent activation.
	ErrActivationBlocked = errors.New("installation Theme activation is blocked")
	// ErrStaleActiveRevision means active Theme state changed after rendering the form.
	ErrStaleActiveRevision = store.ErrActiveInstallationThemeRevisionConflict
	// ErrCommandConflict means a Command ID was reused for different Theme work.
	ErrCommandConflict = store.ErrCommandConflict
)

// Revision is one immutable Installation Theme configuration.
type Revision struct {
	ID                 int               `json:"id"`
	Config             themevalue.Config `json:"config"`
	CreatedByAccountID int               `json:"created_by_account_id,omitempty"`
	CreatedAt          time.Time         `json:"created_at,omitzero"`
}

// Preview is a side-effect-free Revision rendering decision.
type Preview struct {
	Revision Revision             `json:"revision"`
	Findings []themevalue.Finding `json:"findings,omitempty"`
}

// CreateDraftInput creates one immutable Draft Theme Revision.
type CreateDraftInput struct {
	Config    themevalue.Config `json:"config"`
	CommandID string            `json:"command_id"`
}

// ActivateInput selects one exact Revision, including built-in Revision zero.
type ActivateInput struct {
	RevisionID               int    `json:"revision_id"`
	ExpectedActiveRevisionID int    `json:"expected_active_revision_id"`
	CommandID                string `json:"command_id"`
}

// Service owns Installation Theme queries and commands.
type Service struct {
	storage           *store.SQLite
	now               func() time.Time
	notifyThemeChange func()
}

// New creates a Theme service with explicit persistence and clock dependencies.
// notifyThemeChange is optional and runs after a successful activation commit or replay.
func New(
	storage *store.SQLite,
	now func() time.Time,
	notifyThemeChange func(),
) (*Service, error) {
	if storage == nil {
		return nil, errors.New("theme storage is required")
	}
	if now == nil {
		return nil, errors.New("theme clock is required")
	}
	return &Service{storage: storage, now: now, notifyThemeChange: notifyThemeChange}, nil
}

// Active returns the public active Theme or the built-in base Revision.
func (service *Service) Active(ctx context.Context) (Revision, error) {
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
	found, err := service.storage.LoadActiveInstallationThemeRevision(ctx)
	if err != nil {
		return Revision{}, err
	}
	return validateActiveRevision(revision(found))
}

// List returns every immutable Draft plus the built-in base Revision.
func (service *Service) List(ctx context.Context, actor auth.Account) ([]Revision, error) {
	if !actor.Administrator {
		return nil, ErrAdministratorRequired
	}
	found, err := service.storage.ListInstallationThemeRevisions(actor.Context(ctx))
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
	revisions = append(revisions, builtInRevision())
	return revisions, nil
}

// Preview validates and returns one exact Revision without changing active state.
func (service *Service) Preview(
	ctx context.Context,
	actor auth.Account,
	revisionID int,
) (Preview, error) {
	if !actor.Administrator {
		return Preview{}, ErrAdministratorRequired
	}
	found, err := service.loadRevision(actor.Context(ctx), revisionID)
	if err != nil {
		return Preview{}, err
	}
	return Preview{
		Revision: found,
		Findings: themevalue.ActivationFindings(found.Config),
	}, nil
}

// CreateDraft appends one immutable controlled Theme Revision.
func (service *Service) CreateDraft(
	ctx context.Context,
	actor auth.Account,
	input CreateDraftInput,
) (Revision, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return Revision{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Revision{}, errors.New("encode Create Theme Revision command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "CreateInstallationThemeRevision",
		TargetType:     "InstallationThemeRevision",
		TargetID:       "pending",
		Now:            service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Revision]{
		Storage:  service.storage,
		Identity: identity,
		Replay:   replayRevision,
		Authorization: command.Authorization{
			Facts: authz.Installation(), Refusals: themeAuthorizationRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Revision], error) {
			if !actor.Administrator {
				return rejectRevision("administrator_required", ErrAdministratorRequired), nil
			}
			if validationErr := themevalue.ValidateDraft(input.Config); validationErr != nil {
				return rejectRevision("invalid_theme", validationErr), nil
			}
			created, createErr := transaction.CreateInstallationThemeRevision(
				actor.Context(ctx),
				input.Config,
				actor.ID,
				identity.Now,
			)
			if createErr != nil {
				return command.Execution[Revision]{}, createErr
			}
			result := revision(created)
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[Revision]{}, errors.New("encode Theme Revision outcome")
			}
			return command.Success(result, string(encoded)).
				WithTargetID(strconv.Itoa(result.ID)), nil
		},
	})
}

// Activate selects one exact accessible Revision; selecting an older Revision is rollback.
func (service *Service) Activate(
	ctx context.Context,
	actor auth.Account,
	input ActivateInput,
) (Revision, error) {
	if err := command.ValidateID(input.CommandID); err != nil {
		return Revision{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Revision{}, errors.New("encode Activate Theme Revision command")
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash:    command.PayloadHash(string(payload)),
		Action:         "ActivateInstallationThemeRevision",
		TargetType:     "InstallationThemeRevision",
		TargetID:       strconv.Itoa(input.RevisionID),
		Now:            service.now().UTC(),
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Revision]{
		Storage:  service.storage,
		Identity: identity,
		Replay:   replayRevision,
		Notify:   service.notifyThemeChange,
		Authorization: command.Authorization{
			Facts: authz.Installation(), Refusals: themeAuthorizationRejections,
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Revision], error) {
			if !actor.Administrator {
				return rejectRevision("administrator_required", ErrAdministratorRequired), nil
			}
			selected := builtInRevision()
			if input.RevisionID > 0 {
				stored, loadErr := transaction.LoadInstallationThemeRevision(
					actor.Context(ctx),
					input.RevisionID,
				)
				if errors.Is(loadErr, ErrRevisionNotFound) {
					return rejectRevision("theme_revision_not_found", ErrRevisionNotFound), nil
				}
				if loadErr != nil {
					return command.Execution[Revision]{}, loadErr
				}
				selected = revision(stored)
			}
			if len(themevalue.ActivationFindings(selected.Config)) > 0 {
				return rejectRevision("activation_blocked", ErrActivationBlocked), nil
			}
			eventConfigs, loadErr := transaction.ListActiveEventThemeConfigs(actor.Context(ctx))
			if loadErr != nil {
				return command.Execution[Revision]{}, loadErr
			}
			for _, eventConfig := range eventConfigs {
				if len(themevalue.EventActivationFindings(selected.Config, eventConfig)) > 0 {
					return rejectRevision("activation_blocked", ErrActivationBlocked), nil
				}
			}
			stored, activateErr := transaction.ActivateInstallationThemeRevision(
				actor.Context(ctx),
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
			encoded, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[Revision]{}, errors.New("encode Theme activation outcome")
			}
			return command.Success(result, string(encoded)), nil
		},
	})
}

func (service *Service) loadRevision(ctx context.Context, revisionID int) (Revision, error) {
	if revisionID == 0 {
		return builtInRevision(), nil
	}
	found, err := service.storage.LoadInstallationThemeRevision(ctx, revisionID)
	if err != nil {
		return Revision{}, err
	}
	return validateStoredRevision(revision(found))
}

func revision(found store.InstallationThemeRevision) Revision {
	if found.ID == 0 {
		return builtInRevision()
	}
	return Revision{
		ID: found.ID, Config: found.Config,
		CreatedByAccountID: found.CreatedByAccountID, CreatedAt: found.CreatedAt,
	}
}

func builtInRevision() Revision {
	return Revision{Config: themevalue.DefaultConfig()}
}

func validateStoredRevision(found Revision) (Revision, error) {
	if err := themevalue.ValidateDraft(found.Config); err != nil {
		return Revision{}, errors.New("stored Theme Revision is invalid")
	}
	return found, nil
}

func validateActiveRevision(found Revision) (Revision, error) {
	validated, err := validateStoredRevision(found)
	if err != nil {
		return Revision{}, err
	}
	if len(themevalue.ActivationFindings(validated.Config)) != 0 {
		return Revision{}, errors.New("active Theme Revision fails accessibility checks")
	}
	return validated, nil
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

// themeAuthorizationRejections maps the Capability Table's refusal for
// Installation Theme actions back to this package's sentinel, so an
// evaluated refusal returns the error the imperative check returns today.
var themeAuthorizationRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrAdministratorRequired, Code: "administrator_required"},
	},
}

package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/installation"
	"github.com/dotwaffle/beamers/ent/installationthemerevision"
	"github.com/dotwaffle/beamers/ent/predicate"
	"github.com/dotwaffle/beamers/internal/themevalue"
)

var (
	// ErrInstallationThemeRevisionNotFound means a Theme Revision does not exist.
	ErrInstallationThemeRevisionNotFound = errors.New("installation Theme Revision not found")
	// ErrActiveInstallationThemeRevisionConflict means active Theme selection changed.
	ErrActiveInstallationThemeRevisionConflict = errors.New("active Installation Theme Revision conflict")
)

// InstallationThemeRevision is one immutable controlled browser presentation.
type InstallationThemeRevision struct {
	ID                 int
	Config             themevalue.Config
	CreatedByAccountID int
	CreatedAt          time.Time
}

// CreateInstallationThemeRevision appends one immutable Draft inside a command.
func (transaction *CommandTx) CreateInstallationThemeRevision(
	ctx context.Context,
	config themevalue.Config,
	createdByAccountID int,
	now time.Time,
) (InstallationThemeRevision, error) {
	if err := requireActor(ctx, "CommandTx.CreateInstallationThemeRevision"); err != nil {
		return InstallationThemeRevision{}, err
	}

	created, err := transaction.transaction.InstallationThemeRevision.Create().
		SetConfig(config).
		SetCreatedByAccountID(createdByAccountID).
		SetCreatedAt(now).
		Save(ctx)
	if err != nil {
		return InstallationThemeRevision{}, opaqueError("create Installation Theme Revision", err)
	}
	return installationThemeRevision(created), nil
}

// ActivateInstallationThemeRevision selects one exact Revision or the built-in base.
func (transaction *CommandTx) ActivateInstallationThemeRevision(
	ctx context.Context,
	revisionID int,
	expectedActiveRevisionID int,
) (InstallationThemeRevision, error) {
	if err := requireActor(ctx, "CommandTx.ActivateInstallationThemeRevision"); err != nil {
		return InstallationThemeRevision{}, err
	}

	var selected InstallationThemeRevision
	if revisionID > 0 {
		found, err := transaction.transaction.InstallationThemeRevision.Get(ctx, revisionID)
		if ent.IsNotFound(err) {
			return InstallationThemeRevision{}, ErrInstallationThemeRevisionNotFound
		}
		if err != nil {
			return InstallationThemeRevision{}, opaqueError("load Installation Theme Revision", err)
		}
		selected = installationThemeRevision(found)
	}
	routing, err := transaction.transaction.Installation.Query().Only(ctx)
	if err != nil {
		return InstallationThemeRevision{}, opaqueError("load active Installation Theme Revision", err)
	}
	predicates := []predicate.Installation{installation.IDEQ(routing.ID)}
	if expectedActiveRevisionID == 0 {
		predicates = append(predicates, installation.ActiveThemeRevisionIDIsNil())
	} else {
		predicates = append(
			predicates,
			installation.ActiveThemeRevisionIDEQ(expectedActiveRevisionID),
		)
	}
	update := transaction.transaction.Installation.UpdateOneID(routing.ID).Where(predicates...)
	if revisionID == 0 {
		update.ClearActiveThemeRevisionID()
	} else {
		update.SetActiveThemeRevisionID(revisionID)
	}
	if _, err = update.Save(ctx); ent.IsNotFound(err) {
		return InstallationThemeRevision{}, ErrActiveInstallationThemeRevisionConflict
	} else if err != nil {
		return InstallationThemeRevision{}, opaqueError("activate Installation Theme Revision", err)
	}
	return selected, nil
}

// LoadInstallationThemeRevision returns one immutable Draft.
func (installationStore *SQLite) LoadInstallationThemeRevision(
	ctx context.Context,
	revisionID int,
) (InstallationThemeRevision, error) {
	if err := requireActor(ctx, "SQLite.LoadInstallationThemeRevision"); err != nil {
		return InstallationThemeRevision{}, err
	}

	return loadInstallationThemeRevision(ctx, installationStore.client, revisionID)
}

// LoadInstallationThemeRevision returns one immutable Draft inside a command.
func (transaction *CommandTx) LoadInstallationThemeRevision(
	ctx context.Context,
	revisionID int,
) (InstallationThemeRevision, error) {
	if err := requireActor(ctx, "CommandTx.LoadInstallationThemeRevision"); err != nil {
		return InstallationThemeRevision{}, err
	}

	return loadInstallationThemeRevision(ctx, transaction.transaction.Client(), revisionID)
}

func loadInstallationThemeRevision(
	ctx context.Context,
	client *ent.Client,
	revisionID int,
) (InstallationThemeRevision, error) {
	found, err := client.InstallationThemeRevision.Get(ctx, revisionID)
	if ent.IsNotFound(err) {
		return InstallationThemeRevision{}, ErrInstallationThemeRevisionNotFound
	}
	if err != nil {
		return InstallationThemeRevision{}, opaqueError("load Installation Theme Revision", err)
	}
	return installationThemeRevision(found), nil
}

// ListInstallationThemeRevisions returns newest immutable Drafts first.
func (installationStore *SQLite) ListInstallationThemeRevisions(
	ctx context.Context,
) ([]InstallationThemeRevision, error) {
	if err := requireActor(ctx, "SQLite.ListInstallationThemeRevisions"); err != nil {
		return []InstallationThemeRevision(nil), err
	}

	found, err := installationStore.client.InstallationThemeRevision.Query().
		Order(ent.Desc(installationthemerevision.FieldID)).
		All(ctx)
	if err != nil {
		return nil, opaqueError("list Installation Theme Revisions", err)
	}
	revisions := make([]InstallationThemeRevision, len(found))
	for index, item := range found {
		revisions[index] = installationThemeRevision(item)
	}
	return revisions, nil
}

// LoadActiveInstallationThemeRevision returns the selected Revision or ID zero.
func (installationStore *SQLite) LoadActiveInstallationThemeRevision(
	ctx context.Context,
) (InstallationThemeRevision, error) {
	if err := requireActor(ctx, "SQLite.LoadActiveInstallationThemeRevision"); err != nil {
		return InstallationThemeRevision{}, err
	}

	return loadActiveInstallationThemeRevision(
		ctx,
		installationStore.client,
		installationStore.LoadInstallationThemeRevision,
	)
}

// LoadActiveInstallationThemeRevision returns active Theme state inside a command.
func (transaction *CommandTx) LoadActiveInstallationThemeRevision(
	ctx context.Context,
) (InstallationThemeRevision, error) {
	if err := requireActor(ctx, "CommandTx.LoadActiveInstallationThemeRevision"); err != nil {
		return InstallationThemeRevision{}, err
	}

	return loadActiveInstallationThemeRevision(
		ctx,
		transaction.transaction.Client(),
		transaction.LoadInstallationThemeRevision,
	)
}

func loadActiveInstallationThemeRevision(
	ctx context.Context,
	client *ent.Client,
	load func(context.Context, int) (InstallationThemeRevision, error),
) (InstallationThemeRevision, error) {
	routing, err := client.Installation.Query().Only(ctx)
	if err != nil {
		return InstallationThemeRevision{}, opaqueError("load active Installation Theme Revision", err)
	}
	if routing.ActiveThemeRevisionID == nil {
		return InstallationThemeRevision{}, nil
	}
	return load(ctx, *routing.ActiveThemeRevisionID)
}

func installationThemeRevision(
	found *ent.InstallationThemeRevision,
) InstallationThemeRevision {
	return InstallationThemeRevision{
		ID: found.ID, Config: found.Config,
		CreatedByAccountID: found.CreatedByAccountID, CreatedAt: found.CreatedAt,
	}
}

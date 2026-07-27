package store

import (
	"context"
	"errors"
	"time"

	"github.com/dotwaffle/beamers/ent"
	"github.com/dotwaffle/beamers/ent/account"
	"github.com/dotwaffle/beamers/ent/votingeligibility"
	"github.com/dotwaffle/beamers/ent/votingkey"
)

var (
	// ErrVotingKeyUnavailable hides unknown, expired, revoked, used, and wrong-Event keys.
	ErrVotingKeyUnavailable = errors.New("voting key is unavailable")
	// ErrVotingKeyConflict means generated protected material collided.
	ErrVotingKeyConflict = errors.New("voting key conflict")
	// ErrVotingAlreadyEligible means an Account already has Event Voting Eligibility.
	ErrVotingAlreadyEligible = errors.New("account is already voting eligible")
)

// VotingKeyParams contains protected issuance material.
type VotingKeyParams struct {
	EventID              int
	TokenHash            string
	CreatedAt, ExpiresAt time.Time
}

// VotingKey is token-free durable key state.
type VotingKey struct {
	ID, EventID, RedeemedByAccountID int
	TokenHash                        string
	CreatedAt, ExpiresAt             time.Time
	RevokedAt, RedeemedAt            time.Time
}

// ListVotingKeys returns token-free Event inventory in issuance order.
func (installation *SQLite) ListVotingKeys(
	ctx context.Context,
	eventID int,
) ([]VotingKey, error) {
	found, err := installation.reader.VotingKey.Query().
		Where(votingkey.EventIDEQ(eventID)).
		Order(ent.Asc(votingkey.FieldID)).
		All(systemContext(ctx))
	if err != nil {
		return nil, opaqueError("list Voting Keys", err)
	}
	keys := make([]VotingKey, 0, len(found))
	for _, key := range found {
		projected := votingKey(key)
		projected.TokenHash = ""
		keys = append(keys, projected)
	}
	return keys, nil
}

// CreateVotingKey stores one protected Event-scoped key.
func (transaction *CommandTx) CreateVotingKey(
	ctx context.Context,
	params VotingKeyParams,
) (VotingKey, error) {
	created, err := transaction.transaction.VotingKey.Create().
		SetEventID(params.EventID).
		SetTokenHash(params.TokenHash).
		SetCreatedAt(params.CreatedAt).
		SetExpiresAt(params.ExpiresAt).
		Save(systemContext(ctx))
	if ent.IsConstraintError(err) {
		return VotingKey{}, ErrVotingKeyConflict
	}
	if err != nil {
		return VotingKey{}, opaqueError("create Voting Key", err)
	}
	return votingKey(created), nil
}

// RedeemVotingKey consumes one valid key and creates durable eligibility.
func (transaction *CommandTx) RedeemVotingKey(
	ctx context.Context,
	tokenHash string,
	eventID, accountID int,
	now time.Time,
) (VotingKey, error) {
	internalContext := systemContext(ctx)
	key, err := transaction.transaction.VotingKey.Query().Where(
		votingkey.TokenHashEQ(tokenHash),
		votingkey.EventIDEQ(eventID),
		votingkey.RevokedAtIsNil(),
		votingkey.RedeemedAtIsNil(),
		votingkey.ExpiresAtGT(now),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return VotingKey{}, ErrVotingKeyUnavailable
	}
	if err != nil {
		return VotingKey{}, opaqueError("load Voting Key", err)
	}
	enabled, err := transaction.transaction.Account.Query().Where(
		account.IDEQ(accountID),
		account.DisabledAtIsNil(),
	).Exist(internalContext)
	if err != nil {
		return VotingKey{}, opaqueError("load Voting Key Account", err)
	}
	if !enabled {
		return VotingKey{}, ErrVotingKeyUnavailable
	}
	eligible, err := transaction.transaction.VotingEligibility.Query().Where(
		votingeligibility.EventIDEQ(eventID),
		votingeligibility.AccountIDEQ(accountID),
	).Exist(internalContext)
	if err != nil {
		return VotingKey{}, opaqueError("inspect Voting Eligibility", err)
	}
	if eligible {
		return VotingKey{}, ErrVotingAlreadyEligible
	}
	if _, err = transaction.transaction.VotingEligibility.Create().
		SetEventID(eventID).
		SetAccountID(accountID).
		SetCreatedAt(now).
		Save(internalContext); err != nil {
		return VotingKey{}, opaqueError("create Voting Eligibility", err)
	}
	updated, err := transaction.transaction.VotingKey.Update().Where(
		votingkey.IDEQ(key.ID),
		votingkey.RevokedAtIsNil(),
		votingkey.RedeemedAtIsNil(),
	).SetRedeemedAt(now).
		SetRedeemedByAccountID(accountID).
		Save(internalContext)
	if err != nil {
		return VotingKey{}, opaqueError("consume Voting Key", err)
	}
	if updated != 1 {
		return VotingKey{}, ErrVotingKeyUnavailable
	}
	key.RedeemedAt = &now
	key.RedeemedByAccountID = accountID
	return votingKey(key), nil
}

// RevokeVotingKey prevents one unconsumed key from being redeemed.
func (transaction *CommandTx) RevokeVotingKey(
	ctx context.Context,
	eventID, keyID int,
	now time.Time,
) (VotingKey, error) {
	internalContext := systemContext(ctx)
	found, err := transaction.transaction.VotingKey.Query().Where(
		votingkey.IDEQ(keyID),
		votingkey.EventIDEQ(eventID),
		votingkey.RevokedAtIsNil(),
		votingkey.RedeemedAtIsNil(),
	).Only(internalContext)
	if ent.IsNotFound(err) {
		return VotingKey{}, ErrVotingKeyUnavailable
	}
	if err != nil {
		return VotingKey{}, opaqueError("load Voting Key for revocation", err)
	}
	updated, err := transaction.transaction.VotingKey.Update().Where(
		votingkey.IDEQ(found.ID),
		votingkey.RevokedAtIsNil(),
		votingkey.RedeemedAtIsNil(),
	).SetRevokedAt(now).Save(internalContext)
	if err != nil {
		return VotingKey{}, opaqueError("revoke Voting Key", err)
	}
	if updated != 1 {
		return VotingKey{}, ErrVotingKeyUnavailable
	}
	found.RevokedAt = &now
	return votingKey(found), nil
}

func votingKey(found *ent.VotingKey) VotingKey {
	result := VotingKey{
		ID: found.ID, EventID: found.EventID,
		RedeemedByAccountID: found.RedeemedByAccountID,
		TokenHash:           found.TokenHash, CreatedAt: found.CreatedAt, ExpiresAt: found.ExpiresAt,
	}
	if found.RevokedAt != nil {
		result.RevokedAt = *found.RevokedAt
	}
	if found.RedeemedAt != nil {
		result.RedeemedAt = *found.RedeemedAt
	}
	return result
}

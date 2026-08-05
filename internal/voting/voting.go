// Package voting owns Event Voting Key issuance and redemption.
package voting

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
)

const (
	votingTokenBytes = 20
	maxIssueCount    = 100
	maxKeyLifetime   = 30 * 24 * time.Hour
)

var (
	// ErrProducerRequired means Voting Keys were managed without Event authority.
	ErrProducerRequired = errors.New("producer authority required")
	// ErrInvalidInput means a Voting Key request is malformed or outside bounds.
	ErrInvalidInput = errors.New("invalid Voting Key request")
	// ErrKeysAlreadyIssued means an issuance retry cannot reveal secrets again.
	ErrKeysAlreadyIssued = errors.New("voting keys were already issued")
	// ErrKeyUnavailable hides invalid, wrong-Event, expired, revoked, and used keys.
	ErrKeyUnavailable = errors.New("voting key is unavailable")
	// ErrAlreadyEligible means the Account already has Event Voting Eligibility.
	ErrAlreadyEligible = errors.New("account is already voting eligible")
)

// IssueInput requests one bounded printable batch.
type IssueInput struct {
	EventID   int
	Count     int
	ExpiresAt time.Time
	CommandID string
}

// IssuedKey contains plaintext material returned exactly once.
type IssuedKey struct {
	ID        int
	Token     string
	ExpiresAt time.Time
}

// Key is token-free Producer inventory.
type Key struct {
	ID                  int       `json:"id"`
	RedeemedByAccountID int       `json:"redeemed_by_account_id,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	RevokedAt           time.Time `json:"revoked_at,omitzero"`
	RedeemedAt          time.Time `json:"redeemed_at,omitzero"`
}

// Service owns Voting Key command lifecycle and protected material.
type Service struct {
	storage      *store.SQLite
	random       io.Reader
	now          func() time.Time
	notifyVoting func()
}

// New creates a Voting Key service with explicit dependencies.
func New(
	storage *store.SQLite,
	random io.Reader,
	now func() time.Time,
	notifyVoting func(),
) (*Service, error) {
	if storage == nil {
		return nil, errors.New("voting key storage is required")
	}
	if random == nil {
		random = rand.Reader
	}
	if now == nil {
		return nil, errors.New("voting key clock is required")
	}
	return &Service{
		storage: storage, random: random, now: now, notifyVoting: notifyVoting,
	}, nil
}

// Issue creates one protected batch and returns printable values once.
func (service *Service) Issue(
	ctx context.Context,
	actor auth.Account,
	input IssueInput,
) ([]IssuedKey, error) {
	now := service.now().UTC()
	if err := command.ValidateID(input.CommandID); err != nil ||
		input.EventID <= 0 || input.Count <= 0 || input.Count > maxIssueCount ||
		!input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(maxKeyLifetime)) {
		return nil, ErrInvalidInput
	}
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      input.CommandID,
		PayloadHash: command.PayloadHash(
			strconv.Itoa(input.EventID),
			strconv.Itoa(input.Count),
			input.ExpiresAt.UTC().Format(time.RFC3339Nano),
		),
		Action:     "IssueVotingKeys",
		TargetType: "Event",
		TargetID:   strconv.Itoa(input.EventID),
		Now:        now,
	}
	ctx = actor.Context(ctx)
	result, err := command.Execute(ctx, command.Plan[[]IssuedKey]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(input.EventID), Refusals: votingKeyRejections,
		},
		Replay: func(outcome string) ([]IssuedKey, error) {
			var summary struct {
				Count int `json:"count"`
			}
			if err := decodeReceipt(outcome, &summary); err != nil {
				return nil, err
			}
			return nil, ErrKeysAlreadyIssued
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[[]IssuedKey], error) {
			issued := make([]IssuedKey, input.Count)
			for index := range input.Count {
				token, tokenErr := newVotingToken(service.random)
				if tokenErr != nil {
					return command.Execution[[]IssuedKey]{}, tokenErr
				}
				normalized, _ := normalizeVotingToken(token)
				created, createErr := transaction.CreateVotingKey(ctx, store.VotingKeyParams{
					EventID: input.EventID, TokenHash: tokenDigest(normalized),
					CreatedAt: now, ExpiresAt: input.ExpiresAt.UTC(),
				})
				if createErr != nil {
					return command.Execution[[]IssuedKey]{}, createErr
				}
				issued[index] = IssuedKey{
					ID: created.ID, Token: token, ExpiresAt: input.ExpiresAt.UTC(),
				}
			}
			outcome, encodeErr := json.Marshal(struct {
				Count     int       `json:"count"`
				ExpiresAt time.Time `json:"expires_at"`
			}{Count: len(issued), ExpiresAt: input.ExpiresAt.UTC()})
			if encodeErr != nil {
				return command.Execution[[]IssuedKey]{}, errors.New("encode Voting Key issuance")
			}
			return command.Success(issued, string(outcome)), nil
		},
	})
	return result, err
}

// Redeem consumes one key for the signed-in Account.
func (service *Service) Redeem(
	ctx context.Context,
	actor auth.Account,
	eventID int,
	token, commandID string,
) error {
	if err := command.ValidateID(commandID); err != nil || eventID <= 0 || actor.ID <= 0 {
		return ErrInvalidInput
	}
	normalized, err := normalizeVotingToken(token)
	if err != nil {
		return ErrKeyUnavailable
	}
	now := service.now().UTC()
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(strconv.Itoa(eventID), tokenDigest(normalized)),
		Action:      "RedeemVotingKey", TargetType: "Event", TargetID: strconv.Itoa(eventID), Now: now,
	}
	ctx = actor.Context(ctx)
	_, err = command.Execute(ctx, command.Plan[struct{}]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Installation(), Refusals: votingKeyRejections,
		},
		Replay: func(outcome string) (struct{}, error) {
			var stored struct{}
			decodeErr := decodeReceipt(outcome, &stored)
			return stored, decodeErr
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[struct{}], error) {
			_, redeemErr := transaction.RedeemVotingKey(
				ctx, tokenDigest(normalized), eventID, actor.ID, now,
			)
			switch {
			case errors.Is(redeemErr, store.ErrVotingKeyUnavailable):
				return reject[struct{}](ErrKeyUnavailable), nil
			case errors.Is(redeemErr, store.ErrVotingAlreadyEligible):
				return reject[struct{}](ErrAlreadyEligible), nil
			case redeemErr != nil:
				return command.Execution[struct{}]{}, redeemErr
			}
			return command.Success(struct{}{}, "{}"), nil
		},
	})
	return err
}

// Keys returns token-free inventory to an Event Producer.
func (service *Service) Keys(
	ctx context.Context,
	actor auth.Account,
	eventID int,
) ([]Key, error) {
	if !authz.Holds(actor.Identity(), eventID, authz.ManageVoting) {
		return nil, ErrProducerRequired
	}
	found, err := service.storage.ListVotingKeys(actor.Context(ctx), eventID)
	if err != nil {
		return nil, err
	}
	keys := make([]Key, 0, len(found))
	for _, item := range found {
		keys = append(keys, Key{
			ID: item.ID, RedeemedByAccountID: item.RedeemedByAccountID,
			CreatedAt: item.CreatedAt, ExpiresAt: item.ExpiresAt,
			RevokedAt: item.RevokedAt, RedeemedAt: item.RedeemedAt,
		})
	}
	return keys, nil
}

// Revoke prevents one unconsumed key from being redeemed.
func (service *Service) Revoke(
	ctx context.Context,
	actor auth.Account,
	eventID, keyID int,
	commandID string,
) (Key, error) {
	if err := command.ValidateID(commandID); err != nil || eventID <= 0 || keyID <= 0 {
		return Key{}, ErrInvalidInput
	}
	now := service.now().UTC()
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID, CommandID: commandID,
		PayloadHash: command.PayloadHash(strconv.Itoa(eventID), strconv.Itoa(keyID)),
		Action:      "RevokeVotingKey", TargetType: "VotingKey", TargetID: strconv.Itoa(keyID), Now: now,
	}
	ctx = actor.Context(ctx)
	return command.Execute(ctx, command.Plan[Key]{
		Storage: service.storage, Identity: identity,
		Authorization: command.Authorization{
			Facts: authz.Event(eventID), Refusals: votingKeyRejections,
		},
		Replay: func(outcome string) (Key, error) {
			var original Key
			err := decodeReceipt(outcome, &original)
			return original, err
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[Key], error) {
			revoked, revokeErr := transaction.RevokeVotingKey(ctx, eventID, keyID, now)
			if errors.Is(revokeErr, store.ErrVotingKeyUnavailable) {
				return reject[Key](ErrKeyUnavailable), nil
			}
			if revokeErr != nil {
				return command.Execution[Key]{}, revokeErr
			}
			result := key(revoked)
			outcome, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				return command.Execution[Key]{}, errors.New("encode Voting Key revocation")
			}
			return command.Success(result, string(outcome)), nil
		},
	})
}

func key(item store.VotingKey) Key {
	return Key{
		ID: item.ID, RedeemedByAccountID: item.RedeemedByAccountID,
		CreatedAt: item.CreatedAt, ExpiresAt: item.ExpiresAt,
		RevokedAt: item.RevokedAt, RedeemedAt: item.RedeemedAt,
	}
}

// votingKeyRejections is the single source for Voting Key rejection codes in
// both directions.
var votingKeyRejections = command.RejectionTable{
	Rejections: []command.Rejection{
		{Err: ErrProducerRequired, Code: "producer_required"},
		{Err: ErrAlreadyEligible, Code: "voting_already_eligible"},
		{Err: ErrKeyUnavailable, Code: "voting_key_unavailable"},
	},
}

func reject[T any](err error) command.Execution[T] {
	rejection, known := votingKeyRejections.Rejection(err)
	if !known {
		rejection = store.CommandRejection{Code: "voting_key_unavailable"}
	}
	var zero T
	return command.Reject(zero, rejection, err)
}

func decodeReceipt(outcome string, target any) error {
	return votingKeyRejections.Restore(store.DecodeCommandReceipt(outcome, target))
}

func newVotingToken(random io.Reader) (string, error) {
	raw := make([]byte, votingTokenBytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", errors.New("generate Voting Key")
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	parts := make([]string, 0, len(encoded)/4)
	for encoded != "" {
		parts = append(parts, encoded[:4])
		encoded = encoded[4:]
	}
	return strings.Join(parts, "-"), nil
}

func normalizeVotingToken(value string) (string, error) {
	value = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(
		strings.TrimSpace(value), "-", "",
	), " ", ""))
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(value)
	if err != nil || len(decoded) != votingTokenBytes {
		return "", ErrKeyUnavailable
	}
	return value, nil
}

func tokenDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

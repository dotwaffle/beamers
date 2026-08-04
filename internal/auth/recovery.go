package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/dotwaffle/beamers/internal/authz"
	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/systemactor"
)

// ReplaceRecoveryCodes invalidates prior codes and returns one write-only replacement set.
func (service *Service) ReplaceRecoveryCodes(
	ctx context.Context,
	actor Account,
	commandID string,
) ([]string, error) {
	if service.storageDegraded() {
		return nil, ErrStorageDegraded
	}
	if actor.ID <= 0 {
		return nil, ErrInvalidSession
	}
	if err := command.ValidateID(commandID); err != nil {
		return nil, ErrInvalidAccountDetails
	}
	codes := make([]string, recoveryCodeCount)
	hashes := make([]string, recoveryCodeCount)
	for index := range codes {
		code, err := service.newToken()
		if err != nil {
			return nil, err
		}
		codes[index] = code
		hashes[index] = tokenDigest(code)
	}
	now := service.now().UTC()
	ctx = actor.Context(ctx)
	result, err := command.Execute(ctx, command.Plan[[]string]{
		Storage:       service.storage,
		Authorization: command.Authorization{Facts: authz.Installation(), Refusals: accountRejections},
		Identity: store.CommandIdentity{
			ActorAccountID: actor.ID,
			CommandID:      commandID,
			PayloadHash:    command.PayloadHash(strconv.Itoa(actor.ID)),
			Action:         "ReplaceRecoveryCodes",
			TargetType:     "Account",
			TargetID:       strconv.Itoa(actor.ID),
			Now:            now,
		},
		Replay: func(outcome string) ([]string, error) {
			var original struct {
				Count int `json:"count"`
			}
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return nil, decodeErr
			}
			return nil, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[[]string], error) {
			if replaceErr := transaction.ReplaceRecoveryCodes(
				actor.Context(ctx),
				actor.ID,
				hashes,
				now,
			); replaceErr != nil {
				return command.Execution[[]string]{}, replaceErr
			}
			outcome, encodeErr := json.Marshal(struct {
				Count int `json:"count"`
			}{Count: len(codes)})
			if encodeErr != nil {
				return command.Execution[[]string]{}, errors.New(
					"encode Recovery Code replacement outcome",
				)
			}
			return command.Success(codes, string(outcome)), nil
		},
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, ErrRecoveryCodesAlreadyReplaced
	}
	return result, nil
}

// Recover consumes one Account or Administrator recovery credential and
// replaces the Account password and sessions atomically.
func (service *Service) Recover(
	ctx context.Context,
	handle string,
	secret string,
	password string,
	commandID string,
) (Session, error) {
	if service.storageDegraded() {
		return Session{}, ErrStorageDegraded
	}
	normalizedName, _, nameErr := normalizeAccountName(handle)
	if nameErr != nil || !validToken(secret) {
		return Session{}, ErrAuthenticationFailed
	}
	if err := command.ValidateID(commandID); err != nil {
		return Session{}, ErrInvalidAccountDetails
	}
	if !service.validPassword(password) {
		return Session{}, ErrInvalidAccountDetails
	}
	passwordHash, err := service.hashPassword(password)
	if err != nil {
		return Session{}, err
	}
	sessionToken, err := service.newToken()
	if err != nil {
		return Session{}, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(service.sessionTTL)
	secretHash := tokenDigest(secret)
	ctx = systemactor.NewContext(ctx, systemactor.PublicVisitor)
	recoverable, err := service.storage.FindRecoveryTarget(ctx, normalizedName)
	if errors.Is(err, store.ErrInvalidRecovery) {
		return Session{}, ErrAuthenticationFailed
	}
	if err != nil {
		return Session{}, err
	}
	type recoveryCommandResult struct {
		account store.AccountCredential
		applied bool
	}
	recovered, err := command.Execute(
		account(recoverable).Context(ctx),
		command.Plan[recoveryCommandResult]{
			Storage:       service.storage,
			Authorization: command.Authorization{Facts: authz.Installation(), Refusals: accountRejections},
			Identity: store.CommandIdentity{
				ActorAccountID: recoverable.ID,
				CommandID:      commandID,
				PayloadHash:    command.PayloadHash(normalizedName),
				Action:         "RecoverAccount",
				TargetType:     "Account",
				TargetID:       strconv.Itoa(recoverable.ID),
				Now:            now,
			},
			Replay: func(outcome string) (recoveryCommandResult, error) {
				var original store.AccountCredential
				if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
					return recoveryCommandResult{}, decodeErr
				}
				return recoveryCommandResult{account: original}, nil
			},
			Apply: func(transaction *store.CommandTx) (
				command.Execution[recoveryCommandResult],
				error,
			) {
				updated, recoverErr := transaction.RecoverAccount(
					account(recoverable).Context(ctx),
					store.RecoverAccountParams{
						NormalizedName: normalizedName,
						SecretHash:     secretHash,
						PasswordHash:   passwordHash,
						SessionHash:    tokenDigest(sessionToken),
						Now:            now,
						SessionExpiry:  expiresAt,
					},
				)
				if recoverErr != nil {
					return command.Execution[recoveryCommandResult]{}, recoverErr
				}
				outcome, encodeErr := json.Marshal(updated)
				if encodeErr != nil {
					return command.Execution[recoveryCommandResult]{}, errors.New(
						"encode Account recovery outcome",
					)
				}
				return command.Success(
					recoveryCommandResult{account: updated, applied: true},
					string(outcome),
				), nil
			},
		},
	)
	if errors.Is(err, store.ErrInvalidRecovery) || errors.Is(err, store.ErrLastAdministrator) {
		return Session{}, ErrAuthenticationFailed
	}
	if err != nil {
		return Session{}, err
	}
	if !recovered.applied {
		return Session{}, ErrRecoveryAlreadyCompleted
	}
	service.forgetAccountSessions(recovered.account.ID)
	session := newSession(sessionToken, expiresAt, recovered.account)
	service.rememberSession(sessionToken, session.Account, expiresAt)
	return session, nil
}

// IssueRecoveryToken creates one audited Administrator-assisted recovery grant.
func (service *Service) IssueRecoveryToken(
	ctx context.Context,
	actor Account,
	accountID int,
	reason string,
	commandID string,
) (RecoveryToken, error) {
	if service.storageDegraded() {
		return RecoveryToken{}, ErrStorageDegraded
	}
	if err := command.ValidateID(commandID); err != nil {
		return RecoveryToken{}, ErrInvalidAccountDetails
	}
	reason = strings.TrimSpace(reason)
	token, err := service.newToken()
	if err != nil {
		return RecoveryToken{}, err
	}
	now := service.now().UTC()
	expiresAt := now.Add(service.recoveryTokenTTL)
	identity := store.CommandIdentity{
		ActorAccountID: actor.ID,
		CommandID:      commandID,
		PayloadHash:    command.PayloadHash(strconv.Itoa(accountID), reason),
		Action:         "IssueAccountRecoveryToken",
		TargetType:     "Account",
		TargetID:       strconv.Itoa(accountID),
		Now:            now,
	}
	ctx = actor.Context(ctx)
	result, err := command.Execute(ctx, command.Plan[RecoveryToken]{
		Storage:       service.storage,
		Authorization: command.Authorization{Facts: authz.Installation(), Refusals: accountRejections},
		Identity:      identity,
		Replay: func(outcome string) (RecoveryToken, error) {
			var original struct {
				AccountID int `json:"account_id"`
			}
			if decodeErr := store.DecodeCommandReceipt(outcome, &original); decodeErr != nil {
				return RecoveryToken{}, restoreRejected(decodeErr)
			}
			return RecoveryToken{}, nil
		},
		Apply: func(transaction *store.CommandTx) (command.Execution[RecoveryToken], error) {
			switch {
			case !actor.Administrator:
				return accountRejectionExecution[RecoveryToken](ErrAdministratorRequired), nil
			case reason == "" || len(reason) > 1000:
				return accountRejectionExecution[RecoveryToken](ErrRecoveryReasonRequired), nil
			}
			issueErr := transaction.IssueRecoveryToken(actor.Context(ctx), store.RecoveryTokenParams{
				AccountID: accountID,
				TokenHash: tokenDigest(token),
				Now:       now,
				ExpiresAt: expiresAt,
			})
			switch {
			case errors.Is(issueErr, store.ErrInvalidRecovery):
				return accountRejectionExecution[RecoveryToken](ErrRecoveryAccountNotFound), nil
			case errors.Is(issueErr, store.ErrLastAdministrator):
				return accountRejectionExecution[RecoveryToken](ErrLastAdministrator), nil
			case issueErr != nil:
				return command.Execution[RecoveryToken]{}, issueErr
			}
			outcome, encodeErr := json.Marshal(struct {
				AccountID int `json:"account_id"`
			}{AccountID: accountID})
			if encodeErr != nil {
				return command.Execution[RecoveryToken]{}, errors.New("encode recovery token outcome")
			}
			return command.Success(
				RecoveryToken{Token: token, ExpiresAt: expiresAt},
				string(outcome),
			).WithAudit(store.AuditDetails{Reason: reason}), nil
		},
	})
	if err != nil {
		return RecoveryToken{}, err
	}
	if result.Token == "" {
		return RecoveryToken{}, ErrRecoveryTokenAlreadyIssued
	}
	service.forgetAccountSessions(accountID)
	return result, nil
}

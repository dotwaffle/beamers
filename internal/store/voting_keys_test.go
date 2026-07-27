package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dotwaffle/beamers/ent/votingeligibility"
)

func TestVotingKeyRedemptionIsSingleUseAndEventScoped(t *testing.T) {
	client := openEntTestClient(t)
	installation := &SQLite{client: client}
	ctx := systemContext(t.Context())
	now := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	event := createSchemaTestEvent(t, client)
	otherEvent := createSchemaTestEvent(t, client)
	first := createSubmissionTestAccount(t, client, ctx, "alice-voter", "Alice Voter")
	second := createSubmissionTestAccount(t, client, ctx, "bob-voter", "Bob Voter")

	issue, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Voting Key issuance: %v", err)
	}
	created, err := issue.CreateVotingKey(ctx, VotingKeyParams{
		EventID: event.ID, TokenHash: strings.Repeat("a", 64),
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create Voting Key: %v", err)
	}
	expired, err := issue.CreateVotingKey(ctx, VotingKeyParams{
		EventID: event.ID, TokenHash: strings.Repeat("b", 64),
		CreatedAt: now.Add(-time.Hour), ExpiresAt: now,
	})
	if err != nil {
		t.Fatalf("create expired Voting Key: %v", err)
	}
	if err = issue.Commit(); err != nil {
		t.Fatalf("commit Voting Key issuance: %v", err)
	}

	wrongEvent, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin wrong-Event redemption: %v", err)
	}
	if _, err = wrongEvent.RedeemVotingKey(
		ctx, created.TokenHash, otherEvent.ID, first.ID, now,
	); !errors.Is(err, ErrVotingKeyUnavailable) {
		t.Fatalf("wrong-Event redemption error = %v", err)
	}
	_ = wrongEvent.Rollback()

	expiredRedemption, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin expired redemption: %v", err)
	}
	if _, err = expiredRedemption.RedeemVotingKey(
		ctx, expired.TokenHash, event.ID, first.ID, now,
	); !errors.Is(err, ErrVotingKeyUnavailable) {
		t.Fatalf("expired Voting Key error = %v", err)
	}
	_ = expiredRedemption.Rollback()

	redeem, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin Voting Key redemption: %v", err)
	}
	if _, err = redeem.RedeemVotingKey(
		ctx, created.TokenHash, event.ID, first.ID, now,
	); err != nil {
		t.Fatalf("redeem Voting Key: %v", err)
	}
	if err = redeem.Commit(); err != nil {
		t.Fatalf("commit Voting Key redemption: %v", err)
	}
	if count := client.VotingEligibility.Query().Where(
		votingeligibility.EventIDEQ(event.ID),
		votingeligibility.AccountIDEQ(first.ID),
	).CountX(ctx); count != 1 {
		t.Fatalf("Voting Eligibility count = %d, want 1", count)
	}

	reuse, err := installation.BeginCommand(ctx)
	if err != nil {
		t.Fatalf("begin reused Voting Key redemption: %v", err)
	}
	if _, err = reuse.RedeemVotingKey(
		ctx, created.TokenHash, event.ID, second.ID, now,
	); !errors.Is(err, ErrVotingKeyUnavailable) {
		t.Fatalf("reused Voting Key error = %v", err)
	}
	_ = reuse.Rollback()
}

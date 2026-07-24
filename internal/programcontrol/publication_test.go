package programcontrol

import (
	"testing"

	"github.com/dotwaffle/beamers/internal/command"
	"github.com/dotwaffle/beamers/internal/results"
)

func TestProgressiveReconciliationIdentityIncludesCommandScope(t *testing.T) {
	states := []results.ResultItemStageState{{
		Ref: results.ResultItemRef{
			Kind: results.ResultItemCompetition, CompetitionSessionID: 17,
		},
		Status: results.ResultItemRevealed, Release: results.ResultReleaseReady,
	}}
	first, err := progressiveReconciliationPayload(3, 7, 11, states)
	if err != nil {
		t.Fatalf("encode first reconciliation identity: %v", err)
	}
	second, err := progressiveReconciliationPayload(3, 8, 11, states)
	if err != nil {
		t.Fatalf("encode second reconciliation identity: %v", err)
	}
	if command.PayloadHash(first) == command.PayloadHash(second) {
		t.Fatal("Progressive reconciliation identities collide across Sessions")
	}
}

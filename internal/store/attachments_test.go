package store

import (
	"testing"
	"time"
)

func TestReopenWindowActiveUsesExplicitServerTime(t *testing.T) {
	expiresAt := time.Date(2026, time.July, 23, 15, 0, 0, 0, time.UTC)
	if !reopenWindowActive(expiresAt, time.Time{}, expiresAt.Add(-time.Nanosecond)) {
		t.Fatal("Reopen Window is inactive before its expiry")
	}
	if reopenWindowActive(expiresAt, time.Time{}, expiresAt) {
		t.Fatal("Reopen Window is active at its exact expiry")
	}
	if reopenWindowActive(expiresAt, expiresAt.Add(-time.Minute), expiresAt.Add(-time.Hour)) {
		t.Fatal("closed Reopen Window is active")
	}
}

func TestEntryReopenWindowMustFollowRejectionClosure(t *testing.T) {
	rejectedAt := time.Date(2026, time.July, 23, 15, 0, 0, 0, time.UTC)
	now := rejectedAt.Add(time.Minute)
	if entryReopenWindowActive(
		now.Add(time.Hour), time.Time{}, rejectedAt.Add(-time.Minute), rejectedAt, now,
	) {
		t.Fatal("Reopen Window created before rejection remains active")
	}
	if !entryReopenWindowActive(
		now.Add(time.Hour), time.Time{}, rejectedAt.Add(time.Second), rejectedAt, now,
	) {
		t.Fatal("Reopen Window created after rejection is inactive")
	}
}

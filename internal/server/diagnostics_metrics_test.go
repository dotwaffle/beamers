package server

import (
	"math"
	"testing"
)

func TestSaturatingInt64ClampsAtMaxInt64(t *testing.T) {
	if got := saturatingInt64(math.MaxUint64); got != math.MaxInt64 {
		t.Fatalf("saturatingInt64(MaxUint64) = %d, want %d", got, int64(math.MaxInt64))
	}
	if got := saturatingInt64(0); got != 0 {
		t.Fatalf("saturatingInt64(0) = %d, want 0", got)
	}
	if got := saturatingInt64(42); got != 42 {
		t.Fatalf("saturatingInt64(42) = %d, want 42", got)
	}
}

func TestCollectStorageStatsDefaultsAttachmentsDirFromDataDir(t *testing.T) {
	dataDir := t.TempDir()
	stats, err := collectStorageStats(dataDir, "")
	if err != nil {
		t.Fatalf("collectStorageStats: %v", err)
	}
	if stats.freeDiskBytes == 0 {
		t.Fatal("free disk bytes = 0, want positive")
	}
	// No attachments directory was created under dataDir, so DirSize
	// reports zero rather than failing, matching its not-yet-created
	// contract used by the default filepath.Join(dataDir, "attachments").
	if stats.attachmentsBytes != 0 {
		t.Fatalf("attachments bytes = %d, want 0", stats.attachmentsBytes)
	}
}

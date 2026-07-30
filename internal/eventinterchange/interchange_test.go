package eventinterchange_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/eventinterchange"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestPublishedEventRoundTripsThroughReviewedDraftImport(t *testing.T) {
	storage, actor, sourceEventID := publishedSourceEvent(t)
	now := func() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) }
	service, err := eventinterchange.New(storage, now)
	if err != nil {
		t.Fatalf("create Event Interchange service: %v", err)
	}

	exported, err := service.Export(t.Context(), actor, eventinterchange.ExportInput{
		EventID: sourceEventID, CommandID: "export-source-event",
	})
	if err != nil {
		t.Fatalf("export source Event: %v", err)
	}
	if bytes.Contains(exported.Document, []byte("must not leave this installation")) {
		t.Fatalf("Event interchange exported Crew Notes: %s", exported.Document)
	}
	review, err := service.ReviewImport(t.Context(), actor, exported.Document)
	if err != nil {
		t.Fatalf("review Event import: %v", err)
	}
	if review.EventName != "Révision 日本 2026" ||
		review.LocationCount != 1 ||
		review.LaneCount != 1 ||
		review.TrackCount != 1 ||
		review.SessionCount != 1 {
		t.Fatalf("Import Review = %+v", review)
	}
	imported, err := service.Import(t.Context(), actor, eventinterchange.ImportInput{
		Document: exported.Document, ReviewFingerprint: review.Fingerprint,
		CommandID: "import-source-event",
	})
	if err != nil {
		t.Fatalf("import Event: %v", err)
	}
	if imported.EventID == sourceEventID || imported.DraftRevision != 1 || len(imported.Changes) != 4 {
		t.Fatalf("Import result = %+v", imported)
	}

	importedActor := actor
	importedActor.EventRoles = cloneRoles(actor.EventRoles)
	importedActor.EventRoles[imported.EventID] = viewer.Producer
	publishImported(t, storage, importedActor, imported)

	reexported, err := service.Export(t.Context(), importedActor, eventinterchange.ExportInput{
		EventID: imported.EventID, CommandID: "export-imported-event",
	})
	if err != nil {
		t.Fatalf("re-export imported Event: %v", err)
	}
	if !bytes.Equal(reexported.Document, exported.Document) {
		t.Fatalf("round-trip document changed:\nsource: %s\nimported: %s", exported.Document, reexported.Document)
	}

	revokedActor := actor
	revokedActor.Administrator = false
	revokedActor.EventRoles = nil
	replayed, err := service.Import(t.Context(), revokedActor, eventinterchange.ImportInput{
		Document: exported.Document, ReviewFingerprint: review.Fingerprint,
		CommandID: "import-source-event",
	})
	if err != nil || replayed.EventID != imported.EventID {
		t.Fatalf("exact Import retry = %+v, %v", replayed, err)
	}
	changed := bytes.Replace(exported.Document, []byte("Bienvenue"), []byte("Salut"), 1)
	if _, err = service.Import(t.Context(), actor, eventinterchange.ImportInput{
		Document: changed, ReviewFingerprint: review.Fingerprint,
		CommandID: "import-source-event",
	}); !errors.Is(err, eventinterchange.ErrCommandConflict) {
		t.Fatalf("changed Import retry error = %v, want %v", err, eventinterchange.ErrCommandConflict)
	}
	audits, err := storage.ListAuditEntries(actor.Context(t.Context()))
	if err != nil {
		t.Fatalf("list Event interchange Audit Entries: %v", err)
	}
	if countAuditAction(audits, "ExportEventInterchange") != 2 ||
		countAuditAction(audits, "ImportEventInterchange") != 2 {
		t.Fatalf("Event interchange Audit actions = %+v", audits)
	}
}

func TestImportRejectsForwardVersionBeforeMutation(t *testing.T) {
	storage, actor, sourceEventID := publishedSourceEvent(t)
	service, err := eventinterchange.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Event Interchange service: %v", err)
	}
	exported, err := service.Export(t.Context(), actor, eventinterchange.ExportInput{
		EventID: sourceEventID, CommandID: "export-for-forward-version",
	})
	if err != nil {
		t.Fatalf("export source Event: %v", err)
	}
	forward := bytes.Replace(exported.Document, []byte(`"version":2`), []byte(`"version":3`), 1)

	if _, err = service.ReviewImport(t.Context(), actor, forward); !errors.Is(err, eventinterchange.ErrUnsupportedVersion) {
		t.Fatalf("forward-version Review error = %v, want %v", err, eventinterchange.ErrUnsupportedVersion)
	}
	if _, err = service.Import(t.Context(), actor, eventinterchange.ImportInput{
		Document: forward, ReviewFingerprint: strings.Repeat("0", 64),
		CommandID: "import-forward-version",
	}); !errors.Is(err, eventinterchange.ErrUnsupportedVersion) {
		t.Fatalf("forward-version Import error = %v, want %v", err, eventinterchange.ErrUnsupportedVersion)
	}

	review, err := service.ReviewImport(t.Context(), actor, exported.Document)
	if err != nil {
		t.Fatalf("review valid document after rejection: %v", err)
	}
	imported, err := service.Import(t.Context(), actor, eventinterchange.ImportInput{
		Document: exported.Document, ReviewFingerprint: review.Fingerprint,
		CommandID: "import-after-forward-version",
	})
	if err != nil || imported.EventID <= sourceEventID {
		t.Fatalf("valid Import after rejection = %+v, %v", imported, err)
	}
}

func TestVersionOneImportDefaultsSubmissionEligibility(t *testing.T) {
	storage, actor, sourceEventID := publishedSourceEvent(t)
	now := func() time.Time { return time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) }
	service, err := eventinterchange.New(storage, now)
	if err != nil {
		t.Fatalf("create Event Interchange service: %v", err)
	}
	exported, err := service.Export(t.Context(), actor, eventinterchange.ExportInput{
		EventID: sourceEventID, CommandID: "export-for-version-one",
	})
	if err != nil {
		t.Fatalf("export source Event: %v", err)
	}
	versionOne := bytes.Replace(exported.Document, []byte(`"version":2`), []byte(`"version":1`), 1)
	versionOne = bytes.Replace(
		versionOne,
		[]byte(`,"submission_eligibility":"VotingEligibleAccounts"`),
		nil,
		1,
	)
	review, err := service.ReviewImport(t.Context(), actor, versionOne)
	if err != nil {
		t.Fatalf("review version 1 import: %v", err)
	}
	imported, err := service.Import(t.Context(), actor, eventinterchange.ImportInput{
		Document: versionOne, ReviewFingerprint: review.Fingerprint,
		CommandID: "import-version-one",
	})
	if err != nil {
		t.Fatalf("import version 1 Event: %v", err)
	}
	eventService, err := events.New(storage, now, nil, nil)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	importedActor := actor
	importedActor.EventRoles = cloneRoles(actor.EventRoles)
	importedActor.EventRoles[imported.EventID] = viewer.Producer
	found, err := eventService.CrewEvent(t.Context(), importedActor, imported.EventID)
	if err != nil || found.SubmissionEligibility != "AllAccounts" {
		t.Fatalf("version 1 Submission Eligibility = %q, %v", found.SubmissionEligibility, err)
	}
}

func TestImportRejectsDuplicateAndDanglingDocumentRefs(t *testing.T) {
	storage, actor, sourceEventID := publishedSourceEvent(t)
	service, err := eventinterchange.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Event Interchange service: %v", err)
	}
	exported, err := service.Export(t.Context(), actor, eventinterchange.ExportInput{
		EventID: sourceEventID, CommandID: "export-for-invalid-refs",
	})
	if err != nil {
		t.Fatalf("export source Event: %v", err)
	}
	duplicate := bytes.Replace(
		exported.Document,
		[]byte(`"ref":"lane-1"`),
		[]byte(`"ref":"location-1"`),
		1,
	)
	if _, err = service.ReviewImport(t.Context(), actor, duplicate); !errors.Is(err, eventinterchange.ErrInvalidDocument) {
		t.Fatalf("duplicate-ref Review error = %v, want %v", err, eventinterchange.ErrInvalidDocument)
	}
	duplicateRelationship := bytes.Replace(
		exported.Document,
		[]byte(`"lane_refs":["lane-1"]`),
		[]byte(`"lane_refs":["lane-1","lane-1"]`),
		1,
	)
	if _, err = service.ReviewImport(t.Context(), actor, duplicateRelationship); !errors.Is(err, eventinterchange.ErrInvalidDocument) {
		t.Fatalf("duplicate relationship Review error = %v, want %v", err, eventinterchange.ErrInvalidDocument)
	}
	dangling := bytes.Replace(
		exported.Document,
		[]byte(`"location_ref":"location-1"`),
		[]byte(`"location_ref":"another-event-location"`),
		1,
	)
	if _, err = service.ReviewImport(t.Context(), actor, dangling); !errors.Is(err, eventinterchange.ErrInvalidDocument) {
		t.Fatalf("cross-Event ref Review error = %v, want %v", err, eventinterchange.ErrInvalidDocument)
	}
	if _, err = service.Import(t.Context(), actor, eventinterchange.ImportInput{
		Document: dangling, ReviewFingerprint: fingerprintForTest(dangling),
		CommandID: "import-dangling-ref",
	}); !errors.Is(err, eventinterchange.ErrInvalidDocument) {
		t.Fatalf("dangling-ref Import error = %v, want %v", err, eventinterchange.ErrInvalidDocument)
	}

	review, err := service.ReviewImport(t.Context(), actor, exported.Document)
	if err != nil {
		t.Fatalf("review valid document after invalid refs: %v", err)
	}
	if _, err = service.Import(t.Context(), actor, eventinterchange.ImportInput{
		Document: exported.Document, ReviewFingerprint: review.Fingerprint,
		CommandID: "import-after-invalid-refs",
	}); err != nil {
		t.Fatalf("valid Import after invalid refs: %v", err)
	}
}

func TestEventInterchangeRequiresExplicitAuthorityAndExactReview(t *testing.T) {
	storage, actor, sourceEventID := publishedSourceEvent(t)
	service, err := eventinterchange.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Event Interchange service: %v", err)
	}
	observer := actor
	observer.Administrator = false
	observer.EventRoles = map[int]viewer.Role{sourceEventID: viewer.Observer}
	if _, err = service.Export(t.Context(), observer, eventinterchange.ExportInput{
		EventID: sourceEventID, CommandID: "unauthorized-export",
	}); !errors.Is(err, eventinterchange.ErrEventAccessDenied) {
		t.Fatalf("Observer Export error = %v, want %v", err, eventinterchange.ErrEventAccessDenied)
	}
	if _, err = service.Export(t.Context(), observer, eventinterchange.ExportInput{
		EventID: sourceEventID, CommandID: "unauthorized-export",
	}); !errors.Is(err, eventinterchange.ErrEventAccessDenied) {
		t.Fatalf("replayed Observer Export error = %v, want %v", err, eventinterchange.ErrEventAccessDenied)
	}

	exported, err := service.Export(t.Context(), actor, eventinterchange.ExportInput{
		EventID: sourceEventID, CommandID: "authorized-export",
	})
	if err != nil {
		t.Fatalf("Producer Export: %v", err)
	}
	if _, err = service.ReviewImport(t.Context(), observer, exported.Document); !errors.Is(err, eventinterchange.ErrAdministratorRequired) {
		t.Fatalf("non-Administrator Review error = %v, want %v", err, eventinterchange.ErrAdministratorRequired)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = service.ReviewImport(canceled, actor, exported.Document); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Review error = %v, want %v", err, context.Canceled)
	}
	if _, err = service.Import(t.Context(), actor, eventinterchange.ImportInput{
		Document: exported.Document, ReviewFingerprint: strings.Repeat("0", 64),
		CommandID: "mismatched-review",
	}); !errors.Is(err, eventinterchange.ErrReviewMismatch) {
		t.Fatalf("mismatched Review error = %v, want %v", err, eventinterchange.ErrReviewMismatch)
	}
	needsTrimming := bytes.Replace(exported.Document, []byte(`"name":"Révision`), []byte(`"name":" Révision`), 1)
	if _, err = service.ReviewImport(t.Context(), actor, needsTrimming); !errors.Is(err, eventinterchange.ErrInvalidDocument) {
		t.Fatalf("noncanonical Unicode text error = %v, want %v", err, eventinterchange.ErrInvalidDocument)
	}
}

func TestOversizeImportRejectionIsAuditedAndReplayable(t *testing.T) {
	storage, actor, _ := publishedSourceEvent(t)
	service, err := eventinterchange.New(storage, time.Now)
	if err != nil {
		t.Fatalf("create Event Interchange service: %v", err)
	}
	document := make([]byte, (64<<20)+1)
	copy(document, `{"format":"beamers.event","version":1}`)
	input := eventinterchange.ImportInput{
		Document: document, ReviewFingerprint: strings.Repeat("0", 64),
		CommandID: "oversize-import",
	}
	if _, err = service.Import(t.Context(), actor, input); !errors.Is(err, eventinterchange.ErrInvalidDocument) {
		t.Fatalf("oversize Import error = %v, want %v", err, eventinterchange.ErrInvalidDocument)
	}

	revokedActor := actor
	revokedActor.Administrator = false
	revokedActor.EventRoles = nil
	if _, err = service.Import(t.Context(), revokedActor, input); !errors.Is(err, eventinterchange.ErrInvalidDocument) {
		t.Fatalf("replayed oversize Import error = %v, want %v", err, eventinterchange.ErrInvalidDocument)
	}
	audits, err := storage.ListAuditEntries(actor.Context(t.Context()))
	if err != nil {
		t.Fatalf("list oversize Import Audit Entries: %v", err)
	}
	if countAuditAction(audits, "ImportEventInterchange") != 1 {
		t.Fatalf("oversize Import Audit actions = %+v", audits)
	}
}

func publishedSourceEvent(t *testing.T) (*store.SQLite, auth.Account, int) {
	t.Helper()
	dataDir := t.TempDir()
	if err := store.Initialize(t.Context(), dataDir); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}
	storage, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := storage.Close(); closeErr != nil {
			t.Errorf("close storage: %v", closeErr)
		}
	})
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	bootstrapHash := strings.Repeat("b", 64)
	if err = storage.IssueBootstrap(t.Context(), bootstrapHash, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	account, err := storage.BootstrapAdministrator(t.Context(), store.BootstrapAdministratorParams{
		BootstrapHash: bootstrapHash,
		Name:          "Producer", NormalizedName: "producer", PasswordHash: "test-password-hash",
		SessionHash: strings.Repeat("s", 64), Now: now, SessionExpiry: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	actor := auth.Account{ID: account.ID, Name: account.Name, Administrator: true}
	eventService, err := events.New(storage, func() time.Time { return now }, nil, nil)
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(t.Context(), actor, events.CreateInput{
		Name: "Révision 日本 2026", PlannedStartDate: "2026-10-24", PlannedEndDate: "2026-10-26",
		Timezone: "Europe/Berlin", EventLocale: "fr-FR", ContentLanguage: "ja",
		EventDayBoundary: "06:00", EntryDefaultDisposition: "Included",
		SubmissionEligibility:          "VotingEligibleAccounts",
		TargetAdjustmentPresetsSeconds: []int{-120, 180}, CommandID: "create-interchange-source",
	})
	if err != nil {
		t.Fatalf("create source Event: %v", err)
	}
	if _, err = eventService.GrantEventAccess(
		t.Context(), actor, event.ID, actor.ID, "Producer", "grant-interchange-producer",
	); err != nil {
		t.Fatalf("grant source Producer: %v", err)
	}
	actor.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}

	// 01:30 UTC is the later 02:30 occurrence after Berlin's fall-back.
	plannedStart := time.Date(2026, time.October, 25, 1, 30, 0, 0, time.UTC)
	commands, err := rundown.NewCommands(storage, func() time.Time { return now }, nil, nil)
	if err != nil {
		t.Fatalf("create Rundown Commands: %v", err)
	}
	edited, err := commands.EditDraft(t.Context(), actor, rundown.EditDraftInput{
		EventID: event.ID, CommandID: "draft-interchange-source",
		Locations: []rundown.LocationDraftInput{{Ref: "location", Name: "Scène principale"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: "lane", Name: "Voie α", Location: rundown.TargetRef{Ref: "location"},
		}},
		Tracks: []rundown.TrackDraftInput{{Ref: "track", Name: "日本語"}},
		Sessions: []rundown.SessionDraftInput{{
			Ref: "session", Title: "Ouverture 日本", Speaker: "Zoë",
			Type: rundown.SessionCeremony, AudienceVisibility: rundown.AudiencePublic,
			PublicDetails: "Bienvenue 👋", CrewNotes: "must not leave this installation",
			PlannedStart: plannedStart, PlannedEnd: plannedStart.Add(time.Hour),
			TimingPolicy: rundown.TimingFixedEnd, MinimumDuration: 30 * time.Minute,
			StartBoundary: rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
			Lanes: []rundown.TargetRef{{Ref: "lane"}}, Locations: []rundown.TargetRef{{Ref: "location"}},
			Tracks: []rundown.TargetRef{{Ref: "track"}},
		}},
	})
	if err != nil {
		t.Fatalf("create source Draft: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown Queries: %v", err)
	}
	preview, err := queries.PublishPreview(t.Context(), actor, rundown.PublishPreviewInput{
		EventID: event.ID, ChangeIDs: []int{edited.Changes[len(edited.Changes)-1].ID},
	})
	if err != nil {
		t.Fatalf("preview source Publish: %v", err)
	}
	if _, err = commands.Publish(t.Context(), actor, rundown.PublishInput{
		EventID: event.ID, CommandID: "publish-interchange-source",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		t.Fatalf("publish source Event: %v", err)
	}
	return storage, actor, event.ID
}

func publishImported(
	t *testing.T,
	storage *store.SQLite,
	actor auth.Account,
	imported eventinterchange.ImportResult,
) {
	t.Helper()
	commands, err := rundown.NewCommands(storage, time.Now, nil, nil)
	if err != nil {
		t.Fatalf("create imported Rundown Commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create imported Rundown Queries: %v", err)
	}
	changeIDs := make([]int, 0, len(imported.Changes))
	for _, change := range imported.Changes {
		changeIDs = append(changeIDs, change.ID)
	}
	preview, err := queries.PublishPreview(t.Context(), actor, rundown.PublishPreviewInput{
		EventID: imported.EventID, ChangeIDs: changeIDs,
	})
	if err != nil {
		t.Fatalf("preview imported Publish: %v", err)
	}
	if _, err = commands.Publish(t.Context(), actor, rundown.PublishInput{
		EventID: imported.EventID, CommandID: "publish-imported-event",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		t.Fatalf("publish imported Event: %v", err)
	}
}

func cloneRoles(source map[int]viewer.Role) map[int]viewer.Role {
	cloned := make(map[int]viewer.Role, len(source)+1)
	maps.Copy(cloned, source)
	return cloned
}

func fingerprintForTest(document []byte) string {
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}

func countAuditAction(entries []store.AuditEntry, action string) int {
	count := 0
	for _, entry := range entries {
		if entry.Action == action {
			count++
		}
	}
	return count
}

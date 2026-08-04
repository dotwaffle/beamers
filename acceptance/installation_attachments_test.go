package acceptance_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	activationv1 "github.com/dotwaffle/beamers/gen/beamers/activation/v1"
	"github.com/dotwaffle/beamers/gen/beamers/activation/v1/activationv1connect"
	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
)

func TestAnonymousUploadLinkRoutesAreRemoved(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	presentationID := prepareActiveSchedule(t, administrator, server)
	issued := requestJSON(
		t.Context(),
		administrator,
		server.address,
		"/crew/events/1/upload-links",
		map[string]any{
			"target_type": "Presentation",
			"target_id":   presentationID,
			"command_id":  "removed-upload-link-issuance",
		},
	)
	if issued.status != http.StatusNotFound {
		t.Fatalf("removed Upload Link issuance = %d %q", issued.status, issued.body)
	}
	for _, path := range []string{
		"/crew/events/1/upload-links/1/revoke",
		"/upload/retired-credential",
	} {
		result := requestJSON(
			t.Context(),
			administrator,
			server.address,
			path,
			map[string]string{"command_id": "removed-upload-link-route"},
		)
		if result.status != http.StatusNotFound {
			t.Fatalf("removed Upload Link route %q = %d %q", path, result.status, result.body)
		}
	}
	server.stop(t)
}

func TestFinalAttachmentsReleaseByPolicyAndSurviveRestart(t *testing.T) {
	fixture := prepareReleasedEntryAttachments(t)
	administrator, server := fixture.administrator, fixture.server
	competitionID, entryID := fixture.competitionID, fixture.entryID
	competitionClient := fixture.competitionClient
	publicVersion := fixture.publicVersion
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)

	override := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/competitions/%d/attachment-release", competitionID),
		map[string]any{
			"policy": "OnEventReleaseCue", "override": true, "expected_revision": 0,
			"command_id": "configure-cue-attachment-release",
		},
	)
	if override.status != http.StatusOK {
		t.Fatalf("configure Competition Attachment release = %d: %s", override.status, override.body)
	}
	assertReleasedAttachmentsOnListeners(t, server)
	staleOverride := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/competitions/%d/attachment-release", competitionID),
		map[string]any{
			"policy": "OnEnded", "override": true, "expected_revision": 0,
			"command_id": "stale-competition-attachment-release",
		},
	)
	if staleOverride.status != http.StatusConflict {
		t.Fatalf("stale Competition Attachment release = %d: %s", staleOverride.status, staleOverride.body)
	}
	staleOverrideRetry := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/competitions/%d/attachment-release", competitionID),
		map[string]any{
			"policy": "OnEnded", "override": true, "expected_revision": 0,
			"command_id": "stale-competition-attachment-release",
		},
	)
	if staleOverrideRetry.status != http.StatusConflict {
		t.Fatalf(
			"retried stale Competition Attachment release = %d: %s",
			staleOverrideRetry.status,
			staleOverrideRetry.body,
		)
	}
	rundownClient := rundownv1connect.NewRundownServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	rundown, err := rundownClient.GetCrewRundown(t.Context(), connect.NewRequest(
		&rundownv1.GetCrewRundownRequest{EventId: 1},
	))
	if err != nil {
		t.Fatalf("load ceremony Session for Attachment Release Cue: %v", err)
	}
	var cueSessionID, presentationID int64
	for _, candidate := range rundown.Msg.GetSessions() {
		if candidate.GetType() == rundownv1.SessionType_SESSION_TYPE_CEREMONY {
			cueSessionID = candidate.GetId()
		}
		if candidate.GetType() == rundownv1.SessionType_SESSION_TYPE_PRESENTATION {
			presentationID = candidate.GetId()
		}
	}
	if cueSessionID == 0 || presentationID == 0 {
		t.Fatal("published Ceremony or Presentation missing for bound Attachment Release Cue")
	}
	invalidBinding := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/attachment-release",
		map[string]any{
			"policy": "OnEventReleaseCue", "cue_session_id": presentationID,
			"expected_revision": 1, "command_id": "reject-nonceremony-release-cue",
		},
	)
	if invalidBinding.status != http.StatusNotFound {
		t.Fatalf("non-Ceremony Attachment Release Cue = %d: %s", invalidBinding.status, invalidBinding.body)
	}
	bound := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/attachment-release",
		map[string]any{
			"policy": "OnEventReleaseCue", "cue_session_id": cueSessionID,
			"expected_revision": 1, "command_id": "bind-attachment-release-cue",
		},
	)
	if bound.status != http.StatusOK {
		t.Fatalf("bind Attachment Release Cue = %d: %s", bound.status, bound.body)
	}
	if _, err = sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: cueSessionID, CommandId: "start-attachment-release-cue",
		ExpectedLiveStateRevision: proto.Int64(0),
	})); err != nil {
		t.Fatalf("start bound Attachment Release Cue Session: %v", err)
	}
	assertReleasedAttachmentsOnListeners(t, server, publicVersion.ID)
	cuePreview := requestJSONMethod(
		t.Context(), http.MethodGet, administrator, server.address,
		"/crew/events/1/attachment-release-cue", nil,
	)
	var releaseImpact struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err = json.Unmarshal([]byte(cuePreview.body), &releaseImpact); err != nil ||
		cuePreview.status != http.StatusOK || releaseImpact.Fingerprint == "" {
		t.Fatalf("preview Attachment Release Cue = %d: %s (%v)", cuePreview.status, cuePreview.body, err)
	}
	cue := requestJSON(
		t.Context(), administrator, server.address, "/crew/events/1/attachment-release-cue",
		map[string]any{
			"expected_revision": 3, "preview_fingerprint": releaseImpact.Fingerprint,
			"confirmed": true, "command_id": "fire-attachment-release-cue",
		},
	)
	if cue.status != http.StatusOK {
		t.Fatalf("fire Attachment Release Cue = %d: %s", cue.status, cue.body)
	}
	assertReleasedAttachmentsOnListeners(t, server, publicVersion.ID)

	current, err := competitionClient.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(current.Msg.GetEntries()) != 1 {
		t.Fatalf("load Entry for Release Hold = %+v, %v", current, err)
	}
	entryHeld, err := competitionClient.SetEntryReleaseHold(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryReleaseHoldRequest{
			EventId: 1, SessionId: competitionID, EntryId: entryID,
			CommandId:        "hold-entry-attachments",
			ExpectedRevision: current.Msg.GetEntries()[0].GetRevision(),
			Hold:             true,
			CrewReason:       "review public package",
		},
	))
	if err != nil || !entryHeld.Msg.GetEntry().GetReleaseHold() {
		t.Fatalf("apply Entry Release Hold = %+v, %v", entryHeld, err)
	}
	assertReleasedAttachmentsOnListeners(t, server)
	assertPublicAttachmentOnListeners(
		t, server, publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	entryLifted, err := competitionClient.SetEntryReleaseHold(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryReleaseHoldRequest{
			EventId: 1, SessionId: competitionID, EntryId: entryID,
			CommandId:        "lift-entry-attachment-hold",
			ExpectedRevision: entryHeld.Msg.GetEntry().GetRevision(),
			CrewReason:       "public package reviewed",
		},
	))
	if err != nil || entryLifted.Msg.GetEntry().GetReleaseHold() {
		t.Fatalf("lift Entry Release Hold = %+v, %v", entryLifted, err)
	}
	assertReleasedAttachmentsOnListeners(t, server, publicVersion.ID)

	held := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/attachment-versions/%d/release", publicVersion.ID),
		map[string]any{
			"hold": true, "expected_revision": 0,
			"command_id": "hold-public-attachment",
		},
	)
	if held.status != http.StatusOK {
		t.Fatalf("hold public Attachment = %d: %s", held.status, held.body)
	}
	staleHold := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/attachment-versions/%d/release", publicVersion.ID),
		map[string]any{
			"hold": false, "expected_revision": 0,
			"command_id": "reject-stale-public-attachment-hold",
		},
	)
	if staleHold.status != http.StatusConflict {
		t.Fatalf("stale public Attachment hold = %d: %s", staleHold.status, staleHold.body)
	}
	assertReleasedAttachmentsOnListeners(t, server)
	assertPublicAttachmentOnListeners(
		t, server, publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	lifted := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/attachment-versions/%d/release", publicVersion.ID),
		map[string]any{
			"hold": false, "expected_revision": 1,
			"command_id": "lift-public-attachment-hold",
		},
	)
	if lifted.status != http.StatusOK {
		t.Fatalf("lift public Attachment hold = %d: %s", lifted.status, lifted.body)
	}
	staleHoldRetry := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		fmt.Sprintf("/crew/events/1/attachment-versions/%d/release", publicVersion.ID),
		map[string]any{
			"hold": false, "expected_revision": 0,
			"command_id": "reject-stale-public-attachment-hold",
		},
	)
	if staleHoldRetry.status != http.StatusConflict {
		t.Fatalf(
			"retried stale public Attachment hold = %d: %s",
			staleHoldRetry.status,
			staleHoldRetry.body,
		)
	}
	audit := get(t, administrator, server.address, "/admin/audit")
	auditBody, readErr := io.ReadAll(audit.Body)
	closeErr := audit.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Attachment release Audit history: %v", err)
	}
	if bytes.Count(auditBody, []byte(`"action":"ConfigureCompetitionAttachmentRelease"`)) != 2 ||
		bytes.Count(auditBody, []byte(`"reason":"stale_revision"`)) != 2 ||
		!bytes.Contains(auditBody, []byte(`"reason":"attachment_target_not_found"`)) {
		t.Fatalf("Attachment release rejection Audit evidence = %s", auditBody)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	restarted := startBeamersWithPublicListener(t, bin, dataDir)
	assertReleasedAttachmentsOnListeners(t, restarted, publicVersion.ID)
	assertPublicAttachmentOnListeners(
		t, restarted, publicVersion.ID, http.StatusOK, "public release",
	)
	prepareAndActivateSecondEvent(t, administrator, restarted)
	assertReleasedAttachmentsOnListeners(t, restarted)
	assertPublicAttachmentOnListeners(
		t, restarted, publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	restarted.stop(t)
}

func TestFinalFilesExportDownloadAndDestination(t *testing.T) {
	fixture := prepareReleasedEntryAttachments(t)
	administrator, server := fixture.administrator, fixture.server

	unauthenticatedPreview := requestJSON(
		t.Context(), http.DefaultClient, server.address,
		"/admin/final-files/preview",
		map[string]any{"event_id": 1},
	)
	if unauthenticatedPreview.status != http.StatusUnauthorized {
		t.Fatalf(
			"unauthenticated Final Files Export preview = %d: %s",
			unauthenticatedPreview.status, unauthenticatedPreview.body,
		)
	}
	webPreview := requestJSON(
		t.Context(), administrator, server.address,
		"/admin/final-files/preview",
		map[string]any{"event_id": 1},
	)
	var downloadable struct {
		PreviewDigest string `json:"preview_digest"`
		Files         []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if decodeErr := json.Unmarshal([]byte(webPreview.body), &downloadable); decodeErr != nil ||
		webPreview.status != http.StatusOK ||
		downloadable.PreviewDigest == "" ||
		len(downloadable.Files) != 1 {
		t.Fatalf(
			"Administrator Final Files Export preview = %d: %s (%v)",
			webPreview.status, webPreview.body, decodeErr,
		)
	}
	download := requestJSON(
		t.Context(), administrator, server.address,
		"/admin/final-files",
		map[string]any{
			"event_id":       1,
			"preview_digest": downloadable.PreviewDigest,
		},
	)
	if download.status != http.StatusOK ||
		download.header.Get("Content-Type") != "application/zip" {
		t.Fatalf(
			"Administrator Final Files Export download = %d, %q: %s",
			download.status, download.header.Get("Content-Type"), download.body,
		)
	}
	archive, err := zip.NewReader(
		bytes.NewReader([]byte(download.body)),
		int64(len(download.body)),
	)
	if err != nil {
		t.Fatalf("open Final Files Export download: %v", err)
	}
	if len(archive.File) != 2 ||
		archive.File[0].Name != downloadable.Files[0].Path ||
		archive.File[1].Name != "manifest.json" {
		t.Fatalf("Final Files Export download entries = %+v", archive.File)
	}

	dataDir, bin := server.dataDir, server.bin
	server.stop(t)

	outputDir := filepath.Join(t.TempDir(), "final-files")
	var preview struct {
		PreviewDigest string   `json:"preview_digest"`
		Collisions    []string `json:"collisions"`
		Files         []struct {
			Path             string `json:"path"`
			SHA256           string `json:"sha256"`
			OriginalFilename string `json:"original_filename"`
		} `json:"files"`
	}
	if err = json.Unmarshal([]byte(runBeamersOutput(
		t, bin,
		"export-final-files", "preview",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", outputDir,
	)), &preview); err != nil {
		t.Fatalf("decode Final Files Export preview: %v", err)
	}
	if preview.PreviewDigest == "" || len(preview.Collisions) != 0 || len(preview.Files) != 1 {
		t.Fatalf("Final Files Export preview = %+v", preview)
	}
	exported := preview.Files[0]
	if !strings.HasPrefix(exported.Path, "untracked/competitions/") ||
		exported.OriginalFilename != "public.txt" ||
		exported.SHA256 == "" {
		t.Fatalf("Final Files Export file = %+v", exported)
	}
	otherOutput := filepath.Join(t.TempDir(), "other-final-files")
	var otherPreview struct {
		PreviewDigest string `json:"preview_digest"`
	}
	if err = json.Unmarshal([]byte(runBeamersOutput(
		t, bin,
		"export-final-files", "preview",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", otherOutput,
	)), &otherPreview); err != nil {
		t.Fatalf("decode alternate Final Files Export preview: %v", err)
	}
	if otherPreview.PreviewDigest == preview.PreviewDigest {
		t.Fatal("Final Files Export preview digest did not bind the destination")
	}
	runBeamersFails(
		t, bin,
		"export-final-files", "apply",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", otherOutput,
		"--preview-digest", preview.PreviewDigest,
		"--approve-export",
	)
	if _, statErr := os.Lstat(otherOutput); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("mismatched Final Files Export created output: %v", statErr)
	}
	runBeamersFails(
		t, bin,
		"export-final-files", "apply",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", outputDir,
		"--preview-digest", "stale-preview",
		"--approve-export",
	)
	if _, statErr := os.Lstat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale Final Files Export created output: %v", statErr)
	}
	runBeamers(
		t, bin,
		"export-final-files", "apply",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", outputDir,
		"--preview-digest", preview.PreviewDigest,
		"--approve-export",
	)
	content, err := os.ReadFile(filepath.Join(outputDir, filepath.FromSlash(exported.Path)))
	if err != nil || string(content) != "public release" {
		t.Fatalf("read Final Files Export content = %q, %v", content, err)
	}
	firstManifest, err := os.ReadFile(filepath.Join(outputDir, "manifest.json"))
	if err != nil || !bytes.Contains(firstManifest, []byte(`"original_filename":"public.txt"`)) ||
		bytes.Contains(firstManifest, []byte(`"original_filename":"crew.txt"`)) {
		t.Fatalf("Final Files Export manifest = %s, %v", firstManifest, err)
	}
	var repeated struct {
		PreviewDigest string   `json:"preview_digest"`
		Collisions    []string `json:"collisions"`
	}
	if err = json.Unmarshal([]byte(runBeamersOutput(
		t, bin,
		"export-final-files", "preview",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", outputDir,
	)), &repeated); err != nil {
		t.Fatalf("decode repeated Final Files Export preview: %v", err)
	}
	if repeated.PreviewDigest == preview.PreviewDigest || len(repeated.Collisions) == 0 {
		t.Fatalf("repeated Final Files Export preview = %+v", repeated)
	}
	runBeamersFails(
		t, bin,
		"export-final-files", "apply",
		"--data-dir", dataDir,
		"--event-id", "1",
		"--output", outputDir,
		"--preview-digest", preview.PreviewDigest,
		"--approve-export",
	)
}

type releasedEntryAttachments struct {
	administrator     *http.Client
	server            *runningServer
	competitionClient competitionv1connect.CompetitionServiceClient
	competitionID     int64
	entryID           int64
	publicVersion     attachmentVersionResponse
	crewVersion       attachmentVersionResponse
}

func prepareReleasedEntryAttachments(t *testing.T) releasedEntryAttachments {
	t.Helper()
	administrator, server := startAuthenticatedAdministratorWithPublicListener(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := competitionv1connect.NewCompetitionServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	created, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-release-entry",
			Name: "Release Project",
		},
	))
	if err != nil {
		t.Fatalf("create Entry for Attachment release: %v", err)
	}
	entryID := created.Msg.GetEntry().GetId()
	publicVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(entryID, 10),
			"name":        "public",
			"command_id":  "upload-public-release",
		},
		"public.txt", "text/plain", []byte("public release"),
	))
	crewVersion := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(entryID, 10),
			"name":        "crew",
			"command_id":  "upload-crew-release",
			"crew_only":   "true",
		},
		"crew.txt", "text/plain", []byte("crew only"),
	))
	if publicVersion.ReleaseEligibility != "Public" ||
		crewVersion.ReleaseEligibility != "CrewOnly" {
		t.Fatalf(
			"upload release eligibility = %q then %q",
			publicVersion.ReleaseEligibility, crewVersion.ReleaseEligibility,
		)
	}
	assertPublicAttachmentOnListeners(
		t, server, publicVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	for _, version := range []attachmentVersionResponse{publicVersion, crewVersion} {
		_, err = competitionClient.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
			&competitionv1.SetEntryAttachmentReadinessRequest{
				EventId: 1, SessionId: competitionID, EntryId: entryID,
				AttachmentVersionId: int64(version.ID),
				CommandId:           fmt.Sprintf("finalize-release-%d", version.ID),
				ExpectedRevision:    int64(version.ReadinessRevision),
				Final:               true,
				Primary:             version.ID == publicVersion.ID,
			},
		))
		if err != nil {
			t.Fatalf("finalize Attachment Version %d: %v", version.ID, err)
		}
	}
	configured := requestJSONMethod(
		t.Context(), http.MethodPatch, administrator, server.address,
		"/crew/events/1/attachment-release",
		map[string]any{
			"policy": "OnLive", "expected_revision": 0,
			"command_id": "configure-live-attachment-release",
		},
	)
	if configured.status != http.StatusOK {
		t.Fatalf("configure Attachment Release Policy = %d: %s", configured.status, configured.body)
	}
	assertReleasedAttachmentsOnListeners(t, server)
	sessionClient := sessionv1connect.NewSessionControlServiceClient(
		administrator, "http://"+server.address, connect.WithProtoJSON(),
	)
	if _, err = sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: competitionID, CommandId: "start-release-competition",
		ExpectedLiveStateRevision: proto.Int64(0),
	})); err != nil {
		t.Fatalf("start Competition for Attachment release: %v", err)
	}
	assertReleasedAttachmentsOnListeners(t, server, publicVersion.ID)
	assertPublicAttachmentOnListeners(
		t, server, publicVersion.ID, http.StatusOK, "public release",
	)
	assertPublicAttachmentOnListeners(
		t, server, crewVersion.ID,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	assertPublicAttachmentOnListeners(
		t, server, 999999,
		http.StatusNotFound, "Attachment Version not found\n",
	)
	return releasedEntryAttachments{
		administrator: administrator, server: server,
		competitionClient: competitionClient,
		competitionID:     competitionID, entryID: entryID,
		publicVersion: publicVersion, crewVersion: crewVersion,
	}
}

func setPresentationUploadDeadline(
	t *testing.T,
	client rundownv1connect.RundownServiceClient,
	draftRevision, presentationID int64,
	deadline time.Time,
) {
	t.Helper()
	edited, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "set-presentation-upload-deadline",
		ExpectedDraftRevision: draftRevision,
		Sessions: []*rundownv1.SessionDraft{{
			Id: presentationID, UploadDeadline: timestamppb.New(deadline),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"upload_deadline"}},
		}},
	}))
	if err != nil {
		t.Fatalf("set Presentation Upload Deadline: %v", err)
	}
	publishEditedDraft(t, client, edited.Msg, "publish-presentation-upload-deadline")
}

type attachmentVersionResponse struct {
	ID                 int    `json:"id"`
	AttachmentID       int    `json:"attachment_id"`
	Version            int    `json:"version"`
	SHA256             string `json:"sha256"`
	UploaderType       string `json:"uploader_type"`
	UploaderID         int    `json:"uploader_id"`
	Primary            bool   `json:"primary"`
	Final              bool   `json:"final"`
	ReadinessRevision  int    `json:"readiness_revision"`
	ReleaseEligibility string `json:"release_eligibility"`
	ReleaseRevision    int    `json:"release_revision"`
}

func decodeAttachmentVersion(t *testing.T, response jsonResponse) attachmentVersionResponse {
	t.Helper()
	var version attachmentVersionResponse
	if err := json.Unmarshal([]byte(response.body), &version); err != nil ||
		response.status != http.StatusCreated || version.ID <= 0 {
		t.Fatalf("decode Attachment Version = %d %+v: %s (%v)", response.status, version, response.body, err)
	}
	return version
}

func assertReleasedAttachmentIDs(t *testing.T, address string, want ...int) {
	t.Helper()
	response := get(t, http.DefaultClient, address, "/public/attachments")
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read released Attachment list: %v", err)
	}
	var versions []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &versions); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("released Attachments = %d %q (%v)", response.StatusCode, body, err)
	}
	got := make([]int, 0, len(versions))
	for _, version := range versions {
		got = append(got, version.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("released Attachment IDs = %v, want %v", got, want)
	}
}

func assertPublicAttachmentBytes(
	t *testing.T,
	address string,
	versionID, wantStatus int,
	wantBody string,
) {
	t.Helper()
	response := get(
		t, http.DefaultClient, address,
		fmt.Sprintf("/public/attachments/%d", versionID),
	)
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read public Attachment Version %d: %v", versionID, err)
	}
	if response.StatusCode != wantStatus || string(body) != wantBody {
		t.Fatalf(
			"public Attachment Version %d = %d %q, want %d %q",
			versionID, response.StatusCode, body, wantStatus, wantBody,
		)
	}
}

func assertReleasedAttachmentsOnListeners(
	t *testing.T,
	server *runningServer,
	want ...int,
) {
	t.Helper()
	assertReleasedAttachmentIDs(t, server.address, want...)
	if server.publicAddress != "" {
		assertReleasedAttachmentIDs(t, server.publicAddress, want...)
	}
}

func assertPublicAttachmentOnListeners(
	t *testing.T,
	server *runningServer,
	versionID, wantStatus int,
	wantBody string,
) {
	t.Helper()
	assertPublicAttachmentBytes(t, server.address, versionID, wantStatus, wantBody)
	if server.publicAddress != "" {
		assertPublicAttachmentBytes(
			t, server.publicAddress, versionID, wantStatus, wantBody,
		)
	}
}

func addCompetitionSession(
	t *testing.T,
	client *http.Client,
	server *runningServer,
) (int64, time.Time) {
	t.Helper()
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before adding Competition: %v", err)
	}
	deadline := time.Date(2099, 8, 21, 11, 30, 0, 0, time.UTC)
	plannedStart := time.Date(2099, 8, 21, 12, 0, 0, 0, time.UTC)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "add-competition-session",
		ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "competition", Title: "Demo Competition",
			Type:               rundownv1.SessionType_SESSION_TYPE_COMPETITION,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PublicDetails:      "Projects presented by attendees",
			PlannedStart:       timestamppb.New(plannedStart),
			PlannedEnd:         timestamppb.New(plannedStart.Add(time.Hour)),
			TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
			MinimumDuration:    durationpb.New(30 * time.Minute),
			StartBoundary:      rundownv1.Boundary_BOUNDARY_HARD,
			EndBoundary:        rundownv1.Boundary_BOUNDARY_HARD,
			Lanes: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Id{Id: current.Msg.GetLanes()[0].GetId()},
			}},
			Locations: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Id{Id: current.Msg.GetLocations()[0].GetId()},
			}},
			SubmissionDeadline:      timestamppb.New(deadline),
			EntryDefaultDisposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED,
		}},
	}))
	if err != nil {
		t.Fatalf("add Competition Session: %v", err)
	}
	var competitionID int64
	for _, change := range edited.Msg.GetChanges() {
		if change.GetKind() == "CreateSession" {
			competitionID = change.GetTargetId()
		}
	}
	publishEditedDraft(t, rundownClient, edited.Msg, "publish-competition-session")
	return competitionID, deadline
}

func setCompetitionSubmissionDeadline(
	t *testing.T,
	client *http.Client,
	server *runningServer,
	competitionID int64,
	deadline time.Time,
) {
	t.Helper()
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before closing Competition uploads: %v", err)
	}
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "close-competition-uploads",
		ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: competitionID, SubmissionDeadline: timestamppb.New(deadline),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"submission_deadline"}},
		}},
	}))
	if err != nil {
		t.Fatalf("set Competition Submission Deadline: %v", err)
	}
	publishEditedDraft(t, rundownClient, edited.Msg, "publish-closed-competition-uploads")
}

func prepareCommunicatedTimeSchedule(
	t *testing.T,
	client *http.Client,
	server *runningServer,
	plannedStart time.Time,
) int64 {
	t.Helper()
	assertJSONRequest(
		t, client, server.address, "/admin/events",
		map[string]string{
			"name": "Communicated Time", "planned_start_date": plannedStart.Format(time.DateOnly),
			"planned_end_date": plannedStart.AddDate(0, 0, 1).Format(time.DateOnly), "timezone": "UTC",
			"event_locale": "en-GB", "content_language": "en-GB",
			"event_day_boundary": "00:00", "command_id": "create-communicated-time-event",
		},
		http.StatusCreated,
		fmt.Sprintf(
			"{\"id\":1,\"name\":\"Communicated Time\",\"planned_start_date\":%q,\"planned_end_date\":%q,\"timezone\":\"UTC\",\"event_locale\":\"en-GB\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"00:00\",\"revision\":1}\n",
			plannedStart.Format(time.DateOnly), plannedStart.AddDate(0, 0, 1).Format(time.DateOnly),
		),
	)
	assertJSONRequest(
		t, client, server.address, "/admin/events/1/grants",
		map[string]any{"account_id": 1, "role": "Producer", "command_id": "grant-communicated-time-producer"},
		http.StatusCreated, "{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	publishEventListing(
		t,
		client,
		server,
		"Communicated Time",
		"communicated-time",
		plannedStart.Format(time.DateOnly),
		plannedStart.AddDate(0, 0, 1).Format(time.DateOnly),
		"UTC",
		"00:00",
	)
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "edit-communicated-time", ExpectedDraftRevision: 0,
		Locations: []*rundownv1.LocationDraft{{Ref: "room", Name: "Room"}},
		Lanes: []*rundownv1.LaneDraft{{
			Ref: "lane", Name: "Lane",
			Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "room"}},
		}},
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "session", Title: "Communicated Session",
			Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PlannedStart:       timestamppb.New(plannedStart),
			PlannedEnd:         timestamppb.New(plannedStart.Add(30 * time.Minute)),
			TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
			MinimumDuration:    durationpb.New(15 * time.Minute),
			StartBoundary:      rundownv1.Boundary_BOUNDARY_HARD,
			EndBoundary:        rundownv1.Boundary_BOUNDARY_HARD,
			Locations: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Ref{Ref: "room"},
			}},
			Lanes: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Ref{Ref: "lane"},
			}},
		}},
	}))
	if err != nil {
		t.Fatalf("edit communicated-time Rundown: %v", err)
	}
	var sessionID int64
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
		if change.GetKind() == "CreateSession" {
			sessionID = change.GetTargetId()
		}
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(
		&rundownv1.PublishPreviewRequest{EventId: 1, ChangeIds: changeIDs},
	))
	if err != nil {
		t.Fatalf("preview communicated-time Publish: %v", err)
	}
	if _, publishErr := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-communicated-time",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: preview.Msg.GetDraftRevision(), PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds: preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); publishErr != nil {
		t.Fatalf("publish communicated-time Rundown: %v", publishErr)
	}
	activationClient := activationv1connect.NewActivationServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	preflight, err := activationClient.Preflight(
		t.Context(), connect.NewRequest(&activationv1.PreflightRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("preflight communicated-time Event: %v", err)
	}
	if _, err := activationClient.Activate(t.Context(), connect.NewRequest(&activationv1.ActivateRequest{
		EventId: 1, CommandId: "activate-communicated-time", Confirmation: preflight.Msg.GetConfirmation(),
	})); err != nil {
		t.Fatalf("activate communicated-time Event: %v", err)
	}
	return sessionID
}

func prepareActiveSchedule(t *testing.T, client *http.Client, server *runningServer) int64 {
	t.Helper()
	assertJSONRequest(
		t, client, server.address, "/admin/events",
		map[string]string{
			"name": "BeamConf 2099", "planned_start_date": "2099-08-21",
			"planned_end_date": "2099-08-23", "timezone": "Europe/Berlin",
			"event_locale": "en-GB", "content_language": "en-GB",
			"event_day_boundary": "06:00", "command_id": "create-schedule-event",
		},
		http.StatusCreated,
		"{\"id\":1,\"name\":\"BeamConf 2099\",\"planned_start_date\":\"2099-08-21\",\"planned_end_date\":\"2099-08-23\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"en-GB\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, client, server.address, "/admin/events/1/grants",
		map[string]any{"account_id": 1, "role": "Producer", "command_id": "grant-schedule-producer"},
		http.StatusCreated, "{\"event_id\":1,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	publishEventListing(
		t,
		client,
		server,
		"BeamConf 2099",
		"beamconf-2099",
		"2099-08-21",
		"2099-08-23",
		"Europe/Berlin",
		"06:00",
	)

	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	plannedStart := time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "edit-schedule", ExpectedDraftRevision: 0,
		Locations: []*rundownv1.LocationDraft{{Ref: "main", Name: "Main Hall"}},
		Lanes: []*rundownv1.LaneDraft{{
			Ref: "main-lane", Name: "Main Lane",
			Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "main"}},
		}},
		Tracks: []*rundownv1.TrackDraft{{Ref: "general", Name: "General"}},
		Sessions: []*rundownv1.SessionDraft{
			{
				Ref: "keynote", Title: "Opening Keynote",
				Speaker:            "Original Speaker",
				Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
				AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
				PublicDetails:      "Welcome to BeamConf 2099",
				CrewNotes:          "Call Pat on +44 20 7946 0958; /srv/beamers/private/keynote.pdf",
				PlannedStart:       timestamppb.New(plannedStart), PlannedEnd: timestamppb.New(plannedStart.Add(time.Hour)),
				TimingPolicy:    rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
				MinimumDuration: durationpb.New(30 * time.Minute),
				StartBoundary:   rundownv1.Boundary_BOUNDARY_HARD,
				EndBoundary:     rundownv1.Boundary_BOUNDARY_HARD,
				Locations:       []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main"}}},
				Lanes:           []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main-lane"}}},
				Tracks:          []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "general"}}},
			},
			{
				Ref: "soundcheck", Title: "Private Soundcheck",
				Type:               rundownv1.SessionType_SESSION_TYPE_ACTIVITY,
				AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_CREW_ONLY,
				PublicDetails:      "must remain undiscoverable", CrewNotes: "radio channel 4",
				PlannedStart: timestamppb.New(plannedStart.Add(-time.Hour)), PlannedEnd: timestamppb.New(plannedStart),
				TimingPolicy:    rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
				MinimumDuration: durationpb.New(30 * time.Minute),
				StartBoundary:   rundownv1.Boundary_BOUNDARY_HARD,
				EndBoundary:     rundownv1.Boundary_BOUNDARY_SOFT,
				Locations:       []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main"}}},
				Lanes:           []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main-lane"}}},
			},
			{
				Ref: "old-announcement", Title: "Old Announcement",
				Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
				AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
				PublicDetails:      "This historical Session is no longer upcoming",
				PlannedStart:       timestamppb.New(time.Date(2000, 1, 1, 8, 0, 0, 0, time.UTC)),
				PlannedEnd:         timestamppb.New(time.Date(2000, 1, 1, 9, 0, 0, 0, time.UTC)),
				TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
				MinimumDuration:    durationpb.New(30 * time.Minute),
				StartBoundary:      rundownv1.Boundary_BOUNDARY_HARD,
				EndBoundary:        rundownv1.Boundary_BOUNDARY_SOFT,
				Locations:          []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main"}}},
				Lanes:              []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main-lane"}}},
			},
			{
				Ref: "closing", Title: "Closing Session",
				Type:               rundownv1.SessionType_SESSION_TYPE_CEREMONY,
				AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
				PublicDetails:      "The unchanged later Session",
				PlannedStart:       timestamppb.New(plannedStart.Add(2 * time.Hour)),
				PlannedEnd:         timestamppb.New(plannedStart.Add(3 * time.Hour)),
				TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
				MinimumDuration:    durationpb.New(30 * time.Minute),
				StartBoundary:      rundownv1.Boundary_BOUNDARY_HARD,
				EndBoundary:        rundownv1.Boundary_BOUNDARY_SOFT,
				Locations:          []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main"}}},
				Lanes:              []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "main-lane"}}},
			},
		},
	}))
	if err != nil {
		t.Fatalf("Edit public Schedule Draft: %v", err)
	}
	var publicSessionID int64
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
		if change.GetKind() == "CreateSession" && publicSessionID == 0 {
			publicSessionID = change.GetTargetId()
		}
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: changeIDs,
	}))
	if err != nil {
		t.Fatalf("Preview public Schedule Publish: %v", err)
	}
	if _, publishErr := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-schedule",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: preview.Msg.GetDraftRevision(), PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds: preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); publishErr != nil {
		t.Fatalf("Publish public Schedule: %v", publishErr)
	}

	activationClient := activationv1connect.NewActivationServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	preflight, err := activationClient.Preflight(t.Context(), connect.NewRequest(&activationv1.PreflightRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Preflight public Schedule Event: %v", err)
	}
	if _, err := activationClient.Activate(t.Context(), connect.NewRequest(&activationv1.ActivateRequest{
		EventId: 1, CommandId: "activate-schedule", Confirmation: preflight.Msg.GetConfirmation(),
	})); err != nil {
		t.Fatalf("Activate public Schedule Event: %v", err)
	}
	return publicSessionID
}

func addSoftRippleSession(t *testing.T, client *http.Client, server *runningServer) int64 {
	t.Helper()
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before adding ripple Session: %v", err)
	}
	plannedStart := time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "add-soft-ripple-session",
		ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "soft-ripple", Title: "Soft Ripple Session",
			Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PlannedStart:       timestamppb.New(plannedStart),
			PlannedEnd:         timestamppb.New(plannedStart.Add(time.Hour)),
			TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_DURATION,
			MinimumDuration:    durationpb.New(55 * time.Minute),
			StartBoundary:      rundownv1.Boundary_BOUNDARY_SOFT,
			EndBoundary:        rundownv1.Boundary_BOUNDARY_SOFT,
			Locations: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Id{Id: 1},
			}},
			Lanes: []*rundownv1.TargetRef{{
				Target: &rundownv1.TargetRef_Id{Id: 1},
			}},
		}},
	}))
	if err != nil {
		t.Fatalf("add soft ripple Session: %v", err)
	}
	var sessionID int64
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
		if change.GetKind() == "CreateSession" {
			sessionID = change.GetTargetId()
		}
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(
		&rundownv1.PublishPreviewRequest{EventId: 1, ChangeIds: changeIDs},
	))
	if err != nil {
		t.Fatalf("preview soft ripple Session Publish: %v", err)
	}
	if _, err := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-soft-ripple-session",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision:     preview.Msg.GetDraftRevision(),
			PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds:         preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); err != nil {
		t.Fatalf("publish soft ripple Session: %v", err)
	}
	if sessionID <= 0 {
		t.Fatal("soft ripple Session ID is missing")
	}
	return sessionID
}

func publishEventListing(
	t *testing.T,
	client *http.Client,
	server *runningServer,
	name, slug, startDate, endDate, timezone, dayBoundary string,
) {
	t.Helper()
	result := requestJSONMethod(
		t.Context(),
		http.MethodPut,
		client,
		server.address,
		"/crew/events/1",
		map[string]any{
			"name": name, "public": true, "public_slug": slug,
			"planned_start_date": startDate, "planned_end_date": endDate,
			"timezone": timezone, "event_locale": "en-GB",
			"content_language": "en-GB", "event_day_boundary": dayBoundary,
			"expected_revision": 1, "command_id": "publish-test-event-listing",
		},
	)
	if result.err != nil || result.status != http.StatusOK {
		t.Fatalf("publish test Event listing = %d %q, %v", result.status, result.body, result.err)
	}
}

func addPlacementLane(
	t *testing.T,
	client *http.Client,
	server *runningServer,
) (int64, int64) {
	t.Helper()
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	current, err := rundownClient.GetCrewRundown(
		t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}),
	)
	if err != nil {
		t.Fatalf("load Rundown before adding placement Lane: %v", err)
	}
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(
		&rundownv1.EditDraftRequest{
			EventId: 1, CommandId: "add-placement-lane",
			ExpectedDraftRevision: current.Msg.GetDraftRevision(),
			Locations: []*rundownv1.LocationDraft{{
				Ref: "side-hall", Name: "Side Hall",
			}},
			Lanes: []*rundownv1.LaneDraft{{
				Ref: "side-lane", Name: "Side Lane",
				Location: &rundownv1.TargetRef{
					Target: &rundownv1.TargetRef_Ref{Ref: "side-hall"},
				},
			}},
		},
	))
	if err != nil {
		t.Fatalf("add placement Lane: %v", err)
	}
	var locationID, laneID int64
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
		switch change.GetKind() {
		case "CreateLocation":
			locationID = change.GetTargetId()
		case "CreateLane":
			laneID = change.GetTargetId()
		}
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(
		&rundownv1.PublishPreviewRequest{EventId: 1, ChangeIds: changeIDs},
	))
	if err != nil {
		t.Fatalf("preview placement Lane Publish: %v", err)
	}
	if _, err := rundownClient.Publish(t.Context(), connect.NewRequest(
		&rundownv1.PublishRequest{
			EventId: 1, CommandId: "publish-placement-lane",
			Confirmation: &rundownv1.PublishConfirmation{
				DraftRevision:     preview.Msg.GetDraftRevision(),
				PublishedRevision: preview.Msg.GetPublishedRevision(),
				ChangeIds:         preview.Msg.GetChangeIds(),
				Fingerprint:       preview.Msg.GetFingerprint(),
			},
		},
	)); err != nil {
		t.Fatalf("publish placement Lane: %v", err)
	}
	if locationID <= 0 || laneID <= 0 {
		t.Fatalf("placement identity IDs = Location %d, Lane %d", locationID, laneID)
	}
	return locationID, laneID
}

func prepareAndActivateSecondEvent(t *testing.T, client *http.Client, server *runningServer) {
	t.Helper()
	assertJSONRequest(
		t, client, server.address, "/admin/events",
		map[string]string{
			"name": "Revision 2100", "planned_start_date": "2100-09-01",
			"planned_end_date": "2100-09-02", "timezone": "Europe/Berlin",
			"event_locale": "en-GB", "content_language": "en-GB",
			"event_day_boundary": "06:00", "command_id": "create-second-display-event",
		},
		http.StatusCreated,
		"{\"id\":2,\"name\":\"Revision 2100\",\"planned_start_date\":\"2100-09-01\",\"planned_end_date\":\"2100-09-02\",\"timezone\":\"Europe/Berlin\",\"event_locale\":\"en-GB\",\"content_language\":\"en-GB\",\"event_day_boundary\":\"06:00\",\"revision\":1}\n",
	)
	assertJSONRequest(
		t, client, server.address, "/admin/events/2/grants",
		map[string]any{"account_id": 1, "role": "Producer", "command_id": "grant-second-display-event"},
		http.StatusCreated, "{\"event_id\":2,\"account_id\":1,\"role\":\"Producer\"}\n",
	)
	rundownClient := rundownv1connect.NewRundownServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	start := time.Date(2100, 9, 1, 8, 0, 0, 0, time.UTC)
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 2, CommandId: "edit-second-display-event", ExpectedDraftRevision: 0,
		Locations: []*rundownv1.LocationDraft{{Ref: "annex", Name: "Annex"}},
		Lanes: []*rundownv1.LaneDraft{{
			Ref: "annex-lane", Name: "Annex Lane",
			Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "annex"}},
		}},
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "annex-opening", Title: "Annex Opening",
			Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PlannedStart:       timestamppb.New(start), PlannedEnd: timestamppb.New(start.Add(time.Hour)),
			TimingPolicy:    rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
			MinimumDuration: durationpb.New(30 * time.Minute),
			StartBoundary:   rundownv1.Boundary_BOUNDARY_SOFT, EndBoundary: rundownv1.Boundary_BOUNDARY_SOFT,
			Locations: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "annex"}}},
			Lanes:     []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Ref{Ref: "annex-lane"}}},
		}},
	}))
	if err != nil {
		t.Fatalf("Edit second Event Draft: %v", err)
	}
	changeIDs := make([]int64, 0, len(edited.Msg.GetChanges()))
	for _, change := range edited.Msg.GetChanges() {
		changeIDs = append(changeIDs, change.GetId())
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 2, ChangeIds: changeIDs,
	}))
	if err != nil {
		t.Fatalf("Preview second Event Publish: %v", err)
	}
	if _, publishErr := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 2, CommandId: "publish-second-display-event",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: preview.Msg.GetDraftRevision(), PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds: preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); publishErr != nil {
		t.Fatalf("Publish second Event: %v", publishErr)
	}
	activationClient := activationv1connect.NewActivationServiceClient(
		client, "http://"+server.address, connect.WithProtoJSON(),
	)
	preflight, err := activationClient.Preflight(t.Context(), connect.NewRequest(&activationv1.PreflightRequest{EventId: 2}))
	if err != nil {
		t.Fatalf("Preflight second Event: %v", err)
	}
	if _, err := activationClient.Activate(t.Context(), connect.NewRequest(&activationv1.ActivateRequest{
		EventId: 2, CommandId: "activate-second-display-event", Confirmation: preflight.Msg.GetConfirmation(),
	})); err != nil {
		t.Fatalf("Activate second Event: %v", err)
	}
}

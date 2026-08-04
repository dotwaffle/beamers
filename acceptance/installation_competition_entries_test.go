package acceptance_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	competitionv1 "github.com/dotwaffle/beamers/gen/beamers/competition/v1"
	"github.com/dotwaffle/beamers/gen/beamers/competition/v1/competitionv1connect"
	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/gen/beamers/results/v1/resultsv1connect"
	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
)

func TestProducerCreatesIncludedCompetitionEntry(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, deadline := addCompetitionSession(t, administrator, server)
	client := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address)

	configured, err := client.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("Get Competition: %v", err)
	}
	if !configured.Msg.GetSubmissionDeadline().AsTime().Equal(deadline) ||
		configured.Msg.GetEffectiveDefaultDisposition() !=
			rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED {
		t.Fatalf("Competition configuration = %+v", configured.Msg)
	}
	schedulePage := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule")
	schedulePageBody, readErr := io.ReadAll(schedulePage.Body)
	closeErr := schedulePage.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read Schedule before Entry invalidation: %v", joinedErr)
	}
	entryStream, entryStreamReader := openPublicScheduleEvents(
		t,
		authenticatedClient(t),
		server.address,
		publicScheduleEventsPath(t, schedulePageBody),
	)
	created, err := client.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-included-entry",
			Name: "Project Aurora", PublicDetails: "An attendee-visible demo",
			CrewNotes: "Needs the HDMI adapter",
		},
	))
	if err != nil {
		t.Fatalf("Create Entry: %v", err)
	}
	readPublicScheduleInvalidation(t, entryStreamReader)
	if closeErr = entryStream.Body.Close(); closeErr != nil {
		t.Fatalf("close Entry Schedule stream: %v", closeErr)
	}
	entry := created.Msg.GetEntry()
	if entry.GetId() <= 0 || entry.GetCompetitionSessionId() != competitionID ||
		entry.GetName() != "Project Aurora" ||
		entry.GetDisposition() != rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED ||
		!entry.GetParticipating() || entry.GetRevision() != 1 {
		t.Fatalf("created Competition Entry = %+v", entry)
	}
	updated, err := client.UpdateEntry(t.Context(), connect.NewRequest(
		&competitionv1.UpdateEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "update-included-entry", ExpectedRevision: entry.GetRevision(),
			Name: "Project Aurora Revised", PublicDetails: "An attendee-visible revised demo",
			CrewNotes: "Needs the HDMI adapter",
		},
	))
	if err != nil || updated.Msg.GetEntry().GetRevision() != 2 ||
		updated.Msg.GetEntry().GetName() != "Project Aurora Revised" {
		t.Fatalf("updated Competition Entry = %+v, %v", updated, err)
	}
	_, staleUpdateErr := client.UpdateEntry(t.Context(), connect.NewRequest(
		&competitionv1.UpdateEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "stale-entry-update", ExpectedRevision: entry.GetRevision(),
			Name: "Stale Project",
		},
	))
	if connect.CodeOf(staleUpdateErr) != connect.CodeAborted {
		t.Fatalf("stale Entry update error = %v, want Aborted", staleUpdateErr)
	}
	entry = updated.Msg.GetEntry()
	scheduleBody := func() string {
		t.Helper()
		response := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule/sessions/"+strconv.FormatInt(competitionID, 10))
		body, bodyReadErr := io.ReadAll(response.Body)
		bodyCloseErr := response.Body.Close()
		if joinedErr := errors.Join(bodyReadErr, bodyCloseErr); joinedErr != nil {
			t.Fatalf("read public Competition: %v", joinedErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("public Competition status = %d: %s", response.StatusCode, body)
		}
		return string(body)
	}
	if body := scheduleBody(); !strings.Contains(body, "Project Aurora Revised") ||
		strings.Contains(body, "Needs the HDMI adapter") {
		t.Fatalf("Included Entry public projection = %q", body)
	}
	pending, err := client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "make-entry-pending", ExpectedRevision: entry.GetRevision(),
			Disposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_PENDING,
		},
	))
	if err != nil || pending.Msg.GetEntry().GetParticipating() {
		t.Fatalf("make Entry Pending = %+v, %v", pending, err)
	}
	if body := scheduleBody(); strings.Contains(body, "Project Aurora Revised") ||
		strings.Contains(body, "Needs the HDMI adapter") {
		t.Fatalf("Pending Entry leaked publicly = %q", body)
	}
	included, err := client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "include-entry", ExpectedRevision: pending.Msg.GetEntry().GetRevision(),
			Disposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED,
		},
	))
	if err != nil || !included.Msg.GetEntry().GetParticipating() {
		t.Fatalf("include Entry = %+v, %v", included, err)
	}
	if _, err = client.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "disable-delivery-for-disposition-test",
			ExpectedReadinessRevision: 0, FileDeliveryRequired: false,
		},
	)); err != nil {
		t.Fatalf("disable file delivery for disposition test: %v", err)
	}
	sessionClient := connectClient(sessionv1connect.NewSessionControlServiceClient, administrator, server.address)
	started, err := sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID, CommandId: "start-competition",
			ExpectedLiveStateRevision: new(int64(0)),
		},
	))
	if err != nil {
		t.Fatalf("start Competition: %v", err)
	}
	_, err = client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId:        "reject-live-entry-without-confirmation",
			ExpectedRevision: included.Msg.GetEntry().GetRevision(),
			Disposition:      rundownv1.EntryDisposition_ENTRY_DISPOSITION_REJECTED,
		},
	))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed live disposition error = %v, want FailedPrecondition", err)
	}
	rejected, err := client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "reject-live-entry", ExpectedRevision: included.Msg.GetEntry().GetRevision(),
			Disposition:           rundownv1.EntryDisposition_ENTRY_DISPOSITION_REJECTED,
			ConfirmedLiveOverride: true,
		},
	))
	if err != nil || rejected.Msg.GetEntry().GetParticipating() {
		t.Fatalf("reject live Entry = %+v, %v", rejected, err)
	}
	if body := scheduleBody(); strings.Contains(body, "Project Aurora Revised") {
		t.Fatalf("Rejected Entry remained public = %q", body)
	}
	preview, err := sessionClient.PreviewAdjustTarget(t.Context(), connect.NewRequest(
		&sessionv1.PreviewAdjustTargetRequest{
			EventId: 1, SessionId: competitionID,
			Adjustment: &sessionv1.PreviewAdjustTargetRequest_Custom{
				Custom: durationpb.New(5 * time.Minute),
			},
		},
	))
	if err != nil {
		t.Fatalf("preview Competition reschedule: %v", err)
	}
	_, err = sessionClient.AdjustTarget(t.Context(), connect.NewRequest(
		&sessionv1.AdjustTargetRequest{
			EventId: 1, SessionId: competitionID, CommandId: "reschedule-competition",
			ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
			Adjustment: &sessionv1.AdjustTargetRequest_Custom{
				Custom: durationpb.New(5 * time.Minute),
			},
			PreviewFingerprint: preview.Msg.GetPreviewFingerprint(),
			Confirmed:          true, HardBoundaryConfirmed: true,
		},
	))
	if err != nil {
		t.Fatalf("reschedule Competition: %v", err)
	}
	afterReschedule, err := client.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !afterReschedule.Msg.GetSubmissionDeadline().AsTime().Equal(deadline) {
		t.Fatalf("Competition Deadline moved during reschedule = %+v, %v", afterReschedule, err)
	}
	retained, err := client.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(retained.Msg.GetEntries()) != 1 ||
		retained.Msg.GetEntries()[0].GetRevision() != rejected.Msg.GetEntry().GetRevision() ||
		retained.Msg.GetEntries()[0].GetCrewNotes() != "Needs the HDMI adapter" {
		t.Fatalf("closed Competition changed Entry history = %+v, %v", retained, err)
	}
	auditResponse := get(t, administrator, server.address, "/admin/audit")
	auditBody, readErr := io.ReadAll(auditResponse.Body)
	closeErr = auditResponse.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Competition Audit history: %v", err)
	}
	if !strings.Contains(string(auditBody), "ChangeCompetitionEntryDisposition") {
		t.Fatalf("Competition disposition change missing from Audit history: %s", auditBody)
	}
	server.stop(t)
}

func TestProducerStagesAndReviewsCompetitionResults(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address)
	first, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-results-entry-first",
			Name: "First Result",
		},
	))
	if err != nil {
		t.Fatalf("create first Results Entry: %v", err)
	}
	second, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-results-entry-second",
			Name: "Second Result",
		},
	))
	if err != nil {
		t.Fatalf("create second Results Entry: %v", err)
	}
	resultsClient := connectClient(resultsv1connect.NewResultsServiceClient, administrator, server.address)
	initial, err := resultsClient.GetCompetitionResultsDraft(
		t.Context(),
		connect.NewRequest(&resultsv1.GetCompetitionResultsDraftRequest{
			EventId: 1, SessionId: competitionID,
		}),
	)
	if err != nil || initial.Msg.GetDraft().GetRevision() != 0 ||
		initial.Msg.GetDraft().GetDisposition() !=
			resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PENDING {
		t.Fatalf("initial Results Draft = %+v, %v", initial, err)
	}
	saved, err := resultsClient.SaveCompetitionResultsDraft(
		t.Context(),
		connect.NewRequest(&resultsv1.SaveCompetitionResultsDraftRequest{
			EventId: 1, SessionId: competitionID, CommandId: "save-results-draft",
			ExpectedRevision: 0,
			Disposition:      resultsv1.ResultsDisposition_RESULTS_DISPOSITION_PUBLISH,
			Score: &resultsv1.ScorePolicy{
				Type:           resultsv1.ScoreType_SCORE_TYPE_NONE,
				Visibility:     resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC,
				Requirement:    resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL,
				Interpretation: resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL,
			},
			Standings: []*resultsv1.CompetitionResultStanding{
				{
					EntryId:   first.Msg.GetEntry().GetId(),
					Standing:  resultsv1.ResultStanding_RESULT_STANDING_PLACED,
					Placement: proto.Int64(1), DisplayOrder: 1,
				},
				{
					EntryId:      second.Msg.GetEntry().GetId(),
					Standing:     resultsv1.ResultStanding_RESULT_STANDING_UNPLACED,
					DisplayOrder: 2,
				},
			},
		}),
	)
	if err != nil || saved.Msg.GetDraft().GetRevision() != 1 ||
		saved.Msg.GetDraft().GetReady() {
		t.Fatalf("save Results Draft = %+v, %v", saved, err)
	}
	ready, err := resultsClient.MarkCompetitionResultsReady(
		t.Context(),
		connect.NewRequest(&resultsv1.MarkCompetitionResultsReadyRequest{
			EventId: 1, SessionId: competitionID, CommandId: "review-results-draft",
			ExpectedRevision: 1,
		}),
	)
	if err != nil || !ready.Msg.GetDraft().GetReady() ||
		ready.Msg.GetDraft().GetRevision() != 1 {
		t.Fatalf("mark Results Ready = %+v, %v", ready, err)
	}
	_, err = competitionClient.UpdateEntry(t.Context(), connect.NewRequest(
		&competitionv1.UpdateEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: first.Msg.GetEntry().GetId(),
			CommandId:        "update-reviewed-results-entry",
			ExpectedRevision: first.Msg.GetEntry().GetRevision(),
			Name:             "Renamed Result",
		},
	))
	if err != nil {
		t.Fatalf("update reviewed Results Entry: %v", err)
	}
	superseded, err := resultsClient.GetCompetitionResultsDraft(
		t.Context(),
		connect.NewRequest(&resultsv1.GetCompetitionResultsDraftRequest{
			EventId: 1, SessionId: competitionID,
		}),
	)
	if err != nil || superseded.Msg.GetDraft().GetRevision() != 2 ||
		superseded.Msg.GetDraft().GetReady() {
		t.Fatalf("superseded Results Draft = %+v, %v", superseded, err)
	}
	noPublic, err := resultsClient.SaveCompetitionResultsDraft(
		t.Context(),
		connect.NewRequest(&resultsv1.SaveCompetitionResultsDraftRequest{
			EventId: 1, SessionId: competitionID, CommandId: "withhold-public-results",
			ExpectedRevision:   2,
			Disposition:        resultsv1.ResultsDisposition_RESULTS_DISPOSITION_NO_PUBLIC_RESULTS,
			NoPublicCrewReason: "Judging could not be completed",
			PublicExplanation:  "No results published.",
			Score: &resultsv1.ScorePolicy{
				Type:           resultsv1.ScoreType_SCORE_TYPE_NONE,
				Visibility:     resultsv1.ScoreVisibility_SCORE_VISIBILITY_PUBLIC,
				Requirement:    resultsv1.ScoreRequirement_SCORE_REQUIREMENT_OPTIONAL,
				Interpretation: resultsv1.ScoreInterpretation_SCORE_INTERPRETATION_INFORMATIONAL,
			},
		}),
	)
	if err != nil || noPublic.Msg.GetDraft().GetRevision() != 3 ||
		noPublic.Msg.GetDraft().GetReady() ||
		noPublic.Msg.GetDraft().GetNoPublicCrewReason() == "" {
		t.Fatalf("save No Public Results revision = %+v, %v", noPublic, err)
	}
	_, staleReadyErr := resultsClient.MarkCompetitionResultsReady(
		t.Context(),
		connect.NewRequest(&resultsv1.MarkCompetitionResultsReadyRequest{
			EventId: 1, SessionId: competitionID, CommandId: "stale-results-review",
			ExpectedRevision: 1,
		}),
	)
	if connect.CodeOf(staleReadyErr) != connect.CodeAborted {
		t.Fatalf("stale Results Ready error = %v, want Aborted", staleReadyErr)
	}
	server.stop(t)
}

func TestCompetitionStartPreflightRequiresFinalPrimaryDelivery(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address)
	created, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-preflight-entry",
			Name: "Ready Project",
		},
	))
	if err != nil {
		t.Fatalf("create preflight Entry: %v", err)
	}
	preflight, err := competitionClient.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preflight Competition Start: %v", err)
	}
	if preflight.Msg.GetRequireEntryReview() || !preflight.Msg.GetFileDeliveryRequired() ||
		len(preflight.Msg.GetBlockers()) != 1 ||
		preflight.Msg.GetBlockers()[0].GetCode() != "missing_file_delivery" ||
		preflight.Msg.GetBlockers()[0].GetEntryId() != created.Msg.GetEntry().GetId() {
		t.Fatalf("default Competition Preflight = %+v", preflight.Msg)
	}
	sessionClient := connectClient(sessionv1connect.NewSessionControlServiceClient, administrator, server.address)
	_, err = sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: competitionID, CommandId: "start-unready-competition",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "missing_file_delivery") {
		t.Fatalf("unready Competition Start error = %v", err)
	}
	uploaded := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(created.Msg.GetEntry().GetId(), 10),
			"name":        "entry",
			"command_id":  "upload-preflight-entry",
		},
		"entry.zip", "application/zip", []byte("one complete entry"),
	))
	if !uploaded.Primary || uploaded.Final {
		t.Fatalf("sole Attachment Version readiness = %+v", uploaded)
	}
	ready, err := competitionClient.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(ready.Msg.GetBlockers()) != 0 ||
		len(ready.Msg.GetAttachments()) != 1 ||
		!ready.Msg.GetAttachments()[0].GetPrimary() ||
		!ready.Msg.GetAttachments()[0].GetFinal() ||
		ready.Msg.GetAttachments()[0].GetAttachmentVersionId() != int64(uploaded.ID) ||
		ready.Msg.GetAttachments()[0].GetRevision() <= int64(uploaded.ReadinessRevision) {
		t.Fatalf("ready Competition Preflight = %+v, %v", ready, err)
	}
	_, previewRevisionErr := competitionClient.SetEntryAttachmentReadiness(
		t.Context(),
		connect.NewRequest(&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: created.Msg.GetEntry().GetId(),
			AttachmentVersionId: int64(uploaded.ID), CommandId: "reject-preview-revision",
			ExpectedRevision: ready.Msg.GetAttachments()[0].GetRevision(),
			Final:            true,
			Primary:          true,
		}),
	)
	if connect.CodeOf(previewRevisionErr) != connect.CodeAborted {
		t.Fatalf("persisted Competition Preflight revision error = %v, want Aborted", previewRevisionErr)
	}
	if _, err = sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: competitionID, CommandId: "start-ready-competition",
		ExpectedLiveStateRevision: proto.Int64(0),
	})); err != nil {
		t.Fatalf("start ready Competition: %v", err)
	}
	_, staleAfterStartErr := competitionClient.SetEntryAttachmentReadiness(
		t.Context(),
		connect.NewRequest(&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: created.Msg.GetEntry().GetId(),
			AttachmentVersionId: int64(uploaded.ID), CommandId: "reject-pre-start-revision",
			ExpectedRevision: int64(uploaded.ReadinessRevision),
			Final:            true,
			Primary:          true,
		}),
	)
	if connect.CodeOf(staleAfterStartErr) != connect.CodeAborted {
		t.Fatalf("unpersisted Start automation error = %v, want Aborted", staleAfterStartErr)
	}
	server.stop(t)
}

func TestEntryReviewFinalizesSoleUploadAndContentChangeInvalidatesIt(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	client := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address)
	configured, err := client.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "require-entry-review",
			ExpectedReadinessRevision: 0, RequireEntryReview: true, FileDeliveryRequired: true,
		},
	))
	if err != nil || !configured.Msg.GetRequireEntryReview() ||
		!configured.Msg.GetFileDeliveryRequired() || configured.Msg.GetReadinessRevision() != 1 {
		t.Fatalf("configure Competition readiness = %+v, %v", configured, err)
	}
	created, err := client.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-reviewed-entry",
			Name: "Reviewed Project",
		},
	))
	if err != nil {
		t.Fatalf("create reviewed Entry: %v", err)
	}
	uploaded := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(created.Msg.GetEntry().GetId(), 10),
			"name":        "entry",
			"command_id":  "upload-reviewed-entry",
		},
		"entry.zip", "application/zip", []byte("review me"),
	))
	if !uploaded.Primary || uploaded.Final {
		t.Fatalf("review-gated sole upload = %+v", uploaded)
	}
	withoutReview, err := client.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "disable-entry-review",
			ExpectedReadinessRevision: 1, FileDeliveryRequired: true,
		},
	))
	if err != nil || withoutReview.Msg.GetReadinessRevision() != 2 {
		t.Fatalf("disable Entry Review = %+v, %v", withoutReview, err)
	}
	autoFinal, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(autoFinal.Msg.GetBlockers()) != 0 ||
		len(autoFinal.Msg.GetAttachments()) != 1 || !autoFinal.Msg.GetAttachments()[0].GetFinal() {
		t.Fatalf("review-disabled Preflight automation = %+v, %v", autoFinal, err)
	}
	withReview, err := client.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "restore-entry-review",
			ExpectedReadinessRevision: 2, RequireEntryReview: true, FileDeliveryRequired: true,
		},
	))
	if err != nil || withReview.Msg.GetReadinessRevision() != 3 {
		t.Fatalf("restore Entry Review = %+v, %v", withReview, err)
	}
	blocked, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !findingCodesEqual(
		blocked.Msg.GetBlockers(), "unresolved_entry_review", "non_final_primary_attachment",
	) {
		t.Fatalf("review-gated Preflight = %+v, %v", blocked, err)
	}
	_, staleReviewErr := client.ReviewEntry(t.Context(), connect.NewRequest(
		&competitionv1.ReviewEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: created.Msg.GetEntry().GetId(),
			CommandId:        "stale-review-after-upload",
			ExpectedRevision: created.Msg.GetEntry().GetRevision(),
		},
	))
	if connect.CodeOf(staleReviewErr) != connect.CodeAborted {
		t.Fatalf("stale review after upload error = %v, want Aborted", staleReviewErr)
	}
	current, err := client.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(current.Msg.GetEntries()) != 1 {
		t.Fatalf("load review-gated Entry: %+v, %v", current, err)
	}
	entry := current.Msg.GetEntries()[0]
	reviewed, err := client.ReviewEntry(t.Context(), connect.NewRequest(
		&competitionv1.ReviewEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "review-entry", ExpectedRevision: entry.GetRevision(),
		},
	))
	if err != nil || !reviewed.Msg.GetEntry().GetReviewCurrent() {
		t.Fatalf("review Entry = %+v, %v", reviewed, err)
	}
	ready, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(ready.Msg.GetBlockers()) != 0 {
		t.Fatalf("reviewed Entry Preflight = %+v, %v", ready, err)
	}
	changed, err := client.UpdateEntry(t.Context(), connect.NewRequest(
		&competitionv1.UpdateEntryRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "change-reviewed-entry", ExpectedRevision: reviewed.Msg.GetEntry().GetRevision(),
			Name: "Reviewed Project Revised",
		},
	))
	if err != nil || changed.Msg.GetEntry().GetReviewCurrent() {
		t.Fatalf("change reviewed Entry = %+v, %v", changed, err)
	}
	invalidated, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !findingCodesEqual(
		invalidated.Msg.GetBlockers(),
		"unresolved_entry_review", "non_final_primary_attachment",
	) {
		t.Fatalf("invalidated Entry review Preflight = %+v, %v", invalidated, err)
	}
	server.stop(t)
}

func TestCompetitionPreflightRequiresDispositionAndUnambiguousPrimary(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	client := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address)
	created, err := client.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID, CommandId: "create-primary-entry",
			Name: "Primary Project",
		},
	))
	if err != nil {
		t.Fatalf("create Primary Entry: %v", err)
	}
	entry := created.Msg.GetEntry()
	pending, err := client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "make-primary-entry-pending", ExpectedRevision: entry.GetRevision(),
			Disposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_PENDING,
		},
	))
	if err != nil {
		t.Fatalf("make Entry Pending: %v", err)
	}
	blocked, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !findingCodesEqual(blocked.Msg.GetBlockers(), "pending_entry") {
		t.Fatalf("Pending Entry Preflight = %+v, %v", blocked, err)
	}
	included, err := client.ChangeEntryDisposition(t.Context(), connect.NewRequest(
		&competitionv1.ChangeEntryDispositionRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			CommandId: "include-primary-entry", ExpectedRevision: pending.Msg.GetEntry().GetRevision(),
			Disposition: rundownv1.EntryDisposition_ENTRY_DISPOSITION_INCLUDED,
		},
	))
	if err != nil {
		t.Fatalf("include Primary Entry: %v", err)
	}
	first := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(included.Msg.GetEntry().GetId(), 10),
			"name":        "entry",
			"command_id":  "upload-primary-v1",
		},
		"entry-v1.zip", "application/zip", []byte("first"),
	))
	second := decodeAttachmentVersion(t, requestMultipart(
		t.Context(), administrator, server.address, "/crew/events/1/attachments",
		map[string]string{
			"target_type": "Entry",
			"target_id":   strconv.FormatInt(included.Msg.GetEntry().GetId(), 10),
			"name":        "entry",
			"command_id":  "upload-primary-v2",
		},
		"entry-v2.zip", "application/zip", []byte("second"),
	))
	if !first.Primary || first.Final || second.Primary || second.Final {
		t.Fatalf("two uploaded versions = %+v then %+v", first, second)
	}
	cleared, err := client.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			AttachmentVersionId: int64(first.ID), CommandId: "clear-primary-v1",
			ExpectedRevision: int64(first.ReadinessRevision), Final: true, Primary: false,
		},
	))
	if err != nil {
		t.Fatalf("clear first Primary: %v", err)
	}
	autoSelected, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	firstCandidate := attachmentCandidate(autoSelected.Msg.GetAttachments(), int64(first.ID))
	if err != nil || len(autoSelected.Msg.GetBlockers()) != 0 ||
		firstCandidate == nil || !firstCandidate.GetPrimary() || !firstCandidate.GetFinal() {
		t.Fatalf("sole Final candidate Preflight = %+v, %v", autoSelected, err)
	}
	firstReadiness, err := client.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			AttachmentVersionId: int64(first.ID), CommandId: "clear-primary-v1-again",
			ExpectedRevision: cleared.Msg.GetAttachment().GetRevision(), Final: true, Primary: false,
		},
	))
	if err != nil {
		t.Fatalf("clear automatically selected Primary: %v", err)
	}
	secondFinal, err := client.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			AttachmentVersionId: int64(second.ID), CommandId: "finalize-v2",
			ExpectedRevision: int64(second.ReadinessRevision), Final: true, Primary: false,
		},
	))
	if err != nil || !secondFinal.Msg.GetAttachment().GetFinal() {
		t.Fatalf("finalize second version = %+v, %v", secondFinal, err)
	}
	ambiguous, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !findingCodesEqual(ambiguous.Msg.GetBlockers(), "ambiguous_primary_attachment") {
		t.Fatalf("ambiguous Primary Preflight = %+v, %v", ambiguous, err)
	}
	primary, err := client.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			AttachmentVersionId: int64(second.ID), CommandId: "select-v2-primary",
			ExpectedRevision: secondFinal.Msg.GetAttachment().GetRevision(), Final: true, Primary: true,
		},
	))
	if err != nil || !primary.Msg.GetAttachment().GetPrimary() {
		t.Fatalf("select second Primary = %+v, %v", primary, err)
	}
	ready, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(ready.Msg.GetBlockers()) != 0 {
		t.Fatalf("multiple Final Versions with one Primary = %+v, %v", ready, err)
	}
	nonFinal, err := client.SetEntryAttachmentReadiness(t.Context(), connect.NewRequest(
		&competitionv1.SetEntryAttachmentReadinessRequest{
			EventId: 1, SessionId: competitionID, EntryId: entry.GetId(),
			AttachmentVersionId: int64(second.ID), CommandId: "make-primary-non-final",
			ExpectedRevision: primary.Msg.GetAttachment().GetRevision(), Final: false, Primary: true,
		},
	))
	if err != nil || nonFinal.Msg.GetAttachment().GetFinal() {
		t.Fatalf("make Primary non-Final = %+v, %v", nonFinal, err)
	}
	nonFinalBlocked, err := client.PreflightStart(t.Context(), connect.NewRequest(
		&competitionv1.PreflightStartRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !findingCodesEqual(
		nonFinalBlocked.Msg.GetBlockers(), "non_final_primary_attachment",
	) {
		t.Fatalf("non-Final Primary Preflight = %+v, %v", nonFinalBlocked, err)
	}
	if firstReadiness.Msg.GetAttachment().GetPrimary() {
		t.Fatal("first Attachment remained Primary after explicit clear")
	}
	server.stop(t)
}

func TestCompetitionEntryOrderPreviewIsDeterministicByDefault(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	client := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address)
	var entryIDs []int64
	for index, name := range []string{"Alpha", "Bravo", "Charlie"} {
		created, err := client.CreateEntry(t.Context(), connect.NewRequest(
			&competitionv1.CreateEntryRequest{
				EventId: 1, SessionId: competitionID,
				CommandId: fmt.Sprintf("create-ordered-entry-%d", index), Name: name,
			},
		))
		if err != nil {
			t.Fatalf("create ordered Entry %q: %v", name, err)
		}
		entryIDs = append(entryIDs, created.Msg.GetEntry().GetId())
	}
	first, err := client.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preview default Entry Order: %v", err)
	}
	second, err := client.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	order := first.Msg.GetEntryOrder()
	if err != nil ||
		order.GetPolicy() != competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_DETERMINISTIC_SHUFFLE ||
		order.GetSeed() <= 0 || order.GetRevision() != 0 || order.GetLocked() ||
		!sameInt64Set(order.GetEntryIds(), entryIDs) ||
		!slices.Equal(order.GetEntryIds(), second.Msg.GetEntryOrder().GetEntryIds()) ||
		first.Msg.GetFingerprint() == "" ||
		first.Msg.GetFingerprint() != second.Msg.GetFingerprint() {
		t.Fatalf("default deterministic Entry Order = %+v then %+v, %v", first, second, err)
	}
	configured, err := client.GetCompetition(t.Context(), connect.NewRequest(
		&competitionv1.GetCompetitionRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || configured.Msg.GetEntryOrder().GetSeed() != order.GetSeed() ||
		configured.Msg.GetEntryOrder().GetPolicy() != order.GetPolicy() {
		t.Fatalf("stored default Entry Order = %+v, %v", configured, err)
	}
	server.stop(t)
}

func TestCrewConfiguresCompetitionEntryOrder(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	client := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address)
	var entries []*competitionv1.Entry
	for index, name := range []string{"Alpha", "Bravo", "Charlie"} {
		created, err := client.CreateEntry(t.Context(), connect.NewRequest(
			&competitionv1.CreateEntryRequest{
				EventId: 1, SessionId: competitionID,
				CommandId: fmt.Sprintf("create-configured-order-entry-%d", index), Name: name,
			},
		))
		if err != nil {
			t.Fatalf("create configured-order Entry %q: %v", name, err)
		}
		entries = append(entries, created.Msg.GetEntry())
	}
	submissionIDs := []int64{entries[0].GetId(), entries[1].GetId(), entries[2].GetId()}
	submission, err := client.ConfigureEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureEntryOrderRequest{
			EventId: 1, SessionId: competitionID, CommandId: "use-submission-order",
			ExpectedRevision: 0,
			Policy:           competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_SUBMISSION_ORDER,
		},
	))
	if err != nil || submission.Msg.GetEntryOrder().GetRevision() != 1 ||
		!slices.Equal(submission.Msg.GetEntryOrder().GetEntryIds(), submissionIDs) {
		t.Fatalf("configure Submission Order = %+v, %v", submission, err)
	}
	manualIDs := []int64{entries[2].GetId(), entries[0].GetId(), entries[1].GetId()}
	manual, err := client.ConfigureEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureEntryOrderRequest{
			EventId: 1, SessionId: competitionID, CommandId: "use-manual-order",
			ExpectedRevision: 1,
			Policy:           competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_MANUAL_ORDER,
			ManualEntryIds:   manualIDs,
		},
	))
	if err != nil || manual.Msg.GetEntryOrder().GetRevision() != 2 ||
		!slices.Equal(manual.Msg.GetEntryOrder().GetEntryIds(), manualIDs) {
		t.Fatalf("configure Manual Order = %+v, %v", manual, err)
	}
	shuffled, err := client.ConfigureEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureEntryOrderRequest{
			EventId: 1, SessionId: competitionID, CommandId: "use-seeded-order",
			ExpectedRevision: 2,
			Policy:           competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_DETERMINISTIC_SHUFFLE,
			Seed:             4242,
		},
	))
	if err != nil || shuffled.Msg.GetEntryOrder().GetRevision() != 3 ||
		shuffled.Msg.GetEntryOrder().GetSeed() != 4242 ||
		!sameInt64Set(shuffled.Msg.GetEntryOrder().GetEntryIds(), submissionIDs) {
		t.Fatalf("configure Deterministic Shuffle = %+v, %v", shuffled, err)
	}
	_, err = client.ConfigureEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureEntryOrderRequest{
			EventId: 1, SessionId: competitionID, CommandId: "restore-manual-order",
			ExpectedRevision: 3,
			Policy:           competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_MANUAL_ORDER,
			ManualEntryIds:   manualIDs,
		},
	))
	if err != nil {
		t.Fatalf("restore Manual Order: %v", err)
	}
	preview, err := client.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || !slices.Equal(preview.Msg.GetEntryOrder().GetEntryIds(), manualIDs) {
		t.Fatalf("preview Manual Order = %+v, %v", preview, err)
	}
	if _, err = client.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "disable-order-test-delivery",
			ExpectedReadinessRevision: 0,
		},
	)); err != nil {
		t.Fatalf("disable file delivery for Entry Order test: %v", err)
	}
	sessionClient := connectClient(sessionv1connect.NewSessionControlServiceClient, administrator, server.address)
	if _, err = sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID, CommandId: "start-ordered-competition",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	)); err != nil {
		t.Fatalf("start ordered Competition: %v", err)
	}
	_, liveConfigureErr := client.ConfigureEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureEntryOrderRequest{
			EventId: 1, SessionId: competitionID, CommandId: "rewrite-live-order",
			ExpectedRevision: 4,
			Policy:           competitionv1.EntryOrderPolicy_ENTRY_ORDER_POLICY_SUBMISSION_ORDER,
		},
	))
	if connect.CodeOf(liveConfigureErr) != connect.CodeFailedPrecondition {
		t.Fatalf("live Entry Order configuration error = %v, want FailedPrecondition", liveConfigureErr)
	}
	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	client = connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, restarted.address)
	restored, err := client.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || restored.Msg.GetEntryOrder().GetLocked() ||
		!slices.Equal(restored.Msg.GetEntryOrder().GetEntryIds(), manualIDs) {
		t.Fatalf("restored Entry Order = %+v, %v", restored, err)
	}
	audit := get(t, administrator, restarted.address, "/admin/audit")
	auditBody, readErr := io.ReadAll(audit.Body)
	closeErr := audit.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Entry Order Audit history: %v", err)
	}
	if !bytes.Contains(auditBody, []byte("ConfigureCompetitionEntryOrder")) {
		t.Fatalf("Entry Order commands missing from Audit history: %s", auditBody)
	}
	restarted.stop(t)
}

func TestControlOwnerTakesCompetitionEntryToDurableProgramOutput(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	displayClient := enrollAndAssignDisplay(
		t, administrator, server, "Competition Display", "competition-output",
	)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address)
	entry, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
		&competitionv1.CreateEntryRequest{
			EventId: 1, SessionId: competitionID,
			CommandId: "create-program-entry", Name: "Aurora",
		},
	))
	if err != nil {
		t.Fatalf("create Program Entry: %v", err)
	}
	if _, err = competitionClient.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "disable-program-file-delivery",
			ExpectedReadinessRevision: 0,
		},
	)); err != nil {
		t.Fatalf("disable Program Competition file delivery: %v", err)
	}
	sessionClient := connectClient(sessionv1connect.NewSessionControlServiceClient, administrator, server.address)
	if _, err = sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID, CommandId: "start-program-competition",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	)); err != nil {
		t.Fatalf("start Program Competition: %v", err)
	}
	order, err := competitionClient.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preview Program Entry Order: %v", err)
	}
	programClient := connectClient(programv1connect.NewProgramControlServiceClient, administrator, server.address)
	claimed, err := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:    programv1.ControlAction_CONTROL_ACTION_CLAIM,
			CommandId: "claim-program-control",
		},
	))
	if err != nil || claimed.Msg.GetChannel().GetControlOwner().GetAccountId() != 1 ||
		claimed.Msg.GetChannel().GetPrevious() != nil ||
		claimed.Msg.GetChannel().GetCurrent() != nil ||
		claimed.Msg.GetChannel().GetNext().GetKind() !=
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_UPCOMING ||
		claimed.Msg.GetChannel().GetPreview().GetKind() !=
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_UPCOMING ||
		claimed.Msg.GetChannel().GetProgramOutput().GetKind() !=
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_STANDBY {
		t.Fatalf("claim Program Channel = %+v, %v", claimed, err)
	}
	controlView := get(
		t, administrator, server.address,
		fmt.Sprintf("/crew/program/%d?event_id=1", competitionID),
	)
	controlViewBody, readErr := io.ReadAll(controlView.Body)
	closeErr := controlView.Body.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Program control View: %v", err)
	}
	for _, want := range []string{
		"Previous", "Current", "Next", "Preview", "Program Output",
		"Consuming Displays", "Take Preview", "Defer current Entry",
		"Back to Program Output and Overrides",
	} {
		if !bytes.Contains(controlViewBody, []byte(want)) {
			t.Fatalf("Program control View missing %q: %s", want, controlViewBody)
		}
	}
	openAndCloseProgramStream(t, administrator, server.address, competitionID)
	var presence *programv1.ProgramChannel
	for range 50 {
		current, currentErr := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
			&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
		))
		if currentErr != nil {
			t.Fatalf("read disconnected Program owner: %v", currentErr)
		}
		presence = current.Msg.GetChannel()
		if !presence.GetControlOwner().GetConnected() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if presence.GetControlOwner().GetConnected() {
		t.Fatalf("closed control stream retained connected owner: %+v", presence)
	}
	entryItem := &programv1.ProgramItem{
		Kind:    programv1.ProgramItemKind_PROGRAM_ITEM_KIND_ENTRY,
		EntryId: entry.Msg.GetEntry().GetId(),
	}
	if len(claimed.Msg.GetChannel().GetItems()) != 5 {
		t.Fatalf("Competition Program Items = %+v", claimed.Msg.GetChannel().GetItems())
	}
	controlRevision := presence.GetControlStateRevision()
	for index, item := range claimed.Msg.GetChannel().GetItems() {
		previewed, previewErr := programClient.SelectPreview(t.Context(), connect.NewRequest(
			&programv1.SelectPreviewRequest{
				EventId: 1, SessionId: competitionID, Item: item,
				CommandId:                    fmt.Sprintf("preview-program-item-%d", index),
				ExpectedControlStateRevision: controlRevision,
			},
		))
		if previewErr != nil ||
			previewed.Msg.GetChannel().GetPreview().GetKind() != item.GetKind() ||
			previewed.Msg.GetChannel().GetProgramOutput().GetKind() !=
				programv1.ProgramItemKind_PROGRAM_ITEM_KIND_STANDBY {
			t.Fatalf("select %s Preview = %+v, %v", item.GetKind(), previewed, previewErr)
		}
		controlRevision = previewed.Msg.GetChannel().GetControlStateRevision()
	}
	selected, err := programClient.SelectPreview(t.Context(), connect.NewRequest(
		&programv1.SelectPreviewRequest{
			EventId: 1, SessionId: competitionID, Item: entryItem,
			CommandId:                    "select-entry-program-preview",
			ExpectedControlStateRevision: controlRevision,
		},
	))
	if err != nil ||
		selected.Msg.GetChannel().GetPreview().GetEntryId() != entryItem.GetEntryId() ||
		selected.Msg.GetChannel().GetProgramOutput().GetKind() !=
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_STANDBY {
		t.Fatalf("select Program Preview = %+v, %v", selected, err)
	}
	offline, err := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || len(offline.Msg.GetChannel().GetConsumingDisplays()) != 1 ||
		offline.Msg.GetChannel().GetConsumingDisplays()[0].GetDeliveryState() != "offline" {
		t.Fatalf("offline consuming Display = %+v, %v", offline, err)
	}
	acknowledgeDisplaySnapshot(
		t, displayClient, server.address, readDisplaySnapshot(t, displayClient, server.address),
	)
	applied, err := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil ||
		applied.Msg.GetChannel().GetConsumingDisplays()[0].GetDeliveryState() != "applied" {
		t.Fatalf("applied consuming Display = %+v, %v", applied, err)
	}
	operator := provisionOperator(t, administrator, server)
	observer := provisionObserver(t, administrator, server)
	operatorProgram := connectClient(programv1connect.NewProgramControlServiceClient, operator, server.address)
	observerProgram := connectClient(programv1connect.NewProgramControlServiceClient, observer, server.address)
	unauthorizedCommands := []func() error{
		func() error {
			_, commandErr := observerProgram.ChangeControl(t.Context(), connect.NewRequest(
				&programv1.ChangeControlRequest{
					EventId: 1, SessionId: competitionID,
					Action:                       programv1.ControlAction_CONTROL_ACTION_CLAIM,
					CommandId:                    "reject-observer-program-control",
					ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
				},
			))
			return commandErr
		},
		func() error {
			_, commandErr := observerProgram.SelectPreview(t.Context(), connect.NewRequest(
				&programv1.SelectPreviewRequest{
					EventId: 1, SessionId: competitionID, Item: entryItem,
					CommandId:                    "reject-observer-program-preview",
					ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
				},
			))
			return commandErr
		},
		func() error {
			_, commandErr := observerProgram.Take(t.Context(), connect.NewRequest(
				&programv1.TakeRequest{
					EventId: 1, SessionId: competitionID,
					CommandId:                 "reject-observer-program-take",
					ExpectedLiveStateRevision: 0, Preview: entryItem,
					ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
				},
			))
			return commandErr
		},
	}
	for _, unauthorizedCommand := range unauthorizedCommands {
		if commandErr := unauthorizedCommand(); connect.CodeOf(commandErr) != connect.CodePermissionDenied {
			t.Fatalf("unauthorized Program command = %v, want PermissionDenied", commandErr)
		}
	}
	_, ownedErr := operatorProgram.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_CLAIM,
			CommandId:                    "reject-second-program-owner",
			ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if connect.CodeOf(ownedErr) != connect.CodeFailedPrecondition {
		t.Fatalf("second Program owner claim = %v", ownedErr)
	}
	requested, err := operatorProgram.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_REQUEST_HANDOVER,
			CommandId:                    "request-program-handover",
			ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if err != nil {
		t.Fatalf("request Program handover: %v", err)
	}
	openAndCloseProgramStream(t, operator, server.address, competitionID)
	requesterPresence := requested.Msg.GetChannel()
	for range 50 {
		current, currentErr := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
			&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
		))
		if currentErr != nil {
			t.Fatalf("read disconnected Program requester: %v", currentErr)
		}
		requesterPresence = current.Msg.GetChannel()
		if !requesterPresence.GetHandoverRequester().GetConnected() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if requesterPresence.GetHandoverRequester().GetConnected() {
		t.Fatalf("closed control stream retained connected requester: %+v", requesterPresence)
	}
	_, staleHandoverErr := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_HANDOVER,
			CommandId:                    "reject-stale-program-handover",
			ExpectedControlStateRevision: selected.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if connect.CodeOf(staleHandoverErr) != connect.CodeAborted {
		t.Fatalf("stale Program handover = %v, want Aborted", staleHandoverErr)
	}
	handed, err := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_HANDOVER,
			CommandId:                    "hand-over-program-control",
			ExpectedControlStateRevision: requesterPresence.GetControlStateRevision(),
		},
	))
	if err != nil || handed.Msg.GetChannel().GetControlOwner().GetAccountId() != 2 ||
		handed.Msg.GetChannel().GetControlOwner().GetConnected() {
		t.Fatalf("hand over Program Channel = %+v, %v", handed, err)
	}
	takeRequest := &programv1.TakeRequest{
		EventId: 1, SessionId: competitionID, CommandId: "take-aurora-program-slide",
		ExpectedLiveStateRevision: 0, Preview: entryItem,
		ExpectedEntryOrderRevision:   order.Msg.GetEntryOrder().GetRevision(),
		EntryOrderFingerprint:        order.Msg.GetFingerprint(),
		ExpectedControlStateRevision: handed.Msg.GetChannel().GetControlStateRevision(),
	}
	staleOrderRequest := &programv1.TakeRequest{
		EventId: 1, SessionId: competitionID,
		CommandId:                    "reject-stale-entry-order-program-take",
		ExpectedLiveStateRevision:    takeRequest.GetExpectedLiveStateRevision(),
		Preview:                      entryItem,
		ExpectedEntryOrderRevision:   takeRequest.GetExpectedEntryOrderRevision() + 1,
		EntryOrderFingerprint:        takeRequest.GetEntryOrderFingerprint(),
		ExpectedControlStateRevision: takeRequest.GetExpectedControlStateRevision(),
	}
	_, staleOrderErr := operatorProgram.Take(t.Context(), connect.NewRequest(staleOrderRequest))
	if connect.CodeOf(staleOrderErr) != connect.CodeAborted {
		t.Fatalf("stale Entry Order Program Take = %v, want Aborted", staleOrderErr)
	}
	taken, err := operatorProgram.Take(t.Context(), connect.NewRequest(takeRequest))
	if err != nil || taken.Msg.GetChannel().GetLiveStateRevision() != 1 ||
		taken.Msg.GetChannel().GetProgramOutput().GetEntryId() != entryItem.GetEntryId() ||
		taken.Msg.GetChannel().GetConsumingDisplays()[0].GetDeliveryState() != "lagging" {
		t.Fatalf("Take Program Output = %+v, %v", taken, err)
	}
	retried, err := operatorProgram.Take(t.Context(), connect.NewRequest(takeRequest))
	if err != nil || retried.Msg.GetChannel().GetLiveStateRevision() != 1 {
		t.Fatalf("retry Take Program Output = %+v, %v", retried, err)
	}
	replayedPreview, err := programClient.SelectPreview(t.Context(), connect.NewRequest(
		&programv1.SelectPreviewRequest{
			EventId: 1, SessionId: competitionID, Item: entryItem,
			CommandId:                    "select-entry-program-preview",
			ExpectedControlStateRevision: controlRevision,
		},
	))
	if err != nil ||
		replayedPreview.Msg.GetChannel().GetControlStateRevision() !=
			selected.Msg.GetChannel().GetControlStateRevision() ||
		replayedPreview.Msg.GetChannel().GetPreview().GetEntryId() != entryItem.GetEntryId() {
		t.Fatalf("replay original Program Preview outcome = %+v, %v", replayedPreview, err)
	}
	_, staleTakeErr := operatorProgram.Take(t.Context(), connect.NewRequest(
		&programv1.TakeRequest{
			EventId: 1, SessionId: competitionID, CommandId: "reject-stale-program-take",
			ExpectedLiveStateRevision: 0, Preview: claimed.Msg.GetChannel().GetNext(),
			ExpectedControlStateRevision: taken.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if connect.CodeOf(staleTakeErr) != connect.CodeAborted {
		t.Fatalf("stale Program Take = %v, want Aborted", staleTakeErr)
	}
	displaySnapshot := readDisplaySnapshot(t, displayClient, server.address)
	if displaySnapshot.ProgramOutput.Title != "Aurora" ||
		displaySnapshot.ProgramOutputRevision != "1" {
		t.Fatalf("Display Program Output = %+v", displaySnapshot)
	}
	acknowledgeDisplaySnapshot(t, displayClient, server.address, displaySnapshot)
	appliedOutput, err := operatorProgram.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil ||
		appliedOutput.Msg.GetChannel().GetConsumingDisplays()[0].GetDeliveryState() != "applied" {
		t.Fatalf("applied Program Output Display = %+v, %v", appliedOutput, err)
	}
	disconnected, err := operatorProgram.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_DISCONNECT,
			CommandId:                    "disconnect-program-owner",
			ExpectedControlStateRevision: taken.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if err != nil || disconnected.Msg.GetChannel().GetControlOwner().GetConnected() {
		t.Fatalf("disconnect Program owner = %+v, %v", disconnected, err)
	}
	replayedHandover, err := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_HANDOVER,
			CommandId:                    "hand-over-program-control",
			ExpectedControlStateRevision: requesterPresence.GetControlStateRevision(),
		},
	))
	if err != nil ||
		replayedHandover.Msg.GetChannel().GetControlStateRevision() !=
			handed.Msg.GetChannel().GetControlStateRevision() ||
		replayedHandover.Msg.GetChannel().GetControlOwner().GetAccountId() != 2 {
		t.Fatalf("replay original Program handover outcome = %+v, %v", replayedHandover, err)
	}
	replayedTake, err := operatorProgram.Take(t.Context(), connect.NewRequest(takeRequest))
	if err != nil ||
		replayedTake.Msg.GetChannel().GetControlStateRevision() !=
			taken.Msg.GetChannel().GetControlStateRevision() ||
		replayedTake.Msg.GetChannel().GetProgramOutput().GetEntryId() != entryItem.GetEntryId() {
		t.Fatalf("replay original Program Take outcome = %+v, %v", replayedTake, err)
	}
	_, unconfirmedErr := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:                       programv1.ControlAction_CONTROL_ACTION_TAKEOVER,
			CommandId:                    "reject-unconfirmed-program-takeover",
			ExpectedControlStateRevision: disconnected.Msg.GetChannel().GetControlStateRevision(),
		},
	))
	if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed Program takeover = %v", unconfirmedErr)
	}
	if _, err = programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action: programv1.ControlAction_CONTROL_ACTION_TAKEOVER, Confirmed: true,
			CommandId:                    "confirm-program-takeover",
			ExpectedControlStateRevision: disconnected.Msg.GetChannel().GetControlStateRevision(),
		},
	)); err != nil {
		t.Fatalf("confirmed Program takeover: %v", err)
	}
	dataDir, bin := server.dataDir, server.bin
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	programClient = connectClient(programv1connect.NewProgramControlServiceClient, administrator, restarted.address)
	restored, err := programClient.GetProgramChannel(t.Context(), connect.NewRequest(
		&programv1.GetProgramChannelRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil || restored.Msg.GetChannel().GetControlOwner() != nil ||
		restored.Msg.GetChannel().GetProgramOutput().GetEntryId() != entryItem.GetEntryId() ||
		restored.Msg.GetChannel().GetPreview().GetKind() !=
			programv1.ProgramItemKind_PROGRAM_ITEM_KIND_UPCOMING {
		t.Fatalf("restored Program Channel = %+v, %v", restored, err)
	}
	restoredDisplay := readDisplaySnapshot(t, displayClient, restarted.address)
	if restoredDisplay.ProgramOutput.Title != "Aurora" ||
		restoredDisplay.ProgramOutputRevision != "1" {
		t.Fatalf("restored Display Program Output = %+v", restoredDisplay)
	}
	audit := get(t, administrator, restarted.address, "/admin/audit")
	auditBody, readErr := io.ReadAll(audit.Body)
	closeErr = audit.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Program Output Audit: %v", err)
	}
	if bytes.Count(auditBody, []byte(`"action":"TakeProgramOutput"`)) != 4 {
		t.Fatalf("Program Output Audit entries = %s", auditBody)
	}
	if !bytes.Contains(auditBody, []byte("ChangeProgramControlTakeover")) ||
		!bytes.Contains(auditBody, []byte("program_takeover_confirmation_required")) ||
		!bytes.Contains(auditBody, []byte("program_revision_conflict")) ||
		!bytes.Contains(auditBody, []byte("program_control_revision_conflict")) ||
		!bytes.Contains(auditBody, []byte("competition_entry_order_revision_conflict")) ||
		bytes.Count(auditBody, []byte("program_operator_required")) != 3 {
		t.Fatalf("Program takeover Audit evidence missing: %s", auditBody)
	}
	restarted.stop(t)
}

func TestControlOwnerDefersCompetitionEntry(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	competitionID, _ := addCompetitionSession(t, administrator, server)
	competitionClient := connectClient(competitionv1connect.NewCompetitionServiceClient, administrator, server.address)
	entries := make([]*competitionv1.Entry, 0, 2)
	for index, name := range []string{"Aurora", "Beacon"} {
		created, err := competitionClient.CreateEntry(t.Context(), connect.NewRequest(
			&competitionv1.CreateEntryRequest{
				EventId: 1, SessionId: competitionID,
				CommandId: fmt.Sprintf("create-defer-entry-%d", index), Name: name,
			},
		))
		if err != nil {
			t.Fatalf("create defer Entry: %v", err)
		}
		entries = append(entries, created.Msg.GetEntry())
	}
	if _, err := competitionClient.ConfigureReadiness(t.Context(), connect.NewRequest(
		&competitionv1.ConfigureReadinessRequest{
			EventId: 1, SessionId: competitionID, CommandId: "disable-defer-file-delivery",
			ExpectedReadinessRevision: 0,
		},
	)); err != nil {
		t.Fatalf("disable defer Competition file delivery: %v", err)
	}
	sessionClient := connectClient(sessionv1connect.NewSessionControlServiceClient, administrator, server.address)
	if _, err := sessionClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: competitionID, CommandId: "start-defer-competition",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	)); err != nil {
		t.Fatalf("start defer Competition: %v", err)
	}
	order, err := competitionClient.PreviewEntryOrder(t.Context(), connect.NewRequest(
		&competitionv1.PreviewEntryOrderRequest{EventId: 1, SessionId: competitionID},
	))
	if err != nil {
		t.Fatalf("preview defer Entry Order: %v", err)
	}
	entryByID := map[int64]*competitionv1.Entry{}
	for _, entry := range entries {
		entryByID[entry.GetId()] = entry
	}
	orderedIDs := order.Msg.GetEntryOrder().GetEntryIds()
	deferredEntry := entryByID[orderedIDs[0]]
	programClient := connectClient(programv1connect.NewProgramControlServiceClient, administrator, server.address)
	claimed, err := programClient.ChangeControl(t.Context(), connect.NewRequest(
		&programv1.ChangeControlRequest{
			EventId: 1, SessionId: competitionID,
			Action:    programv1.ControlAction_CONTROL_ACTION_CLAIM,
			CommandId: "claim-defer-control",
		},
	))
	if err != nil {
		t.Fatalf("claim defer control: %v", err)
	}
	channel := claimed.Msg.GetChannel()
	for _, commandID := range []string{"take-defer-upcoming", "take-defer-starting"} {
		taken, takeErr := programClient.Take(t.Context(), connect.NewRequest(
			&programv1.TakeRequest{
				EventId: 1, SessionId: competitionID, CommandId: commandID,
				ExpectedLiveStateRevision:    channel.GetLiveStateRevision(),
				ExpectedControlStateRevision: channel.GetControlStateRevision(),
				Preview:                      channel.GetPreview(),
			},
		))
		if takeErr != nil {
			t.Fatalf("advance to first defer Entry: %v", takeErr)
		}
		channel = taken.Msg.GetChannel()
	}
	if channel.GetNext().GetEntryId() != orderedIDs[0] {
		t.Fatalf("canonical defer Next = %+v", channel.GetNext())
	}
	operator := provisionOperator(t, administrator, server)
	operatorProgram := connectClient(programv1connect.NewProgramControlServiceClient, operator, server.address)
	deferRequest := &programv1.DeferEntryRequest{
		EventId: 1, SessionId: competitionID, EntryId: deferredEntry.GetId(),
		CommandId: "defer-first-entry", ExpectedEntryRevision: deferredEntry.GetRevision(),
		ExpectedProgramRevision:      channel.GetLiveStateRevision(),
		ExpectedControlStateRevision: channel.GetControlStateRevision(),
	}
	if _, err = operatorProgram.DeferEntry(
		t.Context(), connect.NewRequest(deferRequest),
	); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-owner Defer = %v, want PermissionDenied", err)
	}
	deferRequest.CommandId = "defer-first-entry-as-owner"
	deferred, err := programClient.DeferEntry(t.Context(), connect.NewRequest(deferRequest))
	if err != nil {
		t.Fatalf("owner Defer Entry: %v", err)
	}
	channel = deferred.Msg.GetChannel()
	if channel.GetCurrent().GetEntryId() != orderedIDs[0] ||
		channel.GetNext().GetEntryId() != orderedIDs[1] ||
		channel.GetPreview().GetEntryId() != orderedIDs[1] ||
		channel.GetControlStateRevision() != deferRequest.GetExpectedControlStateRevision()+1 {
		t.Fatalf("deferred Program Channel = %+v", channel)
	}
	var retries []*programv1.ProgramItem
	for _, item := range channel.GetItems() {
		if item.GetRetry() {
			retries = append(retries, item)
		}
	}
	if len(retries) != 1 || retries[0].GetEntryId() != orderedIDs[0] {
		t.Fatalf("defer retry queue = %+v", retries)
	}
	server.stop(t)
}

func sameInt64Set(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func findingCodesEqual(findings []*competitionv1.PreflightFinding, want ...string) bool {
	if len(findings) != len(want) {
		return false
	}
	for index, finding := range findings {
		if finding.GetCode() != want[index] {
			return false
		}
	}
	return true
}

func attachmentCandidate(
	attachments []*competitionv1.AttachmentReadiness,
	versionID int64,
) *competitionv1.AttachmentReadiness {
	for _, attachment := range attachments {
		if attachment.GetAttachmentVersionId() == versionID {
			return attachment
		}
	}
	return nil
}

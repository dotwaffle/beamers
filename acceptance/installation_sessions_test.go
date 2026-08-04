package acceptance_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
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

	rundownv1 "github.com/dotwaffle/beamers/gen/beamers/rundown/v1"
	"github.com/dotwaffle/beamers/gen/beamers/rundown/v1/rundownv1connect"
	sessionv1 "github.com/dotwaffle/beamers/gen/beamers/session/v1"
	"github.com/dotwaffle/beamers/gen/beamers/session/v1/sessionv1connect"
	"github.com/dotwaffle/beamers/internal/publictime"
	"github.com/dotwaffle/beamers/internal/store/storetest"
)

func TestConcurrentDraftEditsConflictOnlyOnChangedFacts(t *testing.T) {
	producer, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, producer, server)
	client := connectClient(rundownv1connect.NewRundownServiceClient, producer, server.address)
	current, err := client.GetCrewRundown(t.Context(), connect.NewRequest(
		&rundownv1.GetCrewRundownRequest{EventId: 1},
	))
	if err != nil {
		t.Fatalf("read current Rundown revision: %v", err)
	}
	baseRevision := current.Msg.GetDraftRevision()

	titleEdit, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "concurrent-title", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Opening Keynote Updated",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if err != nil {
		t.Fatalf("edit Session title: %v", err)
	}
	notesEdit, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "concurrent-notes", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, CrewNotes: "updated cue notes",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"crew_notes"}},
		}},
	}))
	if err != nil {
		t.Fatalf("independent stale Session edit: %v", err)
	}
	if notesEdit.Msg.GetDraftRevision() != titleEdit.Msg.GetDraftRevision()+1 {
		t.Errorf("independent Draft revisions = %d then %d, want consecutive", titleEdit.Msg.GetDraftRevision(), notesEdit.Msg.GetDraftRevision())
	}

	_, err = client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "conflicting-title", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Last write must not win",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("overlapping stale Edit error = %v, want Aborted", err)
	}
	var conflictErr *connect.Error
	if !errors.As(err, &conflictErr) || len(conflictErr.Details()) != 1 {
		t.Fatalf("overlapping stale Edit detail = %v, want one current-state detail", err)
	}
	conflictValue, detailErr := conflictErr.Details()[0].Value()
	if detailErr != nil {
		t.Fatalf("decode Draft conflict detail: %v", detailErr)
	}
	conflict, ok := conflictValue.(*rundownv1.DraftRevisionConflict)
	if !ok || conflict.GetCurrentDraftRevision() != notesEdit.Msg.GetDraftRevision() ||
		len(conflict.GetOverlappingChanges()) != 1 || conflict.GetOverlappingChanges()[0].GetFactKey() != "title" ||
		conflict.GetOverlappingChanges()[0].GetCurrentValueJson() != `"Opening Keynote Updated"` {
		t.Errorf("Draft conflict detail = %+v", conflictValue)
	}

	preview, err := client.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{titleEdit.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("preview selected title fact: %v", err)
	}
	if _, err = client.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-selected-title",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: preview.Msg.GetDraftRevision(), PublishedRevision: preview.Msg.GetPublishedRevision(),
			ChangeIds: preview.Msg.GetChangeIds(), Fingerprint: preview.Msg.GetFingerprint(),
		},
	})); err != nil {
		t.Fatalf("publish selected title fact: %v", err)
	}
	published, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("read partially Published Rundown: %v", err)
	}
	var publishedSession *rundownv1.CrewSession
	for _, candidate := range published.Msg.GetSessions() {
		if candidate.GetId() == sessionID {
			publishedSession = candidate
		}
	}
	if publishedSession == nil || publishedSession.GetTitle() != "Opening Keynote Updated" || publishedSession.GetCrewNotes() == "updated cue notes" {
		t.Errorf("partially Published Session = %+v, want selected title without unselected notes", publishedSession)
	}
	_, staleAfterPublishErr := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "stale-title-after-publish", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{Id: sessionID, Title: "Stale after Publish",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}}}},
	}))
	if connect.CodeOf(staleAfterPublishErr) != connect.CodeAborted {
		t.Errorf("stale title after Publish = %v, want Aborted", staleAfterPublishErr)
	}
	var staleAfterPublishConnectErr *connect.Error
	if !errors.As(staleAfterPublishErr, &staleAfterPublishConnectErr) || len(staleAfterPublishConnectErr.Details()) != 1 {
		t.Fatalf("stale title after Publish detail = %v", staleAfterPublishErr)
	}
	staleAfterPublishValue, detailErr := staleAfterPublishConnectErr.Details()[0].Value()
	staleAfterPublishConflict, ok := staleAfterPublishValue.(*rundownv1.DraftRevisionConflict)
	if detailErr != nil || !ok || len(staleAfterPublishConflict.GetOverlappingChanges()) != 1 ||
		staleAfterPublishConflict.GetOverlappingChanges()[0].GetCurrentValueJson() != `"Opening Keynote Updated"` {
		t.Errorf("stale title after Publish current state = %+v, %v", staleAfterPublishValue, detailErr)
	}
	if _, err = client.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{notesEdit.Msg.GetChanges()[0].GetId()},
	})); err != nil {
		t.Errorf("unselected notes no longer effective: %v", err)
	}
	discarded, err := client.DiscardDraftChanges(t.Context(), connect.NewRequest(&rundownv1.DiscardDraftChangesRequest{
		EventId: 1, CommandId: "discard-unselected-notes", ExpectedDraftRevision: published.Msg.GetDraftRevision(),
		ChangeIds: []int64{notesEdit.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("discard unselected notes: %v", err)
	}
	if len(discarded.Msg.GetChanges()) != 1 || discarded.Msg.GetChanges()[0].GetStatus() != "Discarded" {
		t.Errorf("Discard response = %+v", discarded.Msg)
	}
	discardPreview, err := client.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{notesEdit.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil || len(discardPreview.Msg.GetValidationFailures()) == 0 {
		t.Errorf("discarded notes Preview = %+v, %v; want validation failure", discardPreview, err)
	}
	reverted, err := client.RevertDraftChange(t.Context(), connect.NewRequest(&rundownv1.RevertDraftChangeRequest{
		EventId: 1, CommandId: "revert-published-title", ExpectedDraftRevision: discarded.Msg.GetDraftRevision(),
		ChangeId: titleEdit.Msg.GetChanges()[0].GetId(),
	}))
	if err != nil {
		t.Fatalf("revert Published title: %v", err)
	}
	if len(reverted.Msg.GetChanges()) != 1 || reverted.Msg.GetChanges()[0].GetKind() != "RevertSession" {
		t.Fatalf("Revert response = %+v", reverted.Msg)
	}
	revertPreview, err := client.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{reverted.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("preview Draft Revert: %v", err)
	}
	if _, err = client.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-reverted-title", Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: revertPreview.Msg.GetDraftRevision(), PublishedRevision: revertPreview.Msg.GetPublishedRevision(),
			ChangeIds: revertPreview.Msg.GetChangeIds(), Fingerprint: revertPreview.Msg.GetFingerprint(),
		},
	})); err != nil {
		t.Fatalf("publish Draft Revert: %v", err)
	}
	revertedRundown, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("read Reverted Rundown: %v", err)
	}
	for _, candidate := range revertedRundown.Msg.GetSessions() {
		if candidate.GetId() == sessionID && candidate.GetTitle() != "Opening Keynote" {
			t.Errorf("Reverted Published title = %q", candidate.GetTitle())
		}
	}
	server.stop(t)
}

func TestConcurrentSessionMembershipEditsUsePerMemberFacts(t *testing.T) {
	producer, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, producer, server)
	client := connectClient(rundownv1connect.NewRundownServiceClient, producer, server.address)
	current, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("read current Rundown: %v", err)
	}
	created, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "create-membership-lanes", ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Locations: []*rundownv1.LocationDraft{{Ref: "membership-location-two", Name: "Membership Hall Two"}, {Ref: "membership-location-three", Name: "Membership Hall Three"}},
		Lanes: []*rundownv1.LaneDraft{
			{Ref: "membership-two", Name: "Membership Two", Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "membership-location-two"}}},
			{Ref: "membership-three", Name: "Membership Three", Location: &rundownv1.TargetRef{Target: &rundownv1.TargetRef_Ref{Ref: "membership-location-three"}}},
		},
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID,
			AddLanes: []*rundownv1.TargetRef{
				{Target: &rundownv1.TargetRef_Ref{Ref: "membership-two"}},
				{Target: &rundownv1.TargetRef_Ref{Ref: "membership-three"}},
			},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"add_lanes"}},
		}},
	}))
	if err != nil {
		t.Fatalf("create Session membership facts: %v", err)
	}
	var laneIDs []int64
	var firstLaneCreationID int64
	for _, change := range created.Msg.GetChanges() {
		if change.GetKind() == "CreateLane" {
			laneIDs = append(laneIDs, change.GetTargetId())
			if firstLaneCreationID == 0 {
				firstLaneCreationID = change.GetId()
			}
		}
	}
	if len(laneIDs) != 2 {
		t.Fatalf("created Lane IDs = %v", laneIDs)
	}
	baseRevision := created.Msg.GetDraftRevision()
	first, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "remove-membership-two", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{Id: sessionID,
			RemoveLanes: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Id{Id: laneIDs[0]}}},
			UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"remove_lanes"}},
		}},
	}))
	if err != nil {
		t.Fatalf("remove first independent membership: %v", err)
	}
	second, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "remove-membership-three", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{Id: sessionID,
			RemoveLanes: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Id{Id: laneIDs[1]}}},
			UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"remove_lanes"}},
		}},
	}))
	if err != nil || second.Msg.GetDraftRevision() != first.Msg.GetDraftRevision()+1 {
		t.Fatalf("independent stale membership edit = %+v, %v", second, err)
	}
	_, err = client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "repeat-membership-two", ExpectedDraftRevision: baseRevision,
		Sessions: []*rundownv1.SessionDraft{{Id: sessionID,
			RemoveLanes: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Id{Id: laneIDs[0]}}},
			UpdateMask:  &fieldmaskpb.FieldMask{Paths: []string{"remove_lanes"}},
		}},
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("same-membership stale edit = %v, want Aborted", err)
	}
	reverted, err := client.RevertDraftChange(t.Context(), connect.NewRequest(&rundownv1.RevertDraftChangeRequest{
		EventId: 1, CommandId: "revert-membership-two-removal", ExpectedDraftRevision: second.Msg.GetDraftRevision(),
		ChangeId: first.Msg.GetChanges()[0].GetId(),
	}))
	if err != nil {
		t.Fatalf("Revert membership removal: %v", err)
	}
	preview, err := client.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{reverted.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("Preview membership Revert: %v", err)
	}
	if !slices.Contains(preview.Msg.GetAutoIncludedChangeIds(), firstLaneCreationID) {
		t.Errorf("membership Revert auto-included changes = %v, want Lane creation %d", preview.Msg.GetAutoIncludedChangeIds(), firstLaneCreationID)
	}
	server.stop(t)
}

func TestOperatorStartsPublishedSessionDurably(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)

	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-keynote",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("Start Session RPC: %v", err)
	}
	state := started.Msg.GetState()
	if state.GetSessionId() != sessionID || state.GetSessionRunId() <= 0 ||
		state.GetLifecycle() != sessionv1.SessionLifecycle_SESSION_LIFECYCLE_LIVE ||
		state.GetLiveStateRevision() != 1 || state.GetActualStart() == nil || state.GetActualEnd() != nil {
		t.Errorf("started Session state = %+v", state)
	}
	wantActualStart := state.GetActualStart().AsTime().In(time.FixedZone("CEST", 2*60*60)).Format(time.RFC3339)
	public := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule")
	publicBody, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Live public Schedule: %v", err)
	}
	if public.StatusCode != http.StatusOK ||
		!strings.Contains(string(publicBody), `data-lifecycle="Live">Live</span>`) ||
		!strings.Contains(string(publicBody), wantActualStart) {
		t.Errorf("Live public Schedule = %d %q", public.StatusCode, publicBody)
	}

	dataDir := server.dataDir
	bin := server.bin
	server.stop(t)
	restarted := startBeamers(t, bin, dataDir)
	deepLink := get(t, authenticatedClient(t), restarted.address, fmt.Sprintf("/events/beamconf-2099/schedule/sessions/%d", sessionID))
	deepLinkBody, readErr := io.ReadAll(deepLink.Body)
	closeErr = deepLink.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read restarted Live public Session: %v", err)
	}
	if deepLink.StatusCode != http.StatusOK || !strings.Contains(string(deepLinkBody), "Status: Live") ||
		!strings.Contains(string(deepLinkBody), wantActualStart) {
		t.Errorf("restarted Live public Session = %d %q", deepLink.StatusCode, deepLinkBody)
	}
	restarted.stop(t)
}

func TestOperatorCancelsScheduledSessionWithPublicMessage(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)
	crewNotes := strings.Repeat("n", 1001)
	request := &sessionv1.CancelSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "cancel-keynote",
		ExpectedLiveStateRevision: proto.Int64(0),
		Confirmed:                 true,
		PublicCancellationMessage: "Speaker travel was disrupted.",
		CrewNotes:                 crewNotes,
	}

	unconfirmedMessage := proto.Clone(request)
	unconfirmed, ok := unconfirmedMessage.(*sessionv1.CancelSessionRequest)
	if !ok {
		t.Fatalf("cloned Cancel Session request type = %T", unconfirmedMessage)
	}
	unconfirmed.CommandId = "reject-unconfirmed-cancel"
	unconfirmed.Confirmed = false
	for range 2 {
		_, unconfirmedErr := client.CancelSession(
			t.Context(), connect.NewRequest(unconfirmed),
		)
		if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
			t.Fatalf("unconfirmed Cancel Session = %v", unconfirmedErr)
		}
	}
	invalidTextMessage := proto.Clone(request)
	invalidText, ok := invalidTextMessage.(*sessionv1.CancelSessionRequest)
	if !ok {
		t.Fatalf("cloned Cancel Session text request type = %T", invalidTextMessage)
	}
	invalidText.CommandId = "reject-cancellation-text"
	invalidText.PublicCancellationMessage = strings.Repeat("x", 10001)
	for range 2 {
		_, invalidTextErr := client.CancelSession(
			t.Context(), connect.NewRequest(invalidText),
		)
		if connect.CodeOf(invalidTextErr) != connect.CodeInvalidArgument {
			t.Fatalf("invalid Cancel Session text = %v", invalidTextErr)
		}
	}
	audits, _ := readAuditHistory(t, administrator, server.address)
	rejections := map[string]int{
		"cancel_confirmation_required": 1,
		"cancellation_text_invalid":    1,
	}
	for _, entry := range audits {
		if entry.Action == "CancelSession" &&
			entry.TargetType == "Session" &&
			entry.TargetID == strconv.FormatInt(sessionID, 10) &&
			entry.Outcome == "Rejected" {
			rejections[entry.Reason]--
		}
	}
	for reason, remaining := range rejections {
		if remaining != 0 {
			t.Fatalf("Cancel Session rejection %q remaining = %d", reason, remaining)
		}
	}
	canceled, err := client.CancelSession(t.Context(), connect.NewRequest(request))
	if err != nil {
		t.Fatalf("Cancel Session RPC: %v", err)
	}
	if canceled.Msg.GetState().GetLifecycle() !=
		sessionv1.SessionLifecycle_SESSION_LIFECYCLE_CANCELED ||
		canceled.Msg.GetState().GetLiveStateRevision() != 1 ||
		canceled.Msg.GetState().GetSessionRunId() != 0 {
		t.Fatalf("canceled Session state = %+v", canceled.Msg.GetState())
	}
	retried, err := client.CancelSession(t.Context(), connect.NewRequest(request))
	if err != nil || !proto.Equal(retried.Msg, canceled.Msg) {
		t.Fatalf("exact Cancel Session retry = %+v, %v", retried, err)
	}
	public := get(
		t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule",
	)
	body, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read canceled public Session: %v", joinedErr)
	}
	if public.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `data-lifecycle="Canceled">Canceled</span>`) ||
		!strings.Contains(string(body), "Speaker travel was disrupted.") ||
		strings.Contains(string(body), crewNotes) {
		t.Fatalf("canceled public Session = %d %q", public.StatusCode, body)
	}
	history, err := client.GetSessionHistory(t.Context(), connect.NewRequest(
		&sessionv1.GetSessionHistoryRequest{EventId: 1, SessionId: sessionID},
	))
	if err != nil || len(history.Msg.GetCancellations()) != 1 ||
		history.Msg.GetCancellations()[0].SessionRunId != nil ||
		history.Msg.GetCancellations()[0].GetPublicCancellationMessage() !=
			"Speaker travel was disrupted." ||
		history.Msg.GetCancellations()[0].GetCrewNotes() != crewNotes {
		t.Fatalf("cancellation history = %+v, %v", history, err)
	}
	server.stop(t)
}

func TestProducerReinstatesCanceledLiveSessionFromPlacementPreview(t *testing.T) {
	producer, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, producer, server)
	locationID, laneID := addPlacementLane(t, producer, server)
	operator := provisionOperator(t, producer, server)
	operatorClient := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)
	started, err := operatorClient.StartSession(t.Context(), connect.NewRequest(
		&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "start-before-cancel",
			ExpectedLiveStateRevision: proto.Int64(0),
		},
	))
	if err != nil {
		t.Fatalf("Start Session before cancellation: %v", err)
	}
	canceled, err := operatorClient.CancelSession(t.Context(), connect.NewRequest(
		&sessionv1.CancelSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "cancel-live-keynote",
			ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
			Confirmed:                 true,
		},
	))
	if err != nil {
		t.Fatalf("Cancel Live Session: %v", err)
	}
	if canceled.Msg.GetState().GetActualEnd() == nil {
		t.Fatalf("canceled Live Session has no Actual End: %+v", canceled.Msg.GetState())
	}
	canceledPublic := get(
		t, authenticatedClient(t), server.address,
		fmt.Sprintf("/events/beamconf-2099/schedule/sessions/%d", sessionID),
	)
	canceledBody, readErr := io.ReadAll(canceledPublic.Body)
	closeErr := canceledPublic.Body.Close()
	if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
		t.Fatalf("read canceled Live Session: %v", joinedErr)
	}
	if canceledPublic.StatusCode != http.StatusOK ||
		!strings.Contains(string(canceledBody), "Status: Canceled") ||
		!strings.Contains(string(canceledBody), "Canceled.") {
		t.Fatalf(
			"canceled Live public Session = %d %q",
			canceledPublic.StatusCode, canceledBody,
		)
	}

	producerClient := connectClient(sessionv1connect.NewSessionControlServiceClient, producer, server.address)
	proposedStart := time.Date(2099, 8, 21, 9, 30, 0, 0, time.UTC)
	hardPreview, err := producerClient.PreviewReinstateSession(
		t.Context(), connect.NewRequest(&sessionv1.PreviewReinstateSessionRequest{
			EventId: 1, SessionId: sessionID, ForecastStart: timestamppb.New(proposedStart),
			LaneIds: []int64{1}, LocationIds: []int64{1},
		}),
	)
	if err != nil || !hardPreview.Msg.GetRequiresHardBoundaryConfirmation() ||
		len(hardPreview.Msg.GetEffects()) < 2 {
		t.Fatalf("Hard placement preview = %+v, %v", hardPreview, err)
	}
	_, missingHardErr := producerClient.ReinstateSession(
		t.Context(), connect.NewRequest(&sessionv1.ReinstateSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "reject-hard-reinstatement",
			ExpectedLiveStateRevision: new(canceled.Msg.GetState().GetLiveStateRevision()),
			ForecastStart:             timestamppb.New(proposedStart),
			LaneIds:                   []int64{1},
			LocationIds:               []int64{1},
			PreviewFingerprint:        hardPreview.Msg.GetPreviewFingerprint(),
			Confirmed:                 true,
		}),
	)
	if connect.CodeOf(missingHardErr) != connect.CodeFailedPrecondition {
		t.Fatalf("Reinstate without Hard confirmation = %v", missingHardErr)
	}
	previewRequest := &sessionv1.PreviewReinstateSessionRequest{
		EventId: 1, SessionId: sessionID, ForecastStart: timestamppb.New(proposedStart),
		LaneIds: []int64{laneID}, LocationIds: []int64{locationID},
	}
	preview, err := producerClient.PreviewReinstateSession(
		t.Context(), connect.NewRequest(previewRequest),
	)
	if err != nil {
		t.Fatalf("Preview Reinstate Session: %v", err)
	}
	if preview.Msg.GetPreviewFingerprint() == "" ||
		preview.Msg.GetRequiresHardBoundaryConfirmation() ||
		!slices.Equal(preview.Msg.GetCurrentLaneIds(), []int64{1}) ||
		!slices.Equal(preview.Msg.GetProposedLaneIds(), []int64{laneID}) ||
		!slices.Equal(preview.Msg.GetCurrentLocationIds(), []int64{1}) ||
		!slices.Equal(preview.Msg.GetProposedLocationIds(), []int64{locationID}) ||
		len(preview.Msg.GetEffects()) != 1 ||
		len(preview.Msg.GetChanges()) != 1 ||
		preview.Msg.GetChanges()[0].GetSessionId() != sessionID {
		t.Fatalf("Reinstate Session preview = %+v", preview.Msg)
	}
	request := &sessionv1.ReinstateSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "reinstate-keynote",
		ExpectedLiveStateRevision: new(canceled.Msg.GetState().GetLiveStateRevision()),
		ForecastStart:             timestamppb.New(proposedStart),
		LaneIds:                   []int64{laneID},
		LocationIds:               []int64{locationID},
		PreviewFingerprint:        preview.Msg.GetPreviewFingerprint(),
	}
	_, unconfirmedErr := producerClient.ReinstateSession(
		t.Context(), connect.NewRequest(request),
	)
	if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed Reinstate Session = %v", unconfirmedErr)
	}
	request.Confirmed = true
	request.CommandId = "reinstate-keynote-confirmed"
	databasePath := filepath.Join(server.dataDir, "beamers.db")
	if fixtureErr := storetest.FailSessionForecastUpdate(
		t.Context(), databasePath, sessionID,
	); fixtureErr != nil {
		t.Fatalf("install Reinstate Session rollback fixture: %v", fixtureErr)
	}
	_, rollbackErr := producerClient.ReinstateSession(
		t.Context(), connect.NewRequest(request),
	)
	if connect.CodeOf(rollbackErr) != connect.CodeInternal {
		t.Fatalf("forced Reinstate Session rollback = %v", rollbackErr)
	}
	if fixtureErr := storetest.AllowSessionForecastUpdates(
		t.Context(), databasePath,
	); fixtureErr != nil {
		t.Fatalf("remove Reinstate Session rollback fixture: %v", fixtureErr)
	}
	afterRollback, err := producerClient.PreviewReinstateSession(
		t.Context(), connect.NewRequest(previewRequest),
	)
	if err != nil ||
		afterRollback.Msg.GetPreviewFingerprint() != preview.Msg.GetPreviewFingerprint() {
		t.Fatalf("Reinstate Session rollback changed placement = %+v, %v", afterRollback, err)
	}
	reinstated, err := producerClient.ReinstateSession(
		t.Context(), connect.NewRequest(request),
	)
	if err != nil {
		t.Fatalf("Reinstate Session: %v", err)
	}
	if reinstated.Msg.GetState().GetLifecycle() !=
		sessionv1.SessionLifecycle_SESSION_LIFECYCLE_SCHEDULED ||
		reinstated.Msg.GetState().GetLiveStateRevision() != 3 ||
		!reinstated.Msg.GetPreviousForecastStart().AsTime().Equal(
			started.Msg.GetState().GetActualStart().AsTime(),
		) {
		t.Fatalf("reinstated Session = %+v", reinstated.Msg)
	}
	retried, err := producerClient.ReinstateSession(
		t.Context(), connect.NewRequest(request),
	)
	if err != nil || !proto.Equal(retried.Msg, reinstated.Msg) {
		t.Fatalf("exact Reinstate Session retry = %+v, %v", retried, err)
	}
	history, err := producerClient.GetSessionHistory(t.Context(), connect.NewRequest(
		&sessionv1.GetSessionHistoryRequest{EventId: 1, SessionId: sessionID},
	))
	if err != nil || len(history.Msg.GetRuns()) != 1 ||
		history.Msg.GetRuns()[0].GetActualEnd() == nil ||
		history.Msg.GetRuns()[0].GetOutcome() !=
			sessionv1.SessionRunOutcome_SESSION_RUN_OUTCOME_CANCELED ||
		len(history.Msg.GetCancellations()) != 1 ||
		history.Msg.GetCancellations()[0].GetSessionRunId() !=
			history.Msg.GetRuns()[0].GetId() {
		t.Fatalf("reinstated Session history = %+v, %v", history, err)
	}
	public := get(
		t, authenticatedClient(t), server.address,
		fmt.Sprintf("/events/beamconf-2099/schedule/sessions/%d", sessionID),
	)
	body, readErr := io.ReadAll(public.Body)
	closeErr = public.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read reinstated public Session: %v", err)
	}
	if public.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), "Status: Scheduled") ||
		!strings.Contains(string(body), "Forecast Start:") ||
		!strings.Contains(string(body), "Location: Side Hall") ||
		!strings.Contains(string(body), "Lane: Side Lane") ||
		strings.Contains(string(body), "Actual Start:") ||
		strings.Contains(string(body), "Actual End:") ||
		!strings.Contains(
			string(body),
			proposedStart.
				In(time.FixedZone("CEST", 2*60*60)).
				Format(publictime.DateTimeLayout),
		) {
		t.Fatalf("reinstated public Session = %d %q", public.StatusCode, body)
	}
	_, oldLaneScopeErr := operatorClient.StartSession(
		t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
			EventId: 1, SessionId: sessionID, CommandId: "reject-old-lane-scope",
			ExpectedLiveStateRevision: new(reinstated.Msg.GetState().GetLiveStateRevision()),
		}),
	)
	if connect.CodeOf(oldLaneScopeErr) != connect.CodePermissionDenied {
		t.Fatalf("old Lane scope after reinstatement = %v", oldLaneScopeErr)
	}
	server.stop(t)
}

func TestOperatorPreviewsAndAdjustsLiveSessionTarget(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	rippleSessionID := addSoftRippleSession(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)
	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-before-target-adjustment",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("Start Session before Adjust Target: %v", err)
	}
	customPreview, err := client.PreviewAdjustTarget(t.Context(), connect.NewRequest(
		&sessionv1.PreviewAdjustTargetRequest{
			EventId: 1, SessionId: sessionID,
			Adjustment: &sessionv1.PreviewAdjustTargetRequest_Custom{
				Custom: durationpb.New(-2 * time.Minute),
			},
		},
	))
	if err != nil || !customPreview.Msg.GetProposedTarget().AsTime().Equal(
		customPreview.Msg.GetCurrentTarget().AsTime().Add(-2*time.Minute),
	) {
		t.Fatalf("custom Adjust Target preview = %+v, %v", customPreview, err)
	}
	_, unknownPresetErr := client.PreviewAdjustTarget(t.Context(), connect.NewRequest(
		&sessionv1.PreviewAdjustTargetRequest{
			EventId: 1, SessionId: sessionID,
			Adjustment: &sessionv1.PreviewAdjustTargetRequest_Preset{
				Preset: durationpb.New(7 * time.Minute),
			},
		},
	))
	if connect.CodeOf(unknownPresetErr) != connect.CodeInvalidArgument {
		t.Fatalf("unknown Adjust Target preset error = %v, want InvalidArgument", unknownPresetErr)
	}
	previewRequest := &sessionv1.PreviewAdjustTargetRequest{
		EventId: 1, SessionId: sessionID,
		Adjustment: &sessionv1.PreviewAdjustTargetRequest_Preset{
			Preset: durationpb.New(5 * time.Minute),
		},
	}
	preview, err := client.PreviewAdjustTarget(t.Context(), connect.NewRequest(previewRequest))
	if err != nil {
		t.Fatalf("Preview Adjust Target: %v", err)
	}
	if preview.Msg.GetPreviewFingerprint() == "" ||
		!preview.Msg.GetProposedTarget().AsTime().Equal(preview.Msg.GetCurrentTarget().AsTime().Add(5*time.Minute)) ||
		len(preview.Msg.GetConfiguredPresets()) != 3 ||
		!preview.Msg.GetRequiresHardBoundaryConfirmation() ||
		len(preview.Msg.GetEffects()) != 1 ||
		preview.Msg.GetEffects()[0].GetSessionId() != rippleSessionID ||
		!preview.Msg.GetEffects()[0].GetProposedForecastStart().AsTime().Equal(
			time.Date(2099, 8, 21, 9, 5, 0, 0, time.UTC),
		) ||
		!preview.Msg.GetEffects()[0].GetProposedForecastEnd().AsTime().Equal(
			time.Date(2099, 8, 21, 10, 0, 0, 0, time.UTC),
		) {
		t.Fatalf("Adjust Target preview = %+v", preview.Msg)
	}
	targetRequest := func(
		commandID string,
		expectedRevision int64,
		fingerprint string,
		confirmed bool,
		hardBoundaryConfirmed bool,
	) *sessionv1.AdjustTargetRequest {
		return &sessionv1.AdjustTargetRequest{
			EventId: 1, SessionId: sessionID, CommandId: commandID,
			ExpectedLiveStateRevision: new(expectedRevision),
			Adjustment: &sessionv1.AdjustTargetRequest_Preset{
				Preset: durationpb.New(5 * time.Minute),
			},
			PreviewFingerprint: fingerprint, Confirmed: confirmed,
			HardBoundaryConfirmed: hardBoundaryConfirmed,
		}
	}
	unconfirmed := targetRequest(
		"reject-unconfirmed-target", started.Msg.GetState().GetLiveStateRevision(),
		preview.Msg.GetPreviewFingerprint(), false, false,
	)
	_, unconfirmedErr := client.AdjustTarget(t.Context(), connect.NewRequest(unconfirmed))
	if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed Adjust Target error = %v, want FailedPrecondition", unconfirmedErr)
	}
	missingHardConfirmation := targetRequest(
		"reject-unconfirmed-hard-boundary", started.Msg.GetState().GetLiveStateRevision(),
		preview.Msg.GetPreviewFingerprint(), true, false,
	)
	_, missingHardErr := client.AdjustTarget(t.Context(), connect.NewRequest(missingHardConfirmation))
	if connect.CodeOf(missingHardErr) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed Hard Boundary error = %v, want FailedPrecondition", missingHardErr)
	}
	corrected, err := client.CorrectLiveDetails(t.Context(), connect.NewRequest(
		&sessionv1.CorrectLiveDetailsRequest{
			EventId: 1, SessionId: sessionID, CommandId: "correct-before-stale-target",
			ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
			Confirmed:                 true, Title: "Target-adjusted Keynote",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		},
	))
	if err != nil || corrected.Msg.GetState().GetLiveStateRevision() != 2 {
		t.Fatalf("Live correction before stale Adjust Target = %+v, %v", corrected, err)
	}
	staleRequest := targetRequest(
		"reject-stale-target-preview", started.Msg.GetState().GetLiveStateRevision(),
		preview.Msg.GetPreviewFingerprint(), true, true,
	)
	_, staleErr := client.AdjustTarget(t.Context(), connect.NewRequest(staleRequest))
	if connect.CodeOf(staleErr) != connect.CodeAborted {
		t.Fatalf("stale Adjust Target preview error = %v, want Aborted", staleErr)
	}
	freshPreview, err := client.PreviewAdjustTarget(t.Context(), connect.NewRequest(previewRequest))
	if err != nil {
		t.Fatalf("refresh Adjust Target preview: %v", err)
	}
	revisionConflict := targetRequest(
		"reject-target-revision-conflict", started.Msg.GetState().GetLiveStateRevision(),
		freshPreview.Msg.GetPreviewFingerprint(), true, true,
	)
	_, revisionErr := client.AdjustTarget(t.Context(), connect.NewRequest(revisionConflict))
	if connect.CodeOf(revisionErr) != connect.CodeAborted {
		t.Fatalf("Adjust Target revision error = %v, want Aborted", revisionErr)
	}
	databasePath := filepath.Join(server.dataDir, "beamers.db")
	if fixtureErr := storetest.FailSessionRunUpdates(t.Context(), databasePath); fixtureErr != nil {
		t.Fatalf("install Adjust Target rollback fixture: %v", fixtureErr)
	}
	rollbackRequest := targetRequest(
		"force-target-rollback", corrected.Msg.GetState().GetLiveStateRevision(),
		freshPreview.Msg.GetPreviewFingerprint(), true, true,
	)
	_, rollbackErr := client.AdjustTarget(t.Context(), connect.NewRequest(rollbackRequest))
	if connect.CodeOf(rollbackErr) != connect.CodeInternal {
		t.Fatalf("forced Adjust Target rollback error = %v, want Internal", rollbackErr)
	}
	if fixtureErr := storetest.AllowSessionRunUpdates(t.Context(), databasePath); fixtureErr != nil {
		t.Fatalf("remove Adjust Target rollback fixture: %v", fixtureErr)
	}
	request := targetRequest(
		"adjust-keynote-target", corrected.Msg.GetState().GetLiveStateRevision(),
		freshPreview.Msg.GetPreviewFingerprint(), true, true,
	)
	adjusted, err := client.AdjustTarget(t.Context(), connect.NewRequest(request))
	if err != nil {
		t.Fatalf("Adjust Target: %v", err)
	}
	if adjusted.Msg.GetState().GetLiveStateRevision() != 3 ||
		!adjusted.Msg.GetForecastEnd().AsTime().Equal(freshPreview.Msg.GetProposedTarget().AsTime()) ||
		len(adjusted.Msg.GetChanges()) != 2 {
		t.Errorf("adjusted target = %+v", adjusted.Msg)
	}
	retried, err := client.AdjustTarget(t.Context(), connect.NewRequest(request))
	if err != nil || !retried.Msg.GetForecastEnd().AsTime().Equal(adjusted.Msg.GetForecastEnd().AsTime()) {
		t.Fatalf("exact Adjust Target retry = %+v, %v", retried, err)
	}
	listing := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule")
	body, readErr := io.ReadAll(listing.Body)
	closeErr := listing.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read adjusted public Schedule: %v", err)
	}
	if !strings.Contains(string(body), freshPreview.Msg.GetProposedTarget().AsTime().
		In(time.FixedZone("CEST", 2*60*60)).Format(time.RFC3339)) {
		t.Errorf("adjusted public Schedule missing Forecast End: %s", body)
	}
	server.stop(t)
}

func TestOperatorPullsForwardOnlyAfterExplicitEndAndPreview(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	rippleSessionID := addSoftRippleSession(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)
	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-before-pull-forward",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("Start Session before Pull Forward: %v", err)
	}
	ended, err := client.EndSession(t.Context(), connect.NewRequest(&sessionv1.EndSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "end-before-pull-forward",
		ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
	}))
	if err != nil {
		t.Fatalf("End Session before Pull Forward: %v", err)
	}
	preview, err := client.PreviewPullForward(t.Context(), connect.NewRequest(
		&sessionv1.PreviewPullForwardRequest{EventId: 1, SessionId: sessionID},
	))
	if err != nil {
		t.Fatalf("Preview Pull Forward: %v", err)
	}
	if preview.Msg.GetPreviewFingerprint() == "" || len(preview.Msg.GetChanges()) != 1 ||
		len(preview.Msg.GetEffects()) != 1 ||
		preview.Msg.GetChanges()[0].GetSessionId() != rippleSessionID ||
		!preview.Msg.GetEffects()[0].GetCurrentForecastStart().AsTime().Equal(
			time.Date(2099, 8, 21, 9, 0, 0, 0, time.UTC),
		) ||
		!preview.Msg.GetChanges()[0].GetForecastStart().AsTime().Equal(
			ended.Msg.GetState().GetActualEnd().AsTime(),
		) {
		t.Fatalf("Pull Forward preview = %+v", preview.Msg)
	}
	unconfirmed := &sessionv1.PullForwardRequest{
		EventId: 1, SessionId: sessionID, CommandId: "reject-unconfirmed-pull-forward",
		ExpectedLiveStateRevision: new(ended.Msg.GetState().GetLiveStateRevision()),
		PreviewFingerprint:        preview.Msg.GetPreviewFingerprint(),
	}
	_, unconfirmedErr := client.PullForward(t.Context(), connect.NewRequest(unconfirmed))
	if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
		t.Fatalf("unconfirmed Pull Forward error = %v, want FailedPrecondition", unconfirmedErr)
	}
	databasePath := filepath.Join(server.dataDir, "beamers.db")
	if fixtureErr := storetest.FailSessionForecastUpdate(
		t.Context(), databasePath, rippleSessionID,
	); fixtureErr != nil {
		t.Fatalf("install Pull Forward rollback fixture: %v", fixtureErr)
	}
	request := &sessionv1.PullForwardRequest{
		EventId: 1, SessionId: sessionID, CommandId: "pull-forward-after-end",
		ExpectedLiveStateRevision: new(ended.Msg.GetState().GetLiveStateRevision()),
		PreviewFingerprint:        preview.Msg.GetPreviewFingerprint(), Confirmed: true,
	}
	_, rollbackErr := client.PullForward(t.Context(), connect.NewRequest(request))
	if connect.CodeOf(rollbackErr) != connect.CodeInternal {
		t.Fatalf("forced Pull Forward rollback error = %v, want Internal", rollbackErr)
	}
	if fixtureErr := storetest.AllowSessionForecastUpdates(
		t.Context(), databasePath,
	); fixtureErr != nil {
		t.Fatalf("remove Pull Forward rollback fixture: %v", fixtureErr)
	}
	afterRollback, err := client.PreviewPullForward(t.Context(), connect.NewRequest(
		&sessionv1.PreviewPullForwardRequest{EventId: 1, SessionId: sessionID},
	))
	if err != nil ||
		afterRollback.Msg.GetPreviewFingerprint() != preview.Msg.GetPreviewFingerprint() {
		t.Fatalf("Pull Forward rollback changed timing = %+v, %v", afterRollback, err)
	}
	pulled, err := client.PullForward(t.Context(), connect.NewRequest(request))
	if err != nil {
		t.Fatalf(
			"Pull Forward with expected revision %d: %v",
			request.GetExpectedLiveStateRevision(), err,
		)
	}
	if pulled.Msg.GetState().GetLifecycle() !=
		sessionv1.SessionLifecycle_SESSION_LIFECYCLE_ENDED ||
		pulled.Msg.GetState().GetLiveStateRevision() !=
			ended.Msg.GetState().GetLiveStateRevision() ||
		len(pulled.Msg.GetChanges()) != 1 ||
		pulled.Msg.GetChanges()[0].GetSessionId() != rippleSessionID {
		t.Fatalf("Pull Forward result = %+v", pulled.Msg)
	}
	retried, err := client.PullForward(t.Context(), connect.NewRequest(request))
	if err != nil || !proto.Equal(retried.Msg, pulled.Msg) {
		t.Fatalf("exact Pull Forward retry = %+v, %v", retried, err)
	}
	server.stop(t)
}

func TestOperatorCorrectsLiveDetailsWithoutRewritingRunSnapshot(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)
	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-before-detail-correction",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("Start Session before Live Detail Correction: %v", err)
	}
	rundownClient := connectClient(rundownv1connect.NewRundownServiceClient, administrator, server.address)
	current, err := rundownClient.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Get Rundown before conflicting Draft edit: %v", err)
	}
	pending, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "draft-title-before-live-correction", ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Pending Draft Title",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if err != nil {
		t.Fatalf("Edit Draft before Live Detail Correction: %v", err)
	}
	request := &sessionv1.CorrectLiveDetailsRequest{
		EventId: 1, SessionId: sessionID, CommandId: "correct-live-details",
		ExpectedLiveStateRevision: new(started.Msg.GetState().GetLiveStateRevision()),
		Confirmed:                 true, Title: "Corrected Keynote", Speaker: "Avery Speaker",
		PublicDetails: "Corrected public description",
		UpdateMask:    &fieldmaskpb.FieldMask{Paths: []string{"title", "speaker", "public_details"}},
	}
	corrected, err := client.CorrectLiveDetails(t.Context(), connect.NewRequest(request))
	if err != nil {
		t.Fatalf("Correct Live Details RPC: %v", err)
	}
	if corrected.Msg.GetState().GetLiveStateRevision() != 2 || corrected.Msg.GetAmendmentId() <= 0 {
		t.Errorf("corrected Live state = %+v, amendment %d", corrected.Msg.GetState(), corrected.Msg.GetAmendmentId())
	}
	retried, err := client.CorrectLiveDetails(t.Context(), connect.NewRequest(request))
	if err != nil || retried.Msg.GetAmendmentId() != corrected.Msg.GetAmendmentId() {
		t.Fatalf("exact Live Detail Correction retry = %+v, %v", retried, err)
	}
	unconfirmedRequest := &sessionv1.CorrectLiveDetailsRequest{
		EventId: 1, SessionId: sessionID, CommandId: "unconfirmed-live-details",
		ExpectedLiveStateRevision: proto.Int64(2), Title: "Must Not Apply",
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
	}
	for range 2 {
		_, unconfirmedErr := client.CorrectLiveDetails(
			t.Context(), connect.NewRequest(unconfirmedRequest),
		)
		if connect.CodeOf(unconfirmedErr) != connect.CodeFailedPrecondition {
			t.Errorf("unconfirmed Live Detail Correction error = %v, want FailedPrecondition", unconfirmedErr)
		}
	}
	broadCorrectionRequest := &sessionv1.CorrectLiveDetailsRequest{
		EventId: 1, SessionId: sessionID, CommandId: "reject-broad-live-correction",
		ExpectedLiveStateRevision: proto.Int64(2), Confirmed: true,
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"crew_notes"}},
	}
	for range 2 {
		_, broadCorrectionErr := client.CorrectLiveDetails(
			t.Context(), connect.NewRequest(broadCorrectionRequest),
		)
		if connect.CodeOf(broadCorrectionErr) != connect.CodeInvalidArgument {
			t.Errorf("broad Live Detail Correction error = %v, want InvalidArgument", broadCorrectionErr)
		}
	}
	emptyCorrectionRequest := &sessionv1.CorrectLiveDetailsRequest{
		EventId: 1, SessionId: sessionID, CommandId: "reject-empty-live-correction",
		ExpectedLiveStateRevision: proto.Int64(2), Confirmed: true,
	}
	for range 2 {
		_, emptyCorrectionErr := client.CorrectLiveDetails(
			t.Context(), connect.NewRequest(emptyCorrectionRequest),
		)
		if connect.CodeOf(emptyCorrectionErr) != connect.CodeInvalidArgument {
			t.Errorf(
				"empty Live Detail Correction error = %v, want InvalidArgument",
				emptyCorrectionErr,
			)
		}
	}

	public := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule")
	publicBody, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if combinedErr := errors.Join(readErr, closeErr); combinedErr != nil {
		t.Fatalf("read corrected public Schedule: %v", combinedErr)
	}
	for _, expected := range []string{"Corrected Keynote", "Avery Speaker", "Corrected public description"} {
		if !strings.Contains(string(publicBody), expected) {
			t.Errorf("corrected public Schedule missing %q: %s", expected, publicBody)
		}
	}

	if _, endErr := client.EndSession(t.Context(), connect.NewRequest(&sessionv1.EndSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "end-after-live-detail-correction",
		ExpectedLiveStateRevision: proto.Int64(2),
	})); endErr != nil {
		t.Fatalf("End Session after Live Detail Correction: %v", endErr)
	}
	conflictPreview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{pending.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("Preview Draft conflict after Live Detail Correction: %v", err)
	}
	if len(conflictPreview.Msg.GetValidationFailures()) != 1 ||
		!strings.Contains(conflictPreview.Msg.GetValidationFailures()[0], "live detail correction") {
		t.Errorf("corrected fact Draft conflict = %v", conflictPreview.Msg.GetValidationFailures())
	}
	afterCorrection, err := rundownClient.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Get Rundown after Live Detail Correction: %v", err)
	}
	reviewed, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "review-corrected-title", ExpectedDraftRevision: afterCorrection.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Reviewed Corrected Keynote",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if err != nil {
		t.Fatalf("review corrected Draft fact: %v", err)
	}
	reviewedPreview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{reviewed.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil || len(reviewedPreview.Msg.GetValidationFailures()) != 0 {
		t.Fatalf("Preview reviewed correction = %+v, %v", reviewedPreview, err)
	}
	if _, publishErr := rundownClient.Publish(t.Context(), connect.NewRequest(&rundownv1.PublishRequest{
		EventId: 1, CommandId: "publish-reviewed-correction",
		Confirmation: &rundownv1.PublishConfirmation{
			DraftRevision: reviewedPreview.Msg.GetDraftRevision(), PublishedRevision: reviewedPreview.Msg.GetPublishedRevision(),
			ChangeIds: reviewedPreview.Msg.GetChangeIds(), Fingerprint: reviewedPreview.Msg.GetFingerprint(),
		},
	})); publishErr != nil {
		t.Fatalf("Publish reviewed correction: %v", publishErr)
	}
	deepLink := get(t, authenticatedClient(t), server.address, fmt.Sprintf("/events/beamconf-2099/schedule/sessions/%d", sessionID))
	deepLinkBody, readErr := io.ReadAll(deepLink.Body)
	closeErr = deepLink.Body.Close()
	if combinedErr := errors.Join(readErr, closeErr); combinedErr != nil {
		t.Fatalf("read reviewed corrected Session: %v", combinedErr)
	}
	if !strings.Contains(string(deepLinkBody), "Reviewed Corrected Keynote") || strings.Contains(string(deepLinkBody), ">Corrected Keynote<") {
		t.Errorf("reviewed corrected Session = %s", deepLinkBody)
	}

	history, err := client.GetSessionHistory(t.Context(), connect.NewRequest(&sessionv1.GetSessionHistoryRequest{
		EventId: 1, SessionId: sessionID,
	}))
	if err != nil {
		t.Fatalf("Get Session history RPC: %v", err)
	}
	if len(history.Msg.GetRuns()) != 1 {
		t.Fatalf("Session Run history = %+v, want one Run", history.Msg.GetRuns())
	}
	run := history.Msg.GetRuns()[0]
	if run.GetSnapshot().GetTitle() != "Opening Keynote" || run.GetSnapshot().GetSpeaker() != "Original Speaker" ||
		run.GetSnapshot().GetPublishedRevision() != 1 ||
		run.GetSnapshot().GetType() != rundownv1.SessionType_SESSION_TYPE_PRESENTATION ||
		run.GetSnapshot().GetTimingPolicy() != rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END ||
		run.GetSnapshot().GetStartBoundary() != rundownv1.Boundary_BOUNDARY_HARD ||
		run.GetSnapshot().GetEndBoundary() != rundownv1.Boundary_BOUNDARY_HARD ||
		run.GetSnapshot().GetMinimumDuration().AsDuration() != 30*time.Minute ||
		len(run.GetSnapshot().GetLaneIds()) != 1 || len(run.GetSnapshot().GetLocationIds()) != 1 ||
		len(run.GetAmendments()) != 1 || run.GetAmendments()[0].GetId() != corrected.Msg.GetAmendmentId() ||
		run.GetAmendments()[0].GetDetails().GetTitle() != "Corrected Keynote" {
		t.Errorf("Session Run immutable history = %+v", run)
	}
	audits, _ := readAuditHistory(t, administrator, server.address)
	correctionAudits := 0
	rejectedCorrectionReasons := map[string]int{
		"live_detail_confirmation_required": 1,
		"live_detail_fields_invalid":        2,
	}
	for _, entry := range audits {
		if entry.Action != "CorrectLiveDetails" ||
			entry.TargetType != "Session" ||
			entry.TargetID != strconv.FormatInt(sessionID, 10) {
			continue
		}
		switch entry.Outcome {
		case "Succeeded":
			correctionAudits++
		case "Rejected":
			rejectedCorrectionReasons[entry.Reason]--
		}
	}
	if correctionAudits != 1 {
		t.Errorf("successful Live Detail Correction Audit Entries = %d, want 1", correctionAudits)
	}
	for reason, remaining := range rejectedCorrectionReasons {
		if remaining != 0 {
			t.Errorf("Live Detail Correction rejection %q remaining = %d", reason, remaining)
		}
	}
	server.stop(t)
}

func TestOrdinaryPublishDoesNotAlterLiveSession(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	sessionClient := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)
	if _, err := sessionClient.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-before-blocked-publish",
		ExpectedLiveStateRevision: proto.Int64(0),
	})); err != nil {
		t.Fatalf("Start Session before blocked Publish: %v", err)
	}
	rundownClient := connectClient(rundownv1connect.NewRundownServiceClient, administrator, server.address)
	current, err := rundownClient.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Get current Rundown before Live edit: %v", err)
	}
	edited, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "edit-live-session-title", ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Id: sessionID, Title: "Draft Must Not Reach Live",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"title"}},
		}},
	}))
	if err != nil {
		t.Fatalf("Edit currently Live Session Draft: %v", err)
	}
	preview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{edited.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("Preview currently Live Session Publish: %v", err)
	}
	if len(preview.Msg.GetValidationFailures()) != 1 ||
		!strings.Contains(preview.Msg.GetValidationFailures()[0], "currently Live Session") {
		t.Errorf("Live Session Publish validation = %v", preview.Msg.GetValidationFailures())
	}
	structuralEdit, err := rundownClient.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "rename-live-session-lane", ExpectedDraftRevision: edited.Msg.GetDraftRevision(),
		Lanes: []*rundownv1.LaneDraft{{
			Id: current.Msg.GetLanes()[0].GetId(), Name: "Renamed Live Lane",
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		}},
	}))
	if err != nil {
		t.Fatalf("Edit Live Session Lane Draft: %v", err)
	}
	structuralPreview, err := rundownClient.PublishPreview(t.Context(), connect.NewRequest(&rundownv1.PublishPreviewRequest{
		EventId: 1, ChangeIds: []int64{structuralEdit.Msg.GetChanges()[0].GetId()},
	}))
	if err != nil {
		t.Fatalf("Preview Live Session Lane Publish: %v", err)
	}
	if len(structuralPreview.Msg.GetValidationFailures()) != 1 ||
		!strings.Contains(structuralPreview.Msg.GetValidationFailures()[0], "currently Live Session") {
		t.Errorf("Live Session Lane Publish validation = %v", structuralPreview.Msg.GetValidationFailures())
	}
	public := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule")
	publicBody, readErr := io.ReadAll(public.Body)
	closeErr := public.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Schedule after blocked Live Publish: %v", err)
	}
	if !strings.Contains(string(publicBody), "Opening Keynote") || strings.Contains(string(publicBody), "Draft Must Not Reach Live") {
		t.Errorf("public Schedule after blocked Live Publish = %s", publicBody)
	}
	server.stop(t)
}

func TestProducerDeletesOnlyNeverPublishedDraftSession(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	publishedSessionID := prepareActiveSchedule(t, administrator, server)
	client := connectClient(rundownv1connect.NewRundownServiceClient, administrator, server.address)
	current, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil || len(current.Msg.GetLanes()) == 0 || len(current.Msg.GetLocations()) == 0 {
		t.Fatalf("Get Rundown for Draft Session deletion = %+v, %v", current, err)
	}
	created, err := client.EditDraft(t.Context(), connect.NewRequest(&rundownv1.EditDraftRequest{
		EventId: 1, CommandId: "create-disposable-draft-session", ExpectedDraftRevision: current.Msg.GetDraftRevision(),
		Sessions: []*rundownv1.SessionDraft{{
			Ref: "disposable", Title: "Disposable Draft Session",
			Type:               rundownv1.SessionType_SESSION_TYPE_PRESENTATION,
			AudienceVisibility: rundownv1.AudienceVisibility_AUDIENCE_VISIBILITY_PUBLIC,
			PlannedStart:       timestamppb.New(time.Date(2099, 8, 21, 11, 0, 0, 0, time.UTC)),
			PlannedEnd:         timestamppb.New(time.Date(2099, 8, 21, 12, 0, 0, 0, time.UTC)),
			TimingPolicy:       rundownv1.TimingPolicy_TIMING_POLICY_FIXED_END,
			MinimumDuration:    durationpb.New(30 * time.Minute),
			StartBoundary:      rundownv1.Boundary_BOUNDARY_SOFT, EndBoundary: rundownv1.Boundary_BOUNDARY_SOFT,
			Lanes:     []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Id{Id: current.Msg.GetLanes()[0].GetId()}}},
			Locations: []*rundownv1.TargetRef{{Target: &rundownv1.TargetRef_Id{Id: current.Msg.GetLocations()[0].GetId()}}},
		}},
	}))
	if err != nil {
		t.Fatalf("Create disposable Draft Session: %v", err)
	}
	var draftSessionID int64
	for _, change := range created.Msg.GetChanges() {
		if change.GetKind() == "CreateSession" {
			draftSessionID = change.GetTargetId()
		}
	}
	request := &rundownv1.DeleteDraftSessionRequest{
		EventId: 1, SessionId: draftSessionID, CommandId: "delete-disposable-draft-session",
		ExpectedDraftRevision: created.Msg.GetDraftRevision(),
	}
	deleted, err := client.DeleteDraftSession(t.Context(), connect.NewRequest(request))
	if err != nil || deleted.Msg.GetSessionId() != draftSessionID ||
		deleted.Msg.GetDraftRevision() != created.Msg.GetDraftRevision()+1 {
		t.Fatalf("Delete Draft Session = %+v, %v", deleted, err)
	}
	retried, err := client.DeleteDraftSession(t.Context(), connect.NewRequest(request))
	if err != nil || retried.Msg.GetDraftRevision() != deleted.Msg.GetDraftRevision() {
		t.Fatalf("exact Delete Draft Session retry = %+v, %v", retried, err)
	}
	_, publishedErr := client.DeleteDraftSession(t.Context(), connect.NewRequest(&rundownv1.DeleteDraftSessionRequest{
		EventId: 1, SessionId: publishedSessionID, CommandId: "reject-published-session-deletion",
		ExpectedDraftRevision: deleted.Msg.GetDraftRevision(),
	}))
	if connect.CodeOf(publishedErr) != connect.CodeFailedPrecondition {
		t.Errorf("Published Session deletion error = %v, want FailedPrecondition", publishedErr)
	}
	server.stop(t)
}

func TestProducerImportsCSVAsReviewedDraftProposals(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	client := connectClient(rundownv1connect.NewRundownServiceClient, administrator, server.address)
	mappings := []*rundownv1.CSVFieldMapping{
		{SourceColumn: "key", TargetField: "external_key"},
		{SourceColumn: "title", TargetField: "title"},
		{SourceColumn: "speaker", TargetField: "speaker"},
		{SourceColumn: "details", TargetField: "public_details"},
		{SourceColumn: "start", TargetField: "planned_start"},
		{SourceColumn: "end", TargetField: "planned_end"},
		{SourceColumn: "lane", TargetField: "lane"},
	}
	csvData := []byte("key,title,speaker,details,start,end,lane,vendor_only\n" +
		"fosdem-1,Imported Session,Ada Speaker,Imported public details,2099-08-21 12:00,2099-08-21 13:00,Main Lane,ignored\n")
	preview, err := client.PreviewCSVImport(t.Context(), connect.NewRequest(&rundownv1.PreviewCSVImportRequest{
		EventId: 1, CsvData: csvData, Mappings: mappings,
	}))
	if err != nil {
		t.Fatalf("Preview CSV Import: %v", err)
	}
	if len(preview.Msg.GetValidationFailures()) != 0 || len(preview.Msg.GetProposals()) != 1 ||
		preview.Msg.GetProposals()[0].GetClassification() != "Addition" ||
		len(preview.Msg.GetIgnoredFields()) != 1 || preview.Msg.GetIgnoredFields()[0] != "vendor_only" {
		t.Fatalf("CSV Import Preview = %+v", preview.Msg)
	}
	request := &rundownv1.ImportCSVRequest{
		EventId: 1, CommandId: "import-csv-session", ExpectedDraftRevision: preview.Msg.GetDraftRevision(),
		CsvData: csvData, Mappings: mappings, Fingerprint: preview.Msg.GetFingerprint(),
		ProposalIds: []string{preview.Msg.GetProposals()[0].GetId()},
	}
	imported, err := client.ImportCSV(t.Context(), connect.NewRequest(request))
	if err != nil || len(imported.Msg.GetChanges()) != 1 || imported.Msg.GetChanges()[0].GetKind() != "CreateSession" {
		t.Fatalf("Import CSV = %+v, %v", imported, err)
	}
	retried, err := client.ImportCSV(t.Context(), connect.NewRequest(request))
	if err != nil || retried.Msg.GetDraftRevision() != imported.Msg.GetDraftRevision() {
		t.Fatalf("exact CSV Import retry = %+v, %v", retried, err)
	}
	published, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Get Published Rundown after CSV Import: %v", err)
	}
	for _, session := range published.Msg.GetSessions() {
		if session.GetTitle() == "Imported Session" {
			t.Error("CSV Import mutated Published state")
		}
	}

	repeatData := []byte("key,title,details\nfosdem-1,Reviewed Imported Session,Updated imported details\n")
	repeatMappings := []*rundownv1.CSVFieldMapping{
		{SourceColumn: "key", TargetField: "external_key"},
		{SourceColumn: "title", TargetField: "title"},
		{SourceColumn: "details", TargetField: "public_details"},
	}
	repeat, err := client.PreviewCSVImport(t.Context(), connect.NewRequest(&rundownv1.PreviewCSVImportRequest{
		EventId: 1, CsvData: repeatData, Mappings: repeatMappings,
	}))
	if err != nil || len(repeat.Msg.GetProposals()) != 2 {
		t.Fatalf("Preview repeat CSV Import = %+v, %v", repeat, err)
	}
	selected := []string{}
	for _, proposal := range repeat.Msg.GetProposals() {
		if proposal.GetClassification() != "Update" {
			t.Errorf("repeat CSV proposal = %+v, want Update", proposal)
		}
		if proposal.GetField() == "title" {
			selected = append(selected, proposal.GetId())
		}
	}
	updated, err := client.ImportCSV(t.Context(), connect.NewRequest(&rundownv1.ImportCSVRequest{
		EventId: 1, CommandId: "import-reviewed-csv-title", ExpectedDraftRevision: repeat.Msg.GetDraftRevision(),
		CsvData: repeatData, Mappings: repeatMappings, Fingerprint: repeat.Msg.GetFingerprint(), ProposalIds: selected,
	}))
	if err != nil || len(updated.Msg.GetChanges()) != 1 || updated.Msg.GetChanges()[0].GetFactKey() != "title" {
		t.Fatalf("Apply reviewed CSV field = %+v, %v", updated, err)
	}

	duplicate, err := client.PreviewCSVImport(t.Context(), connect.NewRequest(&rundownv1.PreviewCSVImportRequest{
		EventId: 1, CsvData: []byte("key\nduplicate\nduplicate\n"),
		Mappings: []*rundownv1.CSVFieldMapping{{SourceColumn: "key", TargetField: "external_key"}},
	}))
	if err != nil || len(duplicate.Msg.GetValidationFailures()) == 0 ||
		!strings.Contains(duplicate.Msg.GetValidationFailures()[0], "duplicate Import Reference") {
		t.Errorf("duplicate CSV Import References = %+v, %v", duplicate, err)
	}
	unsafe, err := client.PreviewCSVImport(t.Context(), connect.NewRequest(&rundownv1.PreviewCSVImportRequest{
		EventId: 1, CsvData: []byte("key,notes\nunsafe,secret\n"),
		Mappings: []*rundownv1.CSVFieldMapping{
			{SourceColumn: "key", TargetField: "external_key"},
			{SourceColumn: "notes", TargetField: "crew_notes"},
		},
	}))
	if err != nil || len(unsafe.Msg.GetValidationFailures()) == 0 ||
		!strings.Contains(strings.Join(unsafe.Msg.GetValidationFailures(), " "), "cannot target crew_notes") {
		t.Errorf("unsafe CSV target = %+v, %v", unsafe, err)
	}
	_, malformedErr := client.PreviewCSVImport(t.Context(), connect.NewRequest(&rundownv1.PreviewCSVImportRequest{
		EventId: 1, CsvData: []byte("key,title\nmalformed,\"unterminated\n"),
		Mappings: []*rundownv1.CSVFieldMapping{
			{SourceColumn: "key", TargetField: "external_key"},
			{SourceColumn: "title", TargetField: "title"},
		},
	}))
	if connect.CodeOf(malformedErr) != connect.CodeInvalidArgument {
		t.Errorf("malformed CSV error = %v, want InvalidArgument", malformedErr)
	}
	server.stop(t)
}

func TestProducerImportsICalendarWithEventTimeReview(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	prepareActiveSchedule(t, administrator, server)
	client := connectClient(rundownv1connect.NewRundownServiceClient, administrator, server.address)
	calendar := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"X-WR-TIMEZONE:Europe/Berlin\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:fosdem-style-1\r\n" +
		"DTSTART;TZID=Europe/Berlin:20990821T140000\r\n" +
		"DTEND;TZID=Europe/Berlin:20990821T150000\r\n" +
		"SUMMARY:Imported Café & λ\r\n" +
		"DESCRIPTION:Line one\\nline two\r\n" +
		"LOCATION:Main Lane\r\n" +
		"CATEGORIES:General\r\n" +
		"URL:https://fosdem.org/example\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	preview, err := client.PreviewICalendarImport(t.Context(), connect.NewRequest(&rundownv1.PreviewICalendarImportRequest{
		EventId: 1, IcalendarData: calendar,
	}))
	if err != nil || len(preview.Msg.GetProposals()) != 1 ||
		preview.Msg.GetProposals()[0].GetClassification() != "Addition" ||
		!slices.Contains(preview.Msg.GetUnsupportedFields(), "URL") || len(preview.Msg.GetAppliedDefaults()) == 0 {
		t.Fatalf("Preview iCalendar Import = %+v, %v", preview, err)
	}
	request := &rundownv1.ImportICalendarRequest{
		EventId: 1, CommandId: "import-icalendar-session",
		ExpectedDraftRevision: preview.Msg.GetDraftRevision(), IcalendarData: calendar,
		Fingerprint: preview.Msg.GetFingerprint(), ProposalIds: []string{preview.Msg.GetProposals()[0].GetId()},
	}
	imported, err := client.ImportICalendar(t.Context(), connect.NewRequest(request))
	if err != nil || len(imported.Msg.GetChanges()) != 1 || imported.Msg.GetChanges()[0].GetKind() != "CreateSession" {
		t.Fatalf("Import iCalendar = %+v, %v", imported, err)
	}
	retried, err := client.ImportICalendar(t.Context(), connect.NewRequest(request))
	if err != nil || retried.Msg.GetDraftRevision() != imported.Msg.GetDraftRevision() {
		t.Fatalf("exact iCalendar Import retry = %+v, %v", retried, err)
	}
	published, err := client.GetCrewRundown(t.Context(), connect.NewRequest(&rundownv1.GetCrewRundownRequest{EventId: 1}))
	if err != nil {
		t.Fatalf("Get Published Rundown after iCalendar Import: %v", err)
	}
	for _, session := range published.Msg.GetSessions() {
		if session.GetTitle() == "Imported Café & λ" {
			t.Error("iCalendar Import mutated Published state")
		}
	}

	ambiguous := []byte("BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:ambiguous\n" +
		"DTSTART;TZID=Europe/Berlin:20261025T023000\nDTEND;TZID=Europe/Berlin:20261025T034500\n" +
		"SUMMARY:Repeated hour\nLOCATION:Main Lane\nEND:VEVENT\nEND:VCALENDAR\n")
	unresolved, err := client.PreviewICalendarImport(t.Context(), connect.NewRequest(&rundownv1.PreviewICalendarImportRequest{
		EventId: 1, IcalendarData: ambiguous,
	}))
	if err != nil || len(unresolved.Msg.GetProposals()) != 1 ||
		unresolved.Msg.GetProposals()[0].GetClassification() != "Unresolved" ||
		!strings.Contains(unresolved.Msg.GetProposals()[0].GetMessage(), "choose Earlier or Later") {
		t.Fatalf("ambiguous iCalendar preview = %+v, %v", unresolved, err)
	}
	resolved, err := client.PreviewICalendarImport(t.Context(), connect.NewRequest(&rundownv1.PreviewICalendarImportRequest{
		EventId: 1, IcalendarData: ambiguous,
		Choices: []*rundownv1.ICalendarOccurrenceChoice{{
			Uid: "ambiguous", Property: "DTSTART", Occurrence: "Later",
		}},
	}))
	if err != nil || len(resolved.Msg.GetProposals()) != 1 || resolved.Msg.GetProposals()[0].GetClassification() != "Addition" {
		t.Fatalf("resolved repeated-hour preview = %+v, %v", resolved, err)
	}

	nonexistent := []byte("BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:nonexistent\n" +
		"DTSTART;TZID=Europe/Berlin:20260329T023000\nDTEND;TZID=Europe/Berlin:20260329T034500\n" +
		"SUMMARY:Missing hour\nLOCATION:Main Lane\nEND:VEVENT\nEND:VCALENDAR\n")
	blocked, err := client.PreviewICalendarImport(t.Context(), connect.NewRequest(&rundownv1.PreviewICalendarImportRequest{
		EventId: 1, IcalendarData: nonexistent,
	}))
	if err != nil || len(blocked.Msg.GetProposals()) != 1 ||
		blocked.Msg.GetProposals()[0].GetClassification() != "Unresolved" ||
		!strings.Contains(blocked.Msg.GetProposals()[0].GetMessage(), "does not exist") {
		t.Fatalf("nonexistent-time iCalendar preview = %+v, %v", blocked, err)
	}
	server.stop(t)
}

func TestOperatorEndsLiveSessionWithoutMovingLaterSessions(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)
	started, err := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "start-keynote-before-end",
		ExpectedLiveStateRevision: proto.Int64(0),
	}))
	if err != nil {
		t.Fatalf("Start Session before End: %v", err)
	}
	ended, err := client.EndSession(t.Context(), connect.NewRequest(&sessionv1.EndSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "end-keynote",
		ExpectedLiveStateRevision: proto.Int64(1),
	}))
	if err != nil {
		t.Fatalf("End Session RPC: %v", err)
	}
	state := ended.Msg.GetState()
	if state.GetSessionId() != sessionID || state.GetSessionRunId() != started.Msg.GetState().GetSessionRunId() ||
		state.GetLifecycle() != sessionv1.SessionLifecycle_SESSION_LIFECYCLE_ENDED ||
		state.GetLiveStateRevision() != 2 || state.GetActualEnd() == nil ||
		state.GetActualEnd().AsTime().Before(state.GetActualStart().AsTime()) {
		t.Errorf("ended Session state = %+v", state)
	}

	listing := get(t, authenticatedClient(t), server.address, "/events/beamconf-2099/schedule")
	listingBody, readErr := io.ReadAll(listing.Body)
	closeErr := listing.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read Schedule after End: %v", err)
	}
	if listing.StatusCode != http.StatusOK || strings.Contains(string(listingBody), "Opening Keynote") ||
		!strings.Contains(string(listingBody), "Closing Session") ||
		!strings.Contains(string(listingBody), "2099-08-21T12:00:00+02:00") {
		t.Errorf("Schedule after End = %d %q", listing.StatusCode, listingBody)
	}
	deepLink := get(t, authenticatedClient(t), server.address, fmt.Sprintf("/events/beamconf-2099/schedule/sessions/%d", sessionID))
	deepLinkBody, readErr := io.ReadAll(deepLink.Body)
	closeErr = deepLink.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("read ended Session deep link: %v", err)
	}
	if deepLink.StatusCode != http.StatusOK || !strings.Contains(string(deepLinkBody), "Status: Ended") ||
		!strings.Contains(string(deepLinkBody), "Actual End:") {
		t.Errorf("ended Session deep link = %d %q", deepLink.StatusCode, deepLinkBody)
	}
	server.stop(t)
}

func TestSessionCommandsRejectStaleAndConflictingRetries(t *testing.T) {
	administrator, server := startAuthenticatedAdministrator(t)
	sessionID := prepareActiveSchedule(t, administrator, server)
	operator := provisionOperator(t, administrator, server)
	client := connectClient(sessionv1connect.NewSessionControlServiceClient, operator, server.address)
	_, missingRevisionErr := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "missing-live-state-revision",
	}))
	if connect.CodeOf(missingRevisionErr) != connect.CodeInvalidArgument {
		t.Fatalf("missing expected Live State Revision error = %v, want InvalidArgument", missingRevisionErr)
	}
	request := connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "idempotent-start",
		ExpectedLiveStateRevision: proto.Int64(0),
	})
	started, err := client.StartSession(t.Context(), request)
	if err != nil {
		t.Fatalf("first Start Session: %v", err)
	}
	retried, err := client.StartSession(t.Context(), connect.NewRequest(request.Msg))
	if err != nil {
		t.Fatalf("exact Start Session retry: %v", err)
	}
	if retried.Msg.GetState().GetSessionRunId() != started.Msg.GetState().GetSessionRunId() ||
		retried.Msg.GetState().GetLiveStateRevision() != 1 ||
		!retried.Msg.GetState().GetActualStart().AsTime().Equal(started.Msg.GetState().GetActualStart().AsTime()) {
		t.Errorf("exact retry = %+v, want original %+v", retried.Msg.GetState(), started.Msg.GetState())
	}

	staleRequest := &sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "stale-start",
		ExpectedLiveStateRevision: proto.Int64(0),
	}
	_, staleErr := client.StartSession(t.Context(), connect.NewRequest(staleRequest))
	if connect.CodeOf(staleErr) != connect.CodeAborted {
		t.Fatalf("stale Start error = %v, want Aborted", staleErr)
	}
	var staleConnectErr *connect.Error
	if !errors.As(staleErr, &staleConnectErr) {
		t.Fatalf("stale Start error type = %T", staleErr)
	}
	var current *sessionv1.SessionState
	for _, detail := range staleConnectErr.Details() {
		value, detailErr := detail.Value()
		if detailErr != nil {
			t.Fatalf("decode stale Start detail: %v", detailErr)
		}
		if state, ok := value.(*sessionv1.SessionState); ok {
			current = state
		}
	}
	if current == nil || current.GetSessionRunId() != started.Msg.GetState().GetSessionRunId() ||
		current.GetLifecycle() != sessionv1.SessionLifecycle_SESSION_LIFECYCLE_LIVE ||
		current.GetLiveStateRevision() != 1 {
		t.Errorf("stale Start current state = %+v", current)
	}
	_, staleRetryErr := client.StartSession(t.Context(), connect.NewRequest(staleRequest))
	var staleRetryConnectErr *connect.Error
	if connect.CodeOf(staleRetryErr) != connect.CodeAborted ||
		!errors.As(staleRetryErr, &staleRetryConnectErr) || len(staleRetryConnectErr.Details()) != 1 {
		t.Fatalf("stale Start retry error = %v, want original Aborted detail", staleRetryErr)
	}
	retriedDetail, detailErr := staleRetryConnectErr.Details()[0].Value()
	if detailErr != nil {
		t.Fatalf("decode stale Start retry detail: %v", detailErr)
	}
	retriedCurrent, ok := retriedDetail.(*sessionv1.SessionState)
	if !ok || retriedCurrent.GetSessionRunId() != current.GetSessionRunId() ||
		retriedCurrent.GetLiveStateRevision() != current.GetLiveStateRevision() {
		t.Errorf("stale Start retry detail = %+v, want original %+v", retriedCurrent, current)
	}

	_, conflictErr := client.StartSession(t.Context(), connect.NewRequest(&sessionv1.StartSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "idempotent-start",
		ExpectedLiveStateRevision: proto.Int64(1),
	}))
	if connect.CodeOf(conflictErr) != connect.CodeAlreadyExists {
		t.Errorf("conflicting Command ID error = %v, want AlreadyExists", conflictErr)
	}
	ended, err := client.EndSession(t.Context(), connect.NewRequest(&sessionv1.EndSessionRequest{
		EventId: 1, SessionId: sessionID, CommandId: "end-after-rejections",
		ExpectedLiveStateRevision: proto.Int64(1),
	}))
	if err != nil {
		t.Fatalf("End Session after rejected commands: %v", err)
	}
	if ended.Msg.GetState().GetSessionRunId() != started.Msg.GetState().GetSessionRunId() ||
		ended.Msg.GetState().GetLiveStateRevision() != 2 {
		t.Errorf("state mutated by rejected commands: %+v", ended.Msg.GetState())
	}
	server.stop(t)
}

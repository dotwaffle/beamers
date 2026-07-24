package results_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	_ "github.com/dotwaffle/beamers/ent/runtime"
	programv1 "github.com/dotwaffle/beamers/gen/beamers/program/v1"
	"github.com/dotwaffle/beamers/gen/beamers/program/v1/programv1connect"
	resultsv1 "github.com/dotwaffle/beamers/gen/beamers/results/v1"
	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/connectapi"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displaystream"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/programconnect"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/viewer"
)

func TestPrizegivingPublicCommandsPreflightAndPreview(t *testing.T) {
	storage, actor, eventID, _, _ := openPrizegivingApplicationTest(t)
	now := func() time.Time {
		return time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	}
	ceremonyID, competitionID := publishPrizegivingSessions(
		t,
		storage,
		actor,
		eventID,
		now,
	)
	service, err := results.New(storage, now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}
	if _, err = service.DesignatePrizegiving(
		t.Context(),
		actor,
		results.DesignatePrizegivingInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "designate-prizegiving",
		},
	); err != nil {
		t.Fatalf("designate Prizegiving: %v", err)
	}
	draft, err := service.Save(t.Context(), actor, results.SaveInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "save-results", Disposition: results.Publish,
		Score: results.ScorePolicy{Type: results.None},
	})
	if err != nil {
		t.Fatalf("save Results: %v", err)
	}
	draft, err = service.MarkReady(t.Context(), actor, results.MarkReadyInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "mark-results-ready", ExpectedRevision: draft.Revision,
	})
	if err != nil {
		t.Fatalf("mark Results Ready: %v", err)
	}
	item := results.ResultItem{
		Kind: results.ResultItemCompetition, CompetitionSessionID: competitionID,
		DisplayOrder: 1, RevealMethod: "UnknownMethod",
	}
	invalidInput := results.SavePrizegivingPlanInput{
		EventID: eventID, CeremonySessionID: ceremonyID,
		CommandID: "save-invalid-plan", CompetitionSessionIDs: []int{competitionID},
		Sequence: []results.ResultItem{item},
		PublicationOrder: []results.ResultItemRef{
			item.Ref(1),
		},
		Template: results.TextTemplate{
			Revision: 1, Source: "{{call .Command}}",
		},
	}
	invalidPlan, err := service.SavePrizegivingPlan(
		t.Context(),
		actor,
		invalidInput,
	)
	if err != nil {
		t.Fatalf("save editable invalid plan: %v", err)
	}
	revoked := actor
	revoked.EventRoles = nil
	replayed, err := service.SavePrizegivingPlan(
		t.Context(),
		actor,
		invalidInput,
	)
	if err != nil || replayed.Revision != invalidPlan.Revision {
		t.Fatalf("replay Prizegiving plan = %+v, %v", replayed, err)
	}
	conflict := invalidInput
	conflict.Template.Source = "{{.EventTitle}}\n"
	if _, err = service.SavePrizegivingPlan(
		t.Context(),
		actor,
		conflict,
	); !errors.Is(err, results.ErrCommandConflict) {
		t.Fatalf("conflicting Prizegiving command error = %v", err)
	}
	unauthorized := invalidInput
	unauthorized.CommandID = "unauthorized-plan"
	unauthorized.ExpectedRevision = invalidPlan.Revision
	if _, err = service.SavePrizegivingPlan(
		t.Context(),
		revoked,
		unauthorized,
	); !errors.Is(err, results.ErrProducerRequired) {
		t.Fatalf("unauthorized Prizegiving command error = %v", err)
	}
	blocked, err := service.RunPrizegivingPreflight(
		t.Context(),
		actor,
		results.RunPrizegivingPreflightInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "blocked-preflight", ExpectedRevision: invalidPlan.Revision,
		},
	)
	if !errors.Is(err, results.ErrPrizegivingPreflightBlocked) {
		t.Fatalf("blocked Preflight error = %v", err)
	}
	codes := make(map[string]bool, len(blocked.Findings))
	for _, finding := range blocked.Findings {
		codes[finding.Code] = true
	}
	if !codes["invalid_reveal_method"] || !codes["unsafe_results_template"] {
		t.Fatalf("blocked Preflight findings = %+v", blocked.Findings)
	}

	validPlan, err := service.SavePrizegivingPlan(
		t.Context(),
		actor,
		results.SavePrizegivingPlanInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "save-valid-plan", ExpectedRevision: invalidPlan.Revision,
			CompetitionSessionIDs: []int{competitionID},
			ReleasePolicy:         results.ResultsAllAtCue,
			Template: results.TextTemplate{
				Revision: 2, Source: "{{.EventTitle}}\n",
			},
		},
	)
	if err != nil {
		t.Fatalf("save valid Prizegiving plan: %v", err)
	}
	locked, err := service.RunPrizegivingPreflight(
		t.Context(),
		actor,
		results.RunPrizegivingPreflightInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "lock-preflight", ExpectedRevision: validPlan.Revision,
		},
	)
	if err != nil ||
		!locked.Plan.Locked ||
		locked.Plan.ReleasePolicy != results.ResultsAllAtCue ||
		locked.Plan.Lock.ReleasePolicy != results.ResultsAllAtCue {
		t.Fatalf("lock Prizegiving = %+v, %v", locked, err)
	}
	beforeCue, err := storage.LoadResultsPublication(
		t.Context(),
		eventID,
		store.ResultsPublicationPrizegiving,
		ceremonyID,
	)
	if err != nil || beforeCue.Revision != 0 {
		t.Fatalf("Results Publication before cue = %+v, %v", beforeCue, err)
	}
	released, err := service.FirePrizegivingResultsCue(
		t.Context(),
		actor,
		results.FirePrizegivingResultsCueInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "release-results-cue",
		},
	)
	if err != nil ||
		released.Revision != 1 ||
		released.Status != results.ResultsPublicationFinal ||
		len(released.Items) != len(locked.Plan.PublicationOrder) {
		t.Fatalf("fire Results release cue = %+v, %v", released, err)
	}
	replayedRelease, err := service.FirePrizegivingResultsCue(
		t.Context(),
		actor,
		results.FirePrizegivingResultsCueInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "release-results-cue",
		},
	)
	if err != nil || replayedRelease.Revision != released.Revision {
		t.Fatalf("replay Results release cue = %+v, %v", replayedRelease, err)
	}
	if _, err = service.Save(t.Context(), actor, results.SaveInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "save-later-results", ExpectedRevision: draft.Revision,
		Disposition: results.NoPublicResults, NoPublicReason: "withheld",
		Score: results.ScorePolicy{Type: results.None},
	}); err != nil {
		t.Fatalf("save later Results: %v", err)
	}
	preview, err := service.PreviewPrizegiving(
		t.Context(),
		actor,
		eventID,
		ceremonyID,
		results.PrizegivingPreviewModePreview,
	)
	if err != nil {
		t.Fatalf("Preview Prizegiving: %v", err)
	}
	if preview.Watermark == "" ||
		len(preview.CompetitionResults) != 1 ||
		preview.CompetitionResults[0].ID != draft.ID ||
		preview.CompetitionResults[0].Disposition != results.Publish {
		t.Fatalf("Prizegiving Preview = %+v", preview)
	}
}

func TestStandaloneResultsReleaseRequiresReadyUnassignedCompetition(t *testing.T) {
	storage, actor, eventID, _, _ := openPrizegivingApplicationTest(t)
	now := func() time.Time {
		return time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	}
	ceremonyID, competitionID := publishPrizegivingSessions(
		t,
		storage,
		actor,
		eventID,
		now,
	)
	service, err := results.New(storage, now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}
	draft, err := service.Save(t.Context(), actor, results.SaveInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "save-standalone-results", Disposition: results.Publish,
		Score: results.ScorePolicy{Type: results.None},
	})
	if err != nil {
		t.Fatalf("save standalone Results: %v", err)
	}
	if _, err = service.ReleaseStandaloneResults(
		t.Context(),
		actor,
		results.ReleaseStandaloneResultsInput{
			EventID: eventID, CompetitionSessionID: competitionID,
			CommandID: "release-unready-standalone-results",
		},
	); !errors.Is(err, results.ErrResultsPublicationRequired) {
		t.Fatalf("unready standalone Results release error = %v", err)
	}
	draft, err = service.MarkReady(t.Context(), actor, results.MarkReadyInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "ready-standalone-results", ExpectedRevision: draft.Revision,
	})
	if err != nil {
		t.Fatalf("mark standalone Results Ready: %v", err)
	}
	released, err := service.ReleaseStandaloneResults(
		t.Context(),
		actor,
		results.ReleaseStandaloneResultsInput{
			EventID: eventID, CompetitionSessionID: competitionID,
			CommandID: "release-standalone-results",
		},
	)
	if err != nil ||
		released.Revision != 1 ||
		released.Status != results.ResultsPublicationFinal ||
		len(released.Items) != 1 {
		t.Fatalf("release standalone Results = %+v, %v", released, err)
	}
	stored, err := storage.LoadResultsPublication(
		t.Context(),
		eventID,
		store.ResultsPublicationStandalone,
		competitionID,
	)
	if err != nil ||
		stored.Lock.ReleasePolicy != results.ResultsStandalone ||
		len(stored.Lock.CompetitionSources) != 1 ||
		stored.Lock.CompetitionSources[0].DraftID != draft.ID {
		t.Fatalf("stored standalone Results Publication = %+v, %v", stored, err)
	}
	if _, err = service.DesignatePrizegiving(
		t.Context(),
		actor,
		results.DesignatePrizegivingInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "designate-after-standalone-release",
		},
	); err != nil {
		t.Fatalf("designate Prizegiving after standalone release: %v", err)
	}
	if _, err = service.SavePrizegivingPlan(
		t.Context(),
		actor,
		results.SavePrizegivingPlanInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID:             "assign-published-standalone-results",
			CompetitionSessionIDs: []int{competitionID},
			Sequence: []results.ResultItem{{
				Kind: results.ResultItemCompetition, CompetitionSessionID: competitionID,
				DisplayOrder: 1, RevealMethod: results.RevealStatic,
			}},
			PublicationOrder: []results.ResultItemRef{{
				Kind: results.ResultItemCompetition, CompetitionSessionID: competitionID,
				DisplayOrder: 1,
			}},
			ReleasePolicy: results.ResultsProgressiveOnReveal,
			Template:      results.TextTemplate{Revision: 1, Source: "{{.EventTitle}}\n"},
		},
	); !errors.Is(err, results.ErrCompetitionPrizegivingAssignment) {
		t.Fatalf("assign published standalone Results error = %v", err)
	}
}

func TestPrizegivingPublicProgramControlRevealsLockedResult(t *testing.T) {
	storage, actor, eventID, authentication, sessionToken :=
		openPrizegivingApplicationTest(t)
	nowValue := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	now := func() time.Time { return nowValue }
	ceremonyID, competitionID := publishPrizegivingSessions(
		t,
		storage,
		actor,
		eventID,
		now,
	)
	resultsService, err := results.New(storage, now)
	if err != nil {
		t.Fatalf("create Results service: %v", err)
	}
	if _, err = resultsService.DesignatePrizegiving(
		t.Context(),
		actor,
		results.DesignatePrizegivingInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID: "designate-public-program-prizegiving",
		},
	); err != nil {
		t.Fatalf("designate Prizegiving: %v", err)
	}
	draft, err := resultsService.Save(t.Context(), actor, results.SaveInput{
		EventID: eventID, SessionID: competitionID,
		CommandID: "save-public-program-results", Disposition: results.Publish,
		Score: results.ScorePolicy{Type: results.None},
	})
	if err != nil {
		t.Fatalf("save Results: %v", err)
	}
	draft, err = resultsService.SaveCompetitionAwards(
		t.Context(),
		actor,
		results.SaveCompetitionAwardsInput{
			EventID: eventID, SessionID: competitionID,
			CommandID:        "save-public-program-award",
			ExpectedRevision: draft.Revision,
			Awards: []results.Award{{
				Key: "jury", Name: "Jury Award", Promoted: true,
				DisplayOrder: 1,
				Recipients: []results.AwardRecipient{{
					DisplayName: "Team One",
				}},
			}},
		},
	)
	if err != nil {
		t.Fatalf("save promoted Competition Award: %v", err)
	}
	if _, err = resultsService.MarkReady(
		t.Context(),
		actor,
		results.MarkReadyInput{
			EventID: eventID, SessionID: competitionID,
			CommandID:        "ready-public-program-results",
			ExpectedRevision: draft.Revision,
		},
	); err != nil {
		t.Fatalf("mark Results Ready: %v", err)
	}
	item := results.ResultItem{
		Kind: results.ResultItemCompetition, CompetitionSessionID: competitionID,
		DisplayOrder: 1, RevealMethod: results.RevealSequentialPodium,
	}
	awardItem := results.ResultItem{
		Kind:                 results.ResultItemCompetitionAward,
		CompetitionSessionID: competitionID,
		AwardKey:             "jury",
		DisplayOrder:         2,
		RevealMethod:         results.RevealStatic,
	}
	plan, err := resultsService.SavePrizegivingPlan(
		t.Context(),
		actor,
		results.SavePrizegivingPlanInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID:             "save-public-program-plan",
			CompetitionSessionIDs: []int{competitionID},
			Sequence:              []results.ResultItem{item, awardItem},
			PublicationOrder: []results.ResultItemRef{
				item.Ref(1),
				awardItem.Ref(2),
			},
			Template: results.TextTemplate{
				Revision: 1, Source: "{{.EventTitle}}\n",
			},
		},
	)
	if err != nil {
		t.Fatalf("save Prizegiving plan: %v", err)
	}
	if _, err = resultsService.RunPrizegivingPreflight(
		t.Context(),
		actor,
		results.RunPrizegivingPreflightInput{
			EventID: eventID, CeremonySessionID: ceremonyID,
			CommandID:        "lock-public-program-plan",
			ExpectedRevision: plan.Revision,
		},
	); err != nil {
		t.Fatalf("lock Prizegiving plan: %v", err)
	}
	activationService, err := activation.New(storage, now)
	if err != nil {
		t.Fatalf("create Activation service: %v", err)
	}
	preflight, err := activationService.Preflight(t.Context(), actor, eventID)
	if err != nil {
		t.Fatalf("preflight Event activation: %v", err)
	}
	if _, err = activationService.Activate(
		t.Context(),
		actor,
		activation.ActivateInput{
			EventID: eventID, CommandID: "activate-public-program-event",
			Confirmation: preflight.Confirmation,
		},
	); err != nil {
		t.Fatalf("activate Event: %v", err)
	}
	sessionService, err := sessioncontrol.New(storage, now)
	if err != nil {
		t.Fatalf("create Session control: %v", err)
	}
	started, err := sessionService.Start(
		t.Context(),
		actor,
		sessioncontrol.StartInput{
			EventID: eventID, SessionID: ceremonyID,
			CommandID: "start-public-program-prizegiving",
		},
	)
	if err != nil {
		t.Fatalf("start Prizegiving: %v", err)
	}
	programService, err := programcontrol.New(storage, now)
	if err != nil {
		t.Fatalf("create Program control: %v", err)
	}
	claimed, err := programService.Control(
		t.Context(),
		actor,
		programcontrol.ControlInput{
			EventID: eventID, SessionID: ceremonyID,
			Action: programcontrol.ControlClaim, CommandID: "claim-public-program",
		},
	)
	if err != nil {
		t.Fatalf("claim Program Channel: %v", err)
	}
	taken, err := programService.Take(t.Context(), actor, programcontrol.TakeInput{
		EventID: eventID, SessionID: ceremonyID,
		CommandID:               "take-public-program-result",
		ExpectedRevision:        claimed.Channel.Revision,
		ExpectedControlRevision: claimed.ControlRevision,
		Item:                    claimed.Preview,
	})
	if err != nil {
		t.Fatalf("Take Result: %v", err)
	}
	if !taken.Committed ||
		taken.State.Channel.Output.Result == nil ||
		taken.State.Channel.Output.Result.Status != "Taken" ||
		taken.State.Channel.Output.Result.Release != "Held" ||
		taken.State.Preview.Result == nil ||
		taken.State.Preview.Result.Ref.AwardKey != "jury" {
		t.Fatalf("Taken Program Result = %+v", taken)
	}
	if _, err = programService.Take(
		t.Context(),
		actor,
		programcontrol.TakeInput{
			EventID: eventID, SessionID: ceremonyID,
			CommandID:               "premature-second-result",
			ExpectedRevision:        taken.State.Channel.Revision,
			ExpectedControlRevision: taken.State.ControlRevision,
			Item:                    taken.State.Preview,
		},
	); !errors.Is(err, programcontrol.ErrResultRevealRunning) {
		t.Fatalf("premature second Result Take error = %v", err)
	}
	invokeResult, displayStream, programStream := newResultRPCInvoker(
		t,
		storage,
		programService,
		authentication,
		sessionToken,
		now,
	)
	resultItemMessage := &programv1.ProgramItem{
		Kind: programv1.ProgramItemKind_PROGRAM_ITEM_KIND_RESULT,
		Result: &programv1.ProgramResult{
			Item: &resultsv1.ResultItemRef{
				Kind:                 resultsv1.ResultItemKind_RESULT_ITEM_KIND_COMPETITION_RESULTS,
				CompetitionSessionId: int64(competitionID),
				DisplayOrder:         1,
			},
		},
	}
	revealing, err := invokeResult(&programv1.ActOnResultRequest{
		EventId: int64(eventID), SessionId: int64(ceremonyID),
		CommandId: "reveal-public-program-result",
		Action:    programv1.ResultAction_RESULT_ACTION_REVEAL,
		Item:      resultItemMessage,
		ExpectedProgramRevision: int64(
			taken.State.Channel.Revision,
		),
		ExpectedControlStateRevision: int64(
			taken.State.ControlRevision,
		),
	})
	if err != nil {
		t.Fatalf("Reveal Result RPC: %v", err)
	}
	revealingResult := revealing.GetChannel().GetProgramOutput().GetResult()
	if revealingResult.GetStatus() !=
		programv1.ResultStageStatus_RESULT_STAGE_STATUS_REVEALING ||
		revealingResult.GetRelease() !=
			programv1.ResultReleaseState_RESULT_RELEASE_STATE_HELD ||
		revealingResult.GetRevealDuration().AsDuration() != 3*time.Second ||
		displayStream.Cursor().Position != 1 ||
		programStream.Cursor().Position != 1 {
		t.Fatalf("Revealing Program Result RPC = %+v", revealing)
	}
	if _, retryErr := invokeResult(&programv1.ActOnResultRequest{
		EventId: int64(eventID), SessionId: int64(ceremonyID),
		CommandId:                    "reject-second-reveal",
		Action:                       programv1.ResultAction_RESULT_ACTION_REVEAL,
		Item:                         resultItemMessage,
		ExpectedProgramRevision:      revealing.GetChannel().GetLiveStateRevision(),
		ExpectedControlStateRevision: revealing.GetChannel().GetControlStateRevision(),
	}); connect.CodeOf(retryErr) != connect.CodeFailedPrecondition {
		t.Fatalf("second Reveal RPC error = %v", retryErr)
	}
	if displayStream.Cursor().Position != 1 ||
		programStream.Cursor().Position != 1 {
		t.Fatalf(
			"rejected Reveal notifications = display %d, program %d",
			displayStream.Cursor().Position,
			programStream.Cursor().Position,
		)
	}
	beforeElapsed, err := storage.LoadResultsPublication(
		t.Context(),
		eventID,
		store.ResultsPublicationPrizegiving,
		ceremonyID,
	)
	if err != nil || beforeElapsed.Revision != 0 {
		t.Fatalf("Publication before elapsed Reveal = %+v, %v", beforeElapsed, err)
	}
	nowValue = nowValue.Add(3 * time.Second)
	revealed, err := programService.Current(
		t.Context(),
		actor,
		eventID,
		ceremonyID,
	)
	if err != nil {
		t.Fatalf("restore elapsed Result Reveal: %v", err)
	}
	if revealed.Channel.Output.Result.Status != "Revealed" ||
		revealed.Channel.Output.Result.Release != "Ready" ||
		revealed.Channel.Output.Result.CompetitionResults.ID != draft.ID {
		t.Fatalf("restored Program Result = %+v", revealed)
	}
	progressive, err := storage.LoadResultsPublication(
		t.Context(),
		eventID,
		store.ResultsPublicationPrizegiving,
		ceremonyID,
	)
	if err != nil ||
		progressive.Revision != 1 ||
		progressive.Status != store.ResultsPublicationPartial ||
		len(progressive.Items) != 1 ||
		progressive.Items[0].Kind != "CompetitionResults" {
		t.Fatalf("Progressive Publication after Reveal = %+v, %v", progressive, err)
	}
	replayed, err := programService.ActOnResult(
		t.Context(),
		actor,
		programcontrol.ResultActionInput{
			EventID: eventID, SessionID: ceremonyID,
			CommandID:               "replay-public-program-result",
			Action:                  programcontrol.ResultReplayReveal,
			Item:                    revealed.Channel.Output,
			ExpectedProgramRevision: revealed.Channel.Revision,
			ExpectedControlRevision: revealed.ControlRevision,
		},
	)
	if err != nil {
		t.Fatalf("Replay Result: %v", err)
	}
	if !replayed.Committed ||
		!replayed.State.Channel.Output.Result.Replay ||
		replayed.State.Channel.Output.Result.RevealSeed !=
			revealed.Channel.Output.Result.RevealSeed {
		t.Fatalf("Replayed Program Result = %+v", replayed)
	}
	secondTaken, err := programService.Take(
		t.Context(),
		actor,
		programcontrol.TakeInput{
			EventID: eventID, SessionID: ceremonyID,
			CommandID:               "take-second-public-program-result",
			ExpectedRevision:        replayed.State.Channel.Revision,
			ExpectedControlRevision: replayed.State.ControlRevision,
			Item:                    replayed.State.Preview,
		},
	)
	if err != nil ||
		secondTaken.State.Channel.Output.Result == nil ||
		secondTaken.State.Channel.Output.Result.Ref.AwardKey != "jury" {
		t.Fatalf("Take second Result after resolution = %+v, %v", secondTaken, err)
	}
	secondRevealed, err := programService.ActOnResult(
		t.Context(),
		actor,
		programcontrol.ResultActionInput{
			EventID: eventID, SessionID: ceremonyID,
			CommandID:               "reveal-second-public-program-result",
			Action:                  programcontrol.ResultReveal,
			Item:                    secondTaken.State.Channel.Output,
			ExpectedProgramRevision: secondTaken.State.Channel.Revision,
			ExpectedControlRevision: secondTaken.State.ControlRevision,
		},
	)
	if err != nil ||
		secondRevealed.State.Channel.Output.Result.Status != "Revealed" {
		t.Fatalf("Reveal second static Result = %+v, %v", secondRevealed, err)
	}
	progressive, err = storage.LoadResultsPublication(
		t.Context(),
		eventID,
		store.ResultsPublicationPrizegiving,
		ceremonyID,
	)
	if err != nil ||
		progressive.Revision != 2 ||
		progressive.Status != store.ResultsPublicationPartial ||
		len(progressive.Items) != 2 {
		t.Fatalf("Progressive Publication after static Reveal = %+v, %v", progressive, err)
	}
	ended, err := sessionService.End(
		t.Context(),
		actor,
		sessioncontrol.EndInput{
			EventID: eventID, SessionID: ceremonyID,
			CommandID:                 "end-public-program-prizegiving",
			ExpectedLiveStateRevision: started.LiveStateRevision,
		},
	)
	if err != nil || ended.Lifecycle != "Ended" {
		t.Fatalf("end resolved Prizegiving = %+v, %v", ended, err)
	}
	finalPublication, err := storage.LoadResultsPublication(
		t.Context(),
		eventID,
		store.ResultsPublicationPrizegiving,
		ceremonyID,
	)
	if err != nil ||
		finalPublication.Revision != 3 ||
		finalPublication.Status != store.ResultsPublicationFinal ||
		len(finalPublication.Items) != 2 {
		t.Fatalf("Publication after Ceremony End = %+v, %v", finalPublication, err)
	}
}

func newResultRPCInvoker(
	t *testing.T,
	storage *store.SQLite,
	programService *programcontrol.Service,
	authentication *auth.Service,
	sessionToken string,
	now func() time.Time,
) (
	func(*programv1.ActOnResultRequest) (*programv1.ActOnResultResponse, error),
	*displaystream.Hub,
	*displaystream.Hub,
) {
	t.Helper()
	displayConfig := displays.DefaultConfig()
	displayConfig.Now = now
	displayService, err := displays.New(storage, displayConfig)
	if err != nil {
		t.Fatalf("create Display service: %v", err)
	}
	displayStream, err := displaystream.New("result-rpc-display", 1)
	if err != nil {
		t.Fatalf("create Display stream: %v", err)
	}
	programStream, err := displaystream.New("result-rpc-program", 1)
	if err != nil {
		t.Fatalf("create Program stream: %v", err)
	}
	handler, err := programconnect.NewHandler(
		programService,
		displayService,
		displayStream,
		programStream,
	)
	if err != nil {
		t.Fatalf("create Program Connect handler: %v", err)
	}
	authenticationInterceptor, err := connectapi.AuthenticationInterceptor(
		authentication,
	)
	if err != nil {
		t.Fatalf("create Authentication interceptor: %v", err)
	}
	path, serviceHandler := programv1connect.NewProgramControlServiceHandler(
		handler,
		connect.WithInterceptors(
			connectapi.RequestIDInterceptor(),
			programconnect.ErrorInterceptor(),
			authenticationInterceptor,
		),
	)
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		request.Body = http.MaxBytesReader(response, request.Body, 64<<10)
		serviceHandler.ServeHTTP(response, request)
	}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := programv1connect.NewProgramControlServiceClient(
		server.Client(),
		server.URL,
		connect.WithProtoJSON(),
	)
	return func(
		message *programv1.ActOnResultRequest,
	) (*programv1.ActOnResultResponse, error) {
		request := connect.NewRequest(message)
		request.Header().Set("Cookie", "beamers_session="+sessionToken)
		response, invokeErr := client.ActOnResult(t.Context(), request)
		if invokeErr != nil {
			return nil, invokeErr
		}
		return response.Msg, nil
	}, displayStream, programStream
}

func openPrizegivingApplicationTest(
	t *testing.T,
) (*store.SQLite, auth.Account, int, *auth.Service, string) {
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
	if err = storage.IssueBootstrap(
		t.Context(),
		bootstrapHash,
		now,
		now.Add(time.Hour),
	); err != nil {
		t.Fatalf("issue bootstrap: %v", err)
	}
	sessionToken := base64.RawURLEncoding.EncodeToString(
		bytes.Repeat([]byte("s"), 32),
	)
	sessionDigest := sha256.Sum256([]byte(sessionToken))
	created, err := storage.BootstrapAdministrator(
		t.Context(),
		store.BootstrapAdministratorParams{
			BootstrapHash: bootstrapHash,
			Name:          "Producer", NormalizedName: "producer",
			PasswordHash: "test-password-hash",
			SessionHash:  fmt.Sprintf("%x", sessionDigest),
			Now:          now, SessionExpiry: now.Add(time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("bootstrap Administrator: %v", err)
	}
	administrator := auth.Account{
		ID: created.ID, Name: created.Name, Administrator: true,
	}
	authConfig := auth.DefaultConfig()
	authConfig.Now = func() time.Time { return now }
	authentication, err := auth.New(storage, authConfig)
	if err != nil {
		t.Fatalf("create Authentication service: %v", err)
	}
	eventService, err := events.New(storage, func() time.Time { return now })
	if err != nil {
		t.Fatalf("create Event service: %v", err)
	}
	event, err := eventService.Create(
		t.Context(),
		administrator,
		events.CreateInput{
			Name: "Revision 2026", PlannedStartDate: "2026-08-21",
			PlannedEndDate: "2026-08-23", Timezone: "Europe/Berlin",
			EventLocale: "de-DE", EventDayBoundary: "06:00",
			CommandID: "create-event-for-prizegiving",
		},
	)
	if err != nil {
		t.Fatalf("create Event: %v", err)
	}
	if _, err = eventService.GrantEventAccess(
		t.Context(),
		administrator,
		event.ID,
		administrator.ID,
		"Producer",
		"grant-prizegiving-producer",
	); err != nil {
		t.Fatalf("grant Producer: %v", err)
	}
	administrator.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	return storage, administrator, event.ID, authentication, sessionToken
}

func publishPrizegivingSessions(
	t *testing.T,
	storage *store.SQLite,
	actor auth.Account,
	eventID int,
	now func() time.Time,
) (int, int) {
	t.Helper()
	commands, err := rundown.NewCommands(storage, now)
	if err != nil {
		t.Fatalf("create Rundown commands: %v", err)
	}
	queries, err := rundown.NewQueries(storage)
	if err != nil {
		t.Fatalf("create Rundown queries: %v", err)
	}
	start := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	edited, err := commands.EditDraft(t.Context(), actor, rundown.EditDraftInput{
		EventID: eventID, CommandID: "create-prizegiving-sessions",
		Locations: []rundown.LocationDraftInput{{Ref: "main", Name: "Main Hall"}},
		Lanes: []rundown.LaneDraftInput{{
			Ref: "main-lane", Name: "Main Lane",
			Location: rundown.TargetRef{Ref: "main"},
		}},
		Sessions: []rundown.SessionDraftInput{
			{
				Ref: "competition", Title: "Final",
				Type:               rundown.SessionCompetition,
				AudienceVisibility: rundown.AudiencePublic,
				PlannedStart:       start, PlannedEnd: start.Add(time.Hour),
				TimingPolicy:    rundown.TimingFixedEnd,
				MinimumDuration: 30 * time.Minute,
				StartBoundary:   rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
				SubmissionDeadline: start.Add(-time.Hour),
				Lanes:              []rundown.TargetRef{{Ref: "main-lane"}},
			},
			{
				Ref: "ceremony", Title: "Prizegiving",
				Type:               rundown.SessionCeremony,
				AudienceVisibility: rundown.AudiencePublic,
				PlannedStart:       start.Add(2 * time.Hour),
				PlannedEnd:         start.Add(3 * time.Hour),
				TimingPolicy:       rundown.TimingFixedEnd,
				MinimumDuration:    30 * time.Minute,
				StartBoundary:      rundown.BoundaryHard, EndBoundary: rundown.BoundarySoft,
				Lanes: []rundown.TargetRef{{Ref: "main-lane"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("create Prizegiving sessions: %v", err)
	}
	changeIDs := make([]int, 0, len(edited.Changes))
	for _, change := range edited.Changes {
		changeIDs = append(changeIDs, change.ID)
	}
	preview, err := queries.PublishPreview(
		t.Context(),
		actor,
		rundown.PublishPreviewInput{EventID: eventID, ChangeIDs: changeIDs},
	)
	if err != nil {
		t.Fatalf("preview Prizegiving sessions: %v", err)
	}
	if _, err = commands.Publish(t.Context(), actor, rundown.PublishInput{
		EventID: eventID, CommandID: "publish-prizegiving-sessions",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision:     preview.DraftRevision,
			PublishedRevision: preview.PublishedRevision,
			ChangeIDs:         preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		t.Fatalf("publish Prizegiving sessions: %v", err)
	}
	crew, err := queries.CrewRundown(t.Context(), actor, eventID)
	if err != nil {
		t.Fatalf("load Prizegiving sessions: %v", err)
	}
	var ceremonyID, competitionID int
	for _, session := range crew.Sessions {
		switch session.Title {
		case "Prizegiving":
			ceremonyID = session.ID
		case "Final":
			competitionID = session.ID
		}
	}
	if ceremonyID == 0 || competitionID == 0 {
		t.Fatalf("published Prizegiving sessions = %+v", crew.Sessions)
	}
	return ceremonyID, competitionID
}

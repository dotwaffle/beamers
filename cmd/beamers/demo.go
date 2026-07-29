package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/attachments"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/displays"
	"github.com/dotwaffle/beamers/internal/displayviews"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/eventthemes"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/programcontrol"
	"github.com/dotwaffle/beamers/internal/results"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/sessioncontrol"
	"github.com/dotwaffle/beamers/internal/store"
	"github.com/dotwaffle/beamers/internal/themes"
	"github.com/dotwaffle/beamers/internal/themevalue"
	"github.com/dotwaffle/beamers/internal/viewer"
	"github.com/dotwaffle/beamers/internal/voting"
)

const demoPassword = "demo"

func demoSeedNow() time.Time {
	return time.Date(2099, 8, 1, 12, 0, 0, 0, time.UTC)
}

func runDemo(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "persistent demo installation data directory")
	listenAddress := flags.String("listen", "0.0.0.0:8080", "HTTP listen address")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 1
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	fail := func(err error) int {
		logger.Error("command failed", "command", "demo", "error", err)
		return 1
	}
	if flags.NArg() != 0 {
		return fail(errors.New("demo accepts no positional arguments"))
	}

	disposable := *dataDir == ""
	if disposable {
		var err error
		*dataDir, err = os.MkdirTemp("", "beamers-demo-*")
		if err != nil {
			return fail(err)
		}
	}
	if err := seedDemoInstallation(ctx, *dataDir); err != nil {
		if disposable {
			if removeErr := os.RemoveAll(*dataDir); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("remove disposable demo installation: %w", removeErr))
			}
		}
		return fail(err)
	}
	logger.Warn(
		"demo mode enabled",
		"data_dir", *dataDir,
		"listen", *listenAddress,
		"disposable", disposable,
	)
	code := runServe(ctx, []string{
		"--data-dir", *dataDir,
		"--listen", *listenAddress,
		"--insecure-crew",
		"--insecure-display",
	}, stderr, true)
	if disposable {
		if err := os.RemoveAll(*dataDir); err != nil {
			logger.Error("remove disposable demo installation", "data_dir", *dataDir, "error", err)
			return 1
		}
	}
	return code
}

func seedDemoInstallation(ctx context.Context, dataDir string) (returnErr error) {
	if err := operations.Initialize(ctx, dataDir); err != nil {
		return err
	}
	installation, err := operations.OpenInstallationWithConfig(ctx, operations.OpenConfig{
		DataDir: dataDir, AllowDemoPassword: true,
		Now: demoSeedNow,
	})
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, installation.Close())
	}()
	if startupErr := installation.StartupError(); startupErr != nil {
		return startupErr
	}

	authentication := installation.Authentication()
	bootstrapToken, err := authentication.IssueBootstrap(ctx)
	if err != nil {
		return err
	}
	administratorSession, err := authentication.BootstrapFirstAccount(
		ctx,
		bootstrapToken,
		"administrator",
		"Demo Administrator",
		demoPassword,
	)
	if err != nil {
		return err
	}
	administrator := administratorSession.Account
	accounts := make(map[string]auth.Account, 5)
	for _, seed := range []struct {
		handle, displayName string
	}{
		{"attendee", "Demo Attendee"},
		{"voter", "Demo Voter"},
		{"producer", "Demo Producer"},
		{"operator", "Demo Operator"},
		{"observer", "Demo Observer"},
	} {
		created, createErr := authentication.CreateAccountWithDisplayName(
			ctx,
			administrator,
			seed.handle,
			seed.displayName,
			demoPassword,
			"demo-create-"+seed.handle,
		)
		if createErr != nil {
			return createErr
		}
		accounts[seed.handle] = created
	}

	event, err := installation.Events().Create(ctx, administrator, events.CreateInput{
		Name:             "Revision Demo",
		PlannedStartDate: "2099-08-21",
		PlannedEndDate:   "2099-08-22",
		Timezone:         "Europe/Berlin",
		EventLocale:      "en-GB",
		ContentLanguage:  "en-GB",
		EventDayBoundary: "06:00",
		CommandID:        "demo-create-event",
	})
	if err != nil {
		return err
	}
	if _, err = installation.Events().GrantEventAccess(
		ctx,
		administrator,
		event.ID,
		accounts["producer"].ID,
		"Producer",
		"demo-grant-producer",
	); err != nil {
		return err
	}
	if _, err = installation.Events().GrantEventAccess(
		ctx,
		administrator,
		event.ID,
		administrator.ID,
		string(viewer.Producer),
		"demo-grant-administrator-producer",
	); err != nil {
		return err
	}
	producer := accounts["producer"]
	producer.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	event, err = installation.Events().Update(ctx, producer, event.ID, events.CreateInput{
		Name:             event.Name,
		Public:           true,
		PublicSlug:       "revision-demo",
		PlannedStartDate: event.PlannedStartDate,
		PlannedEndDate:   event.PlannedEndDate,
		Timezone:         event.Timezone,
		EventLocale:      event.EventLocale,
		ContentLanguage:  event.ContentLanguage,
		EventDayBoundary: event.EventDayBoundary,
		CommandID:        "demo-publish-event",
		ExpectedRevision: event.Revision,
	})
	if err != nil {
		return err
	}
	if seedErr := seedDemoThemes(ctx, installation, administrator, producer, event.ID); seedErr != nil {
		return seedErr
	}
	demo, err := seedDemoRundown(ctx, installation, administrator, producer, event.ID)
	if err != nil {
		return err
	}
	return seedDemoJourneys(
		ctx,
		installation,
		administrator,
		producer,
		accounts,
		event.ID,
		demo,
	)
}

type demoRundown struct {
	locationID int
	laneID     int
	sessions   map[string]rundown.CrewSession
	entries    map[string][]competition.Entry
}

func seedDemoRundown(
	ctx context.Context,
	installation *operations.Installation,
	administrator, producer auth.Account,
	eventID int,
) (demoRundown, error) {
	dayOne := time.Date(2099, 8, 21, 8, 0, 0, 0, time.UTC)
	dayTwo := dayOne.Add(24 * time.Hour)
	location := rundown.TargetRef{Ref: "main-hall"}
	lane := rundown.TargetRef{Ref: "main-stage"}
	track := rundown.TargetRef{Ref: "general"}
	session := func(
		ref, title string,
		kind rundown.SessionType,
		start time.Time,
	) rundown.SessionDraftInput {
		item := rundown.SessionDraftInput{
			Ref: ref, Title: title, Type: kind,
			AudienceVisibility: rundown.AudiencePublic,
			PlannedStart:       start,
			PlannedEnd:         start.Add(time.Hour),
			TimingPolicy:       rundown.TimingFixedEnd,
			MinimumDuration:    30 * time.Minute,
			StartBoundary:      rundown.BoundaryHard,
			EndBoundary:        rundown.BoundarySoft,
			Locations:          []rundown.TargetRef{location},
			Lanes:              []rundown.TargetRef{lane},
			Tracks:             []rundown.TargetRef{track},
		}
		if kind == rundown.SessionCompetition {
			item.SubmissionDeadline = start.Add(-time.Hour)
			item.EntryDefault = rundown.EntryIncluded
		}
		return item
	}
	edited, err := installation.RundownCommands().EditDraft(
		ctx,
		producer,
		rundown.EditDraftInput{
			EventID: eventID, CommandID: "demo-edit-rundown",
			Locations: []rundown.LocationDraftInput{{Ref: location.Ref, Name: "Main Hall"}},
			Lanes: []rundown.LaneDraftInput{{
				Ref: lane.Ref, Name: "Main Stage", Location: location,
			}},
			Tracks: []rundown.TrackDraftInput{{Ref: track.Ref, Name: "General"}},
			Sessions: []rundown.SessionDraftInput{
				session("opening", "Opening", rundown.SessionCeremony, dayOne),
				session("shader", "Shader Showdown", rundown.SessionCompetition, dayOne.Add(2*time.Hour)),
				session("music", "Tracked Music", rundown.SessionCompetition, dayOne.Add(4*time.Hour)),
				session("oldschool", "Oldschool Demo", rundown.SessionCompetition, dayTwo),
				session("closing", "Closing", rundown.SessionCeremony, dayTwo.Add(2*time.Hour)),
			},
		},
	)
	if err != nil {
		return demoRundown{}, err
	}
	changeIDs := make([]int, 0, len(edited.Changes))
	for _, change := range edited.Changes {
		changeIDs = append(changeIDs, change.ID)
	}
	preview, err := installation.RundownQueries().PublishPreview(
		ctx,
		producer,
		rundown.PublishPreviewInput{EventID: eventID, ChangeIDs: changeIDs},
	)
	if err != nil {
		return demoRundown{}, err
	}
	if _, err = installation.RundownCommands().Publish(ctx, producer, rundown.PublishInput{
		EventID: eventID, CommandID: "demo-publish-rundown",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		return demoRundown{}, err
	}
	published, err := installation.RundownQueries().CrewRundown(ctx, producer, eventID)
	if err != nil {
		return demoRundown{}, err
	}
	demo := demoRundown{
		locationID: published.Locations[0].ID,
		laneID:     published.Lanes[0].ID,
		sessions:   make(map[string]rundown.CrewSession, len(published.Sessions)),
		entries:    make(map[string][]competition.Entry, 3),
	}
	for _, publishedSession := range published.Sessions {
		demo.sessions[publishedSession.Title] = publishedSession
		if publishedSession.Type != rundown.SessionCompetition {
			continue
		}
		entry, createErr := installation.Competition().CreateEntry(ctx, producer, competition.CreateEntryInput{
			EventID: eventID, SessionID: publishedSession.ID,
			CommandID:     "demo-entry-" + strconv.Itoa(publishedSession.ID),
			Name:          publishedSession.Title + " Entry",
			PublicDetails: "A deterministic representative Competition Entry.",
		})
		if createErr != nil {
			return demoRundown{}, createErr
		}
		demo.entries[publishedSession.Title] = []competition.Entry{entry}
	}
	preflight, err := installation.Activation().Preflight(ctx, administrator, eventID)
	if err != nil {
		return demoRundown{}, err
	}
	if _, err = installation.Activation().Activate(ctx, administrator, activation.ActivateInput{
		EventID:      eventID,
		CommandID:    "demo-activate-event",
		Confirmation: preflight.Confirmation,
	}); err != nil {
		return demoRundown{}, err
	}
	return demo, nil
}

func seedDemoThemes(
	ctx context.Context,
	installation *operations.Installation,
	administrator, producer auth.Account,
	eventID int,
) error {
	installationConfig := themevalue.DefaultConfig()
	installationConfig.AccentColor = "#79d7ff"
	installationRevision, err := installation.Themes().CreateDraft(
		ctx,
		administrator,
		themes.CreateDraftInput{
			Config: installationConfig, CommandID: "demo-create-installation-theme",
		},
	)
	if err != nil {
		return err
	}
	if _, err = installation.Themes().Activate(ctx, administrator, themes.ActivateInput{
		RevisionID: installationRevision.ID, CommandID: "demo-activate-installation-theme",
	}); err != nil {
		return err
	}
	eventRevision, err := installation.EventThemes().CreateDraft(
		ctx,
		producer,
		eventID,
		eventthemes.CreateDraftInput{
			Config: themevalue.EventConfig{
				Overrides: themevalue.Config{BrandAsset: themevalue.BrandAssetWordmark},
				Variants: map[string]themevalue.Config{
					themevalue.VariantCompetitionOutput: {AccentColor: "#ffdf6e"},
				},
			},
			CommandID: "demo-create-event-theme",
		},
	)
	if err != nil {
		return err
	}
	_, err = installation.EventThemes().Activate(
		ctx,
		producer,
		eventID,
		eventthemes.ActivateInput{
			RevisionID: eventRevision.ID, CommandID: "demo-activate-event-theme",
		},
	)
	return err
}

func seedDemoJourneys(
	ctx context.Context,
	installation *operations.Installation,
	administrator, producer auth.Account,
	accounts map[string]auth.Account,
	eventID int,
	demo demoRundown,
) error {
	if _, err := installation.Events().GrantEventAccess(
		ctx,
		administrator,
		eventID,
		accounts["observer"].ID,
		string(viewer.Observer),
		"demo-grant-observer",
	); err != nil {
		return err
	}
	if _, err := installation.Events().GrantScopedEventAccess(
		ctx,
		administrator,
		eventID,
		events.GrantInput{
			AccountID:        accounts["operator"].ID,
			Role:             string(viewer.Operator),
			LaneIDs:          []int{demo.laneID},
			DisplayGroupKeys: []string{"main-stage"},
			CommandID:        "demo-grant-operator",
		},
	); err != nil {
		return err
	}
	if err := installation.Schedule().SetFavorite(
		ctx,
		accounts["attendee"].ID,
		demo.sessions["Opening"].ID,
		true,
	); err != nil {
		return err
	}
	submission, err := installation.Competition().CreateSubmission(
		ctx,
		accounts["attendee"],
		competition.CreateSubmissionInput{
			EventID: eventID, SessionID: demo.sessions["Shader Showdown"].ID,
			CommandID:     "demo-attendee-submission",
			Name:          "Attendee Shader",
			PublicDetails: "A signed-in attendee submission ready for editing.",
		},
	)
	if err != nil {
		return err
	}
	if _, err = installation.Attachments().UploadForAccount(
		ctx,
		accounts["attendee"],
		attachments.AccountUploadInput{
			EventID: eventID, TargetID: submission.ID, TargetType: attachments.TargetEntry,
			CommandID: "demo-attendee-attachment", Name: "Executable",
			OriginalFilename: "attendee-shader.zip", MediaType: "application/zip",
			Body: strings.NewReader("deterministic demo attachment\n"),
		},
	); err != nil {
		return err
	}
	if seedErr := seedDemoVotingEligibility(
		ctx,
		installation,
		producer,
		accounts["voter"],
		eventID,
	); seedErr != nil {
		return seedErr
	}
	if seedErr := seedDemoDisplay(
		ctx,
		installation,
		administrator,
		eventID,
		demo.locationID,
	); seedErr != nil {
		return seedErr
	}
	if seedErr := seedCompletedDemoCompetition(
		ctx,
		installation,
		producer,
		eventID,
		demo.sessions["Oldschool Demo"],
		demo.entries["Oldschool Demo"],
	); seedErr != nil {
		return seedErr
	}
	return seedLiveDemoCompetition(
		ctx,
		installation,
		producer,
		eventID,
		demo.sessions["Tracked Music"],
	)
}

func seedDemoVotingEligibility(
	ctx context.Context,
	installation *operations.Installation,
	producer, voter auth.Account,
	eventID int,
) error {
	keys, err := installation.Voting().Issue(ctx, producer, voting.IssueInput{
		EventID: eventID, Count: 2,
		ExpiresAt: time.Date(2099, 8, 30, 0, 0, 0, 0, time.UTC),
		CommandID: "demo-issue-voting-keys",
	})
	if err != nil {
		return err
	}
	return installation.Voting().Redeem(
		ctx,
		voter,
		eventID,
		keys[0].Token,
		"demo-redeem-voting-key",
	)
}

func seedDemoDisplay(
	ctx context.Context,
	installation *operations.Installation,
	administrator auth.Account,
	eventID, locationID int,
) error {
	enrollment, err := installation.Displays().EnrollmentForBrowser(ctx, "", "")
	if err != nil {
		return err
	}
	display, err := installation.Displays().ClaimEnrollment(
		ctx,
		administrator,
		displays.ClaimInput{
			Code: enrollment.Code, Name: "Main Hall Display",
			CommandID: "demo-enroll-display",
		},
	)
	if err != nil {
		return err
	}
	_, err = installation.Displays().Assign(ctx, administrator, displays.AssignInput{
		DisplayID: display.ID, EventID: eventID, LocationID: locationID,
		ViewKey: displayviews.EventOverview, DisplayGroupKeys: []string{"main-stage"},
		CommandID: "demo-assign-display",
	})
	return err
}

func seedCompletedDemoCompetition(
	ctx context.Context,
	installation *operations.Installation,
	producer auth.Account,
	eventID int,
	session rundown.CrewSession,
	entries []competition.Entry,
) error {
	if _, err := installation.Competition().ConfigureReadiness(
		ctx,
		producer,
		competition.ConfigureReadinessInput{
			EventID: eventID, SessionID: session.ID,
			CommandID: "demo-complete-readiness",
		},
	); err != nil {
		return err
	}
	live, err := installation.SessionControl().Start(ctx, producer, sessioncontrol.StartInput{
		EventID: eventID, SessionID: session.ID, CommandID: "demo-start-complete-competition",
	})
	if err != nil {
		return err
	}
	preflight, err := installation.Competition().PreflightEnd(
		ctx,
		producer,
		eventID,
		session.ID,
	)
	if err != nil {
		return err
	}
	if _, err = installation.SessionControl().End(ctx, producer, sessioncontrol.EndInput{
		EventID: eventID, SessionID: session.ID, CommandID: "demo-end-complete-competition",
		ExpectedLiveStateRevision:  live.LiveStateRevision,
		ConfirmedDeferredEntries:   preflight.RequiresConfirmation,
		DeferredEntriesFingerprint: preflight.Fingerprint,
	}); err != nil {
		return err
	}
	standings := make([]results.Standing, 0, len(entries))
	for index, entry := range entries {
		standings = append(standings, results.Standing{
			EntryID: entry.ID, Standing: results.Placed,
			Placement: index + 1, DisplayOrder: index + 1,
		})
	}
	draft, err := installation.Results().Save(ctx, producer, results.SaveInput{
		EventID: eventID, SessionID: session.ID, CommandID: "demo-save-results",
		Disposition: results.Publish, Score: results.ScorePolicy{Type: results.None},
		Standings: standings,
	})
	if err != nil {
		return err
	}
	if _, err = installation.Results().MarkReady(ctx, producer, results.MarkReadyInput{
		EventID: eventID, SessionID: session.ID, CommandID: "demo-ready-results",
		ExpectedRevision: draft.Revision,
	}); err != nil {
		return err
	}
	_, err = installation.Results().ReleaseStandaloneResults(
		ctx,
		producer,
		results.ReleaseStandaloneResultsInput{
			EventID: eventID, CompetitionSessionID: session.ID,
			CommandID: "demo-release-results",
		},
	)
	return err
}

func seedLiveDemoCompetition(
	ctx context.Context,
	installation *operations.Installation,
	producer auth.Account,
	eventID int,
	session rundown.CrewSession,
) error {
	if _, err := installation.Competition().ConfigureReadiness(
		ctx,
		producer,
		competition.ConfigureReadinessInput{
			EventID: eventID, SessionID: session.ID, CommandID: "demo-live-readiness",
		},
	); err != nil {
		return err
	}
	if _, err := installation.SessionControl().Start(ctx, producer, sessioncontrol.StartInput{
		EventID: eventID, SessionID: session.ID, CommandID: "demo-start-live-competition",
	}); err != nil {
		return err
	}
	window, err := installation.Voting().Window(ctx, producer, eventID, session.ID)
	if err != nil {
		return err
	}
	if _, err = installation.Voting().Open(ctx, producer, voting.WindowInput{
		EventID: eventID, SessionID: session.ID, ExpectedRevision: window.Revision,
		CommandID: "demo-open-voting",
	}); err != nil {
		return err
	}
	state, err := installation.ProgramControl().Control(ctx, producer, programcontrol.ControlInput{
		EventID: eventID, SessionID: session.ID, Action: programcontrol.ControlClaim,
		CommandID: "demo-claim-program-control",
	})
	if err != nil {
		return err
	}
	for range 4 {
		input := programcontrol.TakeInput{
			EventID: eventID, SessionID: session.ID,
			CommandID:        "demo-take-program-" + strconv.Itoa(state.Channel.Revision+1),
			ExpectedRevision: state.Channel.Revision, ExpectedControlRevision: state.ControlRevision,
			Item: state.Preview,
		}
		if state.Preview.Kind == store.ProgramItemEntry {
			order, orderErr := installation.Competition().PreviewEntryOrder(
				ctx,
				producer,
				eventID,
				session.ID,
			)
			if orderErr != nil {
				return orderErr
			}
			input.ExpectedEntryOrderRevision = order.Revision
			input.EntryOrderFingerprint = order.Fingerprint
		}
		taken, takeErr := installation.ProgramControl().Take(ctx, producer, input)
		if takeErr != nil {
			return takeErr
		}
		state = taken.State
		if state.Channel.Output.Kind == store.ProgramItemEntry {
			return nil
		}
	}
	return errors.New("demo live Competition did not present an Entry")
}

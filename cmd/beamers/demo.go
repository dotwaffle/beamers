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
	"time"

	"github.com/dotwaffle/beamers/internal/activation"
	"github.com/dotwaffle/beamers/internal/auth"
	"github.com/dotwaffle/beamers/internal/competition"
	"github.com/dotwaffle/beamers/internal/events"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/rundown"
	"github.com/dotwaffle/beamers/internal/viewer"
)

const demoPassword = "demo"

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
	producerSession, err := authentication.BootstrapFirstAccount(
		ctx,
		bootstrapToken,
		"producer",
		"Demo Producer",
		demoPassword,
	)
	if err != nil {
		return err
	}
	producer := producerSession.Account
	accounts := make(map[string]auth.Account, 3)
	for _, handle := range []string{"attendee", "voter", "operator"} {
		created, createErr := authentication.CreateAccount(
			ctx,
			producer,
			handle,
			demoPassword,
			"demo-create-"+handle,
		)
		if createErr != nil {
			return createErr
		}
		accounts[handle] = created
	}

	event, err := installation.Events().Create(ctx, producer, events.CreateInput{
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
		producer,
		event.ID,
		producer.ID,
		"Producer",
		"demo-grant-producer",
	); err != nil {
		return err
	}
	if _, err = installation.Events().GrantEventAccess(
		ctx,
		producer,
		event.ID,
		accounts["operator"].ID,
		"Operator",
		"demo-grant-operator",
	); err != nil {
		return err
	}
	producer.EventRoles = map[int]viewer.Role{event.ID: viewer.Producer}
	return seedDemoRundown(ctx, installation, producer, event.ID)
}

func seedDemoRundown(
	ctx context.Context,
	installation *operations.Installation,
	producer auth.Account,
	eventID int,
) error {
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
				session("graphics", "Graphics Competition", rundown.SessionCompetition, dayOne.Add(2*time.Hour)),
				session("music", "Music Competition", rundown.SessionCompetition, dayTwo),
				session("closing", "Closing", rundown.SessionCeremony, dayTwo.Add(2*time.Hour)),
			},
		},
	)
	if err != nil {
		return err
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
		return err
	}
	if _, err = installation.RundownCommands().Publish(ctx, producer, rundown.PublishInput{
		EventID: eventID, CommandID: "demo-publish-rundown",
		Confirmation: rundown.PublishConfirmation{
			DraftRevision: preview.DraftRevision, PublishedRevision: preview.PublishedRevision,
			ChangeIDs: preview.ChangeIDs, Fingerprint: preview.Fingerprint,
		},
	}); err != nil {
		return err
	}
	published, err := installation.RundownQueries().CrewRundown(ctx, producer, eventID)
	if err != nil {
		return err
	}
	for _, publishedSession := range published.Sessions {
		if publishedSession.Type != rundown.SessionCompetition {
			continue
		}
		if _, err = installation.Competition().CreateEntry(ctx, producer, competition.CreateEntryInput{
			EventID: eventID, SessionID: publishedSession.ID,
			CommandID:     "demo-entry-" + strconv.Itoa(publishedSession.ID),
			Name:          "Demo Entry",
			PublicDetails: "A deterministic representative Competition Entry.",
		}); err != nil {
			return err
		}
	}
	preflight, err := installation.Activation().Preflight(ctx, producer, eventID)
	if err != nil {
		return err
	}
	_, err = installation.Activation().Activate(ctx, producer, activation.ActivateInput{
		EventID:      eventID,
		CommandID:    "demo-activate-event",
		Confirmation: preflight.Confirmation,
	})
	return err
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	_ "github.com/dotwaffle/beamers/ent/runtime" // Register generated hooks, validators, and privacy policies.
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/buildinfo"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/server"
)

func main() {
	os.Exit(mainExitCode())
}

func mainExitCode() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.Args[1:], os.Stdout, os.Stderr)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	var err error
	switch args[0] {
	case "init":
		err = runInit(ctx, args[1:], stdout, stderr)
	case "bootstrap":
		err = runBootstrap(ctx, args[1:], stdout, stderr)
	case "backup":
		err = runBackup(ctx, args[1:], stdout, stderr)
	case "restore":
		err = runRestore(ctx, args[1:], stdout, stderr)
	case "serve":
		err = runServe(ctx, args[1:], stderr, logger)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		printUsage(stderr)
		return 2
	}
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	logger.Error("command failed", "command", args[0], "error", err)
	return 1
}

func runRestore(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "preview":
			return runRestorePreview(ctx, args[1:], stdout, stderr)
		case "apply":
			return runRestoreApply(ctx, args[1:], stdout, stderr)
		}
	}
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "Backup archive path")
	dataDir := flags.String("data-dir", "", "unused installation data directory")
	attachmentsDir := flags.String(
		"attachments-dir",
		"",
		"unused Attachment Store root (default: DATA-DIR/attachments)",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("restore accepts no positional arguments")
	}
	manifest, err := operations.RestoreBackup(ctx, backup.RestoreInput{
		InputPath:      *input,
		DataDir:        *dataDir,
		AttachmentsDir: *attachmentsDir,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "restored %s Backup into %s\n", manifest.Mode, *dataDir)
	return err
}

func runRestorePreview(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("restore preview", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "Backup archive path")
	dataDir := flags.String("data-dir", "", "installation data directory to replace")
	attachmentsDir := flags.String(
		"attachments-dir",
		"",
		"Attachment Store root (default: DATA-DIR/attachments)",
	)
	forceUnsupported := flags.Bool(
		"force-unsupported",
		false,
		"host-only: stage unsupported state without a safety claim",
	)
	reason := flags.String("reason", "", "mandatory reason for forced unsupported Restore")
	acknowledgeUnsupported := flags.Bool(
		"acknowledge-no-safety",
		false,
		"acknowledge that forced unsupported Restore makes no safety claim",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("restore preview accepts no positional arguments")
	}
	plan, err := operations.PrepareRestore(ctx, backup.RestoreInput{
		InputPath:                   *input,
		DataDir:                     *dataDir,
		AttachmentsDir:              *attachmentsDir,
		Replace:                     true,
		ForceUnsupported:            *forceUnsupported,
		ForceReason:                 *reason,
		AcknowledgeUnsupportedRisks: *acknowledgeUnsupported,
	})
	if err != nil {
		return err
	}
	if plan.ForcedUnsupported {
		_, _ = fmt.Fprintln(
			stderr,
			"WARNING: forced unsupported Restore makes no safety claim; review unknown_schema_elements",
		)
	}
	return json.NewEncoder(stdout).Encode(plan)
}

func runRestoreApply(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("restore apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	journal := flags.String("journal", "", "prepared Restore journal path")
	acknowledge := flags.Bool(
		"acknowledge-replacement",
		false,
		"acknowledge that current installation state will move to quarantine",
	)
	acknowledgeUnsupported := flags.Bool(
		"acknowledge-no-safety",
		false,
		"repeat that forced unsupported Restore makes no safety claim",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("restore apply accepts no positional arguments")
	}
	if !*acknowledge {
		return errors.New("restore replacement acknowledgment is required")
	}
	manifest, err := operations.ApplyRestoreWithOptions(
		ctx,
		*journal,
		backup.ApplyOptions{
			AcknowledgeUnsupportedRisks: *acknowledgeUnsupported,
		},
	)
	if err != nil {
		return err
	}
	if *acknowledgeUnsupported {
		_, _ = fmt.Fprintln(
			stderr,
			"WARNING: installed forced unsupported state without a safety claim",
		)
	}
	_, err = fmt.Fprintf(stdout, "restored %s Backup from prepared journal\n", manifest.Mode)
	return err
}

func runBackup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "verify" {
		flags := flag.NewFlagSet("backup verify", flag.ContinueOnError)
		flags.SetOutput(stderr)
		input := flags.String("input", "", "Backup archive path")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("backup verify accepts no positional arguments")
		}
		manifest, err := operations.VerifyBackup(*input)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(
			stdout,
			"verified %s Backup format %d\n",
			manifest.Mode,
			manifest.FormatVersion,
		)
		return err
	}
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "installation data directory")
	attachmentsDir := flags.String(
		"attachments-dir",
		"",
		"Attachment Store root (default: DATA-DIR/attachments)",
	)
	output := flags.String("output", "", "Backup archive output path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("backup accepts no positional arguments")
	}
	manifest, err := operations.CreateBackup(ctx, backup.CreateInput{
		DataDir:        *dataDir,
		AttachmentsDir: *attachmentsDir,
		OutputPath:     *output,
		Mode:           backup.Sanitized,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		stdout,
		"created %s Backup at %s\n",
		manifest.Mode,
		*output,
	)
	return err
}

func runBootstrap(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "installation data directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("bootstrap accepts no positional arguments")
	}
	token, err := operations.IssueAdministratorBootstrap(ctx, *dataDir)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, token)
	return err
}

func runInit(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "installation data directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("init accepts no positional arguments")
	}
	if err := operations.Initialize(ctx, *dataDir); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "initialized installation in %s\n", *dataDir)
	return err
}

func runServe(ctx context.Context, args []string, stderr io.Writer, logger *slog.Logger) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "installation data directory")
	attachmentsDir := flags.String(
		"attachments-dir",
		"",
		"Attachment Store root (default: DATA-DIR/attachments)",
	)
	listenAddress := flags.String("listen", "127.0.0.1:8080", "HTTP listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve accepts no positional arguments")
	}
	return server.Run(ctx, server.Config{
		DataDir:         *dataDir,
		AttachmentsDir:  *attachmentsDir,
		ListenAddress:   *listenAddress,
		BuildVersion:    buildinfo.Version(),
		ShutdownTimeout: 10 * time.Second,
		Logger:          logger,
		TracerProvider:  tracenoop.NewTracerProvider(),
		MeterProvider:   noop.NewMeterProvider(),
		Propagator: propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	})
}

func printUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "usage: beamers <init|bootstrap|backup|restore|serve> [options]")
}

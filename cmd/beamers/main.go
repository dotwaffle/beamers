package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	_ "github.com/dotwaffle/beamers/ent/runtime" // Register generated hooks, validators, and privacy policies.
	"github.com/dotwaffle/beamers/internal/backup"
	"github.com/dotwaffle/beamers/internal/buildinfo"
	"github.com/dotwaffle/beamers/internal/operations"
	"github.com/dotwaffle/beamers/internal/server"
	"github.com/dotwaffle/beamers/internal/telemetry"
)

func main() {
	os.Exit(mainExitCode())
}

func mainExitCode() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		<-signals
		signal.Stop(signals)
		cancel()
	}()
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
	case "export-final-files":
		err = runExportFinalFiles(ctx, args[1:], stdout, stderr)
	case "upgrade":
		err = runUpgrade(ctx, args[1:], stdout, stderr)
	case "serve":
		return runServe(ctx, args[1:], stderr)
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

func runExportFinalFiles(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	if len(args) == 0 {
		return errors.New("export-final-files requires preview or apply")
	}
	switch args[0] {
	case "preview":
		return runExportFinalFilesPreview(ctx, args[1:], stdout, stderr)
	case "apply":
		return runExportFinalFilesApply(ctx, args[1:], stdout, stderr)
	default:
		return errors.New("export-final-files requires preview or apply")
	}
}

func runExportFinalFilesPreview(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) (returnErr error) {
	flags := flag.NewFlagSet("export-final-files preview", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "installation data directory")
	attachmentsDir := flags.String(
		"attachments-dir", "", "Attachment Store root (default: DATA-DIR/attachments)",
	)
	eventID := flags.Int("event-id", 0, "Event identity")
	output := flags.String("output", "", "new export directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("export-final-files preview accepts no positional arguments")
	}
	installation, err := operations.OpenInstallationWithConfig(ctx, operations.OpenConfig{
		DataDir: *dataDir, AttachmentsDir: *attachmentsDir,
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
	plan, err := installation.Attachments().PlanFinalFiles(ctx, *eventID, *output)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(plan)
}

func runExportFinalFilesApply(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) (returnErr error) {
	flags := flag.NewFlagSet("export-final-files apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "installation data directory")
	attachmentsDir := flags.String(
		"attachments-dir", "", "Attachment Store root (default: DATA-DIR/attachments)",
	)
	eventID := flags.Int("event-id", 0, "Event identity")
	output := flags.String("output", "", "new export directory")
	previewDigest := flags.String("preview-digest", "", "exact preview digest")
	approve := flags.Bool("approve-export", false, "confirm the exact Final Files Export")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("export-final-files apply accepts no positional arguments")
	}
	if !*approve {
		return errors.New("approval for Final Files Export is required")
	}
	installation, err := operations.OpenInstallationWithConfig(ctx, operations.OpenConfig{
		DataDir: *dataDir, AttachmentsDir: *attachmentsDir,
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
	manifest, err := installation.Attachments().WriteFinalFilesDirectory(
		ctx, *eventID, *output, *previewDigest,
	)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(manifest)
}

func runUpgrade(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("upgrade requires preview or apply")
	}
	switch args[0] {
	case "preview":
		return runUpgradePreview(ctx, args[1:], stdout, stderr)
	case "apply":
		return runUpgradeApply(ctx, args[1:], stdout, stderr)
	default:
		return errors.New("upgrade requires preview or apply")
	}
}

func runUpgradePreview(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) (returnErr error) {
	flags := flag.NewFlagSet("upgrade preview", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "installation data directory")
	attachmentsDir := flags.String(
		"attachments-dir",
		"",
		"Attachment Store root (default: DATA-DIR/attachments)",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("upgrade preview accepts no positional arguments")
	}
	upgrade, err := operations.PrepareUpgrade(ctx, operations.OpenConfig{
		DataDir: *dataDir, AttachmentsDir: *attachmentsDir,
	})
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, upgrade.Close())
	}()
	return json.NewEncoder(stdout).Encode(upgrade.Plan())
}

func runUpgradeApply(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("upgrade apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "installation data directory")
	attachmentsDir := flags.String(
		"attachments-dir",
		"",
		"Attachment Store root (default: DATA-DIR/attachments)",
	)
	approve := flags.Bool(
		"approve-consequences",
		false,
		"approve the exact previewed migration consequences",
	)
	acknowledgeNoDownMigration := flags.Bool(
		"acknowledge-no-down-migration",
		false,
		"acknowledge rollback requires the prior binary and preserved Backup",
	)
	forceLive := flags.Bool(
		"force-live",
		false,
		"allow migration while explicitly preserved live state exists",
	)
	reason := flags.String("reason", "", "mandatory reason for force-live migration")
	previewDigest := flags.String(
		"preview-digest",
		"",
		"exact digest returned by upgrade preview",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("upgrade apply accepts no positional arguments")
	}
	upgrade, err := operations.PrepareUpgrade(ctx, operations.OpenConfig{
		DataDir: *dataDir, AttachmentsDir: *attachmentsDir,
	})
	if err != nil {
		return err
	}
	if upgrade.Plan().RequiresApproval {
		_, _ = fmt.Fprintln(
			stderr,
			"WARNING: approved upgrade has no down migration; preserve and protect its Full-Fidelity Backup",
		)
	}
	result, err := upgrade.Apply(ctx, operations.UpgradeApproval{
		ApproveConsequences:        *approve,
		AcknowledgeNoDownMigration: *acknowledgeNoDownMigration,
		ForceLive:                  *forceLive,
		Reason:                     *reason,
		HostAuthority:              true,
		PreviewDigest:              *previewDigest,
	})
	if err != nil {
		if result.BackupPath != "" {
			_, _ = fmt.Fprintf(
				stderr,
				"rollback Backup preserved at %s\n",
				result.BackupPath,
			)
		}
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func runRestore(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "preview":
			return runRestorePreview(ctx, args[1:], stdout, stderr)
		case "apply":
			return runRestoreApply(ctx, args[1:], stdout, stderr)
		case "quarantine-journal":
			return runRestoreQuarantineJournal(args[1:], stdout, stderr)
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

func runRestoreQuarantineJournal(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("restore quarantine-journal", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "installation data directory")
	acknowledge := flags.Bool(
		"acknowledge-damaged-journal",
		false,
		"acknowledge that the unreadable journal will move to quarantine",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("restore quarantine-journal accepts no positional arguments")
	}
	if !*acknowledge {
		return errors.New("damaged Restore journal acknowledgment is required")
	}
	preservedPath, err := operations.QuarantineDamagedRestoreJournal(*dataDir)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "preserved damaged Restore journal at %s\n", preservedPath)
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
	attachmentsDir := flags.String(
		"attachments-dir",
		"",
		"Attachment Store root (default: DATA-DIR/attachments)",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("init accepts no positional arguments")
	}
	if err := operations.InitializeWithConfig(ctx, operations.OpenConfig{
		DataDir: *dataDir, AttachmentsDir: *attachmentsDir,
	}); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "initialized installation in %s\n", *dataDir)
	return err
}

func runServe(ctx context.Context, args []string, stderr io.Writer) int {
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	fail := func(err error) int {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		logger.Error("command failed", "command", "serve", "error", err)
		return 1
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "installation data directory")
	attachmentsDir := flags.String(
		"attachments-dir",
		"",
		"Attachment Store root (default: DATA-DIR/attachments)",
	)
	listenAddress := flags.String("listen", "127.0.0.1:8080", "HTTP listen address")
	publicListenAddress := flags.String(
		"public-listen",
		"",
		"optional public Schedule and Results HTTP listen address",
	)
	tlsCertificate := flags.String("tls-cert", "", "TLS certificate file")
	tlsPrivateKey := flags.String("tls-key", "", "TLS private key file")
	insecureCrew := flags.Bool(
		"insecure-crew",
		false,
		"permit plaintext Crew access on a non-loopback listener",
	)
	insecureDisplay := flags.Bool(
		"insecure-display",
		false,
		"permit plaintext Display access on a non-loopback listener",
	)
	var trustedProxies []netip.Prefix
	flags.Func(
		"trusted-proxy",
		"trusted reverse proxy IP address or CIDR (repeatable)",
		func(value string) error {
			prefix, err := netip.ParsePrefix(value)
			if err != nil {
				address, addressErr := netip.ParseAddr(value)
				if addressErr != nil {
					return fmt.Errorf("parse trusted proxy %q: %w", value, err)
				}
				prefix = netip.PrefixFrom(address, address.BitLen())
			}
			trustedProxies = append(trustedProxies, prefix.Masked())
			return nil
		},
	)
	shutdownTimeout := flags.Duration(
		"shutdown-timeout",
		10*time.Second,
		"hosting platform graceful-stop budget",
	)
	otlpEndpoint := flags.String(
		"otlp-endpoint",
		"",
		"OTLP HTTP base URL (disabled when empty)",
	)
	sampleRatio := flags.Float64(
		"telemetry-sample-ratio",
		1,
		"trace sampling ratio from zero to one",
	)
	exportTimeout := flags.Duration(
		"telemetry-export-timeout",
		2*time.Second,
		"maximum duration of one telemetry export",
	)
	replicaURL := flags.String(
		"replica-url",
		"",
		"full-fidelity Litestream file URL (disabled when empty)",
	)
	if err := flags.Parse(args); err != nil {
		return fail(err)
	}
	if flags.NArg() != 0 {
		return fail(errors.New("serve accepts no positional arguments"))
	}
	telemetryRuntime, err := telemetry.New(ctx, telemetry.Config{
		Endpoint:       *otlpEndpoint,
		ServiceVersion: buildinfo.Version(),
		Stderr:         stderr,
		SampleRatio:    *sampleRatio,
		ExportTimeout:  *exportTimeout,
	})
	if err != nil {
		return fail(err)
	}
	logger = telemetryRuntime.Logger()
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), *exportTimeout)
		defer cancel()
		_ = telemetryRuntime.Shutdown(shutdownContext)
	}()
	err = server.Run(ctx, server.Config{
		DataDir:         *dataDir,
		AttachmentsDir:  *attachmentsDir,
		ListenAddress:   *listenAddress,
		BuildVersion:    buildinfo.Version(),
		ShutdownTimeout: *shutdownTimeout,
		Logger:          telemetryRuntime.Logger(),
		TracerProvider:  telemetryRuntime.TracerProvider(),
		MeterProvider:   telemetryRuntime.MeterProvider(),
		Propagator:      telemetryRuntime.Propagator(),
		Telemetry:       telemetryRuntime,
		ReplicaURL:      *replicaURL,
		TLSCertificate:  *tlsCertificate,
		TLSPrivateKey:   *tlsPrivateKey,
		TrustedProxies:  trustedProxies,
		PublicListen:    *publicListenAddress,
		InsecureCrew:    *insecureCrew,
		InsecureDisplay: *insecureDisplay,
	})
	if err != nil {
		return fail(err)
	}
	return 0
}

func printUsage(output io.Writer) {
	_, _ = fmt.Fprintln(
		output,
		"usage: beamers <init|bootstrap|backup|restore|export-final-files|upgrade|serve> [options]",
	)
}

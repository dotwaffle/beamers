package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	listenAddress := flags.String("listen", "127.0.0.1:8080", "HTTP listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve accepts no positional arguments")
	}
	return server.Run(ctx, server.Config{
		DataDir:         *dataDir,
		ListenAddress:   *listenAddress,
		ShutdownTimeout: 10 * time.Second,
		Logger:          logger,
	})
}

func printUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "usage: beamers <init|serve> [options]")
}

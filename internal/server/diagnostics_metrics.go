package server

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"

	"github.com/dotwaffle/beamers/internal/diskspace"
)

// storageStats is the storage footprint diagnostics and OTel gauges both
// report, gathered together so the two stay in agreement.
type storageStats struct {
	freeDiskBytes    uint64
	databaseBytes    uint64
	attachmentsBytes uint64
}

func collectStorageStats(dataDir, attachmentsDir string) (storageStats, error) {
	if attachmentsDir == "" && dataDir != "" {
		attachmentsDir = filepath.Join(dataDir, "attachments")
	}
	usage, err := diskspace.Stat(dataDir)
	if err != nil {
		return storageStats{}, err
	}
	var databasePath string
	if dataDir != "" {
		databasePath = filepath.Join(dataDir, "beamers.db")
	}
	databaseBytes, err := diskspace.FileSize(databasePath)
	if err != nil {
		return storageStats{}, err
	}
	attachmentsBytes, err := diskspace.DirSize(attachmentsDir)
	if err != nil {
		return storageStats{}, err
	}
	return storageStats{
		freeDiskBytes:    usage.FreeBytes,
		databaseBytes:    databaseBytes,
		attachmentsBytes: attachmentsBytes,
	}, nil
}

// registerStorageGaugesInput names the inputs registerStorageGauges needs
// to export storage footprint gauges.
type registerStorageGaugesInput struct {
	MeterProvider  metric.MeterProvider
	DataDir        string
	AttachmentsDir string
	Logger         *slog.Logger
}

// registerStorageGauges exports the same free-disk, database, and
// Attachment Store sizes diagnostics reports as OTel gauges, sampled on
// each collection interval rather than only when an Administrator happens
// to poll /diagnostics.
func registerStorageGauges(input registerStorageGaugesInput) {
	meterProvider := input.MeterProvider
	if meterProvider == nil {
		meterProvider = metricnoop.NewMeterProvider()
	}
	meter := meterProvider.Meter("github.com/dotwaffle/beamers/internal/server")
	freeDisk, err := meter.Int64ObservableGauge(
		"beamers.storage.disk_free_bytes",
		metric.WithUnit("By"),
	)
	if err != nil {
		input.Logger.Error("create disk free bytes gauge", "component", "diagnostics", "error", err)
		return
	}
	databaseBytes, err := meter.Int64ObservableGauge(
		"beamers.storage.database_bytes",
		metric.WithUnit("By"),
	)
	if err != nil {
		input.Logger.Error("create database bytes gauge", "component", "diagnostics", "error", err)
		return
	}
	attachmentBytes, err := meter.Int64ObservableGauge(
		"beamers.storage.attachments_bytes",
		metric.WithUnit("By"),
	)
	if err != nil {
		input.Logger.Error(
			"create Attachment Store bytes gauge", "component", "diagnostics", "error", err,
		)
		return
	}
	_, err = meter.RegisterCallback(
		func(_ context.Context, observer metric.Observer) error {
			stats, statsErr := collectStorageStats(input.DataDir, input.AttachmentsDir)
			if statsErr != nil {
				return fmt.Errorf("observe storage gauges: %w", statsErr)
			}
			observer.ObserveInt64(freeDisk, saturatingInt64(stats.freeDiskBytes))
			observer.ObserveInt64(databaseBytes, saturatingInt64(stats.databaseBytes))
			observer.ObserveInt64(attachmentBytes, saturatingInt64(stats.attachmentsBytes))
			return nil
		},
		freeDisk, databaseBytes, attachmentBytes,
	)
	if err != nil {
		input.Logger.Error("register storage gauges", "component", "diagnostics", "error", err)
	}
}

func saturatingInt64(value uint64) int64 {
	const maxInt64 = 1<<63 - 1
	if value > maxInt64 {
		return maxInt64
	}
	return int64(value)
}

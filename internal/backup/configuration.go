package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	configurationVersion = 1
	configurationReceipt = ".beamers-service-configuration.json"
)

// Configuration is the non-secret effective service configuration recorded by Backup.
type Configuration struct {
	Version            int           `json:"version"`
	DataDir            string        `json:"data_dir"`
	AttachmentsDir     string        `json:"attachments_dir"`
	ListenAddress      string        `json:"listen_address"`
	PublicListen       string        `json:"public_listen,omitempty"`
	TLSCertificate     string        `json:"tls_certificate,omitempty"`
	TLSPrivateKey      string        `json:"tls_private_key,omitempty"`
	TrustedProxies     []string      `json:"trusted_proxies,omitempty"`
	ReplicationEnabled bool          `json:"replication_enabled"`
	ReplicaURL         string        `json:"replica_url,omitempty"`
	ShutdownTimeout    time.Duration `json:"shutdown_timeout"`
	InsecureCrew       bool          `json:"insecure_crew"`
	InsecureDisplay    bool          `json:"insecure_display"`
}

// ConfigurationDifference describes how target host behavior differs from a Backup.
type ConfigurationDifference struct {
	Field       string                  `json:"field"`
	BackupValue string                  `json:"backup_value"`
	TargetValue string                  `json:"target_value"`
	Resolution  ConfigurationResolution `json:"resolution"`
}

// ConfigurationResolution identifies the operator action covering one difference.
type ConfigurationResolution string

const (
	configurationMapped                 ConfigurationResolution = "mapped"
	configurationAcknowledgmentRequired ConfigurationResolution = "acknowledgment_required"
)

// RecordConfiguration atomically records the last configuration that reached server startup.
func RecordConfiguration(configuration Configuration) error {
	normalized, err := normalizeConfiguration(configuration)
	if err != nil {
		return err
	}
	content, err := json.Marshal(normalized)
	if err != nil {
		return errors.New("encode service configuration")
	}
	content = append(content, '\n')
	path := filepath.Join(normalized.DataDir, configurationReceipt)
	temporary, err := os.CreateTemp(normalized.DataDir, ".beamers-service-configuration-*")
	if err != nil {
		return fmt.Errorf("create service configuration receipt: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	err = errors.Join(err, temporary.Close())
	if err != nil {
		return fmt.Errorf("write service configuration receipt: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install service configuration receipt: %w", err)
	}
	if err = syncDirectory(normalized.DataDir); err != nil {
		return fmt.Errorf("sync service configuration receipt: %w", err)
	}
	return nil
}

// RedactConfiguration removes unsafe replication destinations while retaining enablement.
func RedactConfiguration(configuration Configuration) Configuration {
	configuration.ReplicationEnabled = configuration.ReplicaURL != ""
	if !safeReplicaURL(configuration.ReplicaURL) {
		configuration.ReplicaURL = ""
	}
	return configuration
}

// LoadConfiguration reads the last effective receipt or returns service defaults.
func LoadConfiguration(dataDir, attachmentsDir string) (Configuration, error) {
	recorded, err := readConfiguration(dataDir)
	if err == nil {
		recorded.DataDir = dataDir
		recorded.AttachmentsDir = attachmentsDir
		return normalizeConfiguration(recorded)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Configuration{}, err
	}
	return normalizeConfiguration(Configuration{
		DataDir: dataDir, AttachmentsDir: attachmentsDir,
	})
}

func readConfiguration(dataDir string) (Configuration, error) {
	content, err := os.ReadFile( //nolint:gosec // Fixed receipt name under the configured installation root.
		filepath.Join(dataDir, configurationReceipt),
	)
	if err != nil {
		return Configuration{}, err
	}
	var configuration Configuration
	if err = json.Unmarshal(content, &configuration); err != nil {
		return Configuration{}, errors.New("decode service configuration receipt")
	}
	return normalizeConfiguration(configuration)
}

func normalizeConfiguration(configuration Configuration) (Configuration, error) {
	if configuration.Version == 0 {
		configuration.Version = configurationVersion
	}
	if configuration.Version != configurationVersion {
		return Configuration{}, errors.New("service configuration version is unsupported")
	}
	if configuration.DataDir == "" {
		return Configuration{}, errors.New("service configuration data directory is required")
	}
	var err error
	configuration.DataDir, err = filepath.Abs(configuration.DataDir)
	if err != nil {
		return Configuration{}, fmt.Errorf("resolve service configuration data directory: %w", err)
	}
	if configuration.AttachmentsDir == "" {
		configuration.AttachmentsDir = filepath.Join(configuration.DataDir, "attachments")
	} else {
		configuration.AttachmentsDir, err = filepath.Abs(configuration.AttachmentsDir)
		if err != nil {
			return Configuration{}, fmt.Errorf(
				"resolve service configuration Attachment Store: %w",
				err,
			)
		}
	}
	if configuration.ListenAddress == "" {
		configuration.ListenAddress = "127.0.0.1:8080"
	}
	if configuration.ShutdownTimeout == 0 {
		configuration.ShutdownTimeout = 10 * time.Second
	}
	if configuration.ShutdownTimeout < 0 {
		return Configuration{}, errors.New("service configuration shutdown timeout is invalid")
	}
	if (configuration.TLSCertificate == "") != (configuration.TLSPrivateKey == "") {
		return Configuration{}, errors.New(
			"service configuration TLS certificate and private key must be configured together",
		)
	}
	for _, path := range []*string{
		&configuration.TLSCertificate,
		&configuration.TLSPrivateKey,
	} {
		if *path == "" {
			continue
		}
		*path, err = filepath.Abs(*path)
		if err != nil {
			return Configuration{}, fmt.Errorf("resolve service configuration host path: %w", err)
		}
	}
	for index, value := range configuration.TrustedProxies {
		prefix, parseErr := netip.ParsePrefix(value)
		if parseErr != nil {
			return Configuration{}, fmt.Errorf("parse service configuration trusted proxy: %w", parseErr)
		}
		configuration.TrustedProxies[index] = prefix.Masked().String()
	}
	slices.Sort(configuration.TrustedProxies)
	configuration.TrustedProxies = slices.Compact(configuration.TrustedProxies)
	if configuration.ReplicaURL != "" {
		if !safeReplicaURL(configuration.ReplicaURL) {
			return Configuration{}, errors.New(
				"service configuration replica URL must be a file URL without credentials, query, or fragment",
			)
		}
		configuration.ReplicationEnabled = true
	}
	return configuration, nil
}

func safeReplicaURL(value string) bool {
	if value == "" {
		return true
	}
	replica, err := url.Parse(value)
	return err == nil &&
		replica.Scheme == "file" &&
		replica.User == nil &&
		replica.RawQuery == "" &&
		replica.Fragment == ""
}

func configurationSHA256(configuration Configuration) (string, error) {
	normalized, err := normalizeConfiguration(configuration)
	if err != nil {
		return "", err
	}
	content, err := json.Marshal(normalized)
	if err != nil {
		return "", errors.New("encode service configuration")
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func compareConfigurations(
	backupConfiguration Configuration,
	target Configuration,
) ([]ConfigurationDifference, bool) {
	var differences []ConfigurationDifference
	requiresAcknowledgment := false
	add := func(
		field, backupValue, targetValue string,
		resolution ConfigurationResolution,
	) {
		if backupValue == targetValue {
			return
		}
		differences = append(differences, ConfigurationDifference{
			Field: field, BackupValue: backupValue, TargetValue: targetValue,
			Resolution: resolution,
		})
		if resolution == configurationAcknowledgmentRequired {
			requiresAcknowledgment = true
		}
	}
	add("data_dir", backupConfiguration.DataDir, target.DataDir, configurationMapped)
	add(
		"attachments_dir",
		backupConfiguration.AttachmentsDir,
		target.AttachmentsDir,
		configurationMapped,
	)
	addAcknowledged := func(field, backupValue, targetValue string) {
		add(
			field,
			backupValue,
			targetValue,
			configurationAcknowledgmentRequired,
		)
	}
	addAcknowledged(
		"tls_certificate",
		backupConfiguration.TLSCertificate,
		target.TLSCertificate,
	)
	addAcknowledged("tls_private_key", backupConfiguration.TLSPrivateKey, target.TLSPrivateKey)
	addAcknowledged("replica_url", backupConfiguration.ReplicaURL, target.ReplicaURL)
	addAcknowledged("listen_address", backupConfiguration.ListenAddress, target.ListenAddress)
	addAcknowledged("public_listen", backupConfiguration.PublicListen, target.PublicListen)
	addAcknowledged(
		"trusted_proxies",
		strings.Join(backupConfiguration.TrustedProxies, ","),
		strings.Join(target.TrustedProxies, ","),
	)
	addAcknowledged(
		"replication_enabled",
		strconv.FormatBool(backupConfiguration.ReplicationEnabled),
		strconv.FormatBool(target.ReplicationEnabled),
	)
	addAcknowledged(
		"shutdown_timeout",
		backupConfiguration.ShutdownTimeout.String(),
		target.ShutdownTimeout.String(),
	)
	addAcknowledged(
		"insecure_crew",
		strconv.FormatBool(backupConfiguration.InsecureCrew),
		strconv.FormatBool(target.InsecureCrew),
	)
	addAcknowledged(
		"insecure_display",
		strconv.FormatBool(backupConfiguration.InsecureDisplay),
		strconv.FormatBool(target.InsecureDisplay),
	)
	return differences, requiresAcknowledgment
}

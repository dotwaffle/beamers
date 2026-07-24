package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/dotwaffle/beamers/internal/store"
)

const restoreJournalVersion = 1

type restorePhase string

const (
	restorePrepared               restorePhase = "prepared"
	restoreDataQuarantined        restorePhase = "data_quarantined"
	restoreAttachmentsQuarantined restorePhase = "attachments_quarantined"
	restoreAttachmentsInstalled   restorePhase = "attachments_installed"
	restoreDataInstalled          restorePhase = "data_installed"
	restoreCommitted              restorePhase = "committed"
)

// RestorePlan is the exact non-overwriting filesystem replacement plan.
type RestorePlan struct {
	JournalPath           string   `json:"journal_path"`
	DataDir               string   `json:"data_dir"`
	AttachmentsDir        string   `json:"attachments_dir"`
	DataQuarantine        string   `json:"data_quarantine,omitempty"`
	AttachmentsQuarantine string   `json:"attachments_quarantine,omitempty"`
	ReplacesData          bool     `json:"replaces_data"`
	ReplacesAttachments   bool     `json:"replaces_attachments"`
	ForcedUnsupported     bool     `json:"forced_unsupported"`
	ForceReason           string   `json:"force_reason,omitempty"`
	UnknownSchemaElements []string `json:"unknown_schema_elements,omitempty"`
	Manifest              Manifest `json:"manifest"`
}

// ApplyOptions carries the repeated safeguard for a forced unsupported Restore.
type ApplyOptions struct {
	AcknowledgeUnsupportedRisks bool
}

type restoreJournal struct {
	Version                   int          `json:"version"`
	Phase                     restorePhase `json:"phase"`
	Plan                      RestorePlan  `json:"plan"`
	StagingRoot               string       `json:"staging_root"`
	StagedData                string       `json:"staged_data"`
	StagedAttachments         string       `json:"staged_attachments"`
	ExternalAttachments       bool         `json:"external_attachments"`
	DataQuarantineRoot        string       `json:"data_quarantine_root,omitempty"`
	AttachmentsQuarantineRoot string       `json:"attachments_quarantine_root,omitempty"`
}

// PrepareRestore verifies and stages a Backup, then persists its exact cutover plan.
func PrepareRestore(
	ctx context.Context,
	input RestoreInput,
) (_ RestorePlan, returnErr error) {
	if input.InputPath == "" || input.DataDir == "" {
		return RestorePlan{}, errors.New("Restore input and data directory are required")
	}
	input.ForceReason = strings.TrimSpace(input.ForceReason)
	if input.ForceUnsupported &&
		(input.ForceReason == "" || !input.AcknowledgeUnsupportedRisks) {
		return RestorePlan{}, errors.New(
			"forced unsupported Restore requires a reason and acknowledgment that it makes no safety claim",
		)
	}
	absoluteInput, pathErr := filepath.Abs(input.InputPath)
	if pathErr != nil {
		return RestorePlan{}, fmt.Errorf("resolve Restore input: %w", pathErr)
	}
	input.InputPath = absoluteInput
	absoluteDataDir, pathErr := filepath.Abs(input.DataDir)
	if pathErr != nil {
		return RestorePlan{}, fmt.Errorf("resolve Restore data directory: %w", pathErr)
	}
	input.DataDir = absoluteDataDir
	defaultAttachments := filepath.Join(input.DataDir, "attachments")
	if input.AttachmentsDir == "" {
		input.AttachmentsDir = defaultAttachments
	} else {
		absoluteAttachments, attachmentsPathErr := filepath.Abs(input.AttachmentsDir)
		if attachmentsPathErr != nil {
			return RestorePlan{}, fmt.Errorf(
				"resolve Restore Attachment Store: %w",
				attachmentsPathErr,
			)
		}
		input.AttachmentsDir = absoluteAttachments
	}
	externalAttachments := filepath.Clean(input.AttachmentsDir) != filepath.Clean(defaultAttachments)
	if externalAttachments && pathsOverlap(input.DataDir, input.AttachmentsDir) {
		return RestorePlan{}, errors.New(
			"Restore data directory and external Attachment Store must not overlap",
		)
	}

	journalPath := input.DataDir + ".beamers-restore.json"
	if journalErr := requireAbsent(journalPath, "Restore journal"); journalErr != nil {
		return RestorePlan{}, journalErr
	}
	replacesData, inspectErr := pathExists(input.DataDir)
	if inspectErr != nil {
		return RestorePlan{}, fmt.Errorf("inspect Restore data directory: %w", inspectErr)
	}
	replacesAttachments := false
	if externalAttachments {
		found, attachmentsInspectErr := pathExists(input.AttachmentsDir)
		if attachmentsInspectErr != nil {
			return RestorePlan{}, fmt.Errorf(
				"inspect Restore Attachment Store: %w",
				attachmentsInspectErr,
			)
		}
		replacesAttachments = found
	}
	if !input.Replace && (replacesData || replacesAttachments) {
		return RestorePlan{}, errors.New("Restore destination already exists; preview replacement first")
	}

	stagingRoot, stagingErr := os.MkdirTemp(
		filepath.Dir(input.DataDir),
		"."+filepath.Base(input.DataDir)+".beamers-restore-*",
	)
	if stagingErr != nil {
		return RestorePlan{}, fmt.Errorf("create Restore staging directory: %w", stagingErr)
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, os.RemoveAll(stagingRoot))
		}
	}()
	stagedArchive := filepath.Join(stagingRoot, "archive")
	if copyErr := copyFileExclusive(input.InputPath, stagedArchive); copyErr != nil {
		return RestorePlan{}, copyErr
	}
	manifest, verifyErr := Verify(stagedArchive)
	if verifyErr != nil {
		return RestorePlan{}, verifyErr
	}

	stagedData := filepath.Join(stagingRoot, "data")
	if mkdirErr := os.Mkdir(stagedData, 0o700); mkdirErr != nil {
		return RestorePlan{}, errors.New("create Restore data staging")
	}
	stagedAttachments := filepath.Join(stagedData, "attachments")
	if externalAttachments {
		externalStaging, externalStagingErr := os.MkdirTemp(
			filepath.Dir(input.AttachmentsDir),
			"."+filepath.Base(input.AttachmentsDir)+".beamers-restore-*",
		)
		if externalStagingErr != nil {
			return RestorePlan{}, fmt.Errorf(
				"create Restore Attachment staging: %w",
				externalStagingErr,
			)
		}
		stagedAttachments = externalStaging
		defer func() {
			if returnErr != nil {
				returnErr = errors.Join(returnErr, os.RemoveAll(stagedAttachments))
			}
		}()
	}
	if extractErr := extractRestore(
		stagedArchive,
		stagedData,
		stagedAttachments,
		manifest,
	); extractErr != nil {
		return RestorePlan{}, extractErr
	}
	compatibilityErr := validateCompatibility(manifest)
	validationErr := store.ValidateSnapshot(
		ctx,
		filepath.Join(stagedData, "beamers.db"),
	)
	var unsupported store.UnsupportedSnapshotInspection
	switch {
	case compatibilityErr == nil && validationErr == nil:
		if input.ForceUnsupported {
			return RestorePlan{}, errors.New("Restore is supported; force option is not permitted")
		}
	case !input.ForceUnsupported:
		return RestorePlan{}, errors.Join(compatibilityErr, validationErr)
	default:
		unsupported, validationErr = store.InspectUnsupportedSnapshot(
			ctx,
			filepath.Join(stagedData, "beamers.db"),
		)
		if validationErr != nil {
			return RestorePlan{}, validationErr
		}
		if unsupported.SchemaVersion != manifest.SchemaVersion {
			return RestorePlan{}, errors.New(
				"backup manifest schema version does not match its database copy",
			)
		}
	}
	if markerErr := createInstallationMarker(stagedData); markerErr != nil {
		return RestorePlan{}, markerErr
	}
	if syncErr := syncTreeDirectories(stagedData); syncErr != nil {
		return RestorePlan{}, syncErr
	}
	if externalAttachments {
		if syncErr := syncTreeDirectories(stagedAttachments); syncErr != nil {
			return RestorePlan{}, syncErr
		}
	}

	var dataQuarantineRoot, attachmentsQuarantineRoot string
	if replacesData {
		reserved, reserveErr := os.MkdirTemp(
			filepath.Dir(input.DataDir),
			"."+filepath.Base(input.DataDir)+".beamers-quarantine-*",
		)
		if reserveErr != nil {
			return RestorePlan{}, fmt.Errorf("reserve Restore data quarantine: %w", reserveErr)
		}
		dataQuarantineRoot = reserved
		defer func() {
			if returnErr != nil {
				returnErr = errors.Join(returnErr, os.RemoveAll(dataQuarantineRoot))
			}
		}()
	}
	if replacesAttachments {
		reserved, reserveErr := os.MkdirTemp(
			filepath.Dir(input.AttachmentsDir),
			"."+filepath.Base(input.AttachmentsDir)+".beamers-quarantine-*",
		)
		if reserveErr != nil {
			return RestorePlan{}, fmt.Errorf(
				"reserve Restore Attachment quarantine: %w",
				reserveErr,
			)
		}
		attachmentsQuarantineRoot = reserved
		defer func() {
			if returnErr != nil {
				returnErr = errors.Join(returnErr, os.RemoveAll(attachmentsQuarantineRoot))
			}
		}()
	}
	plan := RestorePlan{
		JournalPath:           journalPath,
		DataDir:               input.DataDir,
		AttachmentsDir:        input.AttachmentsDir,
		ReplacesData:          replacesData,
		ReplacesAttachments:   replacesAttachments,
		ForcedUnsupported:     input.ForceUnsupported,
		ForceReason:           input.ForceReason,
		UnknownSchemaElements: unsupported.UnknownSchemaElements,
		Manifest:              manifest,
	}
	if replacesData {
		plan.DataQuarantine = filepath.Join(dataQuarantineRoot, "original")
	}
	if replacesAttachments {
		plan.AttachmentsQuarantine = filepath.Join(attachmentsQuarantineRoot, "original")
	}
	journal := restoreJournal{
		Version:                   restoreJournalVersion,
		Phase:                     restorePrepared,
		Plan:                      plan,
		StagingRoot:               stagingRoot,
		StagedData:                stagedData,
		StagedAttachments:         stagedAttachments,
		ExternalAttachments:       externalAttachments,
		DataQuarantineRoot:        dataQuarantineRoot,
		AttachmentsQuarantineRoot: attachmentsQuarantineRoot,
	}
	if journalErr := createRestoreJournal(journalPath, journal); journalErr != nil {
		return RestorePlan{}, journalErr
	}
	returnErr = nil
	return plan, nil
}

// ApplyRestore executes one prepared journal and proves the installed state ready.
func ApplyRestore(ctx context.Context, journalPath string) (Manifest, error) {
	return applyRestoreWithOptions(ctx, journalPath, ApplyOptions{}, nil)
}

// ApplyRestoreWithOptions executes a prepared forced Restore with repeated acknowledgment.
func ApplyRestoreWithOptions(
	ctx context.Context,
	journalPath string,
	options ApplyOptions,
) (Manifest, error) {
	return applyRestoreWithOptions(ctx, journalPath, options, nil)
}

func applyRestore(
	ctx context.Context,
	journalPath string,
	afterPhase func(restorePhase) error,
) (Manifest, error) {
	return applyRestoreWithOptions(ctx, journalPath, ApplyOptions{}, afterPhase)
}

func applyRestoreWithOptions(
	ctx context.Context,
	journalPath string,
	options ApplyOptions,
	afterPhase func(restorePhase) error,
) (Manifest, error) {
	journal, err := readRestoreJournal(journalPath)
	if err != nil {
		return Manifest{}, err
	}
	if journal.Phase != restorePrepared {
		return Manifest{}, errors.New("Restore journal contains an interrupted cutover; recover it first")
	}
	if journal.Plan.ForcedUnsupported && !options.AcknowledgeUnsupportedRisks {
		return Manifest{}, errors.New(
			"forced unsupported Restore requires repeated acknowledgment that it makes no safety claim",
		)
	}
	if err = validatePreparedRestore(ctx, journal); err != nil {
		return Manifest{}, err
	}
	if journal.Plan.ReplacesData {
		current, openErr := store.Open(ctx, journal.Plan.DataDir)
		if openErr != nil {
			return Manifest{}, openErr
		}
		if closeErr := current.Close(); closeErr != nil {
			return Manifest{}, closeErr
		}
	}
	fail := func(cause error) (Manifest, error) {
		return Manifest{}, errors.Join(cause, rollbackRestore(journal))
	}
	advance := func(phase restorePhase) error {
		journal.Phase = phase
		if updateErr := updateRestoreJournal(journalPath, journal); updateErr != nil {
			return updateErr
		}
		if afterPhase != nil {
			return afterPhase(phase)
		}
		return nil
	}

	if journal.Plan.ReplacesData {
		if err = renameAndSync(journal.Plan.DataDir, journal.Plan.DataQuarantine); err != nil {
			return fail(fmt.Errorf("quarantine current installation: %w", err))
		}
	}
	if advanceErr := advance(restoreDataQuarantined); advanceErr != nil {
		return Manifest{}, advanceErr
	}
	if journal.Plan.ReplacesAttachments {
		if err = renameAndSync(
			journal.Plan.AttachmentsDir,
			journal.Plan.AttachmentsQuarantine,
		); err != nil {
			return fail(fmt.Errorf("quarantine current Attachment Store: %w", err))
		}
	}
	if advanceErr := advance(restoreAttachmentsQuarantined); advanceErr != nil {
		return Manifest{}, advanceErr
	}
	if journal.ExternalAttachments {
		if err = renameAndSync(journal.StagedAttachments, journal.Plan.AttachmentsDir); err != nil {
			return fail(fmt.Errorf("install restored Attachment Store: %w", err))
		}
	}
	if advanceErr := advance(restoreAttachmentsInstalled); advanceErr != nil {
		return Manifest{}, advanceErr
	}
	if err = renameAndSync(journal.StagedData, journal.Plan.DataDir); err != nil {
		return fail(fmt.Errorf("install restored data directory: %w", err))
	}
	if advanceErr := advance(restoreDataInstalled); advanceErr != nil {
		return Manifest{}, advanceErr
	}

	if journal.Plan.ForcedUnsupported {
		inspection, inspectErr := store.InspectUnsupportedSnapshot(
			ctx,
			filepath.Join(journal.Plan.DataDir, "beamers.db"),
		)
		if inspectErr != nil || inspection.SchemaVersion != journal.Plan.Manifest.SchemaVersion {
			return fail(errors.Join(
				inspectErr,
				errors.New("installed unsupported Restore copy changed during cutover"),
			))
		}
	} else {
		installed, openErr := store.Open(ctx, journal.Plan.DataDir)
		if openErr != nil {
			return fail(openErr)
		}
		readyErr := installed.StartupError()
		if readyErr == nil {
			readyErr = installed.Ready(ctx)
		}
		readyErr = errors.Join(readyErr, installed.Close())
		if readyErr != nil {
			return fail(readyErr)
		}
	}
	if err = advance(restoreCommitted); err != nil {
		return fail(err)
	}
	if err := cleanupCommittedRestore(journal); err != nil {
		return Manifest{}, err
	}
	return journal.Plan.Manifest, nil
}

func validatePreparedRestore(ctx context.Context, journal restoreJournal) error {
	manifest, err := Verify(filepath.Join(journal.StagingRoot, "archive"))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(manifest, journal.Plan.Manifest) {
		return errors.New("staged Restore archive no longer matches its preview")
	}
	databasePath := filepath.Join(journal.StagedData, "beamers.db")
	databaseHash, err := fileSHA256(databasePath)
	if err != nil || databaseHash != manifest.DatabaseSHA256 {
		return errors.Join(err, errors.New("staged Restore database integrity check failed"))
	}
	if err = validateStagedAttachments(journal.StagedAttachments, manifest.Attachments); err != nil {
		return err
	}
	if journal.Plan.ForcedUnsupported {
		inspection, inspectErr := store.InspectUnsupportedSnapshot(ctx, databasePath)
		if inspectErr != nil ||
			inspection.SchemaVersion != manifest.SchemaVersion ||
			!slices.Equal(
				inspection.UnknownSchemaElements,
				journal.Plan.UnknownSchemaElements,
			) {
			return errors.Join(
				inspectErr,
				errors.New("staged unsupported Restore no longer matches its preview"),
			)
		}
		return nil
	}
	return errors.Join(validateCompatibility(manifest), store.ValidateSnapshot(ctx, databasePath))
}

func validateStagedAttachments(root string, attachments []Attachment) error {
	expected := make(map[string]Attachment, len(attachments))
	for _, attachment := range attachments {
		expected[filepath.Clean(attachment.StorageKey)] = attachment
		if err := verifyAttachment(root, attachment); err != nil {
			return err
		}
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) && len(expected) == 0 {
				return fs.SkipAll
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil || !entry.Type().IsRegular() {
			return errors.New("staged Restore contains an invalid Attachment")
		}
		if _, ok := expected[relative]; !ok {
			return errors.New("staged Restore contains an unreferenced Attachment")
		}
		delete(expected, relative)
		return nil
	})
	if err != nil {
		return err
	}
	if len(expected) != 0 {
		return errors.New("staged Restore is missing an Attachment")
	}
	return nil
}

// RecoverRestore rolls back an interrupted cutover before an installation opens.
func RecoverRestore(dataDir string) error {
	absoluteDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve Restore recovery path: %w", err)
	}
	journalPath := absoluteDataDir + ".beamers-restore.json"
	journal, err := readRestoreJournal(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if journal.Phase == restoreCommitted {
		return cleanupCommittedRestore(journal)
	}
	started, err := restoreCutoverStarted(journal)
	if err != nil {
		return err
	}
	if !started {
		return nil
	}
	return rollbackRestore(journal)
}

func validateCompatibility(manifest Manifest) error {
	current, err := store.CurrentSchemaVersion()
	if err != nil {
		return err
	}
	if current < manifest.MinimumReaderSchemaVersion ||
		current < manifest.MinimumWriterSchemaVersion ||
		current != manifest.SchemaVersion {
		return fmt.Errorf(
			"Restore schema %d requires reader %d and writer %d; executable supports schema %d",
			manifest.SchemaVersion,
			manifest.MinimumReaderSchemaVersion,
			manifest.MinimumWriterSchemaVersion,
			current,
		)
	}
	return nil
}

func rollbackRestore(journal restoreJournal) error {
	var rollbackErr error
	if err := restoreOriginal(
		journal.Plan.DataDir,
		journal.StagedData,
		journal.Plan.DataQuarantine,
		journal.Plan.ReplacesData,
	); err != nil {
		rollbackErr = errors.Join(rollbackErr, fmt.Errorf("roll back installation: %w", err))
	}
	if journal.ExternalAttachments {
		if err := restoreOriginal(
			journal.Plan.AttachmentsDir,
			journal.StagedAttachments,
			journal.Plan.AttachmentsQuarantine,
			journal.Plan.ReplacesAttachments,
		); err != nil {
			rollbackErr = errors.Join(
				rollbackErr,
				fmt.Errorf("roll back Attachment Store: %w", err),
			)
		}
	}
	if rollbackErr != nil {
		return rollbackErr
	}
	return cleanupRolledBackRestore(journal)
}

func restoreOriginal(destination, staging, quarantine string, replaced bool) error {
	quarantined, err := pathExists(quarantine)
	if err != nil {
		return err
	}
	destinationExists, err := pathExists(destination)
	if err != nil {
		return err
	}
	stagingExists, err := pathExists(staging)
	if err != nil {
		return err
	}
	if quarantined {
		if destinationExists {
			if stagingExists {
				return errors.New("both restored destination and staging exist")
			}
			if err := renameAndSync(destination, staging); err != nil {
				return err
			}
		}
		return renameAndSync(quarantine, destination)
	}
	if !replaced && destinationExists && !stagingExists {
		return renameAndSync(destination, staging)
	}
	return nil
}

func restoreCutoverStarted(journal restoreJournal) (bool, error) {
	if journal.Phase != restorePrepared {
		return true, nil
	}
	for _, path := range []string{
		journal.Plan.DataQuarantine,
		journal.Plan.AttachmentsQuarantine,
	} {
		exists, err := pathExists(path)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}
	stagedDataExists, err := pathExists(journal.StagedData)
	if err != nil {
		return false, err
	}
	if !stagedDataExists {
		return true, nil
	}
	if journal.ExternalAttachments {
		stagedAttachmentsExist, err := pathExists(journal.StagedAttachments)
		if err != nil {
			return false, err
		}
		if !stagedAttachmentsExist {
			return true, nil
		}
	}
	return false, nil
}

func cleanupCommittedRestore(journal restoreJournal) error {
	cleanupErr := os.RemoveAll(journal.StagingRoot)
	if journal.ExternalAttachments {
		cleanupErr = errors.Join(cleanupErr, os.RemoveAll(journal.StagedAttachments))
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return removeJournal(journal.Plan.JournalPath)
}

func cleanupRolledBackRestore(journal restoreJournal) error {
	cleanupErr := os.RemoveAll(journal.StagingRoot)
	if journal.ExternalAttachments {
		cleanupErr = errors.Join(cleanupErr, os.RemoveAll(journal.StagedAttachments))
	}
	if journal.DataQuarantineRoot != "" {
		cleanupErr = errors.Join(cleanupErr, os.RemoveAll(journal.DataQuarantineRoot))
	}
	if journal.AttachmentsQuarantineRoot != "" {
		cleanupErr = errors.Join(cleanupErr, os.RemoveAll(journal.AttachmentsQuarantineRoot))
	}
	if cleanupErr != nil {
		return cleanupErr
	}
	return removeJournal(journal.Plan.JournalPath)
}

func createRestoreJournal(path string, journal restoreJournal) (returnErr error) {
	file, err := os.OpenFile( //nolint:gosec // Host-authorized adjacent journal path.
		path,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("create Restore journal: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	if err = json.NewEncoder(file).Encode(journal); err != nil {
		return fmt.Errorf("encode Restore journal: %w", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Errorf("sync Restore journal: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func updateRestoreJournal(path string, journal restoreJournal) (returnErr error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".beamers-restore-journal-*")
	if err != nil {
		return fmt.Errorf("create Restore journal update: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, removeErr)
		}
	}()
	if err = temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure Restore journal update: %w", err)
	}
	if err = json.NewEncoder(temporary).Encode(journal); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encode Restore journal update: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync Restore journal update: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close Restore journal update: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install Restore journal update: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func readRestoreJournal(path string) (restoreJournal, error) {
	file, err := os.Open(path) //nolint:gosec // Host-authorized adjacent journal path.
	if err != nil {
		return restoreJournal{}, err
	}
	defer func() {
		_ = file.Close()
	}()
	var journal restoreJournal
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&journal); err != nil {
		return restoreJournal{}, fmt.Errorf("decode Restore journal: %w", err)
	}
	if journal.Version != restoreJournalVersion ||
		journal.Plan.JournalPath != path ||
		journal.Plan.DataDir == "" ||
		journal.StagingRoot == "" ||
		journal.StagedData == "" {
		return restoreJournal{}, errors.New("Restore journal is invalid")
	}
	return journal, nil
}

func removeJournal(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Restore journal: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func renameAndSync(source, destination string) error {
	if err := os.Rename(source, destination); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(source)); err != nil {
		return err
	}
	if filepath.Dir(destination) != filepath.Dir(source) {
		return syncDirectory(filepath.Dir(destination))
	}
	return nil
}

func createInstallationMarker(dataDir string) error {
	marker, err := os.OpenFile( //nolint:gosec // Process-owned staged installation path.
		filepath.Join(dataDir, ".beamers.lock"),
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return errors.New("create restored installation marker")
	}
	if err = marker.Sync(); err != nil {
		_ = marker.Close()
		return errors.New("sync restored installation marker")
	}
	if err = marker.Close(); err != nil {
		return errors.New("close restored installation marker")
	}
	return nil
}

func syncTreeDirectories(root string) error {
	directories := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("inspect Restore staging: %w", err)
	}
	slices.Reverse(directories)
	for _, directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path) //nolint:gosec // Process-owned Restore path.
	if err != nil {
		return fmt.Errorf("open Restore directory for sync: %w", err)
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func pathExists(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func pathsOverlap(first, second string) bool {
	for _, pair := range [][2]string{{first, second}, {second, first}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != "." &&
			relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
		if err == nil && relative == "." {
			return true
		}
	}
	return false
}

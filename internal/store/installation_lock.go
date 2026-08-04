package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"syscall"
)

type installationLock struct {
	file *os.File
}

// ExclusiveAccessMarker returns the stable lock path adjacent to one owned root.
func ExclusiveAccessMarker(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve exclusive access root: %w", err)
	}
	return absolute + accessLockSuffix, nil
}

// UsesExternalAttachments reports whether Attachments live outside their default root.
func UsesExternalAttachments(dataDir, attachmentsDir string) bool {
	return attachmentsDir != "" &&
		filepath.Clean(attachmentsDir) != filepath.Clean(filepath.Join(dataDir, "attachments"))
}

// HoldExclusiveAccess locks configured roots without placing markers inside them.
func HoldExclusiveAccess(roots ...string) (func() error, error) {
	markers := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		marker, err := ExclusiveAccessMarker(root)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[marker]; duplicate {
			continue
		}
		seen[marker] = struct{}{}
		markers = append(markers, marker)
	}
	sort.Strings(markers)

	locks := make([]*installationLock, 0, len(markers))
	release := func() error {
		var releaseErr error
		for _, lock := range slices.Backward(locks) {
			releaseErr = errors.Join(releaseErr, lock.close())
		}
		return releaseErr
	}
	for _, marker := range markers {
		file, err := os.OpenFile( //nolint:gosec // Host-configured root selects its adjacent lock.
			marker,
			os.O_CREATE|os.O_RDWR,
			0o600,
		)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("open exclusive access marker: %w", err), release())
		}
		lock, err := lockInstallationFile(file)
		if err != nil {
			return nil, errors.Join(err, release())
		}
		locks = append(locks, lock)
	}
	return release, nil
}

func createInstallationLock(dataDir string) (*installationLock, error) {
	path := filepath.Join(dataDir, lockFilename)
	// The operator-selected data directory is the intended filesystem boundary.
	//nolint:gosec // Opening its fixed lock filename is required installation behavior.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrAlreadyInitialized
	}
	if err != nil {
		return nil, fmt.Errorf("open installation lock: %w", err)
	}
	return lockInstallationFile(file)
}

func openInstallationLock(dataDir string) (*installationLock, error) {
	path := filepath.Join(dataDir, lockFilename)
	//nolint:gosec // requireInstallationMarker verified this fixed path immediately before use.
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open installation lock: %w", err)
	}
	return lockInstallationFile(file)
}

func lockInstallationFile(file *os.File) (*installationLock, error) {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(ErrInstallationInUse, fmt.Errorf("lock installation: %w", err), file.Close())
	}
	return &installationLock{file: file}, nil
}

func (lock *installationLock) sync() error {
	if err := lock.file.Sync(); err != nil {
		return fmt.Errorf("sync installation marker: %w", err)
	}
	return nil
}

func (lock *installationLock) close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(unlockErr, closeErr)
}

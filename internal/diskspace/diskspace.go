// Package diskspace reports and preflights free filesystem capacity.
package diskspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Usage is the free and total capacity of the filesystem containing one path.
type Usage struct {
	FreeBytes  uint64
	TotalBytes uint64
}

// Stat reports the free and total capacity of the filesystem containing path.
func Stat(path string) (Usage, error) {
	if path == "" {
		return Usage{}, errors.New("diskspace path is required")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return Usage{}, fmt.Errorf("stat filesystem for %s: %w", path, err)
	}
	blockSize := uint64(stat.Bsize) //nolint:gosec // Bsize is a positive block size reported by the kernel.
	return Usage{
		FreeBytes:  stat.Bavail * blockSize,
		TotalBytes: stat.Blocks * blockSize,
	}, nil
}

// DirSize sums the apparent size of every regular file under root. A
// missing root reports zero rather than an error, since callers use this
// to size optional or not-yet-created directories such as an Attachment
// Store. Errors walking an existing tree are reported.
func DirSize(root string) (uint64, error) {
	if root == "" {
		return 0, nil
	}
	var total uint64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.Type().IsRegular() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			total += uint64(max(info.Size(), 0))
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("measure directory size for %s: %w", root, err)
	}
	return total, nil
}

// FileSize reports the size of one regular file. A missing file reports
// zero rather than an error, matching DirSize's not-yet-created contract.
func FileSize(path string) (uint64, error) {
	if path == "" {
		return 0, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	return uint64(max(info.Size(), 0)), nil
}

// ErrInsufficientSpace reports that a filesystem lacks the required headroom.
var ErrInsufficientSpace = errors.New("insufficient free disk space")

// RequireFree refuses with ErrInsufficientSpace when the filesystem
// containing path has less than neededBytes of free capacity.
func RequireFree(path string, neededBytes uint64) error {
	usage, err := Stat(path)
	if err != nil {
		return err
	}
	if usage.FreeBytes < neededBytes {
		return fmt.Errorf(
			"%w: %s has %d bytes free, %d bytes required",
			ErrInsufficientSpace,
			path,
			usage.FreeBytes,
			neededBytes,
		)
	}
	return nil
}

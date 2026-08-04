// Package diskspace reports and preflights free filesystem capacity.
package diskspace

import (
	"errors"
	"fmt"

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
	blockSize := uint64(stat.Bsize) //nolint:unconvert // Bsize is platform-dependent (int32 or int64).
	return Usage{
		FreeBytes:  stat.Bavail * blockSize,
		TotalBytes: stat.Blocks * blockSize,
	}, nil
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

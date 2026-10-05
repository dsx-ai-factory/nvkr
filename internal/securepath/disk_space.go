package securepath

import (
	"context"
	"fmt"
	"math"
	"os"
)

type diskSpaceFacts struct {
	blockSize    int64
	available    uint64
	readOnly     bool
	memoryBacked bool
}

// AvailableDiskBackedBytes observes available bytes on the filesystem of a
// held directory, rejecting detected memory-backed or read-only filesystems.
// This is point-in-time headroom, not a reservation or artifact authority.
func AvailableDiskBackedBytes(ctx context.Context, directory *os.File) (uint64, error) {
	return availableDiskBackedBytes(ctx, directory, observeDiskSpace)
}

func availableDiskBackedBytes(ctx context.Context, directory *os.File, observe func(int) (diskSpaceFacts, error)) (uint64, error) {
	if ctx == nil || directory == nil || observe == nil {
		return 0, fmt.Errorf("disk space observation requires context and a held directory")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	info, err := directory.Stat()
	if err != nil {
		return 0, fmt.Errorf("holding scratch directory: %w", err)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("disk space observation requires an open directory")
	}
	facts, err := observe(int(directory.Fd()))
	if err != nil {
		return 0, fmt.Errorf("observing held scratch filesystem: %w", err)
	}
	if facts.readOnly || facts.memoryBacked || facts.blockSize <= 0 {
		return 0, fmt.Errorf("scratch filesystem is read-only, memory-backed or reports an invalid block size")
	}
	blockSize := uint64(facts.blockSize)
	if facts.available > math.MaxUint64/blockSize {
		return 0, fmt.Errorf("scratch filesystem capacity exceeds the supported range")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return facts.available * blockSize, nil
}

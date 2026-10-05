package securepath

import (
	"context"
	"fmt"
	"math"
	"os"
)

// FilesystemSpace is a point-in-time statfs description, not a reservation or
// proof of physical storage. Admission owners decide whether memory-backed
// scratch is suitable for their operation.
type FilesystemSpace struct {
	blockSize    int64
	available    uint64
	readOnly     bool
	memoryBacked bool
}

// ReadOnly reports the filesystem's observed read-only flag.
func (facts FilesystemSpace) ReadOnly() bool { return facts.readOnly }

// AvailableBytes converts available allocation units with checked arithmetic.
func (facts FilesystemSpace) AvailableBytes() (uint64, error) {
	if facts.blockSize <= 0 {
		return 0, fmt.Errorf("scratch filesystem reports an invalid block size")
	}
	blockSize := uint64(facts.blockSize)
	if facts.available > math.MaxUint64/blockSize {
		return 0, fmt.Errorf("scratch filesystem capacity exceeds the supported range")
	}
	return facts.available * blockSize, nil
}

// AvailableDiskBackedBytes observes available bytes on the filesystem of a
// held directory, rejecting detected memory-backed or read-only filesystems.
// This is point-in-time headroom, not a reservation or artifact authority.
func AvailableDiskBackedBytes(ctx context.Context, directory *os.File) (uint64, error) {
	return availableDiskBackedBytes(ctx, directory, observeDiskSpace)
}

func availableDiskBackedBytes(ctx context.Context, directory *os.File, observe func(int) (FilesystemSpace, error)) (uint64, error) {
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
	available, err := facts.AvailableBytes()
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return available, nil
}

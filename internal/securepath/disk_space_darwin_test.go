//go:build darwin

package securepath

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiskScratchClassifiesDarwinFilesystem(t *testing.T) {
	for _, filesystem := range []string{"apfs", "tmpfs", "mfs", "ramfs"} {
		t.Run(filesystem, func(t *testing.T) {
			stat := unix.Statfs_t{Flags: unix.MNT_RDONLY, Bsize: 4096, Bavail: 10}
			copy(stat.Fstypename[:], filesystem)
			facts := diskSpaceFactsFromStat(stat)
			if !facts.readOnly || facts.memoryBacked != (filesystem != "apfs") || facts.blockSize != 4096 || facts.available != 10 {
				t.Fatalf("wrong syscall projection: %+v", facts)
			}
		})
	}
}

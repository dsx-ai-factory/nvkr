//go:build darwin

package securepath

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiskScratchClassifiesDarwinFilesystem(t *testing.T) {
	for _, filesystem := range []string{"apfs", "tmpfs", "mfs", "ramfs"} {
		t.Run(filesystem, func(t *testing.T) {
			for _, flags := range []uint32{0, unix.MNT_RDONLY} {
				stat := unix.Statfs_t{Flags: flags, Bsize: 4096, Bavail: 10}
				copy(stat.Fstypename[:], filesystem)
				facts := FilesystemSpaceFromStat(stat)
				if facts.readOnly != (flags != 0) || facts.ReadOnly() != facts.readOnly || facts.memoryBacked != (filesystem != "apfs") || facts.blockSize != 4096 || facts.available != 10 {
					t.Fatalf("wrong syscall projection: %+v", facts)
				}
			}
		})
	}
}

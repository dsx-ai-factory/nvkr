//go:build linux

package securepath

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiskScratchClassifiesLinuxFilesystem(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		typeID int64
		memory bool
	}{
		{name: "ext", typeID: unix.EXT4_SUPER_MAGIC},
		{name: "tmpfs", typeID: unix.TMPFS_MAGIC, memory: true},
		{name: "ramfs", typeID: unix.RAMFS_MAGIC, memory: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			facts := diskSpaceFactsFromStat(unix.Statfs_t{Type: scenario.typeID, Flags: unix.ST_RDONLY, Bsize: 4096, Bavail: 10})
			if !facts.readOnly || facts.memoryBacked != scenario.memory || facts.blockSize != 4096 || facts.available != 10 {
				t.Fatalf("wrong syscall projection: %+v", facts)
			}
		})
	}
}

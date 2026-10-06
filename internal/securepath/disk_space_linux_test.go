//go:build linux

package securepath

import (
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiskScratchClassifiesLinuxFilesystem(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		typeID uint64
		memory bool
	}{
		{name: "ext", typeID: unix.EXT4_SUPER_MAGIC},
		{name: "tmpfs", typeID: unix.TMPFS_MAGIC, memory: true},
		{name: "ramfs", typeID: unix.RAMFS_MAGIC, memory: true},
		{name: "overlay backing unknown", typeID: unix.OVERLAYFS_SUPER_MAGIC},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			for _, readOnly := range []bool{false, true} {
				var stat unix.Statfs_t
				// Statfs.Type is signed on some architectures and unsigned on
				// others. Reflection sets the native-width syscall field without
				// assuming a 64-bit test target or changing its magic bits.
				field := reflect.ValueOf(&stat.Type).Elem()
				if field.Kind() == reflect.Uint32 || field.Kind() == reflect.Uint64 {
					field.SetUint(scenario.typeID)
				} else {
					field.SetInt(int64(scenario.typeID))
				}
				if readOnly {
					stat.Flags = unix.ST_RDONLY
				}
				stat.Bsize, stat.Bavail = 4096, 10
				facts := FilesystemSpaceFromStat(stat)
				if facts.readOnly != readOnly || facts.ReadOnly() != readOnly || facts.memoryBacked != scenario.memory || facts.blockSize != 4096 || facts.available != 10 {
					t.Fatalf("wrong syscall projection: %+v", facts)
				}
			}
		})
	}
}

func TestLinuxFilesystemUsesAllocationFragmentSize(t *testing.T) {
	for _, scenario := range []struct {
		name string
		stat unix.Statfs_t
		want uint64
	}{
		{name: "fragment size", stat: unix.Statfs_t{Frsize: 1024, Bsize: 4096, Bavail: 10}, want: 10240},
		{name: "fallback block size", stat: unix.Statfs_t{Bsize: 4096, Bavail: 10}, want: 40960},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			got, err := FilesystemSpaceFromStat(scenario.stat).AvailableBytes()
			if err != nil || got != scenario.want {
				t.Fatalf("available bytes=%d, err=%v; want %d", got, err, scenario.want)
			}
		})
	}
}

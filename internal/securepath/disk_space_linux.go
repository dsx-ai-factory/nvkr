//go:build linux

package securepath

import "golang.org/x/sys/unix"

func observeDiskSpace(fd int) (FilesystemSpace, error) {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return FilesystemSpace{}, err
	}
	return FilesystemSpaceFromStat(stat), nil
}

// FilesystemSpaceFromStat projects Linux allocation facts. Bavail is counted in
// Frsize units; older filesystems that report no fragment size use Bsize.
// Overlay filesystems do not reveal the backing mount's storage class here.
func FilesystemSpaceFromStat(stat unix.Statfs_t) FilesystemSpace {
	blockSize := int64(stat.Frsize)
	if blockSize == 0 {
		blockSize = int64(stat.Bsize)
	}
	return FilesystemSpace{blockSize: blockSize, available: uint64(stat.Bavail),
		readOnly:     uint64(stat.Flags)&uint64(unix.ST_RDONLY) != 0,
		memoryBacked: uint32(stat.Type) == uint32(unix.TMPFS_MAGIC) || uint32(stat.Type) == uint32(unix.RAMFS_MAGIC)}
}

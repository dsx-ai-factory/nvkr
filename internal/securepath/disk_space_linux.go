//go:build linux

package securepath

import "golang.org/x/sys/unix"

func observeDiskSpace(fd int) (diskSpaceFacts, error) {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return diskSpaceFacts{}, err
	}
	return diskSpaceFactsFromStat(stat), nil
}

func diskSpaceFactsFromStat(stat unix.Statfs_t) diskSpaceFacts {
	return diskSpaceFacts{blockSize: stat.Bsize, available: stat.Bavail,
		readOnly:     uint64(stat.Flags)&uint64(unix.ST_RDONLY) != 0,
		memoryBacked: stat.Type == unix.TMPFS_MAGIC || stat.Type == unix.RAMFS_MAGIC}
}

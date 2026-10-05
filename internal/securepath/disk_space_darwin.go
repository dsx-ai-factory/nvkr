//go:build darwin

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
	filesystem := unix.ByteSliceToString(stat.Fstypename[:])
	return diskSpaceFacts{blockSize: int64(stat.Bsize), available: stat.Bavail,
		readOnly:     stat.Flags&unix.MNT_RDONLY != 0,
		memoryBacked: filesystem == "tmpfs" || filesystem == "mfs" || filesystem == "ramfs"}
}

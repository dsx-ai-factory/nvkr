//go:build darwin

package securepath

import "golang.org/x/sys/unix"

func observeDiskSpace(fd int) (FilesystemSpace, error) {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return FilesystemSpace{}, err
	}
	return FilesystemSpaceFromStat(stat), nil
}

// FilesystemSpaceFromStat projects Darwin allocation facts.
func FilesystemSpaceFromStat(stat unix.Statfs_t) FilesystemSpace {
	filesystem := unix.ByteSliceToString(stat.Fstypename[:])
	return FilesystemSpace{blockSize: int64(stat.Bsize), available: stat.Bavail,
		readOnly:     stat.Flags&unix.MNT_RDONLY != 0,
		memoryBacked: filesystem == "tmpfs" || filesystem == "mfs" || filesystem == "ramfs"}
}

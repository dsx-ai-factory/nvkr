//go:build darwin || linux

package aicr

import (
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// openQualifiedBundleRoot holds the exact staging directory without following
// a final-component link. O_NONBLOCK prevents an attacker-controlled FIFO from
// turning admission into an unbounded open if the pathname changes mid-check.
func openQualifiedBundleRoot(path string) (*os.File, fs.FileInfo, error) {
	var before unix.Stat_t
	if err := unix.Lstat(path, &before); err != nil {
		return nil, nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil, nil, fmt.Errorf("bundle root is not a real directory")
	}
	fd, err := unix.Open(path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("holding bundle root descriptor")
	}
	info, err := qualifiedBundleOpenedInfo(file, before)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

// openQualifiedBundleChild opens one name relative to its already-held parent.
// The before/open comparison closes the lstat/open substitution window, and
// O_NOFOLLOW makes links fail rather than resolve to a different authority.
func openQualifiedBundleChild(parent *os.File, name string) (*os.File, fs.FileInfo, error) {
	if parent == nil {
		return nil, nil, fmt.Errorf("nil bundle parent descriptor")
	}
	var before unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, nil, err
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	switch before.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		flags |= unix.O_DIRECTORY
	case unix.S_IFREG:
	default:
		return nil, nil, fmt.Errorf("bundle entry is not a regular file or directory")
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, nil, fmt.Errorf("holding bundle entry descriptor")
	}
	info, err := qualifiedBundleOpenedInfo(file, before)
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

// qualifiedBundleChildUnchanged proves that the directory entry still names
// the same held inode after it has been read. The descriptor metadata check also
// detects writes and directory mutations that occur while traversal is active.
func qualifiedBundleChildUnchanged(parent *os.File, name string, file *os.File, before fs.FileInfo) error {
	if parent == nil || file == nil || before == nil {
		return fmt.Errorf("bundle entry recheck is missing its held authority")
	}
	var named unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &opened); err != nil {
		return err
	}
	if !sameQualifiedBundleInode(named, opened) {
		return fmt.Errorf("bundle entry pathname changed")
	}
	after, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("bundle entry descriptor changed")
	}
	return nil
}

func qualifiedBundleOpenedInfo(file *os.File, before unix.Stat_t) (fs.FileInfo, error) {
	var opened unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &opened); err != nil {
		return nil, err
	}
	if !sameQualifiedBundleInode(before, opened) {
		return nil, fmt.Errorf("bundle entry changed while opening")
	}
	return file.Stat()
}

func sameQualifiedBundleInode(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode &&
		left.Size == right.Size && left.Nlink == right.Nlink && left.Uid == right.Uid &&
		left.Gid == right.Gid
}

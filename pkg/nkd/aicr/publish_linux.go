//go:build linux

package aicr

import "golang.org/x/sys/unix"

// publishDirectoryNoReplace makes a completely validated staging tree visible
// without ever giving rename permission to replace an existing destination.
func publishDirectoryNoReplace(stage, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}

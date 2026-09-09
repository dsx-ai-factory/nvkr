//go:build linux

package pipeline

import "golang.org/x/sys/unix"

func publishTransportNoReplace(stage, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}

func publishTransportAtNoReplace(sourceDirectory int, source string,
	destinationDirectory int, destination string,
) error {
	return unix.Renameat2(sourceDirectory, source, destinationDirectory, destination, unix.RENAME_NOREPLACE)
}

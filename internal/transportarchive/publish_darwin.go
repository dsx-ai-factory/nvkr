//go:build darwin

package transportarchive

import "golang.org/x/sys/unix"

func publishTransportNoReplace(stage, destination string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_EXCL)
}

// PublishAtNoReplace atomically renames an entry between held directories and
// rejects an existing destination. The caller owns directory synchronization.
func PublishAtNoReplace(sourceDirectory int, source string,
	destinationDirectory int, destination string,
) error {
	return unix.RenameatxNp(sourceDirectory, source, destinationDirectory, destination, unix.RENAME_EXCL)
}

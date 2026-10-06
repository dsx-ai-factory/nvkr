//go:build !linux && !darwin

package securepath

import "fmt"

func observeDiskSpace(int) (FilesystemSpace, error) {
	return FilesystemSpace{}, fmt.Errorf("disk-backed scratch observation is unsupported on this platform")
}

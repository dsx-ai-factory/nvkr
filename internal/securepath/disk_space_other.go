//go:build !linux && !darwin

package securepath

import "fmt"

func observeDiskSpace(int) (diskSpaceFacts, error) {
	return diskSpaceFacts{}, fmt.Errorf("disk-backed scratch observation is unsupported on this platform")
}

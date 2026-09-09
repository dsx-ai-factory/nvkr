//go:build !darwin && !linux

package aicr

import "fmt"

// A check followed by a portable rename would permit a concurrent replacement.
// Platforms without an atomic no-replace directory primitive therefore fail closed.
func publishDirectoryNoReplace(_, _ string) error {
	return fmt.Errorf("atomic no-replace directory publication is unsupported on this platform")
}

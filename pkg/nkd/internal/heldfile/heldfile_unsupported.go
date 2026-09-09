//go:build !linux && !darwin

package heldfile

import (
	"context"
	"os"
)

func newMutable(string) (*os.File, error) { return nil, ErrUnsupported }
func freezeMutable(context.Context, *os.File, string, bool, int64) (*os.File, error) {
	return nil, ErrUnsupported
}
func platformChildPath(int) (string, error)       { return "", ErrUnsupported }
func platformOpenSource(string) (*os.File, error) { return nil, ErrUnsupported }
func platformCommand([]string) (string, []string, error) {
	return "", nil, ErrUnsupported
}
func platformSupportsExecutable() bool { return false }

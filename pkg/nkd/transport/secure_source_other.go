//go:build !darwin && !linux

package transport

import (
	"fmt"
	"os"
)

func openNoFollowRegular(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a no-follow regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	current, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !current.Mode().IsRegular() ||
		!os.SameFile(before, after) || !os.SameFile(after, current) {
		_ = file.Close()
		return nil, fmt.Errorf("%q changed while opening as a no-follow regular file", path)
	}
	return file, nil
}

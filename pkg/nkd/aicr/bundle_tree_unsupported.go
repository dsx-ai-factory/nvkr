//go:build !darwin && !linux

package aicr

import (
	"fmt"
	"io/fs"
	"os"
)

func openQualifiedBundleRoot(string) (*os.File, fs.FileInfo, error) {
	return nil, nil, fmt.Errorf("descriptor-rooted AICR bundle snapshots are unsupported on this platform")
}

func openQualifiedBundleChild(*os.File, string) (*os.File, fs.FileInfo, error) {
	return nil, nil, fmt.Errorf("descriptor-rooted AICR bundle snapshots are unsupported on this platform")
}

func qualifiedBundleChildUnchanged(*os.File, string, *os.File, fs.FileInfo) error {
	return fmt.Errorf("descriptor-rooted AICR bundle snapshots are unsupported on this platform")
}

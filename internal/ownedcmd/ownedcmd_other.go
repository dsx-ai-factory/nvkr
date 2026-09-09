//go:build !linux && !darwin

package ownedcmd

import (
	"os/exec"
	"time"
)

func configureCommand(_ *exec.Cmd, _ time.Duration) error {
	return ErrUnsupported
}

func cleanupCommandGroup(_ int, _ time.Duration) error {
	return nil
}

//go:build !linux && !darwin

package profile

import "os/exec"

func kernelRelease() string { return "" }

func findExecutable(name string) (string, error) { return exec.LookPath(name) }

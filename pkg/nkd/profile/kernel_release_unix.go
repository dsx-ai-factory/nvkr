//go:build linux || darwin

package profile

import (
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
)

// kernelRelease is a local system call, not a subprocess authority boundary.
func kernelRelease() string {
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return ""
	}
	return strings.TrimRight(string(name.Release[:]), "\x00")
}

func findExecutable(name string) (string, error) { return exec.LookPath(name) }

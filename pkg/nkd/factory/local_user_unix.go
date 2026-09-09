//go:build linux || darwin

package factory

import (
	"os"
	"strconv"
)

func localContainerUser() string {
	return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
}

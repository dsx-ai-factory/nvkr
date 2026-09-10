//go:build !linux

package pipeline

import (
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func applyExecutionSandbox([]string) error {
	return e.New(e.CodeEngineCapability, "direct exec-node builds require Linux Landlock + seccomp isolation")
}

func replaceSandboxProcess(string, []string, []string) error {
	return e.New(e.CodeEngineCapability, "direct exec-node builds require Linux Landlock + seccomp isolation")
}

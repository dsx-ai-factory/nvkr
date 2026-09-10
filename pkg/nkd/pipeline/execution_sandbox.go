package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// ExecutionSandboxMode is recorded in execution observations and builder profiles.
// Changing the kernel policy is therefore an environment-identity change.
const ExecutionSandboxMode = "landlock-v4-write-allowlist+seccomp-inet-deny/v1"

// SandboxExec applies the runner-owned least-privilege policy and replaces the
// current process with the build command. Restrictions survive exec and descendants.
func SandboxExec(writeRoots, command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return e.New(e.CodeUsage, "sandbox-exec requires a command")
	}
	if len(writeRoots) == 0 {
		return e.New(e.CodeEngineCapability, "sandbox-exec requires at least one private write root")
	}
	cleanRoots := make([]string, 0, len(writeRoots))
	for _, root := range writeRoots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "resolving sandbox write root")
		}
		info, err := os.Lstat(absolute)
		if err != nil || !info.IsDir() {
			return e.Wrap(e.CodeEngineCapability, err, "sandbox write root %s is not a directory", absolute)
		}
		cleanRoots = append(cleanRoots, absolute)
	}
	path, err := exec.LookPath(command[0])
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "resolving sandbox command %s", command[0])
	}
	// The unsupported-host implementation always refuses; Linux evaluates the
	// active kernel's Landlock and seccomp capabilities.
	//nolint:staticcheck
	if err := applyExecutionSandbox(cleanRoots); err != nil {
		return err
	}
	return replaceSandboxProcess(path, command, os.Environ())
}

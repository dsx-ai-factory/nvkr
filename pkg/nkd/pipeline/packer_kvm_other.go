//go:build !linux

package pipeline

import (
	"os"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func validateEnabledPackerContainerKVM(_ packerContainerRuntime, _ *os.File) error {
	return e.New(e.CodeEngineCapability, "Packer KVM execution requires Linux")
}

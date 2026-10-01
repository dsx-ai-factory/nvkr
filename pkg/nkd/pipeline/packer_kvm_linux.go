//go:build linux

package pipeline

import (
	"errors"
	"fmt"
	"os"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func validateEnabledPackerContainerKVM(runtime packerContainerRuntime, kvm *os.File) error {
	if runtime.Endpoint() != qualifiedRuntimeEndpoint || kvm == nil {
		return e.New(e.CodeEngineCapability, "Packer KVM resolution requires a held device on the local runtime")
	}
	info, err := kvm.Stat()
	if err != nil || info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice == 0 {
		if err == nil {
			err = errors.New("descriptor is not a character device")
		}
		return e.Wrap(e.CodeEngineCapability, err, "validating admitted KVM device")
	}
	procPath := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), kvm.Fd())
	target, err := os.Readlink(procPath)
	if err != nil || target != "/dev/kvm" {
		if err == nil {
			err = fmt.Errorf("held device resolves to %q", target)
		}
		return e.Wrap(e.CodeEngineCapability, err,
			"validating local Docker KVM device handoff")
	}
	// KVM_GET_API_VERSION (_IO(KVMIO, 0x00) in linux/kvm.h) returns the
	// version as the syscall result and requires a zero argument. IoctlGetInt
	// instead passes a pointer, which KVM rejects with EINVAL.
	version, err := unix.IoctlRetInt(int(kvm.Fd()), 0xAE00)
	if err != nil || version != 12 {
		if err == nil {
			err = fmt.Errorf("KVM API version is %d", version)
		}
		return e.Wrap(e.CodeEngineCapability, err, "revalidating admitted KVM capability")
	}
	return nil
}

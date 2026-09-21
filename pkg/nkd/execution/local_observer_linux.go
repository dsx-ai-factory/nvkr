//go:build linux

package execution

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync/atomic"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

const (
	kvmDevicePath      = "/dev/kvm"
	kvmGetAPIVersion   = uint(0xAE00) // _IO(KVMIO, 0x00), from linux/kvm.h.
	kvmExpectedVersion = 12
	bytesPerMiB        = uint64(1024 * 1024)
)

type systemLocalObserver struct{}

type linuxLocalObservation struct {
	workspaceFD int
	kvmFD       int
	access      localAccessCheck
	kvm         localKVMOperations
	observed    atomic.Bool
}

func (observation *linuxLocalObservation) holdWorkspace() (*os.File, error) {
	return observation.holdFD(observation.workspaceFD, "local VM workspace")
}

func (observation *linuxLocalObservation) holdKVM() (*os.File, error) {
	if observation.kvmFD < 0 {
		return nil, e.New(e.CodeEngineCapability,
			"local VM admission has no held KVM device")
	}
	return observation.holdFD(observation.kvmFD, "local KVM device")
}

func (observation *linuxLocalObservation) holdFD(fd int, name string) (*os.File, error) {
	if fd < 0 {
		return nil, e.New(e.CodeEngineCapability, "%s capability is closed", name)
	}
	held, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "duplicating held %s capability", name)
	}
	file := os.NewFile(uintptr(held), name)
	if file == nil {
		_ = unix.Close(held)
		return nil, e.New(e.CodeInternal, "constructing held %s capability", name)
	}
	return file, nil
}

func (systemLocalObserver) Open(ctx context.Context, workspace *os.File) (localObservationLease, error) {
	return openLinuxLocalObservation(ctx, workspace, unix.Faccessat, systemKVMOperations)
}

type localAccessCheck func(int, string, uint32, int) error

type localFDDuplicator func(uintptr) (int, error)

type localStatfs func(int, *unix.Statfs_t) error

type localKVMOperations struct {
	open  func(string, int, uint32) (int, error)
	fstat func(int, *unix.Stat_t) error
	ioctl func(int, uint) (int, error)
	close func(int) error
}

var systemKVMOperations = localKVMOperations{
	open: unix.Open, fstat: unix.Fstat, ioctl: unix.IoctlRetInt, close: unix.Close,
}

func openLinuxLocalObservation(
	ctx context.Context,
	workspace *os.File,
	checkAccess localAccessCheck,
	kvm localKVMOperations,
) (localObservationLease, error) {
	if err := localObservationContextError(ctx, "opening local Linux observation"); err != nil {
		return nil, err
	}
	if workspace == nil {
		return nil, e.New(e.CodeUsage, "opening local Linux observation requires a held workspace directory")
	}
	workspaceFD, err := duplicateWorkspaceDirectory(workspace, func(fd uintptr) (int, error) {
		return unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
	})
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(workspaceFD, &stat); err != nil {
		_ = unix.Close(workspaceFD)
		return nil, e.Wrap(e.CodeEngineCapability, err, "inspecting the held local workspace directory")
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(workspaceFD)
		return nil, e.New(e.CodeEngineCapability, "held local workspace capability is not a directory")
	}
	if checkAccess == nil {
		_ = unix.Close(workspaceFD)
		return nil, e.New(e.CodeInternal, "local workspace access checker is absent")
	}
	if kvm.open == nil || kvm.fstat == nil || kvm.ioctl == nil || kvm.close == nil {
		_ = unix.Close(workspaceFD)
		return nil, e.New(e.CodeInternal, "local KVM observer is incomplete")
	}
	observation := &linuxLocalObservation{
		workspaceFD: workspaceFD, kvmFD: -1, access: checkAccess, kvm: kvm,
	}
	if err := observation.requireWorkspaceAccess(); err != nil {
		_ = unix.Close(workspaceFD)
		return nil, err
	}
	return observation, nil
}

func duplicateWorkspaceDirectory(workspace *os.File, duplicate localFDDuplicator) (int, error) {
	if workspace == nil {
		return -1, e.New(e.CodeUsage, "duplicating local workspace requires a held directory")
	}
	if duplicate == nil {
		return -1, e.New(e.CodeInternal, "local workspace descriptor duplicator is absent")
	}
	raw, err := workspace.SyscallConn()
	if err != nil {
		return -1, e.Wrap(e.CodeEngineCapability, err, "accessing the held local workspace descriptor")
	}
	duplicated := -1
	var duplicateErr error
	if err := raw.Control(func(fd uintptr) {
		duplicated, duplicateErr = duplicate(fd)
	}); err != nil {
		return -1, e.Wrap(e.CodeEngineCapability, err,
			"holding the local workspace descriptor while duplicating it")
	}
	if duplicateErr != nil {
		return -1, e.Wrap(e.CodeEngineCapability, duplicateErr,
			"duplicating the held local workspace descriptor")
	}
	if duplicated < 0 {
		return -1, e.New(e.CodeInternal, "local workspace descriptor duplicator returned an invalid descriptor")
	}
	return duplicated, nil
}

func (observation *linuxLocalObservation) Observe(ctx context.Context) (ObservedHostFacts, error) {
	if !observation.observed.CompareAndSwap(false, true) {
		return ObservedHostFacts{}, e.New(e.CodeInternal,
			"local Linux observation lease has already been used")
	}
	return observation.observeCurrent(ctx)
}

func (observation *linuxLocalObservation) reobserve(ctx context.Context) (ObservedHostFacts, error) {
	if !observation.observed.Load() {
		return ObservedHostFacts{}, e.New(e.CodeInternal,
			"local Linux observation lease has not completed initial observation")
	}
	return observation.observeCurrent(ctx)
}

func (observation *linuxLocalObservation) observeCurrent(ctx context.Context) (ObservedHostFacts, error) {
	if err := localObservationContextError(ctx, "observing local Linux execution host"); err != nil {
		return ObservedHostFacts{}, err
	}
	if err := observation.requireWorkspaceAccess(); err != nil {
		return ObservedHostFacts{}, err
	}
	platform, err := localLinuxPlatform()
	if err != nil {
		return ObservedHostFacts{}, err
	}
	cpuMillis, err := availableCPUMillis()
	if err != nil {
		return ObservedHostFacts{}, err
	}
	memoryMiB, err := availableMemoryMiB()
	if err != nil {
		return ObservedHostFacts{}, err
	}
	scratchMiB, err := availableScratchMiB(observation.workspaceFD)
	if err != nil {
		return ObservedHostFacts{}, err
	}
	kvm, err := observation.observeKVM()
	if err != nil {
		return ObservedHostFacts{}, err
	}
	if err := localObservationContextError(ctx, "observing local Linux execution host"); err != nil {
		return ObservedHostFacts{}, err
	}

	facts := ObservedHostFacts{
		Platform: platform,
		Resources: ResourceCapacity{
			CPUMillis: cpuMillis, MemoryMiB: memoryMiB, EphemeralStorageMiB: scratchMiB,
		},
	}
	if kvm {
		facts.Capabilities = []CapabilityName{CapabilityKVM}
	}
	return facts, nil
}

func (observation *linuxLocalObservation) requireWorkspaceAccess() error {
	if observation.workspaceFD < 0 {
		return e.New(e.CodeInternal, "local workspace directory handle is closed")
	}
	if observation.access == nil {
		return e.New(e.CodeInternal, "local workspace access checker is absent")
	}
	if err := observation.access(observation.workspaceFD, ".", unix.W_OK|unix.X_OK, unix.AT_EACCESS); err != nil {
		return e.Wrap(e.CodeEngineCapability, err,
			"checking write and traversal access to the held local workspace directory")
	}
	return nil
}

func (observation *linuxLocalObservation) Close() error {
	var closeErrors []error
	if observation.workspaceFD >= 0 {
		if err := unix.Close(observation.workspaceFD); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("closing workspace directory: %w", err))
		}
		observation.workspaceFD = -1
	}
	if observation.kvmFD >= 0 {
		if err := observation.kvm.close(observation.kvmFD); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("closing KVM device: %w", err))
		}
		observation.kvmFD = -1
	}
	return errors.Join(closeErrors...)
}

func localLinuxPlatform() (HostPlatform, error) {
	var name unix.Utsname
	if err := unix.Uname(&name); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "reading local Linux host architecture")
	}
	return hostPlatformForMachine(strings.TrimRight(string(name.Machine[:]), "\x00"))
}

func hostPlatformForMachine(machine string) (HostPlatform, error) {
	switch machine {
	case "x86_64", "amd64":
		return HostPlatformLinuxAMD64, nil
	case "aarch64", "arm64":
		return HostPlatformLinuxARM64, nil
	default:
		return "", e.New(e.CodeEngineCapability,
			"local host observation does not support Linux host architecture %q", machine)
	}
}

// availableCPUMillis returns the smaller of the process CPU affinity and every
// CPU quota in its cgroup-v2 ancestry. It observes current local capacity but
// neither reserves that capacity nor grants provider allocation authority.
func availableCPUMillis() (uint64, error) {
	var affinity unix.CPUSet
	if err := unix.SchedGetaffinity(0, &affinity); err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err, "reading local CPU affinity")
	}
	count := affinity.Count()
	if count <= 0 {
		return 0, e.New(e.CodeEngineCapability, "local CPU affinity exposes no processors")
	}
	affinityMillis := uint64(count) * 1000
	cgroupDir, cgroupRoot, err := currentCgroupV2Location()
	if err != nil {
		return 0, err
	}
	return effectiveCgroupCPUMillis(cgroupDir, cgroupRoot, affinityMillis)
}

// availableMemoryMiB returns the smaller of host-available memory and effective
// headroom across the process's complete cgroup-v2 ancestry. It reserves neither.
func availableMemoryMiB() (uint64, error) {
	hostAvailable, err := readHostAvailableMemoryMiB()
	if err != nil {
		return 0, err
	}
	cgroupDir, cgroupRoot, err := currentCgroupV2Location()
	if err != nil {
		return 0, err
	}
	return effectiveCgroupMemoryMiB(cgroupDir, cgroupRoot, hostAvailable)
}

func availableScratchMiB(fd int) (uint64, error) {
	return availableScratchMiBWith(fd, unix.Fstatfs)
}

func availableScratchMiBWith(fd int, statfs localStatfs) (uint64, error) {
	if fd < 0 {
		return 0, e.New(e.CodeInternal, "local scratch directory handle is closed")
	}
	if statfs == nil {
		return 0, e.New(e.CodeInternal, "local scratch filesystem observer is absent")
	}
	var stat unix.Statfs_t
	if err := statfs(fd, &stat); err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err, "reading local scratch capacity")
	}
	if uint64(stat.Flags)&uint64(unix.ST_RDONLY) != 0 {
		return 0, e.New(e.CodeEngineCapability, "local scratch filesystem is read-only")
	}
	if stat.Bsize <= 0 {
		return 0, e.New(e.CodeEngineCapability, "local scratch filesystem reports an invalid block size")
	}
	blockSize := uint64(stat.Bsize)
	if stat.Bavail > math.MaxUint64/blockSize {
		return 0, e.New(e.CodeEngineCapability, "local scratch capacity exceeds the supported range")
	}
	return stat.Bavail * blockSize / bytesPerMiB, nil
}

func (observation *linuxLocalObservation) observeKVM() (bool, error) {
	if observation.kvmFD < 0 {
		// KVM_GET_API_VERSION alone works with a read-only observation on some
		// kernels, but runtime admission retains this exact descriptor as a VM
		// capability. Open read-write so an enabled resolution never authenticates
		// a descriptor that could not create a VM.
		fd, err := observation.kvm.open(kvmDevicePath,
			unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENODEV) ||
				errors.Is(err, unix.ENXIO) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
				return false, nil
			}
			return false, e.Wrap(e.CodeEngineCapability, err, "opening local KVM device")
		}
		var stat unix.Stat_t
		if err := observation.kvm.fstat(fd, &stat); err != nil {
			_ = observation.kvm.close(fd)
			return false, e.Wrap(e.CodeEngineCapability, err, "inspecting local KVM device")
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
			_ = observation.kvm.close(fd)
			return false, e.New(e.CodeEngineCapability, "%s is not a character device", kvmDevicePath)
		}
		observation.kvmFD = fd
	}
	version, err := observation.kvm.ioctl(observation.kvmFD, kvmGetAPIVersion)
	if err != nil {
		return false, e.Wrap(e.CodeEngineCapability, err, "querying local KVM API version")
	}
	if version != kvmExpectedVersion {
		return false, e.New(e.CodeEngineCapability,
			"local KVM API version is %d, want %d", version, kvmExpectedVersion)
	}
	return true, nil
}

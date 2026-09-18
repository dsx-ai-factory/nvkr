//go:build linux

package execution

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

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
}

func (systemLocalObserver) Open(ctx context.Context, workspace *os.File) (localObservationLease, error) {
	return openLinuxLocalObservation(ctx, workspace, unix.Faccessat, systemKVMOperations)
}

type localAccessCheck func(int, string, uint32, int) error

type localFDDuplicator func(uintptr) (int, error)

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
		return e.New(e.CodePreflightFailed, "local workspace directory handle is closed")
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

// availableCPUMillis is deliberately a local affinity observation, not a
// provider allocation or a cgroup CPU-quota claim. A future allocation adapter
// must authenticate its own effective quota before using provider authority.
func availableCPUMillis() (uint64, error) {
	var affinity unix.CPUSet
	if err := unix.SchedGetaffinity(0, &affinity); err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err, "reading local CPU affinity")
	}
	count := affinity.Count()
	if count <= 0 {
		return 0, e.New(e.CodeEngineCapability, "local CPU affinity exposes no processors")
	}
	return uint64(count) * 1000, nil
}

// availableMemoryMiB is a point-in-time free-host-memory observation. It does
// not claim a cgroup limit or reserve the returned capacity.
func availableMemoryMiB() (uint64, error) {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err, "reading local memory availability")
	}
	unit := uint64(info.Unit)
	if unit == 0 {
		unit = 1
	}
	free := info.Freeram
	if free > math.MaxUint64/unit {
		return 0, e.New(e.CodeEngineCapability, "local memory availability exceeds the supported range")
	}
	return free * unit / bytesPerMiB, nil
}

func availableScratchMiB(fd int) (uint64, error) {
	if fd < 0 {
		return 0, e.New(e.CodePreflightFailed, "local scratch directory handle is closed")
	}
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return 0, e.Wrap(e.CodeEngineCapability, err, "reading local scratch capacity")
	}
	if uint64(stat.Flags)&uint64(unix.ST_RDONLY) != 0 {
		return 0, e.New(e.CodeEngineCapability, "local scratch filesystem is read-only")
	}
	blockSize := uint64(stat.Bsize)
	if blockSize == 0 {
		return 0, e.New(e.CodeEngineCapability, "local scratch filesystem reports a zero block size")
	}
	if stat.Bavail > math.MaxUint64/blockSize {
		return 0, e.New(e.CodeEngineCapability, "local scratch capacity exceeds the supported range")
	}
	return stat.Bavail * blockSize / bytesPerMiB, nil
}

func (observation *linuxLocalObservation) observeKVM() (bool, error) {
	if observation.kvmFD < 0 {
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

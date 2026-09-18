//go:build linux

package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestHostPlatformForMachine(t *testing.T) {
	for _, test := range []struct {
		machine  string
		want     HostPlatform
		wantCode nkderr.Code
	}{
		{machine: "x86_64", want: HostPlatformLinuxAMD64},
		{machine: "amd64", want: HostPlatformLinuxAMD64},
		{machine: "aarch64", want: HostPlatformLinuxARM64},
		{machine: "arm64", want: HostPlatformLinuxARM64},
		{machine: "riscv64", wantCode: nkderr.CodeEngineCapability},
	} {
		t.Run(test.machine, func(t *testing.T) {
			got, err := hostPlatformForMachine(test.machine)
			if test.wantCode != "" {
				if code := errorCode(err); code != test.wantCode {
					t.Fatalf("error = %v, want %s", err, test.wantCode)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("platform = %q, error = %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestLinuxLocalObserverRetainsHeldWorkspaceCapability(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace, err := os.Open(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()

	movedPath := filepath.Join(root, "admitted-workspace")
	if err := os.Rename(workspacePath, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}

	lease, err := (systemLocalObserver{}).Open(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	observation, ok := lease.(*linuxLocalObservation)
	if !ok {
		t.Fatalf("lease type = %T, want *linuxLocalObservation", lease)
	}
	var held, moved, replacement unix.Stat_t
	if err := unix.Fstat(observation.workspaceFD, &held); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat(movedPath, &moved); err != nil {
		t.Fatal(err)
	}
	if err := unix.Stat(workspacePath, &replacement); err != nil {
		t.Fatal(err)
	}
	if held.Dev != moved.Dev || held.Ino != moved.Ino {
		t.Fatal("observer did not retain the caller's held workspace directory")
	}
	if held.Dev == replacement.Dev && held.Ino == replacement.Ino {
		t.Fatal("observer followed the replaceable workspace pathname")
	}
}

func TestLinuxLocalObserverRequiresWriteAndTraversalAccess(t *testing.T) {
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()

	var gotMode uint32
	lease, err := openLinuxLocalObservation(context.Background(), workspace,
		func(_ int, path string, mode uint32, flags int) error {
			if path != "." || flags != unix.AT_EACCESS {
				t.Fatalf("access check path = %q, flags = %d", path, flags)
			}
			gotMode = mode
			return unix.EACCES
		}, systemKVMOperations)
	if lease != nil || err == nil {
		t.Fatalf("lease = %#v, error = %v; want access failure", lease, err)
	}
	if gotMode != unix.W_OK|unix.X_OK {
		t.Fatalf("access mode = %d, want W_OK|X_OK (%d)", gotMode, unix.W_OK|unix.X_OK)
	}
}

func TestLinuxLocalObserverRechecksWorkspaceAccess(t *testing.T) {
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()

	checks := 0
	lease, err := openLinuxLocalObservation(context.Background(), workspace,
		func(_ int, _ string, _ uint32, _ int) error {
			checks++
			if checks > 1 {
				return unix.EACCES
			}
			return nil
		}, systemKVMOperations)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := lease.Observe(context.Background()); err == nil {
		t.Fatal("observation accepted a workspace that lost write/traversal access")
	}
	if checks != 2 {
		t.Fatalf("workspace access checks = %d, want open plus observation", checks)
	}
}

func TestSystemLocalHostObservationExercisesLinuxProbes(t *testing.T) {
	facts, err := ObserveLocalHost(context.Background(), openLocalWorkspace(t))
	if err != nil {
		t.Fatal(err)
	}
	wantPlatform, err := hostPlatformForMachine(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Platform != wantPlatform {
		t.Fatalf("platform = %q, want %q", facts.Platform, wantPlatform)
	}
	if facts.Resources.CPUMillis == 0 || facts.Resources.MemoryMiB == 0 ||
		facts.Resources.EphemeralStorageMiB == 0 {
		t.Fatalf("Linux probes returned empty resources: %#v", facts.Resources)
	}
	for _, capability := range facts.Capabilities {
		if capability != CapabilityKVM {
			t.Fatalf("unexpected capability %q", capability)
		}
	}
}

func TestDuplicateWorkspaceDirectoryProtectsDescriptorLifetime(t *testing.T) {
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	closed := make(chan struct{})
	type duplicateResult struct {
		fd  int
		err error
	}
	duplicated := make(chan duplicateResult, 1)
	go func() {
		fd, duplicateErr := duplicateWorkspaceDirectory(workspace, func(raw uintptr) (int, error) {
			close(entered)
			<-closed
			var stat unix.Stat_t
			if err := unix.Fstat(int(raw), &stat); err != nil {
				return -1, err
			}
			if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
				return -1, unix.ENOTDIR
			}
			return unix.FcntlInt(raw, unix.F_DUPFD_CLOEXEC, 0)
		})
		duplicated <- duplicateResult{fd: fd, err: duplicateErr}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("descriptor duplication did not enter SyscallConn.Control")
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	close(closed)
	result := <-duplicated
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer unix.Close(result.fd)
	var stat unix.Stat_t
	if err := unix.Fstat(result.fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		t.Fatalf("duplicated workspace descriptor is not a directory: stat=%#v error=%v", stat, err)
	}
}

func TestDuplicateWorkspaceDirectoryFailuresAreCoded(t *testing.T) {
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := duplicateWorkspaceDirectory(workspace, func(uintptr) (int, error) {
		return -1, unix.EMFILE
	}); errorCode(err) != nkderr.CodeEngineCapability || !errors.Is(err, unix.EMFILE) {
		t.Fatalf("duplicate failure = %v, want %s wrapping EMFILE", err, nkderr.CodeEngineCapability)
	}
	if _, err := duplicateWorkspaceDirectory(workspace, func(uintptr) (int, error) {
		return -1, nil
	}); errorCode(err) != nkderr.CodeInternal {
		t.Fatalf("invalid descriptor error = %v, want %s", err, nkderr.CodeInternal)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := duplicateWorkspaceDirectory(workspace, func(raw uintptr) (int, error) {
		return unix.FcntlInt(raw, unix.F_DUPFD_CLOEXEC, 0)
	}); errorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("closed workspace error = %v, want %s", err, nkderr.CodeEngineCapability)
	}
}

func TestLinuxObservationCodedProbeFailures(t *testing.T) {
	if _, err := availableScratchMiB(-1); errorCode(err) != nkderr.CodePreflightFailed {
		t.Fatalf("closed workspace capacity error = %v, want %s", err, nkderr.CodePreflightFailed)
	}
	if _, err := hostPlatformForMachine("riscv64"); errorCode(err) != nkderr.CodeEngineCapability {
		t.Fatalf("unsupported platform error = %v, want %s", err, nkderr.CodeEngineCapability)
	}
}

func TestObserveKVMDetection(t *testing.T) {
	for _, test := range []struct {
		name        string
		openErr     error
		fstatErr    error
		mode        uint32
		version     int
		ioctlErr    error
		wantExposed bool
		wantCode    nkderr.Code
		wantClosed  bool
	}{
		{name: "absent", openErr: unix.ENOENT},
		{name: "permission denied", openErr: unix.EACCES},
		{name: "unexpected open failure", openErr: unix.EIO, wantCode: nkderr.CodeEngineCapability},
		{name: "stat failure", fstatErr: unix.EIO, wantCode: nkderr.CodeEngineCapability, wantClosed: true},
		{name: "not character device", mode: unix.S_IFREG, wantCode: nkderr.CodeEngineCapability, wantClosed: true},
		{name: "ioctl failure", mode: unix.S_IFCHR, ioctlErr: unix.EIO, wantCode: nkderr.CodeEngineCapability},
		{name: "version mismatch", mode: unix.S_IFCHR, version: kvmExpectedVersion - 1, wantCode: nkderr.CodeEngineCapability},
		{name: "exposed", mode: unix.S_IFCHR, version: kvmExpectedVersion, wantExposed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			closed := false
			observation := linuxLocalObservation{
				workspaceFD: -1, kvmFD: -1,
				kvm: localKVMOperations{
					open: func(path string, flags int, mode uint32) (int, error) {
						if path != kvmDevicePath || flags == 0 || mode != 0 {
							t.Fatalf("open path=%q flags=%d mode=%d", path, flags, mode)
						}
						return 41, test.openErr
					},
					fstat: func(_ int, stat *unix.Stat_t) error {
						stat.Mode = test.mode
						return test.fstatErr
					},
					ioctl: func(_ int, request uint) (int, error) {
						if request != kvmGetAPIVersion {
							t.Fatalf("ioctl request = %#x, want %#x", request, kvmGetAPIVersion)
						}
						return test.version, test.ioctlErr
					},
					close: func(int) error { closed = true; return nil },
				},
			}
			exposed, err := observation.observeKVM()
			if errorCode(err) != test.wantCode || exposed != test.wantExposed {
				t.Fatalf("exposed = %t, error = %v; want exposed=%t code=%s",
					exposed, err, test.wantExposed, test.wantCode)
			}
			if closed != test.wantClosed {
				t.Fatalf("device closed = %t, want %t", closed, test.wantClosed)
			}
		})
	}
}

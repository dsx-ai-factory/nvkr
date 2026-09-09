//go:build linux

package pipeline

import (
	"errors"
	"os"
	"runtime"
	"unsafe"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func applyExecutionSandbox(writeRoots []string) error {
	abi, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION, 0, 0, 0)
	if errno != 0 {
		return e.Wrap(e.CodeEngineCapability, errno, "querying Landlock ABI")
	}
	if abi < 4 {
		return e.New(e.CodeEngineCapability, "build requires Landlock ABI >= 4 for network and filesystem isolation; host provides %d", abi)
	}
	writeAccess := uint64(
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
			unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
			unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
			unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
			unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
			unix.LANDLOCK_ACCESS_FS_MAKE_REG |
			unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
			unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
			unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
			unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
			unix.LANDLOCK_ACCESS_FS_REFER |
			unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	attr := unix.LandlockRulesetAttr{
		Access_fs:  writeAccess,
		Access_net: unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP,
	}
	fdRaw, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0, 0, 0, 0)
	if errno != 0 {
		return e.Wrap(e.CodeEngineCapability, errno, "creating Landlock execution ruleset")
	}
	fd := int(fdRaw)
	defer func() { _ = unix.Close(fd) }()
	for _, root := range writeRoots {
		parent, err := unix.Open(root, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "opening sandbox write root %s", root)
		}
		rule := unix.LandlockPathBeneathAttr{Allowed_access: writeAccess, Parent_fd: int32(parent)}
		_, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(fd), unix.LANDLOCK_RULE_PATH_BENEATH,
			uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
		_ = unix.Close(parent)
		if errno != 0 {
			return e.Wrap(e.CodeEngineCapability, errno, "adding Landlock write root %s", root)
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "enabling no-new-privileges")
	}
	_, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(fd), 0, 0, 0, 0, 0)
	if errno != 0 {
		return e.Wrap(e.CodeEngineCapability, errno, "applying Landlock execution ruleset")
	}
	if err := closeInheritedDescriptors(); err != nil {
		return err
	}
	if err := denyCommunicationSyscalls(); err != nil {
		return err
	}
	return nil
}

// closeInheritedDescriptors prevents a build from bypassing socket creation and
// pathname controls through a descriptor opened by the CI wrapper. The ruleset fd
// remains live until the process image is replaced and is closed by CLOEXEC.
func closeInheritedDescriptors() error {
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC|unix.CLOSE_RANGE_UNSHARE); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "marking inherited descriptors close-on-exec")
	}
	return nil
}

// denyCommunicationSyscalls removes every newly-created socket, not only TCP.
// Landlock independently denies TCP bind/connect. Blocking io_uring setup closes
// the alternate asynchronous socket/connect path on kernels that support it.
func denyCommunicationSyscalls() error {
	auditArch, err := nativeAuditArchitecture()
	if err != nil {
		return err
	}
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4}, // seccomp_data.arch
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: auditArch, Jt: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}, // seccomp_data.nr
		// On amd64 the x32 ABI shares AUDIT_ARCH_X86_64 but offsets its
		// syscall numbers. Reject all high-bit ABI variants before matching.
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_SOCKET), Jt: 2},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_SOCKETPAIR), Jt: 1},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: uint32(unix.SYS_IO_URING_SETUP), Jt: 0, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "applying seccomp internet-socket denial")
	}
	return nil
}

func nativeAuditArchitecture() (uint32, error) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, nil
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, nil
	default:
		return 0, e.New(e.CodeEngineCapability, "execution sandbox has no audited seccomp ABI for %s", runtime.GOARCH)
	}
}

func replaceSandboxProcess(path string, args, env []string) error {
	if err := unix.Exec(path, args, env); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return e.Wrap(e.CodeEngineCapability, err, "executing command under sandbox")
		}
		return e.Wrap(e.CodeBuildFailed, err, "executing sandboxed build command")
	}
	return nil
}

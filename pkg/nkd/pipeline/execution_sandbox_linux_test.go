//go:build linux

package pipeline

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestExecutionSandboxEnforcesFilesystemAndNetworkPolicy(t *testing.T) {
	abi, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION, 0, 0, 0)
	if errno != 0 || abi < 4 {
		t.Skipf("Landlock ABI >= 4 unavailable: abi=%d errno=%v", abi, errno)
	}
	allowed := t.TempDir()
	outside := t.TempDir()
	for _, mode := range []string{"allowed-write", "outside-write", "network"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestExecutionSandboxPayload$")
		cmd.Env = append(os.Environ(),
			"NKD_SANDBOX_STAGE=apply",
			"NKD_SANDBOX_MODE="+mode,
			"NKD_SANDBOX_ALLOWED="+allowed,
			"NKD_SANDBOX_OUTSIDE="+outside,
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sandbox mode %s failed: %v\n%s", mode, err, output)
		}
	}
	if _, err := os.Stat(filepath.Join(allowed, "artifact")); err != nil {
		t.Fatalf("allowed output was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "tampered")); !os.IsNotExist(err) {
		t.Fatalf("sandbox allowed a write outside its authority: %v", err)
	}
}

// TestExecutionSandboxPayload is a subprocess-only helper. The first invocation
// applies the policy and execs a clean copy of this test binary; the second probes
// the resulting process rather than trusting a declared sandbox label.
func TestExecutionSandboxPayload(t *testing.T) {
	stage := os.Getenv("NKD_SANDBOX_STAGE")
	if stage == "" {
		t.Skip("sandbox subprocess helper")
	}
	if stage == "apply" {
		if err := os.Setenv("NKD_SANDBOX_STAGE", "probe"); err != nil {
			t.Fatal(err)
		}
		if err := SandboxExec([]string{os.Getenv("NKD_SANDBOX_ALLOWED")}, []string{os.Args[0], "-test.run=^TestExecutionSandboxPayload$"}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		t.Fatal("sandbox exec unexpectedly returned")
	}

	switch os.Getenv("NKD_SANDBOX_MODE") {
	case "allowed-write":
		if err := os.WriteFile(filepath.Join(os.Getenv("NKD_SANDBOX_ALLOWED"), "artifact"), []byte("ok"), 0o600); err != nil {
			t.Fatalf("allowed write failed: %v", err)
		}
	case "outside-write":
		if err := os.WriteFile(filepath.Join(os.Getenv("NKD_SANDBOX_OUTSIDE"), "tampered"), []byte("bad"), 0o600); err == nil {
			t.Fatal("write outside the sandbox unexpectedly succeeded")
		}
	case "network":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			_ = listener.Close()
			t.Fatal("internet-family socket creation unexpectedly succeeded")
		}
	default:
		t.Fatalf("unknown sandbox probe mode %q", os.Getenv("NKD_SANDBOX_MODE"))
	}
}

func TestSandboxSeccompProgramLoadsNativeArchitecture(t *testing.T) {
	arch, err := nativeAuditArchitecture()
	if err != nil || arch == 0 {
		t.Fatalf("native audit architecture is unavailable: arch=%x err=%v", arch, err)
	}
}

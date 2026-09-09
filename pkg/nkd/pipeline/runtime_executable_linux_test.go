//go:build linux

package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestQualifiedRuntimeUsesSealedDescriptorForObservationBuildAndCleanup(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "runtime")
	cleanupLog := filepath.Join(root, "cleanup.log")
	original := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = rm ]; then printf original > %q; fi\nprintf original", cleanupLog)
	if err := os.WriteFile(source, []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := snapshotRuntimeExecutable(source)
	if err != nil {
		t.Fatal(err)
	}
	defer held.capability.Close()
	heldFile := held.capability.OSFile()

	link, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", heldFile.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(link, "memfd:nkd-qualified-runtime") || strings.Contains(link, source) {
		t.Fatalf("qualified runtime retained a replaceable snapshot pathname: %q", link)
	}
	const requiredSeals = unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	seals, err := unix.FcntlInt(heldFile.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&requiredSeals != requiredSeals {
		t.Fatalf("qualified runtime descriptor is not sealed: seals=%#x err=%v", seals, err)
	}
	if _, err := heldFile.WriteAt([]byte("attacker"), 0); err == nil {
		t.Fatal("sealed qualified runtime descriptor remained writable")
	}

	malicious := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = rm ]; then printf malicious > %q; fi\nprintf malicious", cleanupLog)
	if err := os.WriteFile(source, []byte(malicious), 0o755); err != nil {
		t.Fatal(err)
	}

	// Stage-two observation invokes this same held capability.
	out, err := heldExecutionOutput(context.Background(), held)
	if err != nil || string(out) != "original" {
		t.Fatalf("held observation executed replaced pathname bytes: %q, %v", out, err)
	}

	// Every qualified build runtime command is routed to the descriptor capability.
	var buildOutput bytes.Buffer
	opts := RunOptions{
		Ctx: context.Background(), Runtime: source,
		Stdout: &buildOutput, Stderr: &buildOutput,
		execution: &verifiedExecution{runtime: held},
	}
	err = runContainerRuntime(opts)
	if err != nil || buildOutput.String() != "original" {
		t.Fatalf("qualified build executed replaced pathname bytes: %q, %v", buildOutput.String(), err)
	}

	// Cancellation/final cleanup uses the descriptor too; it cannot fall back to the
	// now-malicious source pathname after qualification.
	reaper := newReaper(context.Background(), source)
	reaper.setRuntime(source, held)
	reaper.add("container")
	reaper.close()
	if got, err := os.ReadFile(cleanupLog); err != nil || string(got) != "original" {
		t.Fatalf("qualified cleanup executed replaced pathname bytes: %q, %v", got, err)
	}
}

func TestQualifiedRuntimeScrubsAmbientLoaderDockerAndHelperControls(t *testing.T) {
	t.Setenv("LD_PRELOAD", "/attacker/preload.so")
	t.Setenv("LD_LIBRARY_PATH", "/attacker/libs")
	t.Setenv("PATH", "/attacker/bin")
	t.Setenv("HOME", "/attacker/home")
	t.Setenv("DOCKER_HOST", "tcp://attacker.example:2375")
	t.Setenv("DOCKER_CONTEXT", "attacker")
	t.Setenv("DOCKER_CONFIG", "/attacker/docker")
	t.Setenv("DOCKER_CERT_PATH", "/attacker/certs")
	t.Setenv("DOCKER_TLS_VERIFY", "1")

	source := filepath.Join(t.TempDir(), "runtime")
	script := `#!/bin/sh
printf '%s|%s|%s|%s|%s|%s' "${LD_PRELOAD-unset}" "${LD_LIBRARY_PATH-unset}" "$PATH" "$HOME" "$DOCKER_HOST" "$DOCKER_CONFIG"
`
	if err := os.WriteFile(source, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := snapshotRuntimeExecutable(source)
	if err != nil {
		t.Fatal(err)
	}
	defer held.capability.Close()
	out, err := heldExecutionOutput(context.Background(), held)
	if err != nil {
		t.Fatal(err)
	}
	want := "unset|unset|/nonexistent|/dev/null|" + qualifiedRuntimeEndpoint + "|/dev/null"
	if string(out) != want {
		t.Fatalf("qualified runtime inherited ambient execution controls: got %q want %q", out, want)
	}
}

func TestQualifiedRuntimeAcceptsSymlinkInstallButPinsResolvedBytes(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "runtime-original")
	link := filepath.Join(root, "runtime")
	if err := os.WriteFile(original, []byte("#!/bin/sh\nprintf original"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, link); err != nil {
		t.Fatal(err)
	}
	held, err := snapshotRuntimeExecutable(link)
	if err != nil {
		t.Fatalf("ordinary symlink runtime installation did not qualify: %v", err)
	}
	defer held.capability.Close()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	malicious := filepath.Join(root, "runtime-malicious")
	if err := os.WriteFile(malicious, []byte("#!/bin/sh\nprintf malicious"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(malicious, link); err != nil {
		t.Fatal(err)
	}
	out, err := heldExecutionOutput(context.Background(), held)
	if err != nil || string(out) != "original" {
		t.Fatalf("qualified runtime followed a replaced install symlink: %q, %v", out, err)
	}
}

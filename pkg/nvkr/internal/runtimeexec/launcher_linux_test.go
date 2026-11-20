// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runtimeexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func TestMain(tests *testing.M) {
	if len(os.Args) == 5 && os.Args[1] == "runtime-test-launcher" {
		if err := deathTestLauncher(os.Args[2], os.Args[3], os.Args[4] == "outbound"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 0 && os.Args[0] == "/usr/bin/slirp4netns" {
		if err := outboundFixture(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "runtime-test-assembly" {
		if err := assemblyPayload(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "runtime-test-heartbeat" {
		for counter := 0; ; counter++ {
			if err := os.WriteFile("/out/heartbeat", []byte(fmt.Sprintf("%d\n", counter)), 0o644); err != nil {
				os.Exit(1)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if len(os.Args) == 2 && os.Args[1] == "runtime-test-block" {
		child := exec.Command("/bin/payload", "runtime-test-heartbeat")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// This deliberately detached descendant must die with its PID namespace,
		// even though group-directed cancellation cannot reach its new session.
		_ = child.Wait()
		os.Exit(1)
	}
	if len(os.Args) == 2 && os.Args[1] == EntryArgument {
		if err := Entry(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "runtime-test-payload" {
		if err := runtimePayload(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(tests.Run())
}

func assemblyPayload() error {
	if os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() != 1 {
		return fmt.Errorf("assembly did not enter namespace root")
	}
	if err := admitAssemblyIDs(); err != nil {
		return err
	}
	if err := checkPayloadRootFlags(); err != nil {
		return err
	}
	if raw, err := os.ReadFile("/usr/public"); err != nil || string(raw) != "bootstrap\n" {
		return fmt.Errorf("read-only bootstrap projection unavailable: %q %v", raw, err)
	}
	if _, err := os.ReadFile("/usr/private"); !errors.Is(err, unix.EACCES) {
		return fmt.Errorf("namespace root read host-root-only file: %v", err)
	}
	// Even when the child can undo its own read-only bind attribute, host root
	// remains unmapped and cannot be impersonated through namespace capabilities.
	err := unix.Mount("", "/usr", "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_NOSUID|unix.MS_NODEV, "")
	if err != nil && !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.EACCES) {
		return fmt.Errorf("unexpected bootstrap remount result: %w", err)
	}
	if err := os.WriteFile("/usr/public", []byte("modified"), 0o644); !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EROFS) {
		return fmt.Errorf("namespace root modified host-owned bootstrap: %v", err)
	}
	if err := os.Mkdir("/out/mount", 0o755); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", "/out/mount", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=1m"); err != nil {
		return fmt.Errorf("assembly cannot create its nested package mount: %w", err)
	}
	if err := os.WriteFile("/out/mount/package", []byte("installed\n"), 0o644); err != nil {
		return err
	}
	if err := os.Chown("/out/mount/package", 65534, 65534); err != nil {
		return fmt.Errorf("assembly cannot assign package ownership: %w", err)
	}
	if _, err := os.ReadFile("/dev/null"); err != nil {
		return fmt.Errorf("assembly null device unavailable: %w", err)
	}
	for _, device := range []struct {
		name         string
		major, minor uint32
	}{{"full", 1, 7}, {"tty", 5, 0}} {
		var stat unix.Stat_t
		if err := unix.Stat("/dev/"+device.name, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFCHR ||
			unix.Major(stat.Rdev) != device.major || unix.Minor(stat.Rdev) != device.minor {
			return fmt.Errorf("assembly %s device differs from its fixed API: %v", device.name, err)
		}
	}
	if err := os.WriteFile("/dev/full", []byte("x"), 0o600); !errors.Is(err, unix.ENOSPC) {
		return fmt.Errorf("assembly full device does not report ENOSPC: %v", err)
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if tty != nil {
		_ = tty.Close()
	}
	if !errors.Is(err, unix.ENXIO) {
		return fmt.Errorf("assembly retained a controlling terminal: %v", err)
	}
	if _, err := os.Stat("/dev/kvm"); !os.IsNotExist(err) {
		return fmt.Errorf("assembly exposes a KVM device")
	}
	if err := unix.Unmount("/out/mount", 0); err != nil {
		return err
	}
	return os.WriteFile("/out/result", []byte("assembly isolated\n"), 0o644)
}

func TestNativeAssemblyNamespace(t *testing.T) {
	if os.Getenv("NVKR_TEST_NATIVE_RUNTIME") != "1" {
		t.Skip("requires an explicitly selected privileged native Linux test runner")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	launcher, err := AdmitAssembly(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer launcher.Close()
	parent := nativeFixtureParent(t)
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root, output, bootstrap := filepath.Join(parent, "root"), filepath.Join(parent, "output"), filepath.Join(parent, "bootstrap")
	for _, directory := range []string{filepath.Join(root, "bin"), output, bootstrap} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{parent, root, output} {
		if err := os.Chown(directory, AssemblyHostID, AssemblyHostID); err != nil {
			t.Fatal(err)
		}
	}
	for name, mode := range map[string]os.FileMode{"public": 0o644, "private": 0o600} {
		if err := os.WriteFile(filepath.Join(bootstrap, name), []byte("bootstrap\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	source, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	payload, err := os.OpenFile(filepath.Join(root, "bin/payload"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(payload, source)
	if err := errors.Join(copyErr, payload.Close()); err != nil {
		t.Fatal(err)
	}
	var directories []*os.File
	for _, name := range []string{root, output, bootstrap} {
		directory, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		defer directory.Close()
		directories = append(directories, directory)
	}
	var diagnostic bytes.Buffer
	request := Request{Root: directories[0], Projections: []Projection{
		{Directory: directories[1], Target: "/out", Writable: true}, {Directory: directories[2], Target: "/usr"},
	}, Command: []string{"/bin/payload", "runtime-test-assembly"}, Environment: []string{"LANG=C"}, Workdir: "/out", Stdout: &diagnostic, Stderr: &diagnostic}
	if err := launcher.Execute(ctx, request); err != nil {
		t.Fatalf("assembly namespace: %v\n%s", err, diagnostic.String())
	}
	raw, err := os.ReadFile(filepath.Join(output, "result"))
	if err != nil || string(raw) != "assembly isolated\n" {
		t.Fatalf("assembly output: %q %v", raw, err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(filepath.Join(output, "result"), &stat); err != nil || stat.Uid != AssemblyHostID || stat.Gid != AssemblyHostID {
		t.Fatalf("assembly output escaped mapped IDs: %d:%d %v", stat.Uid, stat.Gid, err)
	}
	checkNamespaceCancellation(t, ctx, launcher, request, output)
}

func runtimePayload() error {
	if os.Geteuid() != 65534 || os.Getegid() != 65534 || os.Getpid() != 1 {
		return fmt.Errorf("wrong payload identity or PID namespace")
	}
	if err := checkPayloadRootFlags(); err != nil {
		return err
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	for _, want := range []string{"CapEff:\t0000000000000000", "CapPrm:\t0000000000000000", "CapBnd:\t0000000000000000", "NoNewPrivs:\t1"} {
		if !bytes.Contains(status, []byte(want)) {
			return fmt.Errorf("payload omitted restriction %s", want)
		}
	}
	if err := os.WriteFile("/bin/mutation", []byte("escape"), 0o644); !errors.Is(err, unix.EROFS) {
		return fmt.Errorf("runtime root was writable: %v", err)
	}
	namespace, err := os.Readlink("/proc/self/ns/net")
	if err != nil || namespace == os.Getenv("NVKR_TEST_PARENT_NETNS") || os.Getenv("NVKR_TEST_PARENT_NETNS") == "" {
		return fmt.Errorf("network namespace was not separated from its parent: %q, %v", namespace, err)
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	loopback := false
	for _, network := range interfaces {
		addresses, err := network.Addrs()
		if err != nil {
			return err
		}
		if network.Name == "lo" {
			loopback = network.Flags&net.FlagUp != 0
			continue
		}
		if network.Name == "tap0" && os.Getenv("NVKR_TEST_OUTBOUND") == "1" {
			if network.Flags&net.FlagUp == 0 || network.MTU != 1500 || len(addresses) != 1 || addresses[0].String() != "10.0.2.100/24" {
				return fmt.Errorf("private TAP configuration differs: %v %v", network, addresses)
			}
			continue
		}
		// Loaded kernel tunnel modules may create inactive, addressless fallback
		// devices in every new namespace. They are not external connectivity.
		if network.Flags&net.FlagUp != 0 || len(addresses) != 0 {
			return fmt.Errorf("private namespace exposes a non-loopback network: %v, %v", network, addresses)
		}
	}
	if !loopback {
		return fmt.Errorf("private namespace lacks active loopback")
	}
	if _, err := os.Stat("/etc/passwd"); !os.IsNotExist(err) {
		return fmt.Errorf("host filesystem remained visible")
	}
	if _, err := os.Stat("/dev/kvm"); !os.IsNotExist(err) {
		return fmt.Errorf("unselected KVM device remained visible")
	}
	if _, err := os.ReadFile("/dev/null"); err != nil {
		return fmt.Errorf("private null device unavailable: %w", err)
	}
	if err := os.WriteFile("/out/result", []byte("isolated\n"), 0o644); err != nil {
		return err
	}
	fmt.Println("isolated")
	return nil
}

func outboundFixture() error {
	if !slices.Equal(os.Args, outboundCommand()) || !slices.Equal(os.Environ(), outboundEnvironment()) || os.Getpid() != 1 || os.Geteuid() != 65534 || os.Getegid() != 65534 {
		return fmt.Errorf("outbound helper has unexpected invocation or identity")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	for _, want := range []string{"CapEff:\t0000000000000000", "CapPrm:\t0000000000000000", "CapBnd:\t0000000000000000", "NoNewPrivs:\t1"} {
		if !bytes.Contains(status, []byte(want)) {
			return fmt.Errorf("outbound helper omitted %s", want)
		}
	}
	parent, err := os.ReadFile("/parent-netns")
	if err != nil {
		return err
	}
	namespace, err := os.Readlink("/proc/self/ns/net")
	if err != nil || namespace != string(parent) {
		return fmt.Errorf("helper lost its admitted outer network: %v", err)
	}
	if err := checkPayloadRootFlags(); err != nil {
		return err
	}
	resolver, err := os.ReadFile("/etc/resolv.conf")
	if err != nil || !validResolver(string(resolver)) {
		return fmt.Errorf("helper resolver unavailable: %v", err)
	}
	device, err := unix.NewIfreq("tap0")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(6, unix.TUNGETIFF, device); err != nil || device.Name() != "tap0" {
		return fmt.Errorf("helper TAP unavailable: %v", err)
	}
	if _, err := unix.Write(7, []byte{'1'}); err != nil {
		return err
	}
	if _, err := os.Stat("/exit-helper"); err == nil {
		time.Sleep(100 * time.Millisecond)
		return fmt.Errorf("fixture helper exited after readiness")
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestNativeOutboundRuntimeNamespace(t *testing.T) {
	if os.Getenv("NVKR_TEST_NATIVE_RUNTIME") != "1" {
		t.Skip("requires an explicitly selected privileged native Linux test runner")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	launcher, err := AdmitOutbound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer launcher.Close()
	parent := nativeFixtureParent(t)
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root, output := filepath.Join(parent, "root"), filepath.Join(parent, "output")
	for _, directory := range []string{filepath.Join(root, "bin"), filepath.Join(root, "usr/bin"), output} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(output, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bin/payload", "usr/bin/slirp4netns"} {
		if err := os.WriteFile(filepath.Join(root, name), source, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	parentNamespace, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "parent-netns"), []byte(parentNamespace), 0o644); err != nil {
		t.Fatal(err)
	}
	rootFD, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFD.Close()
	outFD, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer outFD.Close()
	var stdout, stderr bytes.Buffer
	request := Request{Root: rootFD, Outbound: true, Projections: []Projection{{Directory: outFD, Target: "/out", Writable: true}},
		Command: []string{"/bin/payload", "runtime-test-payload"}, Workdir: "/out",
		Environment: []string{"NVKR_TEST_PARENT_NETNS=" + parentNamespace, "NVKR_TEST_OUTBOUND=1"}, Stdout: &stdout, Stderr: &stderr}
	if err := launcher.Execute(ctx, request); err != nil {
		t.Fatalf("outbound namespace: %v\n%s", err, stderr.String())
	}
	if stdout.String() != "isolated\n" {
		t.Fatalf("consumer did not finish: %q", stdout.String())
	}
	checkNamespaceCancellation(t, ctx, launcher, request, output)
	if err := os.WriteFile(filepath.Join(root, "exit-helper"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	request.Command = []string{"/bin/payload", "runtime-test-block"}
	if err := launcher.Execute(ctx, request); err == nil {
		t.Fatal("consumer succeeded after helper exit")
	}
	if ctx.Err() != nil {
		t.Fatal("helper exit did not stop its consumer promptly")
	}
}

func TestNativeRuntimeNamespace(t *testing.T) {
	if os.Getenv("NVKR_TEST_NATIVE_RUNTIME") != "1" {
		t.Skip("requires an explicitly selected privileged native Linux test runner")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	launcher, err := Admit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer launcher.Close()
	parent := nativeFixtureParent(t)
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	payload, err := os.OpenFile(filepath.Join(root, "bin/payload"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(payload, source)
	closeErr := payload.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "output")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(output, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	rootFD, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFD.Close()
	outFD, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer outFD.Close()
	var stdout, stderr bytes.Buffer
	parentNamespace, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Root: rootFD, Projections: []Projection{{Directory: outFD, Target: "/out", Writable: true}}, Command: []string{"/bin/payload", "runtime-test-payload"},
		Environment: []string{"LANG=C", "NVKR_TEST_PARENT_NETNS=" + parentNamespace}, Workdir: "/out", Stdout: &stdout, Stderr: &stderr}
	if err := launcher.Execute(ctx, request); err != nil {
		t.Fatalf("namespace execution: %v\n%s", err, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "isolated" {
		t.Fatalf("unexpected payload output: %s", stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(output, "result"))
	if err != nil || string(raw) != "isolated\n" {
		t.Fatalf("output projection: %q %v", raw, err)
	}
	checkNamespaceCancellation(t, ctx, launcher, request, output)
	if err := launcher.Close(); err != nil {
		t.Fatal(err)
	}
	err = launcher.Execute(ctx, request)
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("closed launcher accepted work: %v", err)
	}
}

func nativeFixtureParent(t *testing.T) string {
	t.Helper()
	root := os.Getenv("NVKR_TEST_RUNTIME_SCRATCH")
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		t.Fatal("native namespace test requires an explicitly selected executable staging filesystem")
	}
	var filesystem unix.Statfs_t
	if err := unix.Statfs(root, &filesystem); err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture statfs flags=%#x; root protection uses mount_setattr(AT_EMPTY_PATH|AT_RECURSIVE, attr_set=RDONLY|NOSUID|NODEV, attr_clr=0)", filesystem.Flags)
	if filesystem.Flags&(unix.ST_NOEXEC|unix.ST_RDONLY) != 0 {
		t.Fatal("fixture filesystem must permit execution and staging; inherited restrictions are never cleared")
	}
	// A direct child avoids Go's host-root-owned 0700 temporary ancestor, which
	// the private assembly user namespace intentionally cannot traverse.
	parent, err := os.MkdirTemp(root, "namespace-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(parent); err != nil {
			t.Error(err)
		}
	})
	return parent
}

func checkPayloadRootFlags() error {
	var filesystem unix.Statfs_t
	if err := unix.Statfs("/", &filesystem); err != nil {
		return err
	}
	const required = unix.ST_RDONLY | unix.ST_NOSUID | unix.ST_NODEV
	if filesystem.Flags&required != required || filesystem.Flags&unix.ST_NOEXEC != 0 {
		return fmt.Errorf("payload root lacks its admitted executable/read-only mount attributes: %#x", filesystem.Flags)
	}
	return nil
}

func checkNamespaceCancellation(t *testing.T, parent context.Context, launcher *Launcher, request Request, output string) {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	request.Command = []string{"/bin/payload", "runtime-test-block"}
	result := make(chan error, 1)
	joined := false
	go func() { result <- launcher.Execute(ctx, request) }()
	defer func() {
		cancel()
		if !joined {
			<-result
		}
	}()
	heartbeat := filepath.Join(output, "heartbeat")
	for {
		if raw, err := os.ReadFile(heartbeat); err == nil && len(raw) > 0 {
			break
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case err := <-result:
			joined = true
			timer.Stop()
			t.Fatalf("namespace exited before its detached descendant started: %v", err)
		case <-parent.Done():
			timer.Stop()
			t.Fatal("namespace descendant did not start before deadline")
		case <-timer.C:
		}
	}
	cancel()
	err := <-result
	joined = true
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("canceled namespace returned %v", err)
	}
	before, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-parent.Done():
		t.Fatal("test deadline expired while checking descendant termination")
	case <-timer.C:
	}
	after, err := os.ReadFile(heartbeat)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("detached descendant continued writing after namespace owner joined: %q -> %q, %v", before, after, err)
	}
}

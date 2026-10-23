// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	bootstrapLimaVersion      = "2.2.1"
	bootstrapLinuxLimaVersion = "f47461e"
)

// hostBootstrap owns only the temporary container backend. NVKE and Kind run
// on the host; the upstream nerdctl.lima entry point supplies containerd access.
// Each platform uses a newly owned private container backend, independent of
// the operator's host containerd socket and existing Lima instances.
type hostBootstrap struct {
	root, view, temporary, limactl string
	runner                         Runner
	process                        *OwnedProcess
	probe                          *OwnedProcess
	sequence                       int
}

func admitBootstrapHost(platform string, env []string) error {
	version, _, err := bootstrapLimaPlatform(platform)
	if err != nil {
		return err
	}
	for _, name := range []string{"limactl", "nerdctl.lima"} {
		if _, err := commandPath(name, env); err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "%s bootstrap requires installed Lima %s", platform, version)
		}
	}
	return nil
}

func bootstrapLimaPlatform(platform string) (string, []string, error) {
	switch platform {
	case "darwin":
		return bootstrapLimaVersion, []string{"--vm-type=vz"}, nil
	case "linux":
		return bootstrapLinuxLimaVersion, []string{
			"--vm-type=qemu", "--arch=aarch64", "--mount-type=9p",
			"--set", `.images = [{"location":"https://cloud-images.ubuntu.com/releases/resolute/release-20260927/ubuntu-26.04-server-cloudimg-arm64.img","arch":"aarch64","digest":"sha256:63a93bd5a8d76e33b15ceb5daa3657bd79be804748051ab178e643b0f5da22e7","variant":"server"}]`,
			"--set", `.containerd.archives = [{"location":"https://github.com/containerd/nerdctl/releases/download/v2.4.1/nerdctl-full-2.4.1-linux-arm64.tar.gz","arch":"aarch64","digest":"sha256:2deffe14625f93866f90a8f265d0f59d6fa476c7dc54b98adb327b1b4613efd4"}]`,
		}, nil
	default:
		return "", nil, e.New(e.CodeEngineCapability, "unsupported bootstrap host %q", platform)
	}
}

func requireBootstrapLima(ctx context.Context, runner Runner, platform string) (string, string, error) {
	if err := requireContext(ctx); err != nil {
		return "", "", e.Wrap(e.CodeTimeout, err, "bootstrap tool admission")
	}
	if err := admitBootstrapHost(platform, runner.Env); err != nil {
		return "", "", err
	}
	want, _, err := bootstrapLimaPlatform(platform)
	if err != nil {
		return "", "", err
	}
	limactl, _ := commandPath("limactl", runner.Env)
	wrapper, _ := commandPath("nerdctl.lima", runner.Env)
	runner.Env, err = bootstrapAccountEnvironment(ctx, platform, runner.Env)
	if err != nil {
		return "", "", err
	}
	version, err := runner.Capture(ctx, []string{limactl, "--version"}, "bootstrap-lima-version", 15*time.Second)
	if err != nil {
		if ctx.Err() != nil {
			return "", "", e.Wrap(e.CodeTimeout, err, "bootstrap version admission")
		}
		return "", "", e.Wrap(e.CodeEngineCapability, err, "verify bootstrap Lima version")
	}
	if strings.TrimSpace(string(version)) != "limactl version "+want {
		return "", "", e.New(e.CodeEngineCapability, "%s bootstrap requires reviewed Lima %s", platform, want)
	}
	return limactl, wrapper, nil
}

func bootstrapAccountEnvironment(ctx context.Context, platform string, env []string) ([]string, error) {
	var cacheSuffix string
	switch platform {
	case "darwin":
		cacheSuffix = filepath.Join("Library", "Caches")
	case "linux":
		cacheSuffix = ".cache"
	default:
		return nil, e.New(e.CodeEngineCapability, "unsupported bootstrap host %q", platform)
	}
	home, err := nativeAccountHome(ctx)
	if err != nil {
		return nil, err
	}
	// Lima caches public downloads under os.UserCacheDir()/lima. Resolve the
	// platform default from the owned account, independently of ambient HOME,
	// USER and XDG settings. Explicit private instance/config bindings survive.
	return SetEnv(env, map[string]string{"HOME": home, "XDG_CACHE_HOME": filepath.Join(home, cacheSuffix)}), nil
}

func prepareHostBootstrap(ctx context.Context, runner Runner, platform, shared, gateway string, images []string) (backend *hostBootstrap, result error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	_, platformArgs, err := bootstrapLimaPlatform(platform)
	if err != nil {
		return nil, err
	}
	runner.Env, err = bootstrapAccountEnvironment(ctx, platform, runner.Env)
	if err != nil {
		return nil, err
	}
	backend = &hostBootstrap{}
	address, err := netip.ParseAddr(gateway)
	if err != nil || !address.Is4() || !address.IsPrivate() && !(platform == "linux" && address.IsLoopback()) {
		return nil, e.New(e.CodeEngineCapability, "bootstrap requires an observed private IPv4 gateway or Linux loopback")
	}
	limactl, wrapper, err := requireBootstrapLima(ctx, runner, platform)
	if err != nil {
		return nil, err
	}
	mounts, err := bootstrapLimaMounts(shared, images)
	if err != nil {
		return nil, err
	}
	// Keep Unix socket paths short. This directory is newly allocated, private
	// and independent of the user's default Lima home and existing instances.
	temporaryRoot := "/tmp"
	if runtime.GOOS == "darwin" {
		temporaryRoot = "/private/tmp"
	}
	root, err := os.MkdirTemp(temporaryRoot, "nvkr-lima-")
	if err != nil {
		return nil, err
	}
	backend.root, backend.limactl = root, limactl
	backend.view, backend.temporary = filepath.Join(root, "bin"), filepath.Join(shared, "tmp")
	defer func() {
		if result == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
		defer cancel()
		cleanupErr := backend.close(cleanup)
		result = errors.Join(result, cleanupErr)
		if cleanupErr == nil {
			backend = nil
		}
	}()
	if err := os.Mkdir(backend.view, 0700); err != nil {
		return backend, err
	}
	if err := os.Symlink(wrapper, filepath.Join(backend.view, "nerdctl")); err != nil {
		return backend, err
	}
	// Upstream nerdctl.lima resolves limactl through PATH. Keep transport logs
	// out of machine-readable nerdctl output without filtering guest stderr.
	// Lifecycle commands still use the admitted absolute binary directly.
	if err := os.WriteFile(filepath.Join(backend.view, "limactl"), []byte("#!/bin/sh\nset -eu\nexec \"$NVKR_BOOTSTRAP_LIMACTL\" --log-level=error \"$@\"\n"), 0700); err != nil {
		return backend, err
	}
	backend.runner = backend.bind(runner)
	args := []string{limactl, "start", "--foreground", "--tty=false", "--name=bootstrap"}
	args = append(args, platformArgs...)
	timeout, err := remainingWork(ctx)
	if err != nil {
		return backend, err
	}
	args = append(args,
		"--cpus=4", "--memory=6", "--disk=20", "--containerd=user", "--timeout="+timeout.String(), "--mount-only="+mounts,
		"--set", fmt.Sprintf(`.portForwards = [{"guestPort":30081,"hostPort":30081,"hostIP":%q,"proto":"tcp"}]`, gateway), "template:default")
	backend.process, err = backend.runner.Start(ctx, args, "bootstrap-lima-runtime")
	if err != nil {
		return backend, err
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	probe := backend.runner
	probe.Progress = nil
	for {
		if exited, err := backend.process.Exited(); exited {
			return backend, errors.Join(errors.New("temporary Lima backend exited during startup"), err)
		}
		backend.sequence++
		phase := fmt.Sprintf("bootstrap-lima-ready-%03d", backend.sequence)
		probeErr := backend.checkReady(ctx, probe, phase)
		if probeErr == nil {
			if exited, err := backend.process.Exited(); exited {
				return backend, errors.Join(errors.New("temporary Lima backend exited during readiness"), err)
			}
			return backend, nil
		}
		if errors.Is(probeErr, ErrUnjoined) {
			return backend, probeErr
		}
		select {
		case <-ctx.Done():
			return backend, ctx.Err()
		case <-tick.C:
		}
	}
}

func (backend *hostBootstrap) checkReady(ctx context.Context, runner Runner, phase string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	process, err := runner.start(ctx, []string{filepath.Join(backend.view, "nerdctl"), "info"}, phase, false, false)
	if err != nil {
		return err
	}
	// Keep the actual owner if the join fails. No later probe or backend
	// deletion may supersede an unresolved dependent process group.
	backend.probe = process
	waitErr := process.waitOwned(ctx)
	return backend.finishProbe(ctx, waitErr)
}

func (backend *hostBootstrap) finishProbe(ctx context.Context, waitErr error) error {
	exited, exitErr := backend.probe.Exited()
	if !exited || errors.Is(errors.Join(waitErr, exitErr), ErrUnjoined) {
		return errors.Join(ErrUnjoined, errors.New("bootstrap readiness probe did not join"), waitErr, exitErr)
	}
	process := backend.probe
	backend.probe = nil
	_, outErr := process.stdout.result()
	_, stderrErr := process.stderr.result()
	var logErr error
	if process.diagnostic != nil {
		_, logErr = process.diagnostic.result()
	}
	return errors.Join(waitErr, exitErr, ctx.Err(), outErr, stderrErr, logErr)
}

func bootstrapLimaMounts(shared string, images []string) (string, error) {
	if !filepath.IsAbs(shared) || filepath.Clean(shared) != shared || strings.ContainsAny(shared, ",:\r\n\x00") || shared == "/" {
		return "", e.New(e.CodeUsage, "bootstrap sharing requires a clean absolute directory")
	}
	paths := []string{shared + ":w"}
	directories := make([]string, 0, len(images))
	for _, image := range images {
		directory := filepath.Dir(image)
		if !filepath.IsAbs(image) || filepath.Clean(image) != image || strings.ContainsAny(image, ",:\r\n\x00") || directory == "/" {
			return "", e.New(e.CodeUsage, "bootstrap image must have an explicit private parent directory")
		}
		if insidePath(directory, shared) || insidePath(shared, directory) {
			return "", e.New(e.CodeUsage, "bootstrap image imports and writable staging must not overlap")
		}
		directories = append(directories, directory)
	}
	slices.Sort(directories)
	paths = append(paths, slices.Compact(directories)...)
	return strings.Join(paths, ","), nil
}

func (backend *hostBootstrap) bind(runner Runner) Runner {
	values := map[string]string{"KIND_EXPERIMENTAL_PROVIDER": "nerdctl"}
	if backend.root != "" {
		values["LIMA_HOME"] = filepath.Join(backend.root, "state")
		values["LIMA_INSTANCE"] = "bootstrap"
		values["NVKR_BOOTSTRAP_LIMACTL"] = backend.limactl
		values["PATH"] = backend.view + string(os.PathListSeparator) + environmentValue(runner.Env, "PATH")
		values["TMPDIR"] = backend.temporary
		runner.directory = filepath.Dir(backend.temporary)
	}
	runner.Env = SetEnv(runner.Env, values)
	return runner
}

func (backend *hostBootstrap) close(ctx context.Context) error {
	if backend == nil || backend.root == "" {
		return nil
	}
	if backend.probe != nil {
		if err := backend.probe.Stop(ctx); err != nil {
			return err
		}
		backend.probe = nil
	}
	if backend.process != nil {
		backend.sequence++
		stopErr := backend.runner.Run(ctx, []string{backend.limactl, "stop", "--tty=false", "bootstrap"},
			fmt.Sprintf("bootstrap-lima-stop-%03d", backend.sequence), 2*time.Minute)
		joinErr := backend.process.Stop(ctx)
		if err := errors.Join(stopErr, joinErr); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(backend.root); err != nil {
		return err
	}
	backend.root = ""
	return nil
}

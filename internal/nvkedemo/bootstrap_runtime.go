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

const bootstrapLimaVersion = "2.2.1"

// hostBootstrap owns only the temporary container backend. NVKE and Kind run
// on the host; the upstream nerdctl.lima entry point supplies containerd access.
// Linux uses its existing native nerdctl without a virtualization adapter.
type hostBootstrap struct {
	root, view, temporary, limactl string
	runner                         Runner
	process                        *OwnedProcess
	sequence                       int
}

func admitBootstrapHost(platform string, env []string) error {
	if platform != "darwin" {
		return nil
	}
	for _, name := range []string{"limactl", "nerdctl.lima"} {
		if _, err := commandPath(name, env); err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "Mac bootstrap requires installed Lima %s", bootstrapLimaVersion)
		}
	}
	return nil
}

func prepareHostBootstrap(ctx context.Context, runner Runner, platform, shared, gateway string, images []string) (backend *hostBootstrap, result error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	backend = &hostBootstrap{}
	if platform == "linux" {
		return backend, nil
	}
	if platform != "darwin" {
		return nil, e.New(e.CodeEngineCapability, "unsupported bootstrap host %q", platform)
	}
	address, err := netip.ParseAddr(gateway)
	if err != nil || !address.Is4() || !address.IsPrivate() {
		return nil, e.New(e.CodeEngineCapability, "Mac bootstrap requires the observed private IPv4 vmnet gateway")
	}
	if err := admitBootstrapHost(platform, runner.Env); err != nil {
		return nil, err
	}
	limactl, _ := commandPath("limactl", runner.Env)
	wrapper, _ := commandPath("nerdctl.lima", runner.Env)
	version, err := runner.Capture(ctx, []string{limactl, "--version"}, "bootstrap-lima-version", 15*time.Second)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(version)) != "limactl version "+bootstrapLimaVersion {
		return nil, e.New(e.CodeEngineCapability, "bootstrap requires reviewed Lima %s", bootstrapLimaVersion)
	}
	mounts, err := bootstrapLimaMounts(shared, images)
	if err != nil {
		return nil, err
	}
	// Keep Unix socket paths short. This directory is newly allocated, private
	// and independent of the user's default Lima home and existing instances.
	temporaryRoot := os.TempDir()
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
	backend.runner = backend.bind(runner)
	// Lima's verified public download cache remains reusable. Instance state
	// and SSH identities are still isolated by the explicit private LIMA_HOME.
	home, err := os.UserHomeDir()
	if err != nil {
		return backend, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return backend, err
	}
	backend.runner.Env = SetEnv(backend.runner.Env, map[string]string{"HOME": home, "XDG_CACHE_HOME": cache})
	args := []string{limactl, "start", "--foreground", "--tty=false", "--name=bootstrap", "--vm-type=vz",
		"--cpus=4", "--memory=6", "--disk=20", "--containerd=user", "--timeout=8m", "--mount-only=" + mounts,
		"--set", fmt.Sprintf(`.portForwards = [{"guestPort":30081,"hostPort":30081,"hostIP":%q,"proto":"tcp"}]`, gateway), "template:default"}
	backend.process, err = backend.runner.Start(ctx, args, "bootstrap-lima-runtime")
	if err != nil {
		return backend, err
	}
	budget, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
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
		if err := probe.Run(budget, []string{filepath.Join(backend.view, "nerdctl"), "info"}, phase, 10*time.Second); err == nil {
			if exited, err := backend.process.Exited(); exited {
				return backend, errors.Join(errors.New("temporary Lima backend exited during readiness"), err)
			}
			return backend, nil
		}
		select {
		case <-budget.Done():
			return backend, budget.Err()
		case <-tick.C:
		}
	}
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

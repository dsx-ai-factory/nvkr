// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
)

// Native observations use the existing private subprocess owner. Neither raw
// output nor command availability becomes public status or acceptance authority.
func nativeObservationRunner(ctx context.Context, runner Runner, prefix string) (Runner, error) {
	if err := requireContext(ctx); err != nil {
		return Runner{}, err
	}
	if !filepath.IsAbs(runner.Work) || runner.Env == nil {
		return Runner{}, errors.New("native observation requires private storage and the selected environment")
	}
	directory, info, err := securepath.OpenDirectory(runner.Work)
	if err != nil {
		return Runner{}, err
	}
	if err := directory.Close(); err != nil || !currentOwner(info) || info.Mode().Perm() != 0o700 {
		return Runner{}, errors.New("native observation directory must be private and owned")
	}
	logs, err := os.MkdirTemp(runner.Work, prefix)
	if err != nil {
		return Runner{}, err
	}
	runner.Work, runner.Progress, runner.output = logs, nil, nil
	return runner, nil
}

func captureNativeObservation(ctx context.Context, runner Runner, args []string, phase string, timeout time.Duration) error {
	started := time.Now().UTC()
	_, err := runner.Capture(ctx, args, phase, timeout)
	code := 0
	if err != nil {
		code = CommandExitCode(err)
	}
	// Exact command status and timestamps accompany the bounded private log.
	// This descriptive record cannot construct a readiness or cleanup receipt.
	storeErr := privateJSON(filepath.Join(runner.Work, phase+"-status.json"), struct {
		StartedAt, FinishedAt time.Time
		ExitCode              int
	}{started, time.Now().UTC(), code})
	return errors.Join(err, storeErr)
}

func observeNativeAPI(ctx context.Context, runner Runner, cli string) error {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	private, err := nativeObservationRunner(bounded, runner, "native-api-observation-")
	if err != nil {
		return err
	}
	return captureNativeObservation(bounded, private, []string{cli, "get", "clusters", "-o", "json"}, "nvke-api", 10*time.Second)
}

func (s *nativeSiteAttempt) presentStatus(ctx context.Context, runner Runner, access OperatorAccess, walkthrough bool) error {
	// A failed observation is retained privately and does not replace the
	// already completed workload result or tear down a retained environment.
	if err := observeNativeAPI(ctx, runner, s.assets.CLI); errors.Is(err, ErrUnjoined) {
		return err
	}
	if walkthrough && runner.Progress != nil {
		return access.writeLiveStatus(ctx, runner, runner.Progress, true)
	}
	return presentNativeStatus(ctx, runner, access)
}

func retainNativeSiteFailure(ctx context.Context, runner Runner, tools, kubeconfig string, cause error) error {
	if cause == nil || errors.Is(cause, ErrUnjoined) {
		return cause
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	private, err := nativeObservationRunner(bounded, runner, "native-site-failure-")
	if err != nil {
		return cause
	}
	kubectl := filepath.Join(tools, "kubectl")
	prefix := []string{kubectl, "--kubeconfig", kubeconfig, "--request-timeout=5s"}
	commands := []struct {
		phase string
		args  []string
	}{
		{"site-pods", []string{"get", "pods", "--all-namespaces", "-o", "json"}},
		{"site-events", []string{"get", "events", "--all-namespaces", "-o", "json"}},
		{"site-services", []string{"get", "services", "--all-namespaces", "-o", "json"}},
		{"site-endpointslices", []string{"get", "endpointslices.discovery.k8s.io", "--all-namespaces", "-o", "json"}},
	}
	// Exact b615 deploy/nico/controller selects namespace nke-controller;
	// its effective overlay selects the controller name and container.
	logs := []string{"-n", "nke-controller", "logs", "--selector=app.kubernetes.io/name=nke-controller,app.kubernetes.io/instance=nke-controller", "--container=nke-controller", "--tail=100", "--limit-bytes=262144", "--timestamps=true"}
	commands = append(commands, struct {
		phase string
		args  []string
	}{"controller-current", logs}, struct {
		phase string
		args  []string
	}{"controller-previous", append(append([]string{}, logs...), "--previous=true")})
	for _, command := range commands {
		if bounded.Err() != nil {
			break
		}
		args := append(append([]string{}, prefix...), command.args...)
		if err := captureNativeObservation(bounded, private, args, command.phase, 5*time.Second); errors.Is(err, ErrUnjoined) {
			// An unjoined observation must not race subsequent destructive
			// cleanup. Ordinary diagnostic failure never blocks cleanup.
			return errors.Join(cause, err)
		}
	}
	return cause
}

// Preserve an operation failure while the bootstrap-selected site still exists,
// including failures before a tenant owner was created. The source bootstrap
// writes this exact kubeconfig; absence or an unsafe file is diagnostic loss,
// not permission to change the original failure or skip ordinary cleanup.
func (s *nativeSiteAttempt) preserveFailure(ctx context.Context, cause error) error {
	if cause == nil || errors.Is(cause, ErrUnjoined) || s.bootstrapRunner == nil {
		return cause
	}
	kubeconfig := filepath.Join(s.bootstrapRunner.Work, "site.kubeconfig")
	file, info, err := securepath.OpenRegular(kubeconfig)
	if err != nil {
		return cause
	}
	if closeErr := file.Close(); closeErr != nil || !currentOwner(info) || info.Mode().Perm()&0o077 != 0 || info.Size() == 0 {
		return cause
	}
	return retainNativeSiteFailure(ctx, *s.bootstrapRunner, s.assets.HostTools, kubeconfig, cause)
}

// Retain backend-owned serial logs before deletion through the bounded,
// no-symlink reader. Darwin's runtime and vmnet files remain best-effort.
func retainNativeInstanceFiles(ctx context.Context, simdir, instanceRoot, out, id string) (result error) {
	if !safePhase(id) || instanceRoot != "instances" && instanceRoot != "var/instances" {
		return nil
	}
	if err := requireContext(ctx); err != nil {
		return err
	}
	for _, item := range []struct {
		name, destination string
		limit             int64
	}{
		{"runtime.json", "native-runtime-" + id + ".json", 1 << 20},
		{"vmnet-client.log", "native-vmnet-client-" + id + ".log", 8 << 20},
	} {
		raw, err := readRunFile(ctx, simdir, filepath.Join(instanceRoot, id, item.name), item.limit)
		if err == nil {
			_ = privateWrite(filepath.Join(out, item.destination), raw)
		}
	}
	consoles := []struct{ name, suffix string }{{"console.log", ".log"}}
	if instanceRoot == "var/instances" {
		// The pinned libvirt renderer appends serial names to disk.qcow2;
		// console.log belongs to the Darwin backend only.
		consoles = []struct{ name, suffix string }{
			{"disk.qcow2.ttyS0.log", "-ttyS0.log"},
			{"disk.qcow2.ttyS1.log", "-ttyS1.log"},
		}
	}
	for _, console := range consoles {
		file, info, err := openRunFile(simdir, filepath.Join(instanceRoot, id, console.name))
		if os.IsNotExist(err) {
			continue
		}
		if err == nil {
			var raw bytes.Buffer
			if !currentOwner(info) || info.Size() < 0 {
				err = errors.New("native console must be owned and have a valid size")
			} else {
				// Logs may append while the guest is alive. Capture at most the
				// last 8 MiB observed; this is not immutable artifact evidence.
				size := min(info.Size(), 8<<20)
				reader := io.NewSectionReader(file, info.Size()-size, size)
				_, err = transportarchive.CopyExact(ctx, &raw, reader, size)
			}
			err = errors.Join(err, file.Close())
			if err == nil {
				err = privateWrite(filepath.Join(out, "native-console-"+id+console.suffix), raw.Bytes())
			}
		}
		result = errors.Join(result, err)
	}
	return result
}

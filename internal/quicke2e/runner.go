// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
)

type runner struct {
	root, work, engine, nvkr string
	env                      []string
}

// Keep diagnostics bounded even when a subprocess unexpectedly becomes noisy.
type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, fmt.Errorf("command stdout exceeds 4 MiB")
	}
	return b.Buffer.Write(p)
}

func (r *runner) command(ctx context.Context, dir, label, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	logPath := filepath.Join(r.work, "logs", label+".log")
	log, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = log.Close() }()
	fmt.Printf("[quick] %s\n", label)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, r.env
	var output boundedOutput
	cmd.Stdout = io.MultiWriter(log, &output)
	cmd.Stderr = log
	if err := ownedcmd.Run(ctx, cmd); err != nil {
		return nil, fmt.Errorf("%s: %w (log: %s)", label, err, logPath)
	}
	return output.Bytes(), nil
}

func (r *runner) cli(ctx context.Context, label string, args ...string) ([]byte, error) {
	return r.command(ctx, r.work, label, r.nvkr, args...)
}

func cleanEnvironment() []string {
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "GIT_") || key == "GOTOOLCHAIN" || key == "GOMAXPROCS" || key == "GOFLAGS" {
			continue
		}
		env = append(env, item)
	}
	return append(env, "GOTOOLCHAIN=local", "GOMAXPROCS=2", "GOFLAGS=-p=2",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
}

func (r *runner) source(ctx context.Context, platform string) error {
	// Keep the fixture pin, platform substitution and greeting expectation in
	// sync with demos/demo-local.sh; both consume demos/hello/source/hello.sh.
	for _, dir := range []string{"catalog", "source"} {
		if err := os.Mkdir(filepath.Join(r.work, dir), 0o755); err != nil {
			return err
		}
	}
	for _, path := range []string{"catalog/registry.yaml", "source/hello.sh"} {
		raw, err := os.ReadFile(filepath.Join(r.root, "demos/hello", path))
		if err != nil {
			return err
		}
		if path == "catalog/registry.yaml" {
			raw = bytes.ReplaceAll(raw, []byte("linux/amd64"), []byte(platform))
		}
		if err := os.WriteFile(filepath.Join(r.work, path), raw, 0o644); err != nil {
			return err
		}
	}
	// Synthetic fixture commits only; this does not alter user Git configuration.
	r.env = append(r.env, "GIT_AUTHOR_NAME=NVKR Quick E2E", "GIT_AUTHOR_EMAIL=quick@example.invalid",
		"GIT_COMMITTER_NAME=NVKR Quick E2E", "GIT_COMMITTER_EMAIL=quick@example.invalid",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url."+filepath.Join(r.work, "source")+".insteadOf",
		"GIT_CONFIG_VALUE_0=https://demo.example.invalid/hello")
	commands := [][]string{
		{"init", "--quiet", "--template=", "--object-format=sha1", "--initial-branch=main"},
		{"add", "hello.sh"}, {"commit", "--quiet", "-m", "hello v1.0.0"}, {"tag", "v1.0.0"},
	}
	for i, args := range commands {
		args = append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgSign=false", "-c", "core.autocrlf=false"}, args...)
		if _, err := r.command(ctx, filepath.Join(r.work, "source"), fmt.Sprintf("source-%d", i), "git", args...); err != nil {
			return err
		}
	}
	_, err := r.cli(ctx, "pin", "catalog", "pin", "--catalog", "catalog", "--tag", "v1.0.0", "hello")
	return err
}

func (r *runner) smoke(ctx context.Context, platform, image string) (resultErr error) {
	name := "nvkr-smoke-" + filepath.Base(r.work)
	cid := filepath.Join(r.work, "smoke.cid")
	defer func() {
		resultErr = errors.Join(resultErr, r.cleanupContainer(cid, "remove-smoke", false))
	}()
	output, err := r.command(ctx, r.work, "qualify-behavior", r.engine, "run", "--name", name, "--cidfile", cid,
		"--pull=never", "--platform", platform, "--network=none", "--read-only", "--cap-drop=ALL",
		"--security-opt=no-new-privileges", "--user", "65534:65534", "--pids-limit", "32",
		"--volume", filepath.Join(r.work, "extracted")+":/artifact:ro", image, "/artifact/hello", "team")
	if err != nil {
		return err
	}
	if string(output) != "Hello, team! This is hello v1.0.0, built by NVKR.\n" {
		return fmt.Errorf("recovered hello failed its expected behavior: %q", output)
	}
	return nil
}

// Cleanup is keyed by the engine-issued ID in our fresh private directory, not
// by a possibly colliding container name after a failed creation.
func (r *runner) cleanupContainer(path, label string, stop bool) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // Creation failed before the engine recorded a container.
	}
	if err != nil {
		return err
	}
	id := strings.TrimSpace(string(raw))
	if !canonical.IsDigest("sha256:" + id) {
		return fmt.Errorf("invalid engine-issued container ID in %s", path)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	args := []string{"rm", "--force", id}
	if stop {
		args = []string{"stop", "-t", "5", id}
	}
	_, err = r.command(cleanup, r.work, label, r.engine, args...)
	return err
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command quicke2e exercises the public CLI with the maintained hello input.
// Its qualification is an unsigned diagnostic, never release authority.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/containerruntime"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Same multi-platform pin as the maintained Tekton kind registry.
const registryImage = "docker.io/library/registry:3.0.0@sha256:6c5666b861f3505b116bb9aa9b25175e71210414bd010d92035ff64018f9457e"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, deadline := context.WithTimeout(ctx, 12*time.Minute)
	defer deadline()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, e.Wrap(e.CodeBuildFailed, err, "quick E2E failed"))
		os.Exit(1)
	}
}

func run(ctx context.Context) (resultErr error) {
	if len(os.Args) != 1 {
		return fmt.Errorf("quicke2e accepts no arguments; use CONTAINER_RUNTIME to select the engine")
	}
	started := time.Now()
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64" {
		return fmt.Errorf("quick E2E requires Linux/macOS AMD64 or ARM64")
	}
	// The same owner admits the executable override and authored Environment.
	requested := containerruntime.Default(os.Getenv("CONTAINER_RUNTIME"))
	engine, err := containerruntime.Select(filepath.Base(requested), requested)
	if err != nil {
		return err
	}
	input, err := definition.LoadDefinition(ctx, filepath.Join(root, "demos/hello/definition.yaml"))
	if err != nil {
		return err
	}
	environment, err := definition.LoadEnvironment(ctx, filepath.Join(root, "demos/hello/environment.yaml"))
	if err != nil {
		return err
	}
	if len(input.Outputs) != 1 || input.Outputs[0].Name != "hello" {
		return fmt.Errorf("quick E2E requires the maintained single hello output")
	}
	parent := filepath.Join(root, ".nvkr")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(parent, "e2e-quick-")
	if err != nil {
		return err
	}
	fmt.Printf("Quick E2E workspace: %s\n", work)
	r := &runner{root: root, work: work, engine: engine, env: cleanEnvironment()}
	if err := os.Mkdir(filepath.Join(work, "logs"), 0o755); err != nil {
		return err
	}
	defer func() {
		fmt.Printf("Quick E2E elapsed: %s; retained workspace: %s\n", time.Since(started).Round(time.Second), work)
	}()
	if _, err := r.command(ctx, root, "runtime", engine, "info"); err != nil {
		return err
	}
	r.nvkr = filepath.Join(work, "nvkr")
	if _, err := r.command(ctx, root, "build-cli", "go", "build", "-p", "2", "-o", r.nvkr, "./cmd/nvkr"); err != nil {
		return err
	}
	// --rm removes only this owned container after stop, including failed runs.
	registry := "nvkr-" + filepath.Base(work)
	registryID := filepath.Join(work, "registry.cid")
	defer func() {
		resultErr = errors.Join(resultErr, r.cleanupContainer(registryID, "stop-registry", true))
	}()
	if _, err := r.command(ctx, work, "start-registry", engine, "run", "--detach", "--rm", "--name", registry,
		"--cidfile", registryID, "--publish", "127.0.0.1::5000", registryImage); err != nil {
		return err
	}
	port, err := r.command(ctx, work, "registry-port", engine, "port", registry, "5000/tcp")
	if err != nil {
		return err
	}
	endpoint := strings.TrimSpace(string(port))
	if !strings.HasPrefix(endpoint, "127.0.0.1:") || strings.ContainsAny(endpoint, "\r\n") {
		return fmt.Errorf("registry did not bind exactly one host loopback endpoint: %q", endpoint)
	}
	if err := waitRegistry(ctx, endpoint); err != nil {
		return err
	}
	platform := "linux/" + runtime.GOARCH
	input.Outputs[0].Platform = platform
	// The hello output keeps its own tiny catalog. The maintained base supplies
	// the existing immutable GitHub action pins needed by generation.
	helloCatalog := input.Catalog
	input.Outputs[0].Catalog = &helloCatalog
	input.Catalog.Base = filepath.Join(root, "inputs/catalogs/nvke/module-tags")
	environment.UtilityPlatform = platform
	environment.RunsOn = "ubuntu-24.04"
	if runtime.GOARCH == "arm64" {
		environment.RunsOn = "ubuntu-24.04-arm"
	}
	environment.Runtime.Runtime = filepath.Base(engine)
	environment.Publication = &definition.Publication{RepositoryPrefix: endpoint + "/quick", Anonymous: true, PlainHTTP: true}
	environment.Signing = &definition.Signing{Mode: "dry-run", Identity: "^quick-e2e-unsigned$", Issuer: "https://nvkr.dev/local"}
	if err := writeJSON(filepath.Join(work, "definition.json"), input); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(work, "environment.json"), environment); err != nil {
		return err
	}
	if err := r.source(ctx, platform); err != nil {
		return err
	}
	common := []string{"--definition", "definition.json", "--environment", "environment.json"}
	plan, err := r.cli(ctx, "plan", append([]string{"run", "plan"}, append(common, "--json")...)...)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(work, "plan.json"), plan, 0o644); err != nil {
		return err
	}
	// These execute the real renderer and byte-drift check, not the emitted jobs.
	if err := os.Mkdir(filepath.Join(work, "generated"), 0o755); err != nil {
		return err
	}
	generate := append([]string{"run", "generate"}, common...)
	generate = append(generate, "--engine", "github", "--root", filepath.Join(work, "generated"))
	if _, err := r.cli(ctx, "generate", generate...); err != nil {
		return err
	}
	if _, err := r.cli(ctx, "generation-check", append(generate, "--check")...); err != nil {
		return err
	}
	local := append([]string{"run", "local"}, common...)
	local = append(local, "--attempt", "quick", "--publish", "--jobs", "1", "--runtime", engine, "--runner-source", root, "--go", "go", "--ui", "plain")
	if _, err := r.cli(ctx, "execute", local...); err != nil {
		return err
	}
	ref, err := os.ReadFile(filepath.Join(work, ".nvkr/runs/quick/publications/release.txt"))
	if err != nil {
		return err
	}
	reference := strings.TrimSpace(string(ref))
	if !strings.HasPrefix(reference, endpoint+"/quick/releases@sha256:") {
		return fmt.Errorf("unexpected publication reference %q", reference)
	}
	if _, err := r.cli(ctx, "pull", "publish", "pull", "--ref", reference, "--out", "readback", "--plain-http", "--anonymous"); err != nil {
		return err
	}
	if _, err := r.cli(ctx, "inspect", "publish", "inspect", "--layout", "readback"); err != nil {
		return err
	}
	if _, err := r.cli(ctx, "extract", "publish", "extract", "--layout", "readback", "--bundle", "hello", "--out", "extracted"); err != nil {
		return err
	}
	if err := qualifyBindings(ctx, work, reference); err != nil {
		return err
	}
	// Execute only the recovered payload, offline with a read-only filesystem.
	if err := r.smoke(ctx, platform, environment.UtilityContainer); err != nil {
		return err
	}
	if err := rejectChangedPredicate(ctx, work, reference); err != nil {
		return err
	}
	report := fmt.Sprintf("PASS: unsigned quick artifact qualification\nplatform: %s\nruntime: %s\nreference: %s\nelapsed: %s\ncomplete Run and Plan, exact SBOM and publication bindings, registry read-back, isolated greeting: pass\nnegative predicate binding: rejected, original restored and rechecked\nNo signatures, real attestations, generated-job needs or carrier transport were verified.\n", platform, engine, reference, time.Since(started).Round(time.Second))
	if err := os.WriteFile(filepath.Join(work, "qualification.txt"), []byte(report), 0o644); err != nil {
		return err
	}
	fmt.Print(report)
	return nil
}

func writeJSON(path string, value any) error {
	raw, err := canonical.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func waitRegistry(ctx context.Context, endpoint string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint+"/v2/", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("registry readiness: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

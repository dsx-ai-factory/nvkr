// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func narrationRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, relative := range []string{
		"inputs/definitions/demo/arm64-nvidia-tcg.json",
		"inputs/definitions/demo/arm64-upstream-tcg.json",
		"inputs/environments/local-arm64-tcg.json",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, relative)), 0o700); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join("..", "..", relative))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, relative), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func TestDemoNarrationSelectedInputsAndPrivacy(t *testing.T) {
	repo := narrationRepository(t)
	for _, row := range []struct{ network, selected, label string }{
		{"", "arm64-nvidia-tcg.json", "NVIDIA OVN (default)"},
		{"ovn", "arm64-nvidia-tcg.json", "NVIDIA OVN (default)"},
		{"upstream-ovn", "arm64-upstream-tcg.json", "upstream OVN diagnostic"},
		{"kind-calico", "arm64-nvidia-tcg.json", "Kind/Calico diagnostic"},
	} {
		t.Run(row.network, func(t *testing.T) {
			var out bytes.Buffer
			err := presentDemoNarration(t.Context(), &out, nvkedemo.BuildConfig{Repository: repo, Network: row.network, AttemptPrefix: "fresh-01"})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{row.label, "29 named outputs", "inputs/definitions/demo/" + row.selected, "inputs/catalogs/nvke/module-cli-arm64/registry.yaml", "inputs/catalogs/nvke/module-cli-arm64/pins.lock.yaml", "inputs/catalogs/node-images/variants/nvidia-tcg.json", "inputs/environments/local-arm64-tcg.json", ".nvkr/laptop-bootstrap/fresh-01/selected-plan.json", ".nvkr/runs/fresh-01/lifecycle.json", demoDocsURL + "user-concepts.md", demoDocsURL + "catalogs-and-components.md", demoDocsURL + "README.md", demoDocsURL + "local-nvke-demo.md"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q", want)
				}
			}
			for _, forbidden := range []string{repo, "nvke-v1", "nvidia-tcg.json/registry.yaml", "nvidia-tcg.json/pins.lock.yaml"} {
				if strings.Contains(out.String(), forbidden) {
					t.Fatalf("unexpected %q", forbidden)
				}
			}
			if _, err := os.Stat(filepath.Join(repo, ".nvkr")); !os.IsNotExist(err) {
				t.Fatal("presentation created attempt state", err)
			}
		})
	}
}

func TestDemoNarrationFailuresBeforeExecution(t *testing.T) {
	repo := narrationRepository(t)
	for _, row := range []struct {
		name string
		cfg  nvkedemo.BuildConfig
		out  io.Writer
		code nvkrerr.Code
	}{
		{"unknown network", nvkedemo.BuildConfig{Repository: "/must-not-read", Network: "unknown"}, &bytes.Buffer{}, nvkrerr.CodeUsage},
		{"missing inputs", nvkedemo.BuildConfig{Repository: t.TempDir()}, &bytes.Buffer{}, nvkrerr.CodeInvalidCatalog},
		{"nil writer", nvkedemo.BuildConfig{Repository: repo}, nil, nvkrerr.CodeUsage},
		{"write error", nvkedemo.BuildConfig{Repository: repo}, failedProgressWriter{}, nvkrerr.CodePreflightFailed},
		{"short write", nvkedemo.BuildConfig{Repository: repo}, failedProgressWriter{short: true}, nvkrerr.CodePreflightFailed},
	} {
		t.Run(row.name, func(t *testing.T) {
			err := presentDemoNarration(t.Context(), row.out, row.cfg)
			assertCode(t, err, row.code)
			if strings.Contains(err.Error(), "private writer details") {
				t.Fatal("private writer error escaped")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	assertCode(t, presentDemoNarration(ctx, &out, nvkedemo.BuildConfig{Repository: repo}), nvkrerr.CodeTimeout)
	if out.Len() != 0 {
		t.Fatal("canceled introduction emitted output")
	}
}

func TestDemoNarrationInvalidAuthoredInputsEmitNothing(t *testing.T) {
	for _, name := range []string{"inputs/definitions/demo/arm64-nvidia-tcg.json", "inputs/environments/local-arm64-tcg.json"} {
		t.Run(name, func(t *testing.T) {
			repo := narrationRepository(t)
			if err := os.WriteFile(filepath.Join(repo, name), []byte(`{"unknown":"rejected"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			assertCode(t, presentDemoNarration(t.Context(), &out, nvkedemo.BuildConfig{Repository: repo}), nvkrerr.CodeInvalidCatalog)
			if out.Len() != 0 {
				t.Fatal("invalid authored input emitted an introduction")
			}
		})
	}
}

func TestE2EWalkthroughIntroductionAdmissionAndOrder(t *testing.T) {
	repo := narrationRepository(t)
	for _, row := range []struct {
		name                   string
		invalid, outputFailure bool
	}{
		{"admitted", false, false}, {"invalid config", true, false}, {"output failure", false, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			var out bytes.Buffer
			var writer io.Writer = &out
			if row.outputFailure {
				writer = failedProgressWriter{}
			}
			calls := 0
			actions := demoActions{retention: func(context.Context) error {
				calls++
				if !strings.Contains(out.String(), "29 named outputs") {
					t.Fatal("runtime admission preceded narration")
				}
				return nvkrerr.New(nvkrerr.CodeEngineCapability, "bounded test stops before runtime")
			}}
			args := []string{"e2e", "--repo", repo, "--attempt", "fresh-01", "--out", filepath.Join(repo, "fresh-output"), "--walkthrough"}
			if row.invalid {
				args = append(args, "--network", "unknown")
			}
			err := executeRoot(t.Context(), newCommand(writer, io.Discard, actions), args)
			want := nvkrerr.CodeEngineCapability
			if row.invalid {
				want = nvkrerr.CodeUsage
			}
			if row.outputFailure {
				want = nvkrerr.CodePreflightFailed
			}
			assertCode(t, err, want)
			if calls != map[bool]int{true: 0, false: 1}[row.invalid || row.outputFailure] {
				t.Fatal("unexpected execution", calls)
			}
			if row.invalid && out.Len() != 0 {
				t.Fatal("invalid configuration emitted introduction")
			}
			for _, name := range []string{".nvkr", "fresh-output"} {
				if _, err := os.Stat(filepath.Join(repo, name)); !os.IsNotExist(err) {
					t.Fatal("presentation wrote runtime state", err)
				}
			}
		})
	}
}

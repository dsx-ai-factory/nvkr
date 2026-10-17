// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func localConfigurationFixture(t *testing.T) LocalConfiguration {
	t.Helper()
	options := optionsFixture()
	options.Kubeconfig.Path = "private/tenant.kubeconfig"
	return LocalConfiguration{ToolsWorkspace: ".nvkr/runs/fresh-demo", Runner: options,
		Bindings: LocalBindings{ProductImages: []LocalImageBinding{{"image", "registry.example.test/nvke:local"}},
			ToolImages: []LocalImageBinding{{"conformance", "registry.example.test/conformance@sha256:" + strings.Repeat("a", 64)},
				{"busybox", "registry.example.test/busybox@sha256:" + strings.Repeat("b", 64)}}}}
}

func saveLocalConfiguration(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local-conformance.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalConfigurationPreservesRunSelectionAndResolvesAccess(t *testing.T) {
	config := localConfigurationFixture(t)
	config.Timeouts.Run = "3h"
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := saveLocalConfiguration(t, raw)
	loaded, options, err := loadLocalConfiguration(t.Context(), path, "report-output")
	if err != nil {
		t.Fatal(err)
	}
	wantWorkspace, err := filepath.Abs("report-output")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ToolsWorkspace != config.ToolsWorkspace || options.Kubeconfig.Path != filepath.Join(filepath.Dir(path), config.Runner.Kubeconfig.Path) ||
		options.WorkspaceRoot != wantWorkspace || options.RunTimeout != 3*time.Hour || options.CommandTimeout != time.Minute || options.CleanupTimeout != 5*time.Minute || options.StartupTimeout != 5*time.Minute {
		t.Fatalf("local configuration changed Run or access selection: %+v %+v", loaded, options)
	}
	if options.Artifacts != nil || options.ToolArtifacts != nil {
		t.Fatal("configuration restored published capabilities")
	}
	if _, err := os.Stat(wantWorkspace); !os.IsNotExist(err) {
		t.Fatalf("configuration admission created report output: %v", err)
	}
}

func TestLocalConfigurationRejectsAmbiguousAuthorityAndUnboundedInputs(t *testing.T) {
	config := localConfigurationFixture(t)
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	valid := string(raw)
	for _, tc := range []struct{ name, raw string }{
		{"duplicate key", `{"toolsWorkspace":"one","toolsWorkspace":"two"}`},
		{"trailing document", valid + ` {}`},
		{"published source", strings.TrimSuffix(valid, "}") + `,"toolsSource":{"reference":"registry.example.test/image:latest","policy":"policy"}}`},
		{"forged receipt", strings.Replace(valid, `"runner":{`, `"runner":{"artifacts":{},`, 1)},
		{"manual executable", strings.Replace(valid, `"hydrophone":{`, `"hydrophone":{"path":"/manual",`, 1)},
		{"manual image", strings.Replace(valid, `"runner":{`, `"runner":{"image":"registry.example.test/image:latest",`, 1)},
		{"invalid duration", strings.Replace(valid, `"timeouts":{`, `"timeouts":{"run":"forever",`, 1)},
		{"unbounded duration", strings.Replace(valid, `"timeouts":{`, `"timeouts":{"run":"13h",`, 1)},
		{"zero duration", strings.Replace(valid, `"timeouts":{`, `"timeouts":{"run":"0s",`, 1)},
		{"missing tools workspace", strings.Replace(valid, `"toolsWorkspace":".nvkr/runs/fresh-demo"`, `"toolsWorkspace":""`, 1)},
		{"tools workspace whitespace", strings.Replace(valid, `"toolsWorkspace":".nvkr/runs/fresh-demo"`, `"toolsWorkspace":" .nvkr/runs/fresh-demo"`, 1)},
		{"oversized tools workspace", strings.Replace(valid, `"toolsWorkspace":".nvkr/runs/fresh-demo"`, `"toolsWorkspace":"`+strings.Repeat("x", 4097)+`"`, 1)},
		{"oversized", strings.Repeat(" ", (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := loadLocalConfiguration(t.Context(), saveLocalConfiguration(t, []byte(tc.raw)), "/must-not-create")
			requireCode(t, err, e.CodeUsage)
		})
	}
}

func TestLocalRunSourcesCaptureSharedRunOnceAndCloseDistinctOwners(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate Runs", true: "same Run"}[same], func(t *testing.T) {
			productPath := ".nvkr/runs/product"
			toolsPath := ".nvkr/runs/tools"
			if same {
				toolsPath = productPath
			}
			var paths []string
			sources, err := captureLocalRunSources(t.Context(), productPath, toolsPath, func(ctx context.Context, path string) (*lifecycle.LocalDiagnosticArtifacts, error) {
				if ctx != t.Context() {
					t.Fatal("capture context changed")
				}
				paths = append(paths, path)
				// Zero owners model identity/lifetime wiring only. NewLocal's
				// separate tests prove that these cannot execute anything.
				return &lifecycle.LocalDiagnosticArtifacts{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if (sources.product == sources.tools) != same || len(paths) != map[bool]int{true: 1, false: 2}[same] || paths[0] != toolsPath {
				t.Fatalf("capture identity differs: paths=%v shared=%v", paths, sources.product == sources.tools)
			}
			closes := map[*lifecycle.LocalDiagnosticArtifacts]int{}
			sentinel := e.New(e.CodeVerifyFailed, "owned close failure")
			err = sources.closeWith(func(owner *lifecycle.LocalDiagnosticArtifacts) error {
				closes[owner]++
				return sentinel
			})
			if !errors.Is(err, sentinel) || len(closes) != len(paths) {
				t.Fatal("cleanup error or distinct owner count changed")
			}
			for _, count := range closes {
				if count != 1 {
					t.Fatal("same concrete owner closed more than once")
				}
			}
		})
	}
}

func TestLocalRunSourcesCaptureFailureNeverIssuesPair(t *testing.T) {
	sentinel := e.New(e.CodeVerifyFailed, "incomplete Run")
	for _, failAt := range []int{1, 2} {
		calls := 0
		sources, err := captureLocalRunSources(t.Context(), ".nvkr/runs/product", ".nvkr/runs/tools", func(context.Context, string) (*lifecycle.LocalDiagnosticArtifacts, error) {
			calls++
			if calls == failAt {
				return nil, sentinel
			}
			return &lifecycle.LocalDiagnosticArtifacts{}, nil
		})
		if !errors.Is(err, sentinel) || calls != failAt || sources.product != nil || sources.tools != nil {
			t.Fatalf("failed capture issued pair: %+v calls=%d err=%v", sources, calls, err)
		}
	}
}

func TestRunLocalFilesCanceledAdmission(t *testing.T) {
	for _, ctx := range []context.Context{nil, canceledLocalContext(t)} {
		output, err := RunLocalFiles(ctx, "/must-not-open", "/must-not-open", lifecycle.QualificationRuntimeOptions{WorkspaceRoot: "/must-not-create"})
		code := e.CodeTimeout
		if ctx == nil {
			code = e.CodeUsage
		}
		requireCode(t, err, code)
		if output.SchemaVersion != "" {
			t.Fatal("admission emitted output")
		}
	}
}

func TestRunLocalFilesPathAdmissionPrecedesCapabilityAndFileReads(t *testing.T) {
	for _, invalid := range []string{"", " /must-not-open", strings.Repeat("x", 4097)} {
		output, err := RunLocalFiles(t.Context(), invalid, "/must-not-open", lifecycle.QualificationRuntimeOptions{WorkspaceRoot: "/must-not-create"})
		requireCode(t, err, e.CodeUsage)
		if output.SchemaVersion != "" {
			t.Fatal("path admission emitted report output")
		}
	}
}

func canceledLocalContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

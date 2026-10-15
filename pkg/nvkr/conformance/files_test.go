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

func configurationBytes(t *testing.T) []byte {
	t.Helper()
	options := optionsFixture()
	options.Kubeconfig.Path = "private/kubeconfig"
	options.Targets = []DeploymentTarget{{Output: "image", Namespace: "nvke", LabelSelector: "app=nvke", Container: "nvke", RequiredReplicas: 1}}
	config := Configuration{Runner: options, ToolsSource: toolsSourceFixture()}
	config.Timeouts.Run = "11m"
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestConfigurationLoadingUsesSelectedPathsAndDurationBounds(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runner.json")
	if err := os.WriteFile(path, configurationBytes(t), 0600); err != nil {
		t.Fatal(err)
	}
	options, _, err := loadConfiguration(t.Context(), path, "selected-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if options.Kubeconfig.Path != filepath.Join(root, "private/kubeconfig") {
		t.Fatalf("relative paths did not resolve beside configuration: %+v", options)
	}
	workspace, err := filepath.Abs("selected-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if options.WorkspaceRoot != workspace || options.RunTimeout != 11*time.Minute || options.CommandTimeout != time.Minute || options.CleanupTimeout != 5*time.Minute || options.StartupTimeout != 5*time.Minute || options.Artifacts != nil {
		t.Fatalf("runtime configuration or receipt ownership differs: %+v", options)
	}
}

func TestConfigurationRejectsAmbiguousAndAuthorityInputs(t *testing.T) {
	valid := string(configurationBytes(t))
	for _, tc := range []struct{ name, raw string }{
		{"duplicate document key", `{"runner":{},"runner":{}}`},
		{"trailing document", valid + ` {}`},
		{"unknown field", strings.TrimSuffix(valid, "}") + `,"receipt":{}}`},
		{"legacy binary paths", strings.Replace(valid, `"hydrophone":{`, `"hydrophone":{"path":"/manual","digest":"sha256:manual",`, 1)},
		{"forged tools receipt", strings.Replace(valid, `"runner":{`, `"runner":{"toolArtifacts":{},`, 1)},
		{"manual image", strings.Replace(valid, `"runner":{`, `"runner":{"image":"registry.example.test/manual@sha256:manual",`, 1)},
		{"manual version", strings.Replace(valid, `"runner":{`, `"runner":{"harnessVersion":"0.8.0",`, 1)},
		{"forged receipt", strings.Replace(valid, `"runner":{`, `"runner":{"artifacts":{},`, 1)},
		{"runner workspace override", strings.Replace(valid, `"runner":{`, `"runner":{"workspaceRoot":"/foreign",`, 1)},
		{"runner timeout override", strings.Replace(valid, `"runner":{`, `"runner":{"runTimeout":1,`, 1)},
		{"invalid duration", strings.Replace(valid, `"11m"`, `"forever"`, 1)},
		{"unbounded duration", strings.Replace(valid, `"11m"`, `"0s"`, 1)},
		{"oversized", strings.Repeat(" ", (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runner.json")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, _, err := loadConfiguration(t.Context(), path, "/must-not-create")
			requireCode(t, err, e.CodeUsage)
		})
	}
}

func TestRunReferenceFilesAdmissionDoesNotReadConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		ctx                            context.Context
		run, policy, config, workspace string
		code                           e.Code
	}{
		{"nil context", nil, "run", "policy", "/must-not-open", "workspace", e.CodeUsage},
		{"missing run", t.Context(), "", "policy", "/must-not-open", "workspace", e.CodeUsage},
		{"missing policy", t.Context(), "run", "", "/must-not-open", "workspace", e.CodeUsage},
		{"missing configuration", t.Context(), "run", "policy", "", "workspace", e.CodeUsage},
		{"missing workspace", t.Context(), "run", "policy", "/must-not-open", "", e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := RunReferenceFiles(tc.ctx, tc.run, tc.policy, tc.config, lifecycle.QualificationRuntimeOptions{WorkspaceRoot: tc.workspace})
			requireCode(t, err, tc.code)
			if output.SchemaVersion != "" {
				t.Fatal("admission emitted partial output")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output, err := RunReferenceFiles(ctx, "run", "policy", "/must-not-open", lifecycle.QualificationRuntimeOptions{WorkspaceRoot: "workspace"})
	requireCode(t, err, e.CodeTimeout)
	if output.SchemaVersion != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost or emitted output")
	}
}

func TestRunReferenceFilesRejectsWrongLocalPinsBeforeRunAuthentication(t *testing.T) {
	options := buildFixture(t, "success")
	options.Kubeconfig.Digest = "sha256:" + strings.Repeat("c", 64)
	raw, err := json.Marshal(Configuration{Runner: options, ToolsSource: toolsSourceFixture()})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// An unreadable policy proves local byte admission precedes the provider owner.
	output, err := RunReferenceFiles(t.Context(), "registry.example.test/release@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "/must-not-open-policy", path, lifecycle.QualificationRuntimeOptions{WorkspaceRoot: t.TempDir()})
	requireCode(t, err, e.CodeUnpinnedInput)
	if output.SchemaVersion != "" {
		t.Fatal("bad local pin emitted output")
	}
}

func TestRunReferenceFilesRefusesWrongLocatorBeforeConfiguration(t *testing.T) {
	for _, reference := range []string{"https://github.com/example/nvkr/actions/runs/42/attempts/2", "registry.example.test/release:latest"} {
		output, err := RunReferenceFiles(t.Context(), reference, "/must-not-open-policy", "/must-not-open-config", lifecycle.QualificationRuntimeOptions{WorkspaceRoot: "/must-not-create"})
		requireCode(t, err, e.CodeUnverifiedRef)
		if output.SchemaVersion != "" {
			t.Fatal("wrong publication locator emitted reports")
		}
	}
}

func toolsSourceFixture() PublishedSource {
	return PublishedSource{Reference: "registry.example.test/tools@sha256:" + strings.Repeat("a", 64), Policy: "/must-not-open-policy"}
}

func TestToolsSourceAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*PublishedSource)
		valid bool
	}{
		{"exact publication", func(*PublishedSource) {}, true},
		{"missing reference", func(s *PublishedSource) { s.Reference = "" }, false},
		{"tagged reference", func(s *PublishedSource) { s.Reference = "registry.example.test/tools:latest" }, false},
		{"URI reference", func(s *PublishedSource) { s.Reference = "oci://" + s.Reference }, false},
		{"missing policy", func(s *PublishedSource) { s.Policy = "" }, false},
		{"policy leading whitespace", func(s *PublishedSource) { s.Policy = " policy.json" }, false},
		{"policy control", func(s *PublishedSource) { s.Policy = "policy\n.json" }, false},
		{"policy maximum", func(s *PublishedSource) { s.Policy = "/" + strings.Repeat("p", 4095) }, true},
		{"policy over maximum", func(s *PublishedSource) { s.Policy = "/" + strings.Repeat("p", 4096) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var config Configuration
			if err := json.Unmarshal(configurationBytes(t), &config); err != nil {
				t.Fatal(err)
			}
			tc.edit(&config.ToolsSource)
			raw, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, _, err = loadConfiguration(t.Context(), path, "/must-not-create")
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireCode(t, err, e.CodeUsage)
				if !strings.Contains(err.Error(), "published tools source and policy") {
					t.Fatalf("wrong admission branch: %v", err)
				}
			}
		})
	}
}

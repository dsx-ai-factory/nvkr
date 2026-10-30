// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestRunRuntimeAdmissionPrecedesCatalogResolution(t *testing.T) {
	root := t.TempDir()
	environment := filepath.Join(root, "environment.json")
	raw := `{"apiVersion":"nvkr.dev/environment/v1","kind":"Environment","metadata":{"name":"test"},"utilityPlatform":"linux/amd64","utilityContainer":"busybox@sha256:` + strings.Repeat("a", 64) + `"}`
	if err := os.WriteFile(environment, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		extra []string
		code  e.Code
	}{
		{"unsupported Environment", nil, e.CodeEngineCapability},
		{"independent qualifier", []string{"--qualification-policy", "missing-policy.yaml"}, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"run", "generate", "--definition", "missing-definition.yaml", "--environment", environment, "--runtime"}
			_, err := invokeLifecycleCLI(t, append(args, test.extra...)...)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("got %v, want admission code %s before missing Definition/catalog work", err, test.code)
			}
		})
	}
}

func TestRunExactSourceRequiresRuntimeBeforeInputWork(t *testing.T) {
	for _, commit := range []string{"", strings.Repeat("a", 40)} {
		t.Run("commit="+commit, func(t *testing.T) {
			_, err := invokeLifecycleCLI(t, "run", "generate", "--definition", "missing-definition.json", "--environment", "missing-environment.json", "--source-commit", commit)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
				t.Fatalf("source selection reached input work without runtime admission: %v", err)
			}
		})
	}
}

func TestRunSourceOverridesAdmissionPrecedesInputWork(t *testing.T) {
	root := t.TempDir()
	environment := filepath.Join(root, "environment.json")
	raw := `{"apiVersion":"nvkr.dev/environment/v1","kind":"Environment","metadata":{"name":"test"},"utilityPlatform":"linux/amd64","utilityContainer":"busybox@sha256:` + strings.Repeat("a", 64) + `"}`
	if err := os.WriteFile(environment, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"without runtime", []string{"--source-overrides", "{}"}, e.CodeUsage},
		{"with source commit", []string{"--runtime", "--source-overrides", "{}", "--source-commit", strings.Repeat("a", 40)}, e.CodeUsage},
		{"unopted Environment", []string{"--runtime", "--source-overrides", "{}"}, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"run", "generate", "--definition", "missing-definition.yaml", "--environment", environment}, test.args...)
			_, err := invokeLifecycleCLI(t, args...)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("got %v, want %s before Definition or catalog work", err, test.code)
			}
		})
	}
}

func TestRunLocalSourceOverrideFlags(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, test := range []struct {
		name   string
		values []string
		want   []lifecycle.SourceOverrideRequest
	}{
		{"none", nil, []lifecycle.SourceOverrideRequest{}},
		{"constraint and commit", []string{"nvke=0.23.x", "ovn-kubernetes=" + commit},
			[]lifecycle.SourceOverrideRequest{{Name: "nvke", Request: "0.23.x"}, {Name: "ovn-kubernetes", Request: commit}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSourceOverrideFlags(test.values)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %v %v, want %v", got, err, test.want)
			}
		})
	}
	// Name, repetition and request grammar are admitted against the Environment
	// by the shared intake; the command rejects only malformed flags first.
	for _, test := range []struct {
		name string
		args []string
	}{
		{"missing request", []string{"--source-override", "nvke"}},
		{"empty request", []string{"--source-override", "nvke="}},
		{"empty name", []string{"--source-override", "=0.23.x"}},
		{"with captured inputs", []string{"--source-override", "nvke=0.23.x", "--capture-inputs-root", "."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"run", "local", "--definition", "missing-definition.yaml", "--environment", "missing-environment.yaml", "--attempt", "attempt", "--ui", "plain"}, test.args...)
			_, err := invokeLifecycleCLI(t, args...)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
				t.Fatalf("got %v, want %s before Definition or catalog work", err, e.CodeUsage)
			}
		})
	}
}

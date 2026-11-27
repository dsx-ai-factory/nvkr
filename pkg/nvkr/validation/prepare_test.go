// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package validation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// selectionFixture adds a suite group that names no artifact beside the shipped suites.
func selectionFixture(t *testing.T) string {
	t.Helper()
	root := registrationFixture(t)
	for _, id := range []string{"nvke/e2e/bootstrap", "nvke/nvkectl/nvkectl"} {
		raw := bytes.ReplaceAll(shippedContract(t), []byte("id: nvke/nvkectl/contract"), []byte("id: "+id))
		writeCatalogFile(t, filepath.Join(root, id+".yaml"), raw)
	}
	return root
}

func preparedIDs(p *PreparedValidation) string {
	var ids []string
	for _, suite := range p.Suites() {
		ids = append(ids, suite.ID)
	}
	return strings.Join(ids, ",")
}

func TestPrepareSelectionOrderAndDeduplication(t *testing.T) {
	root := selectionFixture(t)
	for _, tc := range []struct {
		name       string
		selections []string
		want       string
	}{
		{"exact", []string{"nvke/nvkectl/contract"}, "nvke/nvkectl/contract"},
		{"user-order", []string{"nvke/nvkectl/contract", "nvke/nvkectl/cli"}, "nvke/nvkectl/contract,nvke/nvkectl/cli"},
		{"suite-group", []string{"nvke/nvkectl"}, "nvke/nvkectl/cli,nvke/nvkectl/contract,nvke/nvkectl/nvkectl"},
		{"repository", []string{"nvke"}, "nvke/e2e/bootstrap,nvke/nvkectl/cli,nvke/nvkectl/contract,nvke/nvkectl/nvkectl"},
		{"exact-then-parent", []string{"nvke/nvkectl/contract", "nvke"}, "nvke/nvkectl/contract,nvke/e2e/bootstrap,nvke/nvkectl/cli,nvke/nvkectl/nvkectl"},
		{"parent-then-exact", []string{"nvke", "nvke/nvkectl/cli"}, "nvke/e2e/bootstrap,nvke/nvkectl/cli,nvke/nvkectl/contract,nvke/nvkectl/nvkectl"},
		{"repeated-segment-name", []string{"nvke/nvkectl/nvkectl"}, "nvke/nvkectl/nvkectl"},
		{"repeated", []string{"nvke/nvkectl/cli", "nvke/nvkectl/cli"}, "nvke/nvkectl/cli"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Prepare(t.Context(), root, tc.selections, "local")
			if err != nil {
				t.Fatal(err)
			}
			if got := preparedIDs(p); got != tc.want {
				t.Fatalf("selected %s, want %s", got, tc.want)
			}
			if p.Environment() != "local" {
				t.Fatal(p.Environment())
			}
		})
	}
}

func TestPrepareRefusesSelections(t *testing.T) {
	root := selectionFixture(t)
	for _, tc := range []struct {
		name       string
		selections []string
		code       e.Code
	}{
		{"none", nil, e.CodeUsage},
		{"empty", []string{""}, e.CodeUsage},
		{"trailing-slash", []string{"nvke/"}, e.CodeUsage},
		{"empty-segment", []string{"nvke//cli"}, e.CodeUsage},
		{"uppercase", []string{"NVKE"}, e.CodeUsage},
		{"too-deep", []string{"nvke/nvkectl/contract/extra"}, e.CodeUsage},
		{"unknown-suite", []string{"nvke/nvkectl/missing"}, e.CodeUnsupported},
		{"unknown-parent", []string{"other"}, e.CodeUnsupported},
		{"unknown-after-valid", []string{"nvke/nvkectl/cli", "nvke/missing"}, e.CodeUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Prepare(t.Context(), root, tc.selections, "local")
			catalogCode(t, err, tc.code)
			if p != nil {
				t.Fatal("refusal returned a prepared validation")
			}
		})
	}
}

func TestPrepareAdmitsEnvironmentAndExecutorRequirements(t *testing.T) {
	for _, tc := range []struct {
		name, environment, old, replacement string
		code                                e.Code
	}{
		{"no-environment", "", "", "", e.CodeUsage},
		{"unknown-environment", "forge", "", "", e.CodeUnsupported},
		{"unmet-capability", "local", "capabilities: [docker]", "capabilities: [docker, bare-metal]", e.CodeEngineCapability},
		{"unsupported-platform", "local", "platforms: [linux/amd64, linux/arm64]", "platforms: [linux/amd64, darwin/arm64]", e.CodeEngineCapability},
		{"unsupported-format", "local", "format: executable", "format: oci", e.CodeEngineCapability},
		{"unsupported-component", "local", "component: nvkectl", "component: kubectl", e.CodeEngineCapability},
		// The executor stages each input as /inputs/<role>/<output>.
		{"unstageable-output-name", "local", "output: nvkectl", "output: bin/nvkectl", e.CodeEngineCapability},
		{"unstageable-output-dots", "local", "output: nvkectl", "output: ..", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := selectionFixture(t)
			if tc.old != "" {
				raw := shippedContract(t)
				if !bytes.Contains(raw, []byte(tc.old)) {
					t.Fatalf("fixture no longer contains %q", tc.old)
				}
				writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/contract.yaml"), bytes.Replace(raw, []byte(tc.old), []byte(tc.replacement), 1))
			}
			p, err := Prepare(t.Context(), root, []string{"nvke/nvkectl"}, tc.environment)
			catalogCode(t, err, tc.code)
			if p != nil {
				t.Fatal("refusal returned a prepared validation")
			}
			// Only selected suites are admitted; an unselected suite cannot block another.
			if tc.old != "" {
				if _, err := Prepare(t.Context(), root, []string{"nvke/nvkectl/cli"}, tc.environment); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// With several unsupported roles, the refusal names the first in sorted order.
func TestPrepareAdmissionRefusalDeterministic(t *testing.T) {
	roles := "inputs:\n"
	for _, role := range []string{"e", "d", "c", "b", "a"} {
		roles += "  " + role + ":\n    output: nvkectl\n    component: kubectl\n    format: executable\n"
	}
	raw := bytes.Replace(shippedContract(t), []byte("inputs:\n"), []byte(roles), 1)
	root := selectionFixture(t)
	writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/contract.yaml"), raw)
	for range 20 {
		_, err := Prepare(t.Context(), root, []string{"nvke/nvkectl/contract"}, "local")
		catalogCode(t, err, e.CodeEngineCapability)
		if !strings.Contains(err.Error(), `role "a"`) {
			t.Fatalf("refusal named another role: %v", err)
		}
	}
}

func TestPrepareFreezesSelectedDocuments(t *testing.T) {
	root := selectionFixture(t)
	path := filepath.Join(root, "nvke/nvkectl/contract.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(t.Context(), root, []string{"nvke/nvkectl/contract"}, "local")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(original)
	suites := p.Suites()
	if len(suites) != 1 || suites[0].DocumentSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected frozen suites: %+v", suites)
	}
	writeCatalogFile(t, path, bytes.Replace(original, []byte("timeout: 30m"), []byte("timeout: 1m"), 1))
	suites[0].Command[0] = "poison"
	suites[0].Inputs["executable"] = RegistrationInput{Output: "poison"}
	again := p.Suites()
	if again[0].Timeout != "30m" || again[0].Command[0] != "make" || again[0].Inputs["executable"].Output != "nvkectl" ||
		again[0].DocumentSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("prepared validation changed after preparation: %+v", again[0])
	}
}

func TestPrepareContextAdmission(t *testing.T) {
	root := selectionFixture(t)
	//nolint:staticcheck // the nil-context refusal is part of the contract.
	_, err := Prepare(nil, root, []string{"nvke"}, "local")
	catalogCode(t, err, e.CodeUsage)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = Prepare(canceled, root, []string{"nvke"}, "local")
	catalogCode(t, err, e.CodeTimeout)
}

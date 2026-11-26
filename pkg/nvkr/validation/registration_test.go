// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package validation

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const shippedRegistrations = "../../../inputs/testing/validator-catalog"

// registrationFixture copies the shipped registrations into a private root.
func registrationFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"nvke/nvkectl/contract.yaml", "nvke/nvkectl/cli.yaml"} {
		raw, err := os.ReadFile(filepath.Join(shippedRegistrations, name))
		if err != nil {
			t.Fatal(err)
		}
		writeCatalogFile(t, filepath.Join(root, name), raw)
	}
	return root
}

func shippedContract(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(shippedRegistrations, "nvke/nvkectl/contract.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRegistrationsShippedCatalogDeterministicAndDetached(t *testing.T) {
	c, err := LoadRegistrations(t.Context(), shippedRegistrations)
	if err != nil {
		t.Fatal(err)
	}
	list := c.List()
	if len(list) != 2 || list[0].ID != "nvke/nvkectl/cli" || list[1].ID != "nvke/nvkectl/contract" {
		t.Fatalf("unexpected discovery: %+v", list)
	}
	contract := list[1]
	if contract.Inputs["executable"] != (RegistrationInput{Output: "nvkectl", Component: "nvkectl", Format: "executable"}) ||
		contract.TestOrigin.FromInput != "executable" || contract.Image.FromInput != "executable" ||
		contract.WorkingDirectory != "cli" || contract.Timeout != "30m" ||
		strings.Join(contract.Requires.Platforms, ",") != "linux/amd64,linux/arm64" ||
		strings.Join(contract.Requires.Capabilities, ",") != "docker" {
		t.Fatalf("shipped contract registration changed: %+v", contract)
	}
	before, err := canonical.Marshal(c.List())
	if err != nil {
		t.Fatal(err)
	}
	list[0].Inputs["executable"] = RegistrationInput{Output: "poison"}
	list[0].Command[0] = "poison"
	list[0].Acquisition.Command[0] = "poison"
	list[0].Environment["NVKECTL"] = "poison"
	list[0].Requires.Platforms[0] = "poison"
	list[0].Requires.Capabilities[0] = "poison"
	after, _ := canonical.Marshal(c.List())
	if !bytes.Equal(before, after) {
		t.Fatal("caller mutated loaded registrations")
	}
	described, err := c.Describe("nvke/nvkectl/contract")
	if err != nil || described.ID != "nvke/nvkectl/contract" {
		t.Fatalf("%+v %v", described, err)
	}
	// Describe takes exact IDs only; parent paths and suffixes are not aliases.
	for _, id := range []string{"contract", "nvke/nvkectl", "nvke", "nvke/nvkectl/contract/", ""} {
		_, err := c.Describe(id)
		catalogCode(t, err, e.CodeUnsupported)
	}
}

func TestRegistrationsSuiteGroupsNeedNotNameAnArtifact(t *testing.T) {
	root := registrationFixture(t)
	raw := bytes.ReplaceAll(shippedContract(t), []byte("id: nvke/nvkectl/contract"), []byte("id: nvke/e2e/bootstrap"))
	writeCatalogFile(t, filepath.Join(root, "nvke/e2e/bootstrap.yaml"), raw)
	c, err := LoadRegistrations(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range c.List() {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "nvke/e2e/bootstrap,nvke/nvkectl/cli,nvke/nvkectl/contract" {
		t.Fatal(ids)
	}
}

func TestRegistrationsHeldRootSurvivesDirectoryReplacement(t *testing.T) {
	path := registrationFixture(t)
	before, err := LoadRegistrations(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := openCatalogRoot(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := os.Rename(path, filepath.Join(t.TempDir(), "original")); err != nil {
		t.Fatal(err)
	}
	writeCatalogFile(t, filepath.Join(path, "nvke/nvkectl/contract.yaml"), []byte("replacement: invalid\n"))
	after, err := loadRegistrationRoot(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := canonical.Marshal(before.List())
	got, _ := canonical.Marshal(after.List())
	if !bytes.Equal(want, got) {
		t.Fatal("discovery resolved entries through the replacement directory")
	}
}

func TestRegistrationsStrictDocuments(t *testing.T) {
	valid := string(shippedContract(t))
	for _, tc := range []struct{ name, raw string }{
		{"unknown-field", valid + "extra: true\n"},
		{"unknown-nested-field", strings.Replace(valid, "image:\n  fromInput: executable", "image:\n  fromInput: executable\n  reference: example.invalid/image@sha256:0", 1)},
		{"case-alias", strings.Replace(valid, "apiVersion:", "APIVersion:", 1)},
		{"nested-case-alias", strings.Replace(valid, "acquisition:\n  command:", "acquisition:\n  Command:", 1)},
		{"duplicate-key", valid + "timeout: 10m\n"},
		{"old-version", strings.Replace(valid, "nvkr.dev/validator-registration/v1", "nvkr.dev/validator/v1", 1)},
		{"unknown-version", strings.Replace(valid, "validator-registration/v1", "validator-registration/v2", 1)},
		{"wrong-kind", strings.Replace(valid, "kind: ValidatorRegistration", "kind: Validator", 1)},
		{"null", strings.Replace(valid, "timeout: 30m", "timeout: null", 1)},
		{"type", strings.Replace(valid, "timeout: 30m", "timeout: [30m]", 1)},
		{"multi-document", valid + "---\n" + valid},
		{"anchor", strings.Replace(valid, "platforms: [linux/amd64, linux/arm64]", "platforms: &p [linux/amd64, linux/arm64]", 1)},
		{"merge", strings.Replace(valid, "testOrigin:\n  fromInput: executable", "testOrigin:\n  <<: {fromInput: executable}", 1)},
		{"empty", ""},
		{"oversize", valid + "#" + strings.Repeat(" ", 1<<20) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := registrationFixture(t)
			writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/contract.yaml"), []byte(tc.raw))
			_, err := LoadRegistrations(t.Context(), root)
			catalogCode(t, err, e.CodeUsage)
		})
	}
}

func TestRegistrationFieldValidation(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"id-differs-from-path", "id: nvke/nvkectl/contract", "id: nvke/nvkectl/cli"},
		{"id-invalid", "id: nvke/nvkectl/contract", "id: NVKE/nvkectl/contract"},
		{"description-missing", "description: Run upstream process contracts against a built nvkectl executable\n", ""},
		{"description-unsafe", "description: Run upstream process contracts against a built nvkectl executable\n", "description: \"Run\\u0007upstream\"\n"},
		{"inputs-missing", "inputs:\n  executable:\n    output: nvkectl\n    component: nvkectl\n    format: executable\n", ""},
		{"inputs-empty", "inputs:\n  executable:\n    output: nvkectl\n    component: nvkectl\n    format: executable\n", "inputs: {}\n"},
		{"role-name", "  executable:\n    output", "  Executable:\n    output"},
		{"output-missing", "    output: nvkectl\n", ""},
		{"component-missing", "    component: nvkectl\n", ""},
		{"format-missing", "    format: executable\n", ""},
		{"test-origin-role", "testOrigin:\n  fromInput: executable", "testOrigin:\n  fromInput: other"},
		{"test-origin-missing", "testOrigin:\n  fromInput: executable\n", ""},
		{"image-role", "image:\n  fromInput: executable", "image:\n  fromInput: other"},
		{"image-missing", "image:\n  fromInput: executable\n", ""},
		{"working-directory-parent", "workingDirectory: cli", "workingDirectory: ../cli"},
		{"working-directory-absolute", "workingDirectory: cli", "workingDirectory: /src/cli"},
		{"working-directory-unclean", "workingDirectory: cli", "workingDirectory: cli/./cmd"},
		{"working-directory-dot", "workingDirectory: cli", "workingDirectory: ."},
		{"working-directory-backslash", "workingDirectory: cli", "workingDirectory: 'cli\\cmd'"},
		{"command-missing", "command:\n  - make\n  - --no-print-directory\n  - test-artifact\n  - GO=go\n  - GO_TEST_FLAGS=-count=1 -timeout=15m -json\n", ""},
		{"command-empty", "command:\n  - make\n  - --no-print-directory\n  - test-artifact\n  - GO=go\n  - GO_TEST_FLAGS=-count=1 -timeout=15m -json\n", "command: []\n"},
		{"command-empty-argument", "  - --no-print-directory\n", "  - ''\n"},
		{"command-unsafe-argument", "  - --no-print-directory\n", "  - \"a\\nb\"\n"},
		{"acquisition-empty", "acquisition:\n  command: [go, mod, download]", "acquisition: {}"},
		{"acquisition-empty-command", "acquisition:\n  command: [go, mod, download]", "acquisition:\n  command: []"},
		{"environment-name", "  NVKECTL: ", "  1NVKECTL: "},
		{"environment-reference-role", "${inputs.executable.path}", "${inputs.other.path}"},
		{"environment-reference-attribute", "${inputs.executable.path}", "${inputs.executable.digest}"},
		{"environment-reference-unclosed", "${inputs.executable.path}", "${inputs.executable.path"},
		{"environment-reference-scope", "${inputs.executable.path}", "${env.HOME}"},
		{"command-reference-role", "  - --no-print-directory\n", "  - ${inputs.typo.path}\n"},
		{"command-reference-unclosed", "  - --no-print-directory\n", "  - ${inputs.executable.path\n"},
		{"acquisition-reference-scope", "command: [go, mod, download]", "command: [go, mod, '${env.HOME}']"},
		{"platforms-missing", "  platforms: [linux/amd64, linux/arm64]\n", ""},
		{"platforms-empty", "platforms: [linux/amd64, linux/arm64]", "platforms: []"},
		{"platform-form", "platforms: [linux/amd64, linux/arm64]", "platforms: [linux]"},
		{"platform-duplicate", "platforms: [linux/amd64, linux/arm64]", "platforms: [linux/amd64, linux/amd64]"},
		{"capability-form", "capabilities: [docker]", "capabilities: [Docker]"},
		{"capability-duplicate", "capabilities: [docker]", "capabilities: [docker, docker]"},
		{"timeout-missing", "timeout: 30m\n", ""},
		{"timeout-zero", "timeout: 30m", "timeout: 0s"},
		{"timeout-negative", "timeout: 30m", "timeout: -1m"},
		{"timeout-form", "timeout: 30m", "timeout: soon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := shippedContract(t)
			if !bytes.Contains(raw, []byte(tc.old)) {
				t.Fatalf("fixture no longer contains %q", tc.old)
			}
			root := registrationFixture(t)
			writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/contract.yaml"), bytes.Replace(raw, []byte(tc.old), []byte(tc.replacement), 1))
			_, err := LoadRegistrations(t.Context(), root)
			catalogCode(t, err, e.CodeUsage)
		})
	}
}

func TestRegistrationOptionalFields(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"no-working-directory", "workingDirectory: cli\n", ""},
		{"no-acquisition", "acquisition:\n  command: [go, mod, download]\n", ""},
		{"no-environment", "environment:\n  # The executor mounts source read-only at /src and a writable /cache shared by\n" +
			"  # acquisition and the offline test; Go state and HOME must point there or at /tmp.\n" +
			"  CGO_ENABLED: \"0\"\n  GOCACHE: /cache/go-build\n  GOFLAGS: -mod=readonly -modcacherw\n  GOPATH: /cache/go\n" +
			"  GOTOOLCHAIN: local\n  GOWORK: \"off\"\n  HOME: /tmp\n" +
			"  NVKECTL: ${inputs.executable.path}\n  NVKECTL_EXPECTED_VERSION: ${inputs.executable.version}\n", ""},
		{"no-capabilities", "  capabilities: [docker]\n", ""},
		{"literal-environment", "${inputs.executable.path}", "/opt/nvkectl"},
		{"empty-literal-environment", "${inputs.executable.path}", "''"},
		{"embedded-reference", "${inputs.executable.path}", "path=${inputs.executable.path};v=${inputs.executable.version}"},
		{"command-reference", "  - --no-print-directory\n", "  - ${inputs.executable.path}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := shippedContract(t)
			if !bytes.Contains(raw, []byte(tc.old)) {
				t.Fatalf("fixture no longer contains %q", tc.old)
			}
			root := registrationFixture(t)
			writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/contract.yaml"), bytes.Replace(raw, []byte(tc.old), []byte(tc.replacement), 1))
			if _, err := LoadRegistrations(t.Context(), root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRegistrationsDirectoryLayout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"file-in-root", func(t *testing.T, root string) { writeCatalogFile(t, filepath.Join(root, "README.md"), []byte("x")) }},
		{"hidden-file", func(t *testing.T, root string) {
			writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/.DS_Store"), []byte("x"))
		}},
		{"file-at-group-level", func(t *testing.T, root string) {
			writeCatalogFile(t, filepath.Join(root, "nvke/contract.yaml"), shippedContract(t))
		}},
		{"directory-at-suite-level", func(t *testing.T, root string) {
			writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/deeper/contract.yaml"), shippedContract(t))
		}},
		{"directory-named-yaml", func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, "nvke/nvkectl/extra.yaml"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"yml-extension", func(t *testing.T, root string) {
			if err := os.Rename(filepath.Join(root, "nvke/nvkectl/cli.yaml"), filepath.Join(root, "nvke/nvkectl/cli.yml")); err != nil {
				t.Fatal(err)
			}
		}},
		{"invalid-repository-name", func(t *testing.T, root string) {
			writeCatalogFile(t, filepath.Join(root, "Other/nvkectl/contract.yaml"), shippedContract(t))
		}},
		{"invalid-suite-name", func(t *testing.T, root string) {
			writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/Other.yaml"), shippedContract(t))
		}},
		{"empty-repository", func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, "other"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty-suite-group", func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, "nvke/empty"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"empty-catalog", func(t *testing.T, root string) {
			if err := os.RemoveAll(filepath.Join(root, "nvke")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := registrationFixture(t)
			tc.mutate(t, root)
			_, err := LoadRegistrations(t.Context(), root)
			catalogCode(t, err, e.CodeUsage)
		})
	}
}

func TestRegistrationsRefuseMissingRootAndCancellation(t *testing.T) {
	_, err := LoadRegistrations(t.Context(), filepath.Join(t.TempDir(), "missing"))
	catalogCode(t, err, e.CodeUsage)
	file := filepath.Join(t.TempDir(), "file")
	writeCatalogFile(t, file, []byte("x"))
	_, err = LoadRegistrations(t.Context(), file)
	catalogCode(t, err, e.CodeUsage)
	_, err = LoadRegistrations(t.Context(), "")
	catalogCode(t, err, e.CodeUsage)
	//nolint:staticcheck // the nil-context refusal is part of the contract.
	_, err = LoadRegistrations(nil, shippedRegistrations)
	catalogCode(t, err, e.CodeUsage)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = LoadRegistrations(canceled, shippedRegistrations)
	catalogCode(t, err, e.CodeTimeout)
}

// registrationText returns the shipped contract rewritten for id.
func registrationText(t *testing.T, id string) string {
	t.Helper()
	return strings.Replace(string(shippedContract(t)), "id: nvke/nvkectl/contract", "id: "+id, 1)
}

// catalogMessage requires a coded refusal that names the intended rule.
func catalogMessage(t *testing.T, err error, code e.Code, rule string) {
	t.Helper()
	catalogCode(t, err, code)
	if !strings.Contains(err.Error(), rule) {
		t.Fatalf("expected refusal for %q, got %v", rule, err)
	}
}

func TestRegistrationsCatalogBounds(t *testing.T) {
	for _, tc := range []struct {
		name, rule string
		mutate     func(*testing.T, string)
	}{
		{"directory-entries", "entry limit", func(t *testing.T, root string) {
			for i := 0; i <= maxRegistrationDirectoryEntries; i++ {
				id := fmt.Sprintf("nvke/many/s%d", i)
				writeCatalogFile(t, filepath.Join(root, id+".yaml"), []byte(registrationText(t, id)))
			}
		}},
		{"registrations", "registration limit", func(t *testing.T, root string) {
			for i := 0; i < maxRegistrations; i++ {
				id := fmt.Sprintf("nvke/g%d/s%d", i%2, i)
				writeCatalogFile(t, filepath.Join(root, id+".yaml"), []byte(registrationText(t, id)))
			}
		}},
		{"catalog-bytes", "byte limit", func(t *testing.T, root string) {
			for i := 0; i <= maxRegistrationCatalogBytes/maxRegistrationBytes; i++ {
				id := fmt.Sprintf("nvke/large/s%d", i)
				text := registrationText(t, id)
				text += "#" + strings.Repeat(" ", maxRegistrationBytes-len(text)-3) + "\n"
				writeCatalogFile(t, filepath.Join(root, id+".yaml"), []byte(text))
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := registrationFixture(t)
			tc.mutate(t, root)
			_, err := LoadRegistrations(t.Context(), root)
			catalogMessage(t, err, e.CodeUsage, tc.rule)
		})
	}
}

func TestRegistrationFieldBounds(t *testing.T) {
	repeat := func(n int, line func(int) string) string {
		var b strings.Builder
		for i := range n {
			b.WriteString(line(i))
		}
		return b.String()
	}
	for _, tc := range []struct{ name, old, replacement, rule string }{
		{"inputs", "inputs:\n", "inputs:\n" + repeat(64, func(i int) string {
			return fmt.Sprintf("  r%d:\n    output: nvkectl\n    component: nvkectl\n    format: executable\n", i)
		}), "1..64 input roles"},
		{"command-arguments", "  - --no-print-directory\n", repeat(256, func(int) string { return "  - x\n" }), "command requires"},
		{"acquisition-arguments", "command: [go, mod, download]", "command: [go" + repeat(256, func(int) string { return ", x" }) + "]", "acquisition command requires"},
		{"environment-bindings", "environment:\n", "environment:\n" + repeat(255, func(i int) string { return fmt.Sprintf("  E%d: x\n", i) }), "256 bindings"},
		{"platforms", "platforms: [linux/amd64, linux/arm64]", "platforms: [linux/amd64, linux/arm64" + repeat(63, func(i int) string { return fmt.Sprintf(", linux/a%d", i) }) + "]", "requires.platforms"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := shippedContract(t)
			if !bytes.Contains(raw, []byte(tc.old)) {
				t.Fatalf("fixture no longer contains %q", tc.old)
			}
			root := registrationFixture(t)
			writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/contract.yaml"), bytes.Replace(raw, []byte(tc.old), []byte(tc.replacement), 1))
			_, err := LoadRegistrations(t.Context(), root)
			catalogMessage(t, err, e.CodeUsage, tc.rule)
		})
	}
}

// With several invalid bindings, the refusal names the first in sorted order.
func TestRegistrationEnvironmentRefusalDeterministic(t *testing.T) {
	raw := bytes.Replace(shippedContract(t), []byte("environment:\n"), []byte("environment:\n  E: ${x}\n  D: ${x}\n  C: ${x}\n  B: ${x}\n  A: ${x}\n"), 1)
	root := registrationFixture(t)
	writeCatalogFile(t, filepath.Join(root, "nvke/nvkectl/contract.yaml"), raw)
	for range 20 {
		_, err := LoadRegistrations(t.Context(), root)
		catalogMessage(t, err, e.CodeUsage, `binding "A"`)
	}
}

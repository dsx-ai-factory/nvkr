// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestAdmissionPreservesAuthoredIdentityAndRejectsUnknownInputs(t *testing.T) {
	input := Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind, Metadata: Metadata{Name: "fixture"}, Catalog: Catalog{Base: "catalog"}, Outputs: []DefinitionOutput{{Name: "cli", Platform: "linux/amd64", Components: []string{"cli"}, Format: OutputExecutable}}}
	raw, err := canonical.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
		code e.Code
	}{
		{"exact document", raw, ""},
		{"unsupported namespace", []byte(strings.Replace(string(raw), DefinitionAPIVersion, "unsupported.dev/definition/v5", 1)), e.CodeInvalidCatalog},
		{"old version", []byte(strings.Replace(string(raw), DefinitionAPIVersion, "nvkr.dev/definition/v3", 1)), e.CodeInvalidCatalog},
		{"unknown field", []byte(strings.Replace(string(raw), `"outputs":`, `"clusters":[],"outputs":`, 1)), e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "definition.yaml")
			if err := os.WriteFile(path, tc.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadDefinition(t.Context(), path)
			if tc.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code {
					t.Fatalf("want %s; got %v", tc.code, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := input.Digest()
			if err != nil {
				t.Fatal(err)
			}
			actual, err := got.Digest()
			if err != nil || actual != want {
				t.Fatalf("admission changed authored identity: %q %v", actual, err)
			}
		})
	}
}

func TestEnvironmentPublicationCredentialAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, credential string
		valid            bool
	}{
		{"reference", "github:REGISTRY_AUTH", true},
		{"newline", "github:REGISTRY_AUTH\ninjected", false},
		{"NUL", "github:REGISTRY_AUTH\x00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			environment := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "fixture"}, UtilityContainer: "golang@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64", Publication: &Publication{RepositoryPrefix: "ghcr.io/example/nvkr", CredentialRef: tc.credential}}
			err := environment.Validate()
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("expected invalid credential error, got %v", err)
			}
		})
	}
}

func TestEnvironmentRejectsRetiredBuilderImage(t *testing.T) {
	for _, test := range []struct {
		name, image string
		wantCode    e.Code
	}{
		{name: "empty remains supported"},
		{name: "unpinned", image: "golang:latest", wantCode: e.CodeEngineCapability},
		{name: "pinned", image: "golang@sha256:" + strings.Repeat("a", 64), wantCode: e.CodeEngineCapability},
		{name: "whitespace", image: " ", wantCode: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := Environment{
				APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "fixture"},
				UtilityContainer: "golang@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64",
				Runtime: Runtime{BuilderImage: test.image},
			}
			err := environment.Validate()
			if test.wantCode == "" {
				if err != nil {
					t.Fatal(err)
				}
				raw, err := canonical.Marshal(environment.Runtime)
				if err != nil || string(raw) != "{}" {
					t.Fatalf("empty runtime canonical bytes changed: %q (%v)", raw, err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.wantCode {
				t.Fatalf("admission code = %v, want %s", err, test.wantCode)
			}
			digest, err := environment.Digest()
			if !errors.As(err, &coded) || coded.Code != test.wantCode || digest != "" {
				t.Fatalf("unsupported runtime received identity: %q (%v)", digest, err)
			}
		})
	}
}

func TestEnvironmentBuildNetworkPolicy(t *testing.T) {
	for _, test := range []struct {
		name, policy string
		wantCode     e.Code
	}{
		{name: "omitted is offline"},
		{name: "explicit offline", policy: BuildNetworkOfflineOnly},
		{name: "producer declared", policy: BuildNetworkProducerDeclared},
		{name: "unknown", policy: "open", wantCode: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := Environment{
				APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind,
				Metadata:         Metadata{Name: "fixture"},
				UtilityContainer: "golang@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64",
				Runtime: Runtime{BuildNetworkPolicy: test.policy},
			}
			err := environment.Validate()
			var coded *e.Error
			if test.wantCode == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantCode != "" && (!errors.As(err, &coded) || coded.Code != test.wantCode) {
				t.Fatalf("policy error = %v, want %s", err, test.wantCode)
			}
		})
	}
}

func TestGitHubDispatchBranchAdmission(t *testing.T) {
	for _, tc := range []struct {
		name     string
		branches []string
		valid    bool
	}{
		{"default", nil, true}, {"exact", []string{"feat/nvke-deploy-pipeline"}, true},
		{"multiple", []string{"feat/a", "feat/b"}, true}, {"wildcard", []string{"feat/*"}, false},
		{"expression", []string{"x' || true"}, false}, {"ref", []string{"refs/tags/v1"}, false},
		{"duplicate", []string{"a", "a"}, false}, {"unsorted", []string{"b", "a"}, false},
		{"dotdot", []string{"a..b"}, false}, {"lock", []string{"a.lock/b"}, false},
		{"empty part", []string{"a//b"}, false}, {"hidden part", []string{"a/.b"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "fixture"}, UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64", GitHubDispatchBranches: tc.branches}
			err := env.Validate()
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				cloned := env.Clone()
				if len(tc.branches) > 0 {
					cloned.GitHubDispatchBranches[0] = "changed"
					if env.GitHubDispatchBranches[0] == "changed" {
						t.Fatal("clone aliases branch policy")
					}
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("expected coded rejection, got %v", err)
			}
		})
	}
}

func TestPerOutputPublicationAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, mode, signing string
		valid               bool
	}{
		{"legacy", "", "", true},
		{"per output", "per-output-v1", "cosign", true},
		{"unknown", "future", "cosign", false},
		{"unsigned", "per-output-v1", "", false},
		{"dry run", "per-output-v1", "dry-run", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "fixture"}, UtilityContainer: "golang@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64", Publication: &Publication{Mode: tc.mode, RepositoryPrefix: "ghcr.io/example/nvkr", CredentialRef: "github:REGISTRY_AUTH"}}
			if tc.signing != "" {
				env.Signing = &Signing{Mode: tc.signing, Identity: "^https://github.com/example/repo/.*$", Issuer: "https://token.actions.githubusercontent.com"}
			}
			err := env.Validate()
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
					t.Fatalf("expected capability error, got %v", err)
				}
			}
		})
	}
}

func TestNativeToolchainLocatorsAreBoundedAndOwned(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, row := range []struct {
		name, digest, layout, bundle string
		valid                        bool
	}{
		{"exact local layout", digest, ".nvkr/tools", "packer-tools", true},
		{"unpinned", "latest", ".nvkr/tools", "packer-tools", false},
		{"missing layout", digest, "", "packer-tools", false},
		{"unsafe layout", digest, "layout\nother", "packer-tools", false},
		{"unsafe bundle", digest, ".nvkr/tools", "../other", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			input := Runtime{NativeToolchains: map[string]NativeToolchain{row.digest: {Layout: row.layout, Bundle: row.bundle}}}
			err := input.validate()
			if row.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("invalid native locator: %v", err)
			}
		})
	}
	input := Environment{Runtime: Runtime{NativeToolchains: map[string]NativeToolchain{digest: {Layout: ".nvkr/tools", Bundle: "packer-tools"}}}}
	copy := input.Clone()
	copy.Runtime.NativeToolchains[digest] = NativeToolchain{Layout: "other", Bundle: "other"}
	if input.Runtime.NativeToolchains[digest].Layout != ".nvkr/tools" {
		t.Fatal("Environment clone aliases native toolchain locators")
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func developmentEnvironmentFixture() Environment {
	return Environment{
		APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "development"},
		UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64",
		GitHubDevelopment: true,
	}
}

func TestGitHubDevelopmentAdmission(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Environment)
		code e.Code
	}{
		{name: "build only", edit: func(*Environment) {}},
		{name: "scoped source access", edit: func(environment *Environment) {
			environment.SourceAccess = map[string]SourceAccess{
				"https://github.com/example/private": {Mirror: "https://github.com/example/private", CredentialRef: "github:SOURCE_TOKEN"},
			}
		}},
		{name: "publication", edit: func(environment *Environment) { environment.Publication = &Publication{} }, code: e.CodeEngineCapability},
		{name: "signing", edit: func(environment *Environment) { environment.Signing = &Signing{} }, code: e.CodeEngineCapability},
		{name: "ARC", edit: func(environment *Environment) { environment.GitHubARC = "native-kvm-v1" }, code: e.CodeEngineCapability},
		{name: "legacy AMD64 ARC label", edit: func(environment *Environment) { environment.RunsOn = "arc-amd64-kvm" }, code: e.CodeEngineCapability},
		{name: "legacy ARM64 ARC label", edit: func(environment *Environment) { environment.RunsOn = "arc-arm64-kvm" }, code: e.CodeEngineCapability},
		{name: "arbitrary self-hosted label", edit: func(environment *Environment) { environment.RunsOn = "nvkr-feature-builder" }, code: e.CodeEngineCapability},
		{name: "explicit hosted label", edit: func(environment *Environment) { environment.RunsOn = "ubuntu-24.04" }},
		{name: "disk evidence", edit: func(environment *Environment) { environment.DiskEvidence = "build-provenance-v1" }, code: e.CodeEngineCapability},
		{name: "dispatch allowlist", edit: func(environment *Environment) { environment.GitHubDispatchBranches = []string{"feature/test"} }, code: e.CodeEngineCapability},
		{name: "runtime setup", edit: func(environment *Environment) {
			environment.Runtime.Setup = []string{"docker login ghcr.io --password '${{ secrets.REGISTRY_AUTH }}'"}
		}, code: e.CodeEngineCapability},
		{name: "runtime secret expression", edit: func(environment *Environment) {
			environment.Runtime.Variables = map[string]string{"REGISTRY_AUTH": "${{ secrets.REGISTRY_AUTH }}"}
		}, code: e.CodeEngineCapability},
		{name: "runtime variable without expression", edit: func(environment *Environment) {
			environment.Runtime.Variables = map[string]string{"SETTING": "value"}
		}, code: e.CodeEngineCapability},
		{name: "empty runtime setup and variables", edit: func(environment *Environment) {
			environment.Runtime.Setup = []string{}
			environment.Runtime.Variables = map[string]string{}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := developmentEnvironmentFixture()
			test.edit(&environment)
			raw, err := canonical.Marshal(environment)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "environment.yaml")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, loadErr := LoadEnvironment(t.Context(), path)
			digest, digestErr := environment.Digest()
			for _, err := range []error{environment.Validate(), loadErr, digestErr} {
				if test.code == "" {
					if err != nil {
						t.Fatal(err)
					}
					continue
				}
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != test.code {
					t.Fatalf("admission error = %v, want %s", err, test.code)
				}
			}
			if test.code != "" && digest != "" {
				t.Fatalf("unsupported development configuration received identity %s", digest)
			}
		})
	}
}

func TestGitHubDevelopmentBindsEnvironmentIdentity(t *testing.T) {
	environment := developmentEnvironmentFixture()
	developmentDigest, err := environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	clone := environment.Clone()
	cloneDigest, err := clone.Digest()
	if err != nil || !clone.GitHubDevelopment || cloneDigest != developmentDigest {
		t.Fatalf("clone changed development identity: %s (%v)", cloneDigest, err)
	}
	clone.GitHubDevelopment = false
	defaultDigest, err := clone.Digest()
	if err != nil || defaultDigest == developmentDigest || !environment.GitHubDevelopment {
		t.Fatalf("development policy is not independently bound to identity: %s (%v)", defaultDigest, err)
	}
	raw, err := canonical.Marshal(clone)
	if err != nil || bytes.Contains(raw, []byte("githubDevelopment")) {
		t.Fatalf("default Environment gained development bytes: %s (%v)", raw, err)
	}
}

func TestGitHubDevelopmentPublicationAdmission(t *testing.T) {
	namespace, err := development.ParseNamespace("ghcr.io/example/nvkr/development")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		edit  func(*Environment)
		valid bool
	}{
		{"signed development", func(*Environment) {}, true},
		{"native disk route", func(env *Environment) { env.GitHubARC = "native-kvm-v1"; env.DiskEvidence = "build-provenance-v1" }, true},
		{"missing signer", func(env *Environment) { env.Signing = nil }, false},
		{"missing publication", func(env *Environment) { env.Publication = nil }, false},
		{"dry run signer", func(env *Environment) { env.Signing.Mode = "dry-run" }, false},
		{"aggregate publication", func(env *Environment) { env.Publication.Mode = "" }, false},
		{"production token", func(env *Environment) { env.Publication.CredentialRef = "github:GITHUB_TOKEN" }, false},
		{"read token for writes", func(env *Environment) { env.Publication.CredentialRef = development.ReadCredentialRef }, false},
		{"anonymous", func(env *Environment) { env.Publication.Anonymous = true }, false},
		{"plain HTTP", func(env *Environment) { env.Publication.PlainHTTP = true }, false},
		{"image tags", func(env *Environment) { env.Publication.ImageTags = map[string]string{"image": "latest"} }, false},
		{"production namespace", func(env *Environment) { env.Publication.RepositoryPrefix = "ghcr.io/example/nvkr/m2-candidate" }, false},
		{"namespace child", func(env *Environment) { env.Publication.RepositoryPrefix += "/branch" }, false},
		{"other repository namespace", func(env *Environment) { env.Publication.RepositoryPrefix = "ghcr.io/other/nvkr/development" }, false},
		{"broad identity", func(env *Environment) { env.Signing.Identity = ".*" }, false},
		{"production workflow identity", func(env *Environment) {
			env.Signing.Identity = strings.ReplaceAll(env.Signing.Identity, "nvkr-dev", "nvkr-run")
		}, false},
		{"other issuer", func(env *Environment) { env.Signing.Issuer = "https://issuer.example.com" }, false},
		{"dispatch allowlist", func(env *Environment) { env.GitHubDispatchBranches = []string{"main"} }, false},
		{"injected setup", func(env *Environment) { env.Runtime.Setup = []string{"${{ secrets.TOKEN }}"} }, false},
		{"injected variables", func(env *Environment) { env.Runtime.Variables = map[string]string{"TOKEN": "${{ secrets.TOKEN }}"} }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := developmentEnvironmentFixture()
			env.Publication = &Publication{Mode: "per-output-v1", RepositoryPrefix: namespace.Prefix(), CredentialRef: development.WriteCredentialRef}
			env.Signing = &Signing{Mode: "cosign", Identity: namespace.Identity(), Issuer: development.Issuer}
			test.edit(&env)
			digest, err := env.Digest()
			if test.valid {
				if err != nil || digest == "" {
					t.Fatalf("valid signed development failed: %s (%v)", digest, err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability || digest != "" {
				t.Fatalf("admission = %s (%v)", digest, err)
			}
		})
	}
}

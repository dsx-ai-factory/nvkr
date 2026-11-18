// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
)

func mkosiCatalogFixture(t *testing.T) (Component, *mkosiDeclaration, bytesProvider) {
	t.Helper()
	metadata := "sha256:" + strings.Repeat("1", 64)
	inputs := material.PackageInputs{SchemaVersion: material.PackageInputsSchema, PackageManager: "apt", Architecture: "amd64", Snapshot: "20261109T000000Z",
		Repositories: []material.PackageRepository{{URI: "https://example.invalid/InRelease", Digest: metadata, Size: 123}},
		Packages: []material.PackageInput{{Name: "base-files", Version: "1.0", Architecture: "amd64", Digest: "sha256:" + strings.Repeat("2", 64), Size: 321,
			URI: "https://example.invalid/base.deb", RepositoryDigest: metadata}}}
	raw, err := canonical.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	provider := bytesProvider{"mkosi.conf": []byte("[Output]\nFormat=oci\n"), "packages.json": raw}
	pin := func(name string) patchDeclaration {
		return patchDeclaration{Name: name, SHA256: fmt.Sprintf("sha256:%x", sha256.Sum256(provider[name]))}
	}
	declaration := &mkosiDeclaration{Configuration: pin("mkosi.conf"), Roots: []mkosiRootDeclaration{{Name: "runtime", Distribution: "ubuntu", Release: "resolute", Inputs: pin("packages.json")}}}
	component := Component{FromSource: true, BuilderType: BuilderMkosi, Platforms: []string{"linux/amd64"}, Repo: "https://github.com/systemd/mkosi",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDate: 1, Tag: "v26", OutputType: "oci-image", ArtifactPath: "nvkr-output/runtime"}
	return component, declaration, provider
}

func TestMkosiCatalogBindsProviderFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Component, *mkosiDeclaration, bytesProvider)
		code   e.Code
	}{
		{"valid", func(*Component, *mkosiDeclaration, bytesProvider) {}, ""},
		{"configuration changed", func(_ *Component, _ *mkosiDeclaration, p bytesProvider) { p["mkosi.conf"] = []byte("changed") }, e.CodeUnpinnedInput},
		{"package inputs changed", func(_ *Component, _ *mkosiDeclaration, p bytesProvider) { p["packages.json"] = []byte("changed") }, e.CodeUnpinnedInput},
		{"escaping path", func(_ *Component, d *mkosiDeclaration, _ bytesProvider) { d.Configuration.Name = "../mkosi.conf" }, e.CodeUnpinnedInput},
		{"missing pin", func(_ *Component, d *mkosiDeclaration, _ bytesProvider) { d.Roots[0].Inputs.SHA256 = "" }, e.CodeUnpinnedInput},
		{"wrong builder", func(c *Component, _ *mkosiDeclaration, _ bytesProvider) { c.BuilderType = BuilderOCIHermetic }, e.CodeInvalidCatalog},
		{"no source", func(c *Component, _ *mkosiDeclaration, _ bytesProvider) { c.FromSource = false }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			component, declaration, provider := mkosiCatalogFixture(t)
			tc.mutate(&component, declaration, provider)
			err := resolveMkosi(provider, "runtime", &component, declaration)
			if catalogErrorCode(err) != tc.code {
				t.Fatalf("got %v; want %s", err, tc.code)
			}
			if err == nil {
				if err := validateComponent("runtime", component); err != nil {
					t.Fatal(err)
				}
				if component.Mkosi.Configuration != string(provider["mkosi.conf"]) {
					t.Fatal("configuration bytes changed")
				}
			}
		})
	}
}

func TestMkosiRejectsAmbientBuilderRoutes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Component)
		code   e.Code
	}{
		{"image", func(c *Component) { c.ToolchainImage = "example.invalid/runtime@sha256:" + strings.Repeat("1", 64) }, e.CodeInvalidCatalog},
		{"vendor command", func(c *Component) { c.VendorCommand = []string{"sh", "script"} }, e.CodeInvalidCatalog},
		{"build command", func(c *Component) { c.BuildCommand = []string{"sh", "script"} }, e.CodeInvalidCatalog},
		{"network", func(c *Component) { c.BuildNetwork = "default" }, e.CodeInvalidCatalog},
		{"unpinned source", func(c *Component) { c.Commit = "main" }, e.CodeUnpinnedSource},
		{"wrong artifact", func(c *Component) { c.ArtifactPath = "/tmp/runtime" }, e.CodeInvalidCatalog},
		{"foreign platform", func(c *Component) { c.Platforms = []string{"linux/arm64"} }, e.CodeEngineCapability},
		{"unknown manager", func(c *Component) { c.Mkosi.Roots[0].Inputs.PackageManager = "zypper" }, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			component, declaration, provider := mkosiCatalogFixture(t)
			if err := resolveMkosi(provider, "runtime", &component, declaration); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&component)
			if err := validateComponent("runtime", component); catalogErrorCode(err) != tc.code {
				t.Fatalf("got %v; want %s", err, tc.code)
			}
		})
	}
}

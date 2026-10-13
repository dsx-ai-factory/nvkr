// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

func debianProducerFixture() Component {
	c := goodComponent()
	c.BuilderType, c.OutputType = BuilderOCIHermetic, OutputDebianPackage
	c.ToolchainImage, c.BuildCommand, c.ArtifactPath = "", nil, ""
	c.Debian = &DebianSpec{Routes: []DebianRoute{{
		Target:         target.Debian{Distribution: "ubuntu-24.04", Platform: "linux/amd64"},
		ToolchainImage: goodImage, BuildCommand: []string{"make", "deb", "DISTRO=noble", "ARCH={{.Arch}}"},
		ArtifactPath: "out/package-{{.Arch}}.deb", WriteDir: "out",
	}}}
	return c
}

func TestDebianRoutesLoadStrictlyFromPinnedSource(t *testing.T) {
	c := debianProducerFixture()
	registry := registryDoc{APIVersion: catalogAPIVersion, Kind: catalogKind,
		Sources: map[string]Source{"source": {Repo: c.Repo}},
		Components: map[string]rawComponent{"agent": {FromSource: true, Source: "source", Platforms: c.Platforms,
			OutputType: OutputDebianPackage, Debian: c.Debian}},
	}
	lock := PinLock{APIVersion: lockAPIVersion, Kind: lockKind,
		Pins: map[string]Pin{"source": {Tag: "v1.0.0", Commit: goodCommit, TreeHash: goodTree, SourceDateEpoch: c.SourceDate}},
	}
	registryBytes, err := canonical.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	lockBytes, err := canonical.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadContext(t.Context(), bytesProvider{RegistryFile: registryBytes, LockFile: lockBytes})
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Components["agent"]
	if got.BuilderType != BuilderOCIHermetic || got.Commit != goodCommit || got.Debian.Routes[0].BuildCommand[2] != "DISTRO=noble" {
		t.Fatal("loader lost source pins or source-owned target arguments")
	}
	unknown := strings.Replace(string(registryBytes), `"writeDir":"out"`, `"writeDir":"out","unknown":true`, 1)
	if _, err := LoadContext(t.Context(), bytesProvider{RegistryFile: []byte(unknown), LockFile: lockBytes}); catalogErrorCode(err) != e.CodeInvalidCatalog {
		t.Fatalf("unknown route field was accepted: %v", err)
	}
}

func TestDebianOverlayDoesNotAliasCallerRoutes(t *testing.T) {
	c := debianProducerFixture()
	cat := &Catalog{Components: map[string]Component{"agent": c}}
	digest, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	replacement := debianProducerFixture()
	stack, err := ApplyStack(cat, Overlay{APIVersion: OverlayAPIVersion, Kind: OverlayKind, Name: "supplier",
		RequiresCatalogDigest: digest, Components: map[string]ComponentReplacement{"agent": {Component: &replacement}}})
	if err != nil {
		t.Fatal(err)
	}
	replacement.Debian.Routes[0].BuildCommand[0] = "changed"
	replacement.Debian.Routes[0].Target.Distribution = "changed"
	actual, err := stack.Catalog.SnapshotDigest()
	if err != nil || actual != stack.Digest {
		t.Fatal("caller overlay mutation changed frozen routes", err)
	}
}

func TestDebianProducerRoutesAreCompleteAndPinned(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Component)
		want e.Code
	}{
		{"valid", func(*Component) {}, ""},
		{"missing spec", func(c *Component) { c.Debian = nil }, e.CodeInvalidCatalog},
		{"empty routes", func(c *Component) { c.Debian.Routes = nil }, e.CodeInvalidCatalog},
		{"duplicate route", func(c *Component) { c.Debian.Routes = append(c.Debian.Routes, c.Debian.Routes[0]) }, e.CodeInvalidCatalog},
		{"unpinned unselected route", func(c *Component) {
			route := c.Debian.Routes[0]
			route.Target.Distribution = "ubuntu-26.04"
			route.ToolchainImage = "ubuntu:26.04"
			c.Debian.Routes = append(c.Debian.Routes, route)
		}, e.CodeTagToolchain},
		{"unsupported route platform", func(c *Component) { c.Debian.Routes[0].Target.Platform = "linux/arm64" }, e.CodeInvalidCatalog},
		{"missing platform route", func(c *Component) { c.Platforms = append(c.Platforms, "linux/arm64") }, e.CodeInvalidCatalog},
		{"common toolchain", func(c *Component) { c.ToolchainImage = goodImage }, e.CodeInvalidCatalog},
		{"common command", func(c *Component) { c.BuildCommand = []string{"make"} }, e.CodeInvalidCatalog},
		{"competing output", func(c *Component) { c.OutputType = "host-package" }, e.CodeInvalidCatalog},
		{"not from source", func(c *Component) { c.FromSource = false }, e.CodeInvalidCatalog},
		{"other builder", func(c *Component) { c.BuilderType = BuilderNix }, e.CodeInvalidCatalog},
		{"missing private output", func(c *Component) { c.Debian.Routes[0].WriteDir = "" }, e.CodeInvalidCatalog},
		{"escaping output", func(c *Component) { c.Debian.Routes[0].ArtifactPath = "../package.deb" }, e.CodeInvalidCatalog},
		{"unknown command token", func(c *Component) { c.Debian.Routes[0].BuildCommand = []string{"{{.Distribution}}"} }, e.CodeInvalidCatalog},
		{"invalid argv UTF8", func(c *Component) { c.Debian.Routes[0].BuildCommand = []string{string([]byte{0xff})} }, e.CodeInvalidCatalog},
		{"empty executable", func(c *Component) { c.Debian.Routes[0].BuildCommand = []string{" "} }, e.CodeInvalidCatalog},
		{"NUL argv", func(c *Component) { c.Debian.Routes[0].BuildCommand = []string{"make", "bad\x00argument"} }, e.CodeInvalidCatalog},
		{"missing source pin", func(c *Component) { c.Commit = "main" }, e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := debianProducerFixture()
			tc.edit(&c)
			err := (&Catalog{Components: map[string]Component{"agent": c}}).Validate()
			if catalogErrorCode(err) != tc.want {
				t.Fatalf("admission = %v; want %s", err, tc.want)
			}
		})
	}
}

func TestDebianStackRejectsByteChangingCloneInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Component)
	}{
		{"profile", func(c *Component) { c.Debian.Routes[0].Target.Distribution = string([]byte{0xff}) }},
		{"argv", func(c *Component) { c.Debian.Routes[0].BuildCommand[0] = string([]byte{0xff}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := debianProducerFixture()
			tc.edit(&c)
			stack, err := ApplyStack(&Catalog{Components: map[string]Component{"agent": c}})
			if catalogErrorCode(err) != e.CodeInvalidCatalog || stack != nil {
				t.Fatalf("malformed input was copied: %v, %v", stack, err)
			}
		})
	}
}

func TestDebianCatalogSnapshotAndCloneBindRoutes(t *testing.T) {
	c := debianProducerFixture()
	cat := &Catalog{Components: map[string]Component{"agent": c}}
	digest, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	copy, err := cloneCatalog(cat)
	if err != nil {
		t.Fatal(err)
	}
	c.Debian.Routes[0].BuildCommand[2] = "DISTRO=changed"
	changed, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	if changed == digest {
		t.Fatal("route argv did not bind catalog snapshot")
	}
	cloned, err := copy.SnapshotDigest()
	if err != nil || cloned != digest {
		t.Fatalf("snapshot clone changed: %v", err)
	}
	copySpec := CloneDebianSpec(copy.Components["agent"].Debian)
	copySpec.Routes[0].BuildCommand[0] = "changed"
	if copy.Components["agent"].Debian.Routes[0].BuildCommand[0] != "make" {
		t.Fatal("declaration clone aliases argv")
	}
}

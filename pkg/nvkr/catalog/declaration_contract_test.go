// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCatalogRejectsInvalidProducerDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Catalog)
	}{
		{"missing platforms", func(c *Catalog) { v := c.Components["x"]; v.Platforms = nil; c.Components["x"] = v }},
		{"unknown platform", func(c *Catalog) {
			v := c.Components["x"]
			v.Platforms = []string{"linux/riscv64"}
			c.Components["x"] = v
		}},
		{"duplicate platform", func(c *Catalog) {
			v := c.Components["x"]
			v.Platforms = []string{"linux/amd64", "linux/amd64"}
			c.Components["x"] = v
		}},
		{"unsorted platforms", func(c *Catalog) {
			v := c.Components["x"]
			v.Platforms = []string{"linux/arm64", "linux/amd64"}
			c.Components["x"] = v
		}},
		{"blank component name", func(c *Catalog) { c.Components[" "] = goodComponent() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := base(goodComponent())
			tc.edit(cat)
			assertCatalogContractCode(t, cat.Validate(), e.CodeInvalidCatalog)
		})
	}
}

func TestCatalogRejectsUnusableNixPinsAndDownloadLocations(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*FlakePin)
		code e.Code
	}{
		{"complete pin", func(*FlakePin) {}, ""},
		{"blank reference", func(f *FlakePin) { f.Ref = " " }, e.CodeUnpinnedSource},
		{"blank output", func(f *FlakePin) { f.Output = " " }, e.CodeInvalidCatalog},
		{"wrong image scheme", func(f *FlakePin) { f.Image = "file:///builder" }, e.CodeInvalidCatalog},
		{"mutable builder image", func(f *FlakePin) { f.Image = "nix:latest" }, e.CodeTagToolchain},
		{"pinned builder image", func(f *FlakePin) { f.Image = goodImage }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			component := goodComponent()
			component.BuilderType = "nix"
			component.Flake = &FlakePin{Ref: "github:example/project?rev=" + goodCommit, LockDigest: goodArtifactDigest, Output: "packages.x86_64-linux.default"}
			tc.edit(component.Flake)
			err := base(component).Validate()
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCatalogContractCode(t, err, tc.code)
			}
		})
	}
	for _, location := range []string{"http://example.test/tool", "file:///tool", "example.test/tool"} {
		t.Run(location, func(t *testing.T) {
			component := goodPrebuilt()
			component.Prebuilt.URL = location
			assertCatalogContractCode(t, base(component).Validate(), e.CodeInvalidCatalog)
		})
	}
}

func assertCatalogContractCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error = %v; want %s", err, want)
	}
}

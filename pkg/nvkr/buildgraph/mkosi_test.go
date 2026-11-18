// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

func TestMaintainedMkosiRuntimeCatalog(t *testing.T) {
	directory, err := filepath.Abs("../../../toolchains/nvkr-vm-runtime")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(directory))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(cat, Selection{Platform: "linux/amd64", OutputFormat: "oci",
		Components: []string{"packer-amd64", "packer-plugin-qemu-amd64", "vm-runtime-amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.Kind != ir.KindBuild {
			continue
		}
		if node.BuilderType != ir.BuilderMkosi || node.Mkosi == nil || len(node.Materials) != 2 || len(node.Mkosi.Roots) != 2 {
			t.Fatal("runtime lost its package roots or static-tool producer closure")
		}
		if _, err := node.Mkosi.Invocations(node.Platform, []string{"packer", "packer-plugin-qemu"}); err != nil {
			t.Fatal(err)
		}
	}
}

func mkosiResolvedFixture() *Resolved {
	configuration := "[Output]\nFormat=oci\nManifestFormat=json\n[Build]\nExtraSearchPaths=" + mkosi.ArchiveToolsDirectory + "\n"
	metadata := "sha256:" + strings.Repeat("1", 64)
	inputs := material.PackageInputs{SchemaVersion: material.PackageInputsSchema, PackageManager: "apt", Architecture: "amd64",
		Snapshot: "20261109T000000Z", Repositories: []material.PackageRepository{{URI: "https://example.invalid/InRelease", Digest: metadata, Size: 123}},
		Packages: []material.PackageInput{{Name: "base-files", Version: "1.0", Architecture: "amd64", Digest: "sha256:" + strings.Repeat("2", 64), Size: 321,
			URI: "https://example.invalid/base-files.deb", RepositoryDigest: metadata}}}
	spec := &mkosi.Spec{Configuration: configuration, ConfigurationDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(configuration))),
		Roots: []mkosi.Root{{Name: "runtime", Distribution: "ubuntu", Release: "resolute", Inputs: inputs}}}
	component := catalog.Component{Platforms: []string{"linux/amd64"}, FromSource: true, BuilderType: catalog.BuilderMkosi,
		Repo: "https://github.com/systemd/mkosi", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), Tag: "v26", SourceDate: 1,
		OutputType: "oci-image", ArtifactPath: "nvkr-output/runtime", Mkosi: spec}
	return &Resolved{Platform: "linux/amd64", Components: []catalog.NamedComponent{{Name: "runtime", Component: component}}}
}

func TestMkosiCompilationBindsSourceAndPackageClosure(t *testing.T) {
	resolved := mkosiResolvedFixture()
	graph, err := Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Nodes) != 3 {
		t.Fatalf("got %d nodes, want source/build/verify", len(graph.Nodes))
	}
	var build, fetch *ir.Node
	for index := range graph.Nodes {
		node := &graph.Nodes[index]
		switch node.Kind {
		case ir.KindBuild:
			build = node
		case ir.KindFetch:
			fetch = node
		}
	}
	if build == nil || fetch == nil || build.Mkosi == nil || fetch.Mkosi == nil || build.Toolchain != "" || fetch.VendorImage != "" || len(fetch.VendorCmd) != 0 {
		t.Fatal("mkosi inherited an image route or lost its source acquisition recipe")
	}
	if build.Network != ir.NetworkNone || fetch.Network != ir.NetworkDefault || build.VendorSpecDigest != fetch.VendorSpecDigest {
		t.Fatal("mkosi source acquisition and offline assembly are not bound")
	}
	resolved.Components[0].Mkosi.Roots[0].Inputs.Packages[0].Version = "caller-mutation"
	if err := graph.Validate(); err != nil {
		t.Fatalf("caller mutation changed compiled graph: %v", err)
	}
	build.Mkosi.Roots[0].Inputs.Packages[0].URI = "https://mirror.invalid/substituted-location"
	if code(graph.Validate()) != e.CodeInvalidCatalog {
		t.Fatal("fetch/build recipe divergence was accepted")
	}
}

func TestMkosiBuildIdentityAndSourceCacheSeparatePinsFromLocations(t *testing.T) {
	original, err := Compile(mkosiResolvedFixture())
	if err != nil {
		t.Fatal(err)
	}
	want, err := original.Digest()
	if err != nil {
		t.Fatal(err)
	}
	originalJSON, err := original.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Resolved)
		same   bool
	}{
		{"metadata location", func(r *Resolved) {
			r.Components[0].Mkosi.Roots[0].Inputs.Repositories[0].URI = "https://mirror.invalid/InRelease"
		}, true},
		{"package location", func(r *Resolved) {
			r.Components[0].Mkosi.Roots[0].Inputs.Packages[0].URI = "https://mirror.invalid/pkg.deb"
		}, true},
		{"source location", func(r *Resolved) { r.Components[0].Repo = "https://mirror.invalid/mkosi" }, true},
		{"package bytes", func(r *Resolved) {
			r.Components[0].Mkosi.Roots[0].Inputs.Packages[0].Digest = "sha256:" + strings.Repeat("3", 64)
		}, false},
		{"source pin", func(r *Resolved) { r.Components[0].Commit = strings.Repeat("c", 40) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved := mkosiResolvedFixture()
			tc.mutate(resolved)
			graph, err := Compile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := graph.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if (digest == want) != tc.same {
				t.Fatal("incorrect Build identity boundary")
			}
			raw, err := graph.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(raw, originalJSON) {
				t.Fatal("execution graph lost changed input")
			}
		})
	}
}

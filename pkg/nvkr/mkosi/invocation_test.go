// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mkosi

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
)

func TestOfflineInvocationBoundary(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			spec := specFixture(arch)
			operations, err := spec.Invocations("linux/"+arch, []string{"packer"})
			if err != nil {
				t.Fatal(err)
			}
			if len(operations) != 3 {
				t.Fatalf("got %d operations", len(operations))
			}
			for index, operation := range operations {
				for _, required := range []string{"--cache-only=always", "--with-network=no", "--bootable=no", "--source-date-epoch=0", "--build-sources=", "--history=no"} {
					if !slices.Contains(operation.Command, required) {
						t.Fatalf("operation %d omitted isolation setting %s", index, required)
					}
				}
				command := strings.Join(operation.Command, "\n")
				if strings.Contains(command, "https:") || strings.Contains(command, "example.invalid") || len(operation.Command) > 256 {
					t.Fatalf("operation %d contains transport locations or exceeds launch bound", index)
				}
				if index < len(spec.Roots) {
					if operation.Format != "directory" || operation.Manifest != operation.Output+".manifest" || operation.PackageConfiguration != "[Content]\nPackages=\n    base-files=1.0\n" {
						t.Fatalf("package root lost its exact installed selection: %+v", operation)
					}
				} else if operation.Format != "tar" || operation.PackageConfiguration != "" || strings.Contains(command, "--package-directory=") ||
					operation.Output != "/work/packaging/output/runtime.tar" || operation.Manifest != "" || !slices.Contains(operation.Command, "--output-extension=tar") ||
					!slices.Contains(operation.Command, "--base-tree=/work/roots/runtime/output/runtime") || !slices.Contains(operation.Command, "--extra-tree=/inputs/evidence") {
					t.Fatal("packaging pass lost its completed root/reports or may reinstall packages")
				}
			}
			if !slices.Contains(operations[1].Command, "--extra-tree=/work/roots/inspect/output/inspect:/opt/nvkr/inspection") {
				t.Fatal("runtime omitted its selected inspection root")
			}
		})
	}
}

func TestInvocationReportBinding(t *testing.T) {
	root := specFixture("amd64").Roots[0]
	operation := rootInvocation(root, "x86-64", "directory", "/work/roots/inspect")
	for _, tc := range []struct {
		name   string
		mutate func(*material.MkosiManifest)
		valid  bool
	}{
		{"exact report", func(*material.MkosiManifest) {}, true},
		{"different root", func(m *material.MkosiManifest) { m.Config.Name = "runtime" }, false},
		{"different distribution", func(m *material.MkosiManifest) { m.Config.Distribution = "debian" }, false},
		{"different release", func(m *material.MkosiManifest) { m.Config.Release = "resolute" }, false},
		{"different operation", func(m *material.MkosiManifest) { m.Config.OutputFormat = "oci" }, false},
		{"ambient version", func(m *material.MkosiManifest) { m.Config.Version = "1" }, false},
		{"extra package", func(m *material.MkosiManifest) { m.Packages = append(m.Packages, m.Packages[0]) }, false},
		{"changed version", func(m *material.MkosiManifest) { m.Packages[0].Version = "2.0" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := material.MkosiManifest{ManifestVersion: 1,
				Config:   material.MkosiConfig{Name: "inspect", Distribution: "ubuntu", Release: "noble", Architecture: "x86-64", OutputFormat: "directory"},
				Packages: []material.InstalledPackage{{Type: "deb", Name: "base-files", Version: "1.0", Architecture: "amd64"}}}
			tc.mutate(&report)
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			err = operation.CheckReport(root, raw)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("got %v; want verification rejection", err)
			}
		})
	}
}

func TestRepositoryFilenamePreservesEssentialPackageName(t *testing.T) {
	item := material.PackageInput{Name: "libc6", Version: "2:2.39-0ubuntu8.6", Architecture: "amd64"}
	if got := PackageFilename(item); got != "libc6_2:2.39-0ubuntu8.6_amd64.deb" {
		t.Fatalf("local repository filename lost APT identity: %q", got)
	}
}

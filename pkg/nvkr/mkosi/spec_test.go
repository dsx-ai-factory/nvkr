// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mkosi

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
)

func specFixture(arch string) *Spec {
	configuration := "[Output]\nFormat=oci\nManifestFormat=json\n"
	metadata := "sha256:" + strings.Repeat("1", 64)
	inputs := material.PackageInputs{SchemaVersion: material.PackageInputsSchema, PackageManager: "apt", Architecture: arch,
		Snapshot: "20261109T000000Z", Repositories: []material.PackageRepository{{URI: "https://example.invalid/InRelease", Digest: metadata, Size: 123}},
		Packages: []material.PackageInput{{Name: "base-files", Version: "1.0", Architecture: arch, Digest: "sha256:" + strings.Repeat("2", 64), Size: 321,
			URI: "https://example.invalid/base-files.deb", RepositoryDigest: metadata}}}
	return &Spec{Configuration: configuration, ConfigurationDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(configuration))),
		Roots: []Root{{Name: "inspect", Distribution: "ubuntu", Release: "noble", Inputs: inputs},
			{Name: "runtime", Distribution: "ubuntu", Release: "resolute", Inputs: inputs, Mounts: []Mount{{Root: "inspect", Path: "/opt/nvkr/inspection"}}}},
		Files:        []File{{Material: "packer", Path: "/usr/local/bin/packer", Mode: 0o555}},
		Copies:       []PackageFile{{Source: "/usr/share/firmware/code.fd", Path: "/nvkr/firmware/code.fd", Mode: 0o444, Digest: "sha256:" + strings.Repeat("5", 64)}},
		Contents:     []Content{{Path: "/usr/share/doc/runtime/LICENSE", Data: "synthetic license", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("synthetic license")))}},
		Measurements: []FileMeasurement{{Path: "/usr/local/bin/packer", Digest: "sha256:" + strings.Repeat("3", 64)}}}
}

func TestRecipeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Spec)
		code   e.Code
	}{
		{"valid", func(*Spec) {}, ""},
		{"configuration substitution", func(s *Spec) { s.Configuration += "# changed\n" }, e.CodeUnpinnedInput},
		{"missing configuration", func(s *Spec) { s.Configuration = "" }, e.CodeUnpinnedInput},
		{"no roots", func(s *Spec) { s.Roots = nil }, e.CodeInvalidCatalog},
		{"duplicate root", func(s *Spec) { s.Roots[1].Name = "inspect" }, e.CodeInvalidCatalog},
		{"unsupported distribution", func(s *Spec) { s.Roots[0].Distribution = "opensuse" }, e.CodeEngineCapability},
		{"foreign architecture", func(s *Spec) { s.Roots[0].Inputs.Architecture = "arm64" }, e.CodeEngineCapability},
		{"unbound root", func(s *Spec) { s.Roots[1].Mounts = nil }, e.CodeInvalidCatalog},
		{"self cycle", func(s *Spec) { s.Roots[1].Mounts[0].Root = "runtime" }, e.CodeInvalidCatalog},
		{"future root", func(s *Spec) { s.Roots[0].Mounts = []Mount{{Root: "runtime", Path: "/opt/future"}} }, e.CodeInvalidCatalog},
		{"device projection", func(s *Spec) { s.Roots[1].Mounts[0].Path = "/dev/kvm" }, e.CodeInvalidCatalog},
		{"unclean projection", func(s *Spec) { s.Files[0].Path = "/usr/../etc/config" }, e.CodeInvalidCatalog},
		{"overlapping projection", func(s *Spec) { s.Files[0].Path = "/opt/nvkr/inspection/packer" }, e.CodeInvalidCatalog},
		{"evidence replacement", func(s *Spec) { s.Files[0].Path = "/nvkr/mkosi/recipe.json" }, e.CodeInvalidCatalog},
		{"evidence mount", func(s *Spec) { s.Roots[1].Mounts[0].Path = "/nvkr/mkosi" }, e.CodeInvalidCatalog},
		{"unknown material", func(s *Spec) { s.Files[0].Material = "unknown" }, e.CodeInvalidCatalog},
		{"unused material", func(s *Spec) { s.Files = nil }, e.CodeInvalidCatalog},
		{"writable executable", func(s *Spec) { s.Files[0].Mode = 0o755 }, e.CodeInvalidCatalog},
		{"setuid executable", func(s *Spec) { s.Files[0].Mode = 0o4555 }, e.CodeInvalidCatalog},
		{"unpinned measurement", func(s *Spec) { s.Measurements[0].Digest = "latest" }, e.CodeInvalidCatalog},
		{"duplicate measurement", func(s *Spec) { s.Measurements = append(s.Measurements, s.Measurements[0]) }, e.CodeInvalidCatalog},
		{"copy escape", func(s *Spec) { s.Copies[0].Source = "/usr/../etc/private" }, e.CodeInvalidCatalog},
		{"copy outside runtime paths", func(s *Spec) { s.Copies[0].Path = "/usr/bin/firmware" }, e.CodeInvalidCatalog},
		{"copy evidence replacement", func(s *Spec) { s.Copies[0].Path = "/nvkr/mkosi/report" }, e.CodeInvalidCatalog},
		{"writable copy", func(s *Spec) { s.Copies[0].Mode = 0o644 }, e.CodeInvalidCatalog},
		{"unmeasured copy", func(s *Spec) { s.Copies[0].Digest = "latest" }, e.CodeInvalidCatalog},
		{"content substitution", func(s *Spec) { s.Contents[0].Data += " changed" }, e.CodeUnpinnedInput},
		{"content evidence replacement", func(s *Spec) { s.Contents[0].Path = "/nvkr/mkosi/report" }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := Clone(specFixture("amd64"))
			tc.mutate(spec)
			err := spec.Validate("linux/amd64", []string{"packer"})
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("got %v; want %s", err, tc.code)
			}
		})
	}
	if err := specFixture("arm64").Validate("linux/arm64", []string{"packer"}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectionOverlapWithInterveningSibling(t *testing.T) {
	if err := disjointPaths([]string{"/opt/a", "/opt/a-other", "/opt/a/child"}); err == nil {
		t.Fatal("lexically intervening sibling hid an overlapping path")
	}
}

func TestLogicalRecipeSeparatesTransportFromContent(t *testing.T) {
	original := specFixture("amd64")
	encode := func(s *Spec) []byte {
		t.Helper()
		raw, err := canonical.Marshal(s.Logical())
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	want := encode(original)
	for _, tc := range []struct {
		name   string
		mutate func(*Spec)
		same   bool
	}{
		{"relocated metadata", func(s *Spec) { s.Roots[0].Inputs.Repositories[0].URI = "https://mirror.invalid/InRelease" }, true},
		{"relocated archive", func(s *Spec) { s.Roots[0].Inputs.Packages[0].URI = "https://mirror.invalid/base.deb" }, true},
		{"archive substitution", func(s *Spec) { s.Roots[0].Inputs.Packages[0].Digest = "sha256:" + strings.Repeat("4", 64) }, false},
		{"version substitution", func(s *Spec) { s.Roots[0].Inputs.Packages[0].Version = "2.0" }, false},
		{"snapshot substitution", func(s *Spec) { s.Roots[0].Inputs.Snapshot = "20261110T000000Z" }, false},
		{"firmware substitution", func(s *Spec) { s.Measurements[0].Digest = "sha256:" + strings.Repeat("4", 64) }, false},
		{"projection substitution", func(s *Spec) { s.Files[0].Path = "/usr/local/bin/other" }, false},
		{"firmware path substitution", func(s *Spec) { s.Copies[0].Source = "/usr/share/firmware/other.fd" }, false},
		{"license substitution", func(s *Spec) {
			s.Contents[0].Data = "other"
			s.Contents[0].Digest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("other")))
		}, false},
		{"configuration substitution", func(s *Spec) {
			s.Configuration += "# changed\n"
			s.ConfigurationDigest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(s.Configuration)))
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := Clone(original)
			tc.mutate(changed)
			if err := changed.Validate("linux/amd64", []string{"packer"}); err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(want, encode(changed)) != tc.same {
				t.Fatal("incorrect logical content projection")
			}
			if !bytes.Equal(want, encode(original)) {
				t.Fatal("clone mutated original recipe")
			}
		})
	}
	if bytes.Contains(want, []byte("https://")) {
		t.Fatal("transport URI entered logical recipe")
	}
}

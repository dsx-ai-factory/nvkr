// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package material

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func packageInputsFixture(arch string) PackageInputs {
	repository := "sha256:" + strings.Repeat("1", 64)
	return PackageInputs{
		SchemaVersion: PackageInputsSchema, PackageManager: "apt", Architecture: arch, Snapshot: "20261109T000000Z",
		Repositories: []PackageRepository{{URI: "https://snapshot.example.invalid/20261109T000000Z/InRelease", Digest: repository, Size: 123}},
		Packages: []PackageInput{
			{Name: "base-files", Version: "1:2.0-1", Architecture: arch, Digest: "sha256:" + strings.Repeat("2", 64), Size: 12,
				URI: "https://snapshot.example.invalid/pool/base-files.deb", RepositoryDigest: repository},
			{Name: "documentation", Version: "2.0", Architecture: "all", Digest: "sha256:" + strings.Repeat("3", 64), Size: 15,
				URI: "https://snapshot.example.invalid/pool/documentation.deb", RepositoryDigest: repository},
		},
	}
}

func TestPackageInputClosureByteLimit(t *testing.T) {
	for _, count := range []int{8, 9} {
		t.Run(fmt.Sprintf("%d GiB", count), func(t *testing.T) {
			inputs := packageInputsFixture("amd64")
			sample := inputs.Packages[0]
			inputs.Packages = nil
			for index := 0; index < count; index++ {
				item := sample
				item.Name = fmt.Sprintf("package-%02d", index)
				item.Digest = fmt.Sprintf("sha256:%064x", index)
				item.Size = 1 << 30
				inputs.Packages = append(inputs.Packages, item)
			}
			code := e.Code("")
			if count > 8 {
				code = e.CodeInvalidCatalog
			}
			requirePackageCode(t, inputs.AdmitAPT(), code)
		})
	}
}

func requirePackageCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	if code == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v; want %s", err, code)
	}
}

func TestPackageInputsAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PackageInputs)
		code   e.Code
	}{
		{name: "valid", mutate: func(*PackageInputs) {}},
		{name: "schema", mutate: func(p *PackageInputs) { p.SchemaVersion = "unknown" }, code: e.CodeInvalidCatalog},
		{name: "moving snapshot", mutate: func(p *PackageInputs) { p.Snapshot = "latest" }, code: e.CodeInvalidCatalog},
		{name: "invalid date", mutate: func(p *PackageInputs) { p.Snapshot = "20260230T000000Z" }, code: e.CodeInvalidCatalog},
		{name: "unimplemented adapter", mutate: func(p *PackageInputs) { p.PackageManager = "rpm" }, code: e.CodeEngineCapability},
		{name: "target architecture", mutate: func(p *PackageInputs) { p.Architecture = "riscv64" }, code: e.CodeEngineCapability},
		{name: "foreign package", mutate: func(p *PackageInputs) { p.Packages[0].Architecture = "arm64" }, code: e.CodeEngineCapability},
		{name: "no inputs", mutate: func(p *PackageInputs) { p.Packages = nil }, code: e.CodeInvalidCatalog},
		{name: "duplicate identity", mutate: func(p *PackageInputs) { p.Packages[1] = p.Packages[0] }, code: e.CodeInvalidCatalog},
		{name: "unsorted", mutate: func(p *PackageInputs) { p.Packages[0], p.Packages[1] = p.Packages[1], p.Packages[0] }, code: e.CodeInvalidCatalog},
		{name: "duplicate archive", mutate: func(p *PackageInputs) { p.Packages[1].Digest = p.Packages[0].Digest }, code: e.CodeInvalidCatalog},
		{name: "unbound metadata", mutate: func(p *PackageInputs) { p.Packages[0].RepositoryDigest = "sha256:" + strings.Repeat("4", 64) }, code: e.CodeInvalidCatalog},
		{name: "no repositories", mutate: func(p *PackageInputs) { p.Repositories = nil }, code: e.CodeInvalidCatalog},
		{name: "duplicate repository", mutate: func(p *PackageInputs) { p.Repositories = append(p.Repositories, p.Repositories[0]) }, code: e.CodeInvalidCatalog},
		{name: "unpinned archive", mutate: func(p *PackageInputs) { p.Packages[0].Digest = "latest" }, code: e.CodeInvalidCatalog},
		{name: "empty archive", mutate: func(p *PackageInputs) { p.Packages[0].Size = 0 }, code: e.CodeInvalidCatalog},
		{name: "oversized archive", mutate: func(p *PackageInputs) { p.Packages[0].Size = 1<<30 + 1 }, code: e.CodeInvalidCatalog},
		{name: "credentials", mutate: func(p *PackageInputs) { p.Packages[0].URI = "https://user:synthetic@example.invalid/pkg" }, code: e.CodeInvalidCatalog},
		{name: "query", mutate: func(p *PackageInputs) { p.Packages[0].URI += "?token=synthetic" }, code: e.CodeInvalidCatalog},
		{name: "http", mutate: func(p *PackageInputs) { p.Repositories[0].URI = "http://example.invalid/metadata" }, code: e.CodeInvalidCatalog},
		{name: "metadata size", mutate: func(p *PackageInputs) { p.Repositories[0].Size = 64<<20 + 1 }, code: e.CodeInvalidCatalog},
		{name: "ambiguous name", mutate: func(p *PackageInputs) { p.Packages[0].Name = "base\x00files" }, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := packageInputsFixture("amd64")
			tc.mutate(&inputs)
			requirePackageCode(t, inputs.AdmitAPT(), tc.code)
		})
	}
}

func installedManifestFixture(inputs PackageInputs) MkosiManifest {
	manifest := MkosiManifest{ManifestVersion: 1, Config: MkosiConfig{Name: "runtime", Distribution: "ubuntu", Release: "resolute",
		Architecture: map[string]string{"amd64": "x86-64", "arm64": "arm64"}[inputs.Architecture], OutputFormat: "tar"}, Extension: map[string]string{}}
	for _, item := range inputs.Packages {
		manifest.Packages = append(manifest.Packages, InstalledPackage{Type: "deb", Name: item.Name, Version: item.Version, Architecture: item.Architecture})
	}
	return manifest
}

func TestMkosiInstalledClosure(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, tc := range []struct {
			name   string
			mutate func(*MkosiManifest)
			valid  bool
		}{
			{name: "exact", mutate: func(*MkosiManifest) {}, valid: true},
			{name: "different order", mutate: func(m *MkosiManifest) { m.Packages[0], m.Packages[1] = m.Packages[1], m.Packages[0] }, valid: true},
			{name: "directory root", mutate: func(m *MkosiManifest) { m.Config.OutputFormat = "directory" }, valid: true},
			{name: "OCI root", mutate: func(m *MkosiManifest) { m.Config.OutputFormat = "oci" }, valid: true},
			{name: "missing", mutate: func(m *MkosiManifest) { m.Packages = m.Packages[:1] }},
			{name: "surplus", mutate: func(m *MkosiManifest) {
				m.Packages = append(m.Packages, InstalledPackage{Type: "deb", Name: "apt", Version: "1", Architecture: arch})
			}},
			{name: "duplicate", mutate: func(m *MkosiManifest) { m.Packages[1] = m.Packages[0] }},
			{name: "substituted name", mutate: func(m *MkosiManifest) { m.Packages[0].Name = "different" }},
			{name: "substituted version", mutate: func(m *MkosiManifest) { m.Packages[0].Version += "+changed" }},
			{name: "substituted architecture", mutate: func(m *MkosiManifest) { m.Packages[0].Architecture = "all" }},
			{name: "wrong manager", mutate: func(m *MkosiManifest) { m.Packages[0].Type = "rpm" }},
			{name: "wrong platform", mutate: func(m *MkosiManifest) { m.Config.Architecture = "riscv64" }},
			{name: "wrong schema", mutate: func(m *MkosiManifest) { m.ManifestVersion = 2 }},
			{name: "wrong format", mutate: func(m *MkosiManifest) { m.Config.OutputFormat = "uki" }},
			{name: "extension", mutate: func(m *MkosiManifest) { m.Extension["ID"] = "extension" }},
		} {
			t.Run(arch+"/"+tc.name, func(t *testing.T) {
				inputs := packageInputsFixture(arch)
				manifest := installedManifestFixture(inputs)
				tc.mutate(&manifest)
				raw, err := json.MarshalIndent(manifest, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				code := e.CodeVerifyFailed
				if tc.valid {
					code = ""
				}
				requirePackageCode(t, inputs.CheckMkosiClosure(raw), code)
			})
		}
	}
}

func TestMkosiManifestRejectsAmbiguousJSON(t *testing.T) {
	inputs := packageInputsFixture("amd64")
	raw, err := json.Marshal(installedManifestFixture(inputs))
	if err != nil {
		t.Fatal(err)
	}
	for name, document := range map[string]string{
		"duplicate":  strings.Replace(string(raw), `"manifest_version":1`, `"manifest_version":2,"manifest_version":1`, 1),
		"case alias": strings.Replace(string(raw), `"manifest_version":1`, `"MANIFEST_VERSION":2,"manifest_version":1`, 1),
		"unknown":    strings.Replace(string(raw), `"manifest_version":1`, `"manifest_version":1,"unknown":true`, 1),
		"trailing":   string(raw) + `{}`,
		"null":       "null",
		"empty":      "",
		"oversized":  strings.Repeat(" ", 8<<20+1),
	} {
		t.Run(name, func(t *testing.T) {
			requirePackageCode(t, inputs.CheckMkosiClosure([]byte(document)), e.CodeVerifyFailed)
		})
	}
}

func TestPackageInputsCanonicalRoundTrip(t *testing.T) {
	inputs := packageInputsFixture("arm64")
	raw, err := inputs.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var decoded PackageInputs
	if err := canonical.UnmarshalExact(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	requirePackageCode(t, decoded.AdmitAPT(), "")
}

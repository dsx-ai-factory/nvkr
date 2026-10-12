// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func embeddedFixture() *EmbeddedComponents {
	return &EmbeddedComponents{Manifest: "versions.yaml", Path: "components", Members: map[string]EmbeddedVersion{
		"runtime": {Version: "1.2.3"},
	}}
}

func TestEmbeddedManifestExactMembership(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       e.Code
	}{
		{"match", "components: {runtime: 1.2.3}", ""},
		{"object", "components: {runtime: {version: 1.2.3}}", ""},
		{"missing", "components: {}", e.CodePinSetMismatch},
		{"extra", "components: {runtime: 1.2.3, other: 2.0.0}", e.CodePinSetMismatch},
		{"substitution", "components: {other: 1.2.3}", e.CodePinSetMismatch},
		{"version", "components: {runtime: 1.2.4}", e.CodePinSetMismatch},
		{"wrong shape", "components: [runtime]", e.CodePinSetMismatch},
		{"unknown field", "components: {runtime: {version: 1.2.3, passed: true}}", e.CodePinSetMismatch},
		{"numeric version", "components: {runtime: 12}", e.CodePinSetMismatch},
		{"missing version", "components: {runtime: {}}", e.CodePinSetMismatch},
		{"duplicate", "components: {runtime: 1.2.3, runtime: 1.2.3}", e.CodeInvalidCatalog},
		{"alias", "components: {runtime: &a 1.2.3}", e.CodeInvalidCatalog},
		{"trailing document", "components: {runtime: 1.2.3}\n---\ncomponents: {}", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := compareEmbeddedManifest([]byte(tc.body), embeddedFixture())
			if got := catalogErrorCode(err); got != tc.code {
				t.Fatalf("error = %v (%s), want %s", err, got, tc.code)
			}
		})
	}
}

func TestEmbeddedKnownSourcePin(t *testing.T) {
	declaration := embeddedFixture()
	version := EmbeddedVersion{Version: "1.2.3", Repo: "https://example.test/runtime", Commit: goodCommit}
	declaration.Members["runtime"] = version
	for _, tc := range []struct {
		name, repo, commit string
		pass               bool
	}{
		{"exact", version.Repo, version.Commit, true},
		{"wrong commit", version.Repo, strings.Repeat("f", 40), false},
		{"wrong repository", "https://example.test/other", version.Commit, false},
		{"missing source", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "components: {runtime: {version: 1.2.3, repo: '" + tc.repo + "', commit: '" + tc.commit + "'}}"
			err := compareEmbeddedManifest([]byte(body), declaration)
			if tc.pass && err != nil {
				t.Fatal(err)
			}
			if !tc.pass {
				assertCatalogContractCode(t, err, e.CodePinSetMismatch)
			}
		})
	}
}

func TestEmbeddedDeclarationAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*EmbeddedComponents)
	}{
		{"nil map", func(d *EmbeddedComponents) { d.Members = nil }},
		{"traversal", func(d *EmbeddedComponents) { d.Manifest = "../versions.yaml" }},
		{"selector expression", func(d *EmbeddedComponents) { d.Path = "components[*]" }},
		{"mutable version", func(d *EmbeddedComponents) { d.Members["runtime"] = EmbeddedVersion{Version: "latest"} }},
		{"partial source", func(d *EmbeddedComponents) {
			d.Members["runtime"] = EmbeddedVersion{Version: "1.2.3", Repo: "https://example.test/runtime"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declaration := embeddedFixture()
			tc.mutate(declaration)
			assertCatalogContractCode(t, validateEmbeddedDeclaration(declaration, true), e.CodeInvalidCatalog)
		})
	}
	assertCatalogContractCode(t, validateEmbeddedDeclaration(embeddedFixture(), false), e.CodeInvalidCatalog)
}

func TestEmbeddedCheckAdmitsBeforeSourceWork(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selected []string
		wrapped  bool
		code     e.Code
	}{
		{"unknown", []string{"unknown"}, false, e.CodeInvalidCatalog},
		{"duplicate", []string{"tool", "tool"}, false, e.CodeInvalidCatalog},
		{"missing declaration", []string{"tool"}, true, e.CodePinSetMismatch},
		{"not applicable", []string{"tool"}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			component := goodComponent()
			if tc.wrapped {
				component.BuilderType, component.OutputType = BuilderBuildKit, "oci-image"
				component.CGO = false
			}
			cat := base(component)
			cat.Components = map[string]Component{"tool": component}
			receipt, err := checkEmbeddedComponents(t.Context(), cat, tc.selected, func(context.Context, Source, Pin) (pinnedTree, error) {
				t.Fatal("unexpected source acquisition")
				return nil, nil
			})
			if catalogErrorCode(err) != tc.code {
				t.Fatalf("error %v, want %s", err, tc.code)
			}
			if tc.code == "" {
				description, err := receipt.Description()
				if err != nil || description.Components[0].Status != "not-applicable" {
					t.Fatalf("description %+v: %v", description, err)
				}
			} else if receipt != nil {
				t.Fatal("failure issued a receipt")
			}
		})
	}
}

func TestEmbeddedChecksUsePinnedCommitAndDefensiveReceipt(t *testing.T) {
	repo, pin, run := pinTreeFixture(t, map[string][]byte{"versions.yaml": []byte("components: {runtime: 1.2.3}\n")})
	const upstream = "https://embedded.example.test/source"
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", upstream)
	component := goodComponent()
	component.Repo, component.Commit, component.TreeHash, component.SourceDate = upstream, pin.Commit, pin.TreeHash, pin.SourceDateEpoch
	component.Embeds = embeddedFixture()
	cat := base(component)
	cat.Components = map[string]Component{"tool": component}
	before, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	// A later default-branch manifest cannot change the frozen declaration check.
	if err := os.WriteFile(filepath.Join(repo, "versions.yaml"), []byte("components: {runtime: 9.9.9}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "versions.yaml")
	run("-c", "commit.gpgsign=false", "commit", "-qm", "different upstream version")
	receipt, err := CheckEmbeddedComponents(t.Context(), cat, []string{"tool"})
	if err != nil {
		t.Fatal(err)
	}
	description, err := receipt.Description()
	if err != nil || description.CatalogDigest != before || description.Components[0].Status != "pass" {
		t.Fatalf("receipt %+v, %v", description, err)
	}
	description.Components[0].Status = "fail"
	component.Embeds.Members["runtime"] = EmbeddedVersion{Version: "9.9.9"}
	after, err := receipt.Description()
	if err != nil || after.Components[0].Status != "pass" || after.CatalogDigest != before {
		t.Fatalf("receipt changed: %+v, %v", after, err)
	}
	_, err = CheckEmbeddedComponents(t.Context(), cat, []string{"tool"})
	assertCatalogContractCode(t, err, e.CodePinSetMismatch)
	_, err = (&EmbeddedReceipt{}).Description()
	assertCatalogContractCode(t, err, e.CodeGateUnmet)
}

func TestEmbeddedCatalogStrictLoadingAndIdentity(t *testing.T) {
	registry := registryDoc{APIVersion: catalogAPIVersion, Kind: catalogKind,
		Sources: map[string]Source{"source": {Repo: "https://example.test/source", ToolchainImage: goodImage}},
		Components: map[string]rawComponent{"tool": {Platforms: []string{"linux/amd64"}, FromSource: true,
			Source: "source", OutputType: "raw", BuildCommand: []string{"make"}, ArtifactPath: "out/tool", Embeds: embeddedFixture()}},
	}
	lock := "apiVersion: nvkr.dev/lock/v1\nkind: PinLock\npins:\n  source:\n    tag: v1.0.0\n    commit: " + goodCommit + "\n    treeHash: " + goodTree + "\n    sourceDateEpoch: 1733939955\n"
	raw, err := yaml.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	load := func(body []byte) (*Catalog, error) {
		return LoadContext(t.Context(), bytesProvider{RegistryFile: body, LockFile: []byte(lock)})
	}
	cat, err := load(raw)
	if err != nil {
		t.Fatal(err)
	}
	before, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	if cat.Components["tool"].Embeds.Members["runtime"].Version != "1.2.3" {
		t.Fatal("authored declaration was lost during resolution")
	}
	changed, err := load([]byte(strings.Replace(string(raw), "1.2.3", "1.2.4", 1)))
	if err != nil {
		t.Fatal(err)
	}
	after, err := changed.SnapshotDigest()
	if err != nil || before == after {
		t.Fatalf("embedded declaration did not change catalog identity: %v", err)
	}
	_, err = load([]byte(strings.Replace(string(raw), "manifest: versions.yaml", "manifest: versions.yaml\n      ignored: true", 1)))
	assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
}

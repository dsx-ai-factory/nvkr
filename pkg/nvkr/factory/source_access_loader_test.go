// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"maps"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"sigs.k8s.io/yaml"
)

func TestAcquiredPinSetCatalogLoadingAndCredentialScope(t *testing.T) {
	fixture := newAcquiredPinSetFixture(t)
	t.Chdir(fixture.root)
	for _, tc := range []struct {
		name string
		edit func(*catalog.PinLock)
		code e.Code
	}{
		{"locked metadata despite changed upstream", func(*catalog.PinLock) {}, ""},
		{"malformed pinned metadata", func(lock *catalog.PinLock) {
			lock.Pins["pins"] = fixture.malformed
			artifact := lock.Artifacts["tool"]
			artifact.DerivedFrom = "pins@" + fixture.malformed.Commit
			lock.Artifacts["tool"] = artifact
		}, e.CodePinSetMismatch},
		{"stale version with correct provenance", func(lock *catalog.PinLock) {
			artifact := lock.Artifacts["tool"]
			artifact.Tag = "1.2.4"
			lock.Artifacts["tool"] = artifact
		}, e.CodePinSetMismatch},
		{"stale provenance with correct version", func(lock *catalog.PinLock) {
			artifact := lock.Artifacts["tool"]
			artifact.DerivedFrom = "pins@" + fixture.malformed.Commit
			lock.Artifacts["tool"] = artifact
		}, e.CodePinSetMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lock := fixture.lock
			lock.Pins, lock.Artifacts = maps.Clone(lock.Pins), maps.Clone(lock.Artifacts)
			tc.edit(&lock)
			writeSnapshotFixtureDocument(t, fixture.root, "catalog/"+catalog.LockFile, lock)
			definition, err := authored.LoadDefinition(t.Context(), "definition.json")
			if err != nil {
				t.Fatal(err)
			}
			environment, err := authored.LoadEnvironment(t.Context(), "environment.json")
			if err != nil {
				t.Fatal(err)
			}
			plan, err := CompileDocuments(t.Context(), "definition.json", definition, "environment.json", environment, "test (deadbeef)")
			if tc.code != "" {
				assertSourceAccessCode(t, err, tc.code)
				if plan != nil {
					t.Fatal("invalid pinned metadata returned a partial Plan")
				}
				bundle, renderErr := RenderGitHub(t.Context(), plan)
				assertSourceAccessCode(t, renderErr, e.CodeInvalidCatalog)
				if bundle != nil {
					t.Fatal("invalid pinned metadata returned a credential-bearing bundle")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Builds) != 1 || len(plan.LifecycleOutputs) != 1 {
				t.Fatal("acquired-only fixture lost its exact output selection")
			}
			component := plan.Builds[0].CatalogSnapshot.Components["tool"]
			if component.Tag != "1.2.3" || component.Prebuilt == nil || component.Prebuilt.Image != "supplier.example/tool:1.2.3" ||
				component.SourceDate != fixture.lock.Pins["pins"].SourceDateEpoch {
				t.Fatal("acquired producer did not retain the version and source date derived from the locked Git metadata")
			}
			for _, node := range plan.Builds[0].Graph.Nodes {
				if node.Kind == ir.KindFetch {
					t.Fatal("catalog pin-set access invented a producer source fetch")
				}
			}
			assertPinSetOnlyCredentialScope(t, plan)
		})
	}
}

type acquiredPinSetFixture struct {
	root      string
	lock      catalog.PinLock
	malformed catalog.Pin
}

func newAcquiredPinSetFixture(t *testing.T) acquiredPinSetFixture {
	t.Helper()
	const repository = "https://github.com/example/private-pins"
	root, repo := t.TempDir(), t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", repository)
	git := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "git", append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null",
			"-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid"}, args...)...)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("local Git fixture: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "--template=")
	commit := func(metadata string) catalog.Pin {
		t.Helper()
		writeSnapshotFixtureFile(t, repo, "versions.yaml", []byte(metadata))
		git("add", "versions.yaml")
		git("commit", "-qm", "synthetic pin metadata")
		epoch, err := strconv.ParseInt(git("show", "-s", "--format=%ct", "HEAD"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return catalog.Pin{Tag: "v1.0.0", Commit: git("rev-parse", "HEAD"), TreeHash: git("rev-parse", "HEAD^{tree}"), SourceDateEpoch: epoch}
	}
	pin := commit("tool: {version: 1.2.3}\n")
	git("tag", "v1.0.0")
	malformed := commit("tool: {version: [not-a-version]}\n")
	git("tag", "-f", "v1.0.0")

	// Reuse the normal action pins; only the pin-set source is fetched. The
	// acquired image digest is synthetic and no registry is contacted or trusted.
	provider := testCatalogProvider(t)
	base, err := catalog.LoadContext(t.Context(), provider)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := provider.ReadFile(catalog.LockFile)
	if err != nil {
		t.Fatal(err)
	}
	var original catalog.PinLock
	if err := yaml.UnmarshalStrict(raw, &original); err != nil {
		t.Fatal(err)
	}
	lock := catalog.PinLock{APIVersion: original.APIVersion, Kind: original.Kind, Pins: map[string]catalog.Pin{"pins": pin},
		Artifacts: map[string]catalog.ArtifactPin{"tool": {Tag: "1.2.3", DerivedFrom: "pins@" + pin.Commit,
			Digests: map[string]string{"amd64": "sha256:" + strings.Repeat("a", 64)}}}}
	sources := map[string]catalog.Source{"pins": {Repo: repository, TagPrefix: "v", PinSet: &catalog.PinSet{
		File: "versions.yaml", Members: map[string]catalog.PinSetMember{"tool": {Artifact: "tool", Path: "tool.version"}},
	}}}
	for name, source := range base.Sources {
		if source.Kind == catalog.SourceGitHubAction {
			sources[name], lock.Pins[name] = source, original.Pins[name]
		}
	}
	registry := map[string]any{"apiVersion": base.APIVersion, "kind": base.Kind, "sources": sources,
		"components": map[string]any{"tool": map[string]any{
			"platforms": []string{"linux/amd64"}, "pinnedBy": "pins", "outputType": "oci-image",
			"prebuilt": catalog.Prebuilt{Image: "supplier.example/tool:{{.Version}}", Evidence: &catalog.ExternalEvidence{None: true}},
		}}}
	writeSnapshotFixtureDocument(t, root, "catalog/"+catalog.RegistryFile, registry)
	definition, environment := fixtureInputs("acquired-pin-set", "acquired", "ubuntu-24.04")
	definition.Catalog.Base = "catalog"
	definition.Outputs[0].Components = []string{"tool"}
	definition.Outputs[0].Format = authored.OutputOCI
	environment.SourceAccess = map[string]authored.SourceAccess{repository: {
		Mirror: "https://github.com/example/pin-mirror", CredentialRef: "github:PIN_SOURCE_TOKEN",
	}}
	writeSnapshotFixtureDocument(t, root, "definition.json", definition)
	writeSnapshotFixtureDocument(t, root, "environment.json", environment)
	return acquiredPinSetFixture{root: root, lock: lock, malformed: malformed}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

const companionFixtureName = "aicr-attestation.sigstore.json"

var (
	companionFixtureExecutable  = []byte("pinned aicr executable")
	companionFixtureAttestation = []byte("pinned aicr attestation")
)

func companionFixtureDigest(content []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(content))
}

func releaseCompanionPlan(t *testing.T) *Plan {
	t.Helper()
	tool := catalog.ToolPin{Format: "tar-gzip", URL: "https://example.test/aicr.tar.gz", Platform: "linux/arm64", ExecutablePath: "aicr",
		ArchiveDigest: "sha256:" + strings.Repeat("a", 64), ExecutableDigest: companionFixtureDigest(companionFixtureExecutable),
		Companions: []catalog.ToolCompanion{{Path: companionFixtureName, Digest: companionFixtureDigest(companionFixtureAttestation)}}}
	base := &catalog.Catalog{Components: map[string]catalog.Component{"aicr": {Platforms: []string{tool.Platform}, OutputType: "host-package",
		Tag: "v1.1.2", SourceDate: 1, Prebuilt: &catalog.Prebuilt{URL: tool.URL, Digests: map[string]string{"arm64": tool.ExecutableDigest},
			Executable: &tool, Evidence: &catalog.ExternalEvidence{None: true}}}}}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("aicr", "aicr", "")
	definition.Outputs = []authored.DefinitionOutput{{Name: "aicr", Format: authored.OutputExecutable, Platform: tool.Platform, Components: []string{"aicr"}}}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestReleaseCompanionCompilesBesideExecutable(t *testing.T) {
	plan := releaseCompanionPlan(t)
	nodes, artifacts := planIndex(plan)
	id := releaseCompanionID("aicr", "aicr", companionFixtureName)
	companion := artifacts[id]
	if companion.Kind != ArtifactReleaseCompanion || companion.Name != companionFixtureName || companion.Producer != "aicr/pull-aicr" ||
		companion.Path != filepath.ToSlash(filepath.Join(filepath.Dir(artifacts["artifact/aicr/aicr"].Path), companionFixtureName)) {
		t.Fatalf("companion is not declared beside its executable: %+v", companion)
	}
	for _, consumer := range []string{"aicr/terminal-observe", "package/aicr"} {
		if !containsString(nodes[consumer].Inputs, id) {
			t.Fatalf("%s omits companion: %v", consumer, nodes[consumer].Inputs)
		}
	}
	if sbom, ok := nodes["aicr/sbom"]; ok && containsString(sbom.Inputs, id) {
		t.Fatal("companion was scanned as a producer payload")
	}
	member := false
	for _, dependency := range resolvedDependencies(plan) {
		binding, err := url.Parse(dependency.URI)
		if err == nil && binding.Query().Get("member") == companionFixtureName {
			member = dependency.Digest["sha256"] == strings.TrimPrefix(companionFixtureDigest(companionFixtureAttestation), "sha256:")
		}
	}
	if !member {
		t.Fatal("provenance omits the companion member")
	}
}

func TestReleaseCompanionObservationAndPackage(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, map[string]Artifact)
		code   e.Code
	}{
		{name: "exact"},
		{name: "tampered companion", change: func(t *testing.T, artifacts map[string]Artifact) {
			if err := os.WriteFile(artifacts[releaseCompanionID("aicr", "aicr", companionFixtureName)].Path, []byte("other"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeVerifyFailed},
		{name: "missing companion", change: func(t *testing.T, artifacts map[string]Artifact) {
			if err := os.Remove(artifacts[releaseCompanionID("aicr", "aicr", companionFixtureName)].Path); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeVerifyFailed},
		{name: "undeclared companion", change: func(_ *testing.T, artifacts map[string]Artifact) {
			delete(artifacts, releaseCompanionID("aicr", "aicr", companionFixtureName))
		}, code: e.CodeReleaseIncomplete},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan := releaseCompanionPlan(t)
			nodes, artifacts := planIndex(plan)
			for id, content := range map[string][]byte{
				"artifact/aicr/aicr": companionFixtureExecutable,
				releaseCompanionID("aicr", "aicr", companionFixtureName): companionFixtureAttestation,
			} {
				if err := os.MkdirAll(filepath.Dir(artifacts[id].Path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(artifacts[id].Path, content, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			record, err := canonical.Marshal(pipeline.ArtifactResult{Component: "aicr", OutputType: "host-package", EngineClass: ir.EnginePrebuiltAcquire,
				SHA256: companionFixtureDigest(companionFixtureExecutable), Size: int64(len(companionFixtureExecutable))})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(artifacts["result/aicr/aicr"].Path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(artifacts["result/aicr/aicr"].Path, record, 0o644); err != nil {
				t.Fatal(err)
			}
			if test.change != nil {
				test.change(t, artifacts)
			}
			err = executeObservation(t.Context(), plan, nodes["aicr/terminal-observe"], artifacts)
			if test.code != "" {
				assertFactoryErrorCode(t, err, test.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := executePackage(t.Context(), plan, nodes["package/aicr"], artifacts, io.Discard); err != nil {
				t.Fatal(err)
			}
			bundle, _, err := delivery.InspectBinaryBundle(t.Context(), artifacts["package/aicr"].Path)
			if err != nil {
				t.Fatal(err)
			}
			if len(bundle.Entries) != 2 ||
				bundle.Entries[0] != (delivery.BinaryEntry{Name: "aicr", InstallPath: "/usr/local/bin/aicr", Mode: 0o755,
					SHA256: companionFixtureDigest(companionFixtureExecutable), Size: int64(len(companionFixtureExecutable))}) ||
				bundle.Entries[1] != (delivery.BinaryEntry{Name: companionFixtureName, InstallPath: "/usr/local/bin/" + companionFixtureName, Mode: 0o644,
					SHA256: companionFixtureDigest(companionFixtureAttestation), Size: int64(len(companionFixtureAttestation))}) {
				t.Fatalf("bundle does not place the companion beside the executable: %+v", bundle.Entries)
			}
			observed, err := ObserveLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], artifacts)
			if err != nil {
				t.Fatal(err)
			}
			if len(observed.Components) != 1 || observed.Components[0] != "aicr" {
				t.Fatalf("companion counted as a producer member: %v", observed.Components)
			}
			if err := os.WriteFile(artifacts[releaseCompanionID("aicr", "aicr", companionFixtureName)].Path, []byte("other"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err = ObserveLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], artifacts)
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
		})
	}
}

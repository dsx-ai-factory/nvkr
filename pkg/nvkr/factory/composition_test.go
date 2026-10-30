// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const compositeFixturePath = "config/composite.json"

// compositeFactoryFixture splits the snapshot fixture's two outputs into two
// members beside a composite. mutate may change members before they are pinned.
func compositeFactoryFixture(t *testing.T, mutate func(alpha, beta *authored.Definition)) (string, authored.CompositeDefinition) {
	t.Helper()
	root, plain, _ := snapshotFixture(t)
	alpha, beta := plain.Clone(), plain.Clone()
	alpha.Metadata.Name, beta.Metadata.Name = "alpha", "beta"
	alpha.Outputs, beta.Outputs = alpha.Outputs[1:], beta.Outputs[:1]
	if mutate != nil {
		mutate(&alpha, &beta)
	}
	composite := authored.CompositeDefinition{APIVersion: authored.CompositeAPIVersion, Kind: authored.CompositeKind, Metadata: authored.Metadata{Name: "product"}}
	for _, member := range []authored.Definition{alpha, beta} {
		writeSnapshotFixtureDocument(t, root, "config/"+member.Metadata.Name+".json", member)
		digest, err := member.Digest()
		if err != nil {
			t.Fatal(err)
		}
		composite.Members = append(composite.Members, authored.CompositeMember{Name: member.Metadata.Name, Definition: member.Metadata.Name + ".json", Digest: digest})
	}
	writeSnapshotFixtureDocument(t, root, compositeFixturePath, composite)
	return root, composite
}

func TestCompositeCompilationContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(alpha, beta *authored.Definition)
		env    func(environment *authored.Environment)
		code   e.Code
	}{
		{name: "disjoint members"},
		{name: "within-member variants", mutate: func(alpha, _ *authored.Definition) {
			alpha.Outputs = append(alpha.Outputs, authored.DefinitionOutput{Name: "tools-plain", Platform: "linux/amd64", Format: authored.OutputExecutable,
				Components: []string{"containerd"}, Catalog: &authored.Catalog{Base: "../override"}})
		}},
		{name: "equal cross-member producer", mutate: func(_, beta *authored.Definition) {
			beta.Outputs = append(beta.Outputs, authored.DefinitionOutput{Name: "shared", Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"containerd"}})
		}},
		{name: "divergent cross-member producer", code: e.CodeInvalidCatalog, mutate: func(_, beta *authored.Definition) {
			beta.Outputs = append(beta.Outputs, authored.DefinitionOutput{Name: "shared", Platform: "linux/amd64", Format: authored.OutputExecutable,
				Components: []string{"containerd"}, Catalog: &authored.Catalog{Base: "../override"}})
		}},
		{name: "runtime Plan refused before members", code: e.CodeEngineCapability, env: func(environment *authored.Environment) {
			environment.GitHubBundle, environment.GitHubRuntimePlan = "product", true
		}},
		{name: "fixed development layout refused before members", code: e.CodeEngineCapability, env: func(environment *authored.Environment) {
			environment.GitHubDevelopment = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := compositeFactoryFixture(t, tc.mutate)
			if tc.env != nil {
				_, environment := fixtureInputs("snapshot-fixture", "tools", "")
				tc.env(&environment)
				writeSnapshotFixtureDocument(t, root, snapshotEnvironment, environment)
				// Admission must refuse the environment before reading any member.
				if err := os.Remove(filepath.Join(root, "config", "alpha.json")); err != nil {
					t.Fatal(err)
				}
			}
			plan, snapshot, err := CaptureCompileFiles(t.Context(), root, compositeFixturePath, snapshotEnvironment, "composite-test")
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
				if plan != nil {
					t.Fatal("refused composite returned a Plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.SchemaVersion != CompositePlanSchemaVersion || plan.Composition == nil || plan.Name != "product" || plan.DefinitionPath != compositeFixturePath {
				t.Fatalf("composite Plan header = %s %s %+v", plan.SchemaVersion, plan.Name, plan.Composition)
			}
			if plan.DefinitionDigest == plan.Composition.EffectiveDefinitionDigest {
				t.Fatal("definitionDigest does not name the composite")
			}
			for _, name := range []string{"config/alpha.json", "config/beta.json", compositeFixturePath} {
				if !slices.ContainsFunc(snapshot.Files, func(file InputFile) bool { return file.Path == name }) {
					t.Fatalf("capture omitted %s", name)
				}
			}
			assertCompositeIdentityInvariance(t, root, plan)

			// Replay recompiles every member from captured bytes without the checkout.
			want, err := plan.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(root, filepath.Join(t.TempDir(), "moved")); err != nil {
				t.Fatal(err)
			}
			replayed, err := CompileSnapshot(t.Context(), snapshot, "composite-test")
			if err != nil {
				t.Fatal(err)
			}
			if got, err := replayed.Digest(); err != nil || got != want {
				t.Fatalf("replayed composite Plan = %s, want %s (%v)", got, want, err)
			}
		})
	}
}

// assertCompositeIdentityInvariance compiles each member alone and requires
// every composite output and native build to keep its exact record.
func assertCompositeIdentityInvariance(t *testing.T, root string, plan *Plan) {
	t.Helper()
	builds := map[string][]byte{}
	outputs := map[string][]byte{}
	for _, member := range plan.Composition.Members {
		alone, _, err := CaptureCompileFiles(t.Context(), root, "config/"+member.Name+".json", snapshotEnvironment, "composite-test")
		if err != nil {
			t.Fatal(err)
		}
		if alone.SchemaVersion != PlanSchemaVersion || alone.Composition != nil {
			t.Fatal("plain member compiled with a composition record")
		}
		for _, build := range alone.Builds {
			builds[build.Name] = mustCanonical(t, build)
		}
		for _, output := range alone.LifecycleOutputs {
			outputs[output.Name] = mustCanonical(t, output)
		}
	}
	if len(builds) != len(plan.Builds) || len(outputs) != len(plan.LifecycleOutputs) {
		t.Fatalf("composite has %d builds/%d outputs; members have %d/%d", len(plan.Builds), len(plan.LifecycleOutputs), len(builds), len(outputs))
	}
	for _, build := range plan.Builds {
		if !bytes.Equal(mustCanonical(t, build), builds[build.Name]) {
			t.Fatalf("composition changed native build %q", build.Name)
		}
	}
	for _, output := range plan.LifecycleOutputs {
		if !bytes.Equal(mustCanonical(t, output), outputs[output.Name]) {
			t.Fatalf("composition changed output %q", output.Name)
		}
	}
}

func mustCanonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCompositePlanRecordValidation(t *testing.T) {
	root, _ := compositeFactoryFixture(t, nil)
	compiled, _, err := CaptureCompileFiles(t.Context(), root, compositeFixturePath, snapshotEnvironment, "composite-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(plan *Plan)
		code   e.Code
	}{
		{name: "complete record"},
		{name: "plain schema with record", code: e.CodeInvalidCatalog, mutate: func(plan *Plan) { plan.SchemaVersion = PlanSchemaVersion }},
		{name: "composite schema without record", code: e.CodeInvalidCatalog, mutate: func(plan *Plan) { plan.Composition = nil }},
		{name: "wrong kind", code: e.CodeUnverifiedRef, mutate: func(plan *Plan) { plan.Composition.Kind = authored.DefinitionKind }},
		{name: "tampered effective digest", code: e.CodeUnverifiedRef, mutate: func(plan *Plan) {
			plan.Composition.EffectiveDefinitionDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "tampered member digest", code: e.CodeUnverifiedRef, mutate: func(plan *Plan) {
			plan.Composition.Members[0].Digest = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "unlisted member", code: e.CodeUnverifiedRef, mutate: func(plan *Plan) { plan.Composition.Outputs[0].Member = "gamma" }},
		{name: "missing attribution", code: e.CodeUnverifiedRef, mutate: func(plan *Plan) { plan.Composition.Outputs = plan.Composition.Outputs[1:] }},
		{name: "member without outputs", code: e.CodeUnverifiedRef, mutate: func(plan *Plan) {
			plan.Composition.Outputs[0].Member = plan.Composition.Outputs[1].Member
		}},
		{name: "plain definition digest", code: e.CodeUnverifiedRef, mutate: func(plan *Plan) {
			plan.DefinitionDigest = plan.Composition.EffectiveDefinitionDigest
		}},
		{name: "fixed development layout", code: e.CodeEngineCapability, mutate: func(plan *Plan) {
			plan.Environment.GitHubDevelopment, plan.Environment.GitHubBundle = true, ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := *compiled
			plan.Composition = compiled.Composition.Clone()
			if tc.mutate != nil {
				tc.mutate(&plan)
			}
			err := plan.Validate()
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertFactoryErrorCode(t, err, tc.code)
		})
	}
}

func TestPlainPlanKeepsItsSchemaAndBytes(t *testing.T) {
	_, plan, _ := captureSnapshotFixture(t)
	raw := mustCanonical(t, plan)
	if plan.SchemaVersion != PlanSchemaVersion || plan.Composition != nil || bytes.Contains(raw, []byte(`"composition"`)) {
		t.Fatal("plain Definition Plan gained a composition record or schema major")
	}
}

func TestCompositeCompilationCancellation(t *testing.T) {
	root, _ := compositeFactoryFixture(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := CaptureCompileFiles(ctx, root, compositeFixturePath, snapshotEnvironment, "composite-test")
	assertFactoryErrorCode(t, err, e.CodeTimeout)
}

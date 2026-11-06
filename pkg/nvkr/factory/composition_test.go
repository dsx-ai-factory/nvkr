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

// TestCompositeDeclaredVariantContract proves the ADR-26 §3 opt-in: members
// may resolve different producers for one component only when the composite
// declares them as variants of that component, and each variant stays
// attributed to its member.
func TestCompositeDeclaredVariantContract(t *testing.T) {
	shared := func(name, component string, catalog *authored.Catalog) func(*authored.Definition) {
		return func(member *authored.Definition) {
			member.Outputs = append(member.Outputs, authored.DefinitionOutput{Name: name, Platform: "linux/amd64", Format: authored.OutputExecutable,
				Components: []string{component}, Catalog: catalog})
		}
	}
	override := &authored.Catalog{Base: "../override"}
	diverge, converge := shared("shared", "containerd", override), shared("shared", "containerd", nil)
	// alpha also selects beta's kubectl producer, so a kubectl declaration is selected by both.
	alphaKubectl := shared("tools-kubectl", "kubectl", override)
	variant := func(component string, members ...string) []authored.DeclaredVariant {
		return []authored.DeclaredVariant{{Component: component, Members: members}}
	}
	for _, tc := range []struct {
		name     string
		alpha    func(*authored.Definition)
		beta     func(*authored.Definition)
		gamma    bool // gamma selects containerd from beta's divergent catalog
		variants []authored.DeclaredVariant
		code     e.Code
		distinct int
	}{
		{name: "undeclared divergence", beta: diverge, code: e.CodeInvalidCatalog},
		{name: "declared pair", beta: diverge, variants: variant("containerd", "alpha", "beta"), distinct: 2},
		{name: "declared pair on the wrong component", alpha: alphaKubectl, beta: diverge, variants: variant("kubectl", "alpha", "beta"), code: e.CodeInvalidCatalog},
		{name: "declared pair on the wrong member", beta: diverge, gamma: true, variants: variant("containerd", "alpha", "gamma"), code: e.CodeInvalidCatalog},
		{name: "declared pair on the other wrong member", beta: diverge, gamma: true, variants: variant("containerd", "beta", "gamma"), code: e.CodeInvalidCatalog},
		{name: "declared member set", beta: diverge, gamma: true, variants: variant("containerd", "alpha", "beta", "gamma"), distinct: 2},
		{name: "declared pair with converged producers", beta: converge, variants: variant("containerd", "alpha", "beta"), distinct: 1},
		{name: "declared member without the component", variants: variant("containerd", "alpha", "beta"), code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gamma authored.Definition
			root, composite := compositeFactoryFixture(t, func(alpha, beta *authored.Definition) {
				gamma = beta.Clone()
				gamma.Metadata.Name = "gamma"
				gamma.Outputs = nil
				diverge(&gamma)
				gamma.Outputs[0].Name = "gamma-tools"
				if tc.alpha != nil {
					tc.alpha(alpha)
				}
				if tc.beta != nil {
					tc.beta(beta)
				}
			})
			if tc.gamma {
				writeSnapshotFixtureDocument(t, root, "config/gamma.json", gamma)
				digest, err := gamma.Digest()
				if err != nil {
					t.Fatal(err)
				}
				composite.Members = append(composite.Members, authored.CompositeMember{Name: "gamma", Definition: "gamma.json", Digest: digest})
			}
			composite.Variants = tc.variants
			writeSnapshotFixtureDocument(t, root, compositeFixturePath, composite)
			plan, _, err := CaptureCompileFiles(t.Context(), root, compositeFixturePath, snapshotEnvironment, "composite-test")
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
			if want, err := composite.Digest(); err != nil || plan.DefinitionDigest != want || !slices.EqualFunc(plan.Composition.Variants, tc.variants, func(a, b authored.DeclaredVariant) bool {
				return a.Component == b.Component && slices.Equal(a.Members, b.Members)
			}) {
				t.Fatalf("Plan binds %s with variants %+v, want %s with %+v (%v)", plan.DefinitionDigest, plan.Composition.Variants, want, tc.variants, err)
			}
			assertCompositeIdentityInvariance(t, root, plan)
			// Each distinct containerd producer is attributed only to declared members.
			producers := map[string]map[string]bool{}
			for _, build := range plan.Builds {
				output, _, _ := strings.Cut(build.Name, "/")
				member, _ := plan.Composition.MemberOf(output)
				for _, component := range build.ResolvedComponents {
					if component.Name == "containerd" {
						key := string(mustCanonical(t, component))
						if producers[key] == nil {
							producers[key] = map[string]bool{}
						}
						producers[key][member] = true
					}
				}
			}
			if len(producers) != tc.distinct {
				t.Fatalf("containerd has %d distinct producers, want %d", len(producers), tc.distinct)
			}
			for _, members := range producers {
				for member := range members {
					if !slices.Contains(tc.variants[0].Members, member) {
						t.Fatalf("containerd variant attributed to undeclared member %q", member)
					}
				}
			}
			// A saved Plan stripped of its declaration, with a recomputed digest, still fails validation.
			forged := *plan
			forged.Composition = plan.Composition.Clone()
			forged.Composition.Variants = nil
			if forged.DefinitionDigest, err = authored.CompositeDigest(forged.Composition.Metadata, forged.Composition.Members); err != nil {
				t.Fatal(err)
			}
			if err := forged.Validate(); tc.distinct > 1 {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			} else if err != nil {
				t.Fatalf("converged Plan without its declaration = %v", err)
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
		{name: "added variant", code: e.CodeUnverifiedRef, mutate: func(plan *Plan) {
			plan.Composition.Variants = []authored.DeclaredVariant{{Component: "containerd", Members: []string{"alpha", "beta"}}}
		}},
		{name: "variant naming unlisted member", code: e.CodeUnverifiedRef, mutate: func(plan *Plan) {
			plan.Composition.Variants = []authored.DeclaredVariant{{Component: "containerd", Members: []string{"alpha", "gamma"}}}
		}},
		// The digest recomputes, so only the record's selection check can refuse it: beta selects no containerd.
		{name: "digest-consistent variant member without the component", code: e.CodeInvalidCatalog, mutate: func(plan *Plan) {
			plan.Composition.Variants = []authored.DeclaredVariant{{Component: "containerd", Members: []string{"alpha", "beta"}}}
			if digest, err := authored.CompositeDigest(plan.Composition.Metadata, plan.Composition.Members, plan.Composition.Variants...); err == nil {
				plan.DefinitionDigest = digest
			}
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

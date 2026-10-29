// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

const (
	compositeTestPath = "defs/composite.yaml"
	compositeTestEnv  = "defs/environment.yaml"
	compositeTestUtil = "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161"
)

func writeCompositeTestDocument(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// compositeLifecycleFixture changes into a fresh input root holding the
// fixture catalog, two members, a composite and a local Environment.
func compositeLifecycleFixture(t *testing.T, mutate func(*definition.Environment)) definition.CompositeDefinition {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("composite admission requires bounded no-follow filesystem support")
	}
	_, source, _, _ := runtime.Caller(0)
	catalogDir := filepath.Join(filepath.Dir(source), "..", "..", "..", "testdata", "catalogs", "kubernetes-layered")
	root := t.TempDir()
	for _, name := range []string{"registry.yaml", "pins.lock.yaml"} {
		raw, err := os.ReadFile(filepath.Join(catalogDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "catalog"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "catalog", name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	composite := definition.CompositeDefinition{APIVersion: definition.CompositeAPIVersion, Kind: definition.CompositeKind, Metadata: Metadata{Name: "product"}}
	for _, member := range []struct{ name, component string }{{"alpha", "containerd"}, {"beta", "kubelet"}} {
		doc := Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind, Metadata: Metadata{Name: member.name}, Catalog: Catalog{Base: "../catalog"},
			Outputs: []DefinitionOutput{{Name: member.name + "-tools", Platform: "linux/amd64", Format: OutputExecutable, Components: []string{member.component}}}}
		writeCompositeTestDocument(t, "defs/"+member.name+".yaml", doc)
		digest, err := doc.Digest()
		if err != nil {
			t.Fatal(err)
		}
		composite.Members = append(composite.Members, definition.CompositeMember{Name: member.name, Definition: member.name + ".yaml", Digest: digest})
	}
	writeCompositeTestDocument(t, compositeTestPath, composite)
	environment := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "fixture"},
		UtilityContainer: compositeTestUtil, UtilityPlatform: "linux/amd64"}
	if mutate != nil {
		mutate(&environment)
	}
	writeCompositeTestDocument(t, compositeTestEnv, environment)
	return composite
}

func requireLifecycleCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("want %s; got %v", code, err)
	}
}

func TestCompositeAdmissionMatrix(t *testing.T) {
	compositeLifecycleFixture(t, nil)
	plan, err := CompileFiles(t.Context(), compositeTestPath, compositeTestEnv, "composite-test")
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != factory.CompositePlanSchemaVersion || plan.Composition == nil || len(plan.LifecycleOutputs) != 2 {
		t.Fatalf("run plan = %s %+v", plan.SchemaVersion, plan.Composition)
	}
	input, err := LoadDefinitionInput(t.Context(), compositeTestPath)
	if err != nil || input.Digest != plan.DefinitionDigest {
		t.Fatalf("validate digest = %q, Plan binds %q (%v)", input.Digest, plan.DefinitionDigest, err)
	}
	view, err := InspectComposite(input)
	if err != nil || view.EffectiveDefinitionDigest != plan.Composition.EffectiveDefinitionDigest || len(view.Members) != 2 || view.Members[1].Definition != "defs/beta.yaml" {
		t.Fatalf("inspect view = %+v, %v", view, err)
	}

	// Each deferred §9 use refuses before catalog resolution or attempt output.
	for _, tc := range []struct {
		name  string
		check func() error
	}{
		{"qualification generation and release manifest issuance", func() error { return RefuseComposite(t.Context(), compositeTestPath, "qualification") }},
		{"artifact check and qualification handoff", func() error { return factory.AdmitQualificationHandoff(plan) }},
		// check-artifacts files, release expectations and other plain-only uses load here.
		{"plain-only Definition load", func() error {
			_, err := LoadDefinition(t.Context(), compositeTestPath)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireLifecycleCode(t, tc.check(), e.CodeEngineCapability)
		})
	}
	if _, err := os.Stat(".nvkr"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused uses created attempt or workspace output: %v", err)
	}
	if err := RefuseComposite(t.Context(), "defs/alpha.yaml", "qualification"); err != nil {
		t.Fatalf("plain Definition refused: %v", err)
	}
}

func TestCompositeRuntimePlanRefusedBeforeMembers(t *testing.T) {
	compositeLifecycleFixture(t, func(environment *definition.Environment) {
		environment.GitHubBundle, environment.GitHubRuntimePlan = "product", true
	})
	if err := os.Remove("defs/alpha.yaml"); err != nil {
		t.Fatal(err)
	}
	_, err := CompileFiles(t.Context(), compositeTestPath, compositeTestEnv, "composite-test")
	requireLifecycleCode(t, err, e.CodeEngineCapability)
}

func TestPinDefinitionDelegatesToOwner(t *testing.T) {
	composite := compositeLifecycleFixture(t, nil)
	_, err := PinDefinition(t.Context(), "defs/alpha.yaml", true)
	requireLifecycleCode(t, err, e.CodeUsage)
	report, err := PinDefinition(t.Context(), compositeTestPath, true)
	if err != nil || len(report.Drift) != 0 || report.Written {
		t.Fatalf("current pins = %+v, %v", report, err)
	}
	doc, err := definition.LoadDefinition(t.Context(), "defs/beta.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc.Metadata.Description = "edited"
	writeCompositeTestDocument(t, "defs/beta.yaml", doc)
	_, err = CompileFiles(t.Context(), compositeTestPath, compositeTestEnv, "composite-test")
	requireLifecycleCode(t, err, e.CodeUnverifiedRef)
	report, err = PinDefinition(t.Context(), compositeTestPath, true)
	requireLifecycleCode(t, err, e.CodeUnverifiedRef)
	if len(report.Drift) != 1 || report.Drift[0].Pinned != composite.Members[1].Digest {
		t.Fatalf("drift = %+v", report)
	}
	if report, err = PinDefinition(t.Context(), compositeTestPath, false); err != nil || !report.Written {
		t.Fatalf("pin = %+v, %v", report, err)
	}
	if _, err := CompileFiles(t.Context(), compositeTestPath, compositeTestEnv, "composite-test"); err != nil {
		t.Fatal(err)
	}
}

func TestCompositeRunRecordContract(t *testing.T) {
	compositeLifecycleFixture(t, nil)
	prepared, err := PrepareFiles(t.Context(), compositeTestPath, compositeTestEnv, "composite-test", "attempt-1")
	if err != nil {
		t.Fatal(err)
	}
	run := prepared.run
	if run.SchemaVersion != CompositeRunSchemaVersion || run.Composition == nil || run.DefinitionDigest != prepared.plan.DefinitionDigest ||
		run.Lane != LaneUnqualified || prepared.inputs == "" {
		t.Fatalf("composite Run = %+v", run)
	}
	if member, _ := run.Composition.MemberOf("beta-tools"); member != "beta" {
		t.Fatal("composite Run omits per-output member attribution")
	}
	plain, err := PrepareFiles(t.Context(), "defs/alpha.yaml", compositeTestEnv, "composite-test", "attempt-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(plain.run)
	if err != nil {
		t.Fatal(err)
	}
	if plain.run.SchemaVersion != RunSchemaVersion || strings.Contains(string(raw), `"composition"`) {
		t.Fatal("plain Run changed its v4 schema or bytes")
	}
	for _, tc := range []struct {
		name   string
		mutate func(run *Run)
	}{
		{"complete record", nil},
		{"v4 reader rejects composite major", func(run *Run) { run.SchemaVersion = RunSchemaVersion }},
		{"composite major without record", func(run *Run) { run.Composition = nil }},
		{"attribution to unlisted member", func(run *Run) { run.Composition.Outputs[0].Member = "gamma" }},
		{"missing attribution", func(run *Run) { run.Composition.Outputs = run.Composition.Outputs[1:] }},
		{"extra member", func(run *Run) { run.Composition.Outputs[1].Member = run.Composition.Outputs[0].Member }},
		{"tampered effective digest", func(run *Run) { run.Composition.EffectiveDefinitionDigest = "sha256:short" }},
		{"plain definition digest", func(run *Run) { run.DefinitionDigest = run.Composition.EffectiveDefinitionDigest }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := *run
			candidate.Composition = run.Composition.Clone()
			if tc.mutate != nil {
				tc.mutate(&candidate)
			}
			err := validateInspectedRun(&candidate)
			if tc.mutate == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireLifecycleCode(t, err, e.CodeVerifyFailed)
		})
	}
}

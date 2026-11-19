// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

func TestPlanSharesRuntimePackageSnapshots(t *testing.T) {
	for _, outputs := range [][]string{{"first", "second"}, {"first", "second", "third", "fourth"}} {
		t.Run(strings.Join(outputs, "-"), func(t *testing.T) {
			plan := runtimeConsumersPlanFixture(t, outputs)
			original, err := canonical.Marshal(planValue(*plan))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := canonical.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(raw)) > MaxPlanBytes {
				t.Fatalf("Plan is %d bytes", len(raw))
			}
			document := planDocument{planValue: new(planValue)}
			if err := canonical.UnmarshalExact(raw, &document); err != nil {
				t.Fatal(err)
			}
			if len(document.PackageSnapshots) != 2 {
				t.Fatalf("snapshots = %d, want two distinct roots", len(document.PackageSnapshots))
			}
			if document.SchemaVersion != sharedPlanSchemaVersion {
				t.Fatal("shared package inputs did not select their wire schema")
			}
			for path, snapshot := range document.PackageSnapshots {
				input, err := snapshot.Inputs.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Count(raw, input) != 1 {
					t.Fatal("package document is not stored exactly once")
				}
				if path != "store/package-inputs-"+strings.TrimPrefix(snapshot.Digest, "sha256:")[:12]+".json" {
					t.Fatalf("snapshot path %q", path)
				}
				if got := bytes.Count(raw, []byte(`"inputsDigest":"`+snapshot.Digest+`"`)); got != 4*len(outputs) {
					t.Fatalf("references = %d, want %d", got, 4*len(outputs))
				}
			}
			name := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(name, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			restored, err := LoadPlanContext(t.Context(), name)
			if err != nil {
				t.Fatal(err)
			}
			expanded, err := canonical.Marshal(planValue(*restored))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, expanded) {
				t.Fatal("snapshot restoration changed complete executable descriptions or Build identities")
			}
			after, err := canonical.Marshal(planValue(*plan))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) {
				t.Fatal("serialization mutated the source Plan")
			}
			if !reflect.DeepEqual(plan.InternalTools, restored.InternalTools) {
				t.Fatal("sharing changed independently named runtime requirements")
			}
			again, err := canonical.Marshal(restored)
			if err != nil || !bytes.Equal(raw, again) {
				t.Fatal("shared Plan round trip is not byte-identical", err)
			}
			// Display decoding remains descriptive; canonical admission must still
			// reject the same whitespace-bearing bytes.
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, raw, "", "  "); err != nil {
				t.Fatal(err)
			}
			var display Plan
			if err := json.Unmarshal(pretty.Bytes(), &display); err != nil {
				t.Fatal(err)
			}
			if canonical.UnmarshalExact(pretty.Bytes(), &display) == nil {
				t.Fatal("pretty display passed exact-byte admission")
			}
			t.Logf("requirements=%d inline=%d shared=%d bound=%d", len(outputs), len(original), len(raw), MaxPlanBytes)
		})
	}
}

func TestPlanPackageSnapshotsRejectInvalidReferences(t *testing.T) {
	plan := runtimeConsumersPlanFixture(t, []string{"first", "second"})
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*planDocument)
		code   e.Code
	}{
		{"missing target", func(d *planDocument) { d.PackageSnapshots = nil }, e.CodePipelineDrift},
		{"changed transport bytes", func(d *planDocument) {
			for path, snapshot := range d.PackageSnapshots {
				snapshot.Inputs.Repositories[0].URI += "/changed"
				d.PackageSnapshots[path] = snapshot
				break
			}
		}, e.CodePipelineDrift},
		{"wrong path", func(d *planDocument) {
			for path, snapshot := range d.PackageSnapshots {
				delete(d.PackageSnapshots, path)
				d.PackageSnapshots["store/package-inputs.json"] = snapshot
				break
			}
		}, e.CodePipelineDrift},
		{"duplicate target", func(d *planDocument) {
			for _, snapshot := range d.PackageSnapshots {
				d.PackageSnapshots["store/duplicate.json"] = snapshot
				break
			}
		}, e.CodePipelineDrift},
		{"unused targets", func(d *planDocument) { d.Builds = nil }, e.CodePipelineDrift},
		{"inline and reference", func(d *planDocument) {
			for i := range d.Builds {
				for _, component := range d.Builds[i].CatalogSnapshot.Components {
					if component.Mkosi != nil {
						for _, snapshot := range d.PackageSnapshots {
							component.Mkosi.Roots[0].Inputs = snapshot.Inputs
							return
						}
					}
				}
			}
		}, e.CodePipelineDrift},
		{"empty reference", func(d *planDocument) {
			for i := range d.Builds {
				for _, component := range d.Builds[i].CatalogSnapshot.Components {
					if component.Mkosi != nil {
						component.Mkosi.Roots[0].InputsDigest = ""
						return
					}
				}
			}
		}, e.CodePipelineDrift},
		{"bounded expansion", func(d *planDocument) {
			for _, snapshot := range d.PackageSnapshots {
				input, err := snapshot.Inputs.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				graph := &ir.Graph{}
				for i := 0; i <= maxPlanPackageExpansion/len(input); i++ {
					graph.Nodes = append(graph.Nodes, ir.Node{Mkosi: &mkosi.Spec{Roots: []mkosi.Root{{InputsDigest: snapshot.Digest}}}})
				}
				d.Builds = []BuildPlan{{Graph: graph}}
				return
			}
		}, e.CodePipelineDrift},
		{"unknown schema", func(d *planDocument) { d.SchemaVersion = "nvkr.dev/run-plan/v99" }, e.CodeInvalidCatalog},
		{"legacy schema with references", func(d *planDocument) { d.SchemaVersion = PlanSchemaVersion }, e.CodePipelineDrift},
		{"malformed package document", func(d *planDocument) {
			for path, snapshot := range d.PackageSnapshots {
				snapshot.Inputs.SchemaVersion = "unknown"
				d.PackageSnapshots[path] = snapshot
				break
			}
		}, e.CodeInvalidCatalog},
		{"unsupported platform", func(d *planDocument) {
			for path, snapshot := range d.PackageSnapshots {
				snapshot.Inputs.Architecture = "riscv64"
				d.PackageSnapshots[path] = snapshot
				break
			}
		}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := planDocument{planValue: new(planValue)}
			if err := canonical.UnmarshalExact(raw, &document); err != nil {
				t.Fatal(err)
			}
			tc.change(&document)
			bad, err := canonical.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			var restored Plan
			assertFactoryErrorCode(t, restored.UnmarshalJSON(bad), tc.code)
			if !reflect.DeepEqual(restored, Plan{}) {
				t.Fatal("rejected document returned a partial Plan")
			}
		})
	}
}

func TestGeneratedPlanAdmitsReaderByteBound(t *testing.T) {
	plan := runtimeConsumersPlanFixture(t, []string{"first", "second"})
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	originalVersion := plan.GeneratorVersion
	for _, tc := range []struct {
		name  string
		extra int
	}{{"exact limit", 0}, {"one byte above", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			plan.GeneratorVersion = originalVersion + strings.Repeat("v", int(MaxPlanBytes)-len(raw)+tc.extra)
			files, _, err := renderStaticInputs(t.Context(), plan, generationLayout{planPath: "plan.json"})
			if tc.extra != 0 {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if files != nil {
					t.Fatal("oversized generation returned partial files")
				}
				oversized, err := canonical.Marshal(plan)
				if err != nil {
					t.Fatal(err)
				}
				name := filepath.Join(t.TempDir(), "plan.json")
				if err := os.WriteFile(name, oversized, 0o600); err != nil {
					t.Fatal(err)
				}
				loaded, err := LoadPlanContext(t.Context(), name)
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				if loaded != nil {
					t.Fatal("oversized reader returned a Plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(files["plan.json"])) != MaxPlanBytes {
				t.Fatal("wrong exact-bound fixture")
			}
			var restored Plan
			if err := canonical.UnmarshalExact(files["plan.json"], &restored); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMkosiRejectsUnresolvedPackageReference(t *testing.T) {
	plan := runtimeConsumersPlanFixture(t, []string{"first", "second"})
	for _, build := range plan.Builds {
		for _, component := range build.ResolvedComponents {
			if component.Mkosi == nil {
				continue
			}
			spec := mkosi.Clone(component.Mkosi)
			spec.Roots[0].Inputs = material.PackageInputs{}
			spec.Roots[0].InputsDigest = "sha256:" + strings.Repeat("a", 64)
			assertFactoryErrorCode(t, spec.Validate(build.Platform, nil), e.CodeInvalidCatalog)
			return
		}
	}
	t.Fatal("fixture has no runtime recipe")
}

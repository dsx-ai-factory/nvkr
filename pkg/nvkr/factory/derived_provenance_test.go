// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

func TestDerivedInventoryProvenanceBindsObservedMaterials(t *testing.T) {
	definition, environment, stack := derivedTestInputs(t)
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	required := plan.LifecycleOutputs[1]
	output := delivery.PublicationOutput{Output: delivery.Output{Name: required.Name, Kind: required.Kind, BuildDigest: required.BuildDigest, Derived: required.Derived}, DerivedMaterials: []derivedoutput.Material{{Name: "chart", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	dependencies, err := derivedMaterialDependencies(output.Name, output.Derived, output.DerivedMaterials)
	if err != nil {
		t.Fatal(err)
	}
	run := hostedPredicateRun{builderID: "https://github.com/example/nvkr/.github/workflows/nvkr-factory.yml@refs/heads/main", invocationID: "https://github.com/example/nvkr/actions/runs/42/attempts/2", startedOn: time.Unix(100, 0).UTC(), finishedOn: time.Unix(101, 0).UTC()}
	raw, err := productionPredicateForRun(plan, environment.Signing, run, dependencies...)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProductionPredicate(raw, plan, run.builderID, run.invocationID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*provenance.Predicate)
		code   e.Code
	}{
		{"exact", nil, ""},
		{"changed digest", func(p *provenance.Predicate) {
			for i := range p.BuildDefinition.ResolvedDependencies {
				d := &p.BuildDefinition.ResolvedDependencies[i]
				if strings.HasPrefix(d.URI, derivedDependencyPrefix) {
					d.Digest["sha256"] = strings.Repeat("b", 64)
				}
			}
		}, e.CodeVerifyFailed},
		{"missing dependency", func(p *provenance.Predicate) {
			var kept []provenance.Dependency
			for _, d := range p.BuildDefinition.ResolvedDependencies {
				if !strings.HasPrefix(d.URI, derivedDependencyPrefix) {
					kept = append(kept, d)
				}
			}
			p.BuildDefinition.ResolvedDependencies = kept
		}, e.CodeVerifyFailed},
		{"substituted location", func(p *provenance.Predicate) {
			for i := range p.BuildDefinition.ResolvedDependencies {
				d := &p.BuildDefinition.ResolvedDependencies[i]
				if strings.HasPrefix(d.URI, derivedDependencyPrefix) {
					d.URI += "-other"
				}
			}
		}, e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var predicate provenance.Predicate
			if err := canonical.UnmarshalExact(raw, &predicate); err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(&predicate)
			}
			candidate, err := canonical.Marshal(predicate)
			if err != nil {
				t.Fatal(err)
			}
			err = validateDerivedPredicateMaterials(candidate, plan, output)
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDerivedInventoryProvenancePrefixNamesAndRootCompleteness(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	binding := &derivedoutput.Binding{Encoding: derivedoutput.InventoryV1, RepositoryPrefix: "registry.example.com/nvke", Inputs: []derivedoutput.Input{{Name: "spire", Kind: "helm-oci", BuildDigest: digest}, {Name: "spire-crds", Kind: "helm-oci", BuildDigest: digest}}}
	output := delivery.PublicationOutput{Output: delivery.Output{Name: "inventory", Derived: binding}, DerivedMaterials: []derivedoutput.Material{{Name: "spire", Digest: digest}, {Name: "spire-crds", Digest: digest}}}
	plan := &Plan{LifecycleOutputs: []LifecycleOutputPlan{{Name: "inventory", Derived: binding}}}
	dependencies, err := derivedMaterialDependencies(output.Name, binding, output.DerivedMaterials)
	if err != nil {
		t.Fatal(err)
	}
	if dependencies[0].URI >= dependencies[1].URI {
		t.Fatal("dependencies not in canonical URI order")
	}
	for _, tc := range []struct {
		name         string
		dependencies []provenance.Dependency
		code         e.Code
	}{
		{"complete", dependencies, ""},
		{"all missing", nil, e.CodeVerifyFailed},
		{"one missing", dependencies[:1], e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var predicate provenance.Predicate
			predicate.BuildDefinition.ResolvedDependencies = tc.dependencies
			raw, err := canonical.Marshal(predicate)
			if err != nil {
				t.Fatal(err)
			}
			for _, err := range []error{validateDerivedPredicateMaterials(raw, plan, output), ValidateQualificationRootMaterials(raw, plan, delivery.PublicationDescription{Outputs: []delivery.PublicationOutput{output}})} {
				if tc.code != "" {
					assertFactoryErrorCode(t, err, tc.code)
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestOrdinaryPublicationPredicateWithDerivedSibling(t *testing.T) {
	plan, artifacts := derivedInventoryWorkspace(t, func(environment *authored.Environment) { environment.Publication.Mode = "per-output-v1" })
	if err := executeSBOM(t.Context(), plan, planNode(plan, "chart/sbom"), artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := executePackage(t.Context(), plan, planNode(plan, "package/publication-chart"), artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.Inspect(t.Context(), artifacts["package/publication-chart"].Path)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := publicationDerivedDependencies(inspection)
	if err != nil {
		t.Fatal(err)
	}
	if len(dependencies) != 0 {
		t.Fatal("ordinary publication acquired sibling derived materials")
	}
	description, err := delivery.InspectMetadataDescription(t.Context(), artifacts["package/publication-chart"].Path, plan.Targets[outputPublicationRoute("chart")].Ref+"@"+inspection.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	outputs := description.Outputs
	if len(outputs) != 1 || outputs[0].Name != "chart" || outputs[0].Derived != nil {
		t.Fatal("fixture is not an ordinary output-local publication")
	}
	run := hostedPredicateRun{builderID: "https://github.com/example/nvkr/.github/workflows/nvkr-factory.yml@refs/heads/main", invocationID: "https://github.com/example/nvkr/actions/runs/42/attempts/2", startedOn: time.Unix(100, 0).UTC(), finishedOn: time.Unix(101, 0).UTC()}
	raw, err := productionPredicateForRun(plan, plan.Environment.Signing, run, dependencies...)
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		ValidateProductionPredicate(raw, plan, run.builderID, run.invocationID),
		validatePublicationDerivedMaterials(raw, plan, inspection),
		validateDerivedPredicateMaterials(raw, plan, outputs[0]),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	required := plan.LifecycleOutputs[1]
	if required.Derived == nil {
		t.Fatal("fixture lost derived sibling")
	}
	derived := delivery.PublicationOutput{Output: delivery.Output{Name: required.Name, Kind: required.Kind, BuildDigest: required.BuildDigest, Derived: required.Derived}, DerivedMaterials: []derivedoutput.Material{{Name: "chart", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	for _, tc := range []struct {
		name     string
		validate func() error
	}{
		{"derived output", func() error { return validateDerivedPredicateMaterials(raw, plan, derived) }},
		{"aggregate root", func() error {
			return ValidateQualificationRootMaterials(raw, plan, delivery.PublicationDescription{Outputs: []delivery.PublicationOutput{outputs[0], derived}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { assertFactoryErrorCode(t, tc.validate(), e.CodeVerifyFailed) })
	}
}

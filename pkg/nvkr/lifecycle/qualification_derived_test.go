// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
)

func TestQualificationDerivedInventoryReprojectsSignedMembership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*delivery.PublicationDescription)
		code   e.Code
	}{
		{"exact", nil, ""},
		{"changed chart digest", func(p *delivery.PublicationDescription) {
			p.Outputs[0].ChartArchiveDigest = "sha256:" + strings.Repeat("d", 64)
		}, e.CodeVerifyFailed},
		{"relocated sibling", func(p *delivery.PublicationDescription) {
			p.Outputs[0].Reference = "registry.example.com/other/chart@" + p.Outputs[0].Digest
		}, e.CodeVerifyFailed},
		{"missing sibling", func(p *delivery.PublicationDescription) { p.Outputs = p.Outputs[1:] }, e.CodeReleaseIncomplete},
		{"removed declaration", func(p *delivery.PublicationDescription) { p.Outputs[1].Derived = nil }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := "sha256:" + strings.Repeat("a", 64)
			manifest := "sha256:" + strings.Repeat("b", 64)
			binding := &derivedoutput.Binding{Encoding: derivedoutput.InventoryV1, RepositoryPrefix: "registry.example.com/nvke", Inputs: []derivedoutput.Input{{Name: "chart", Kind: "helm-oci", BuildDigest: build}}}
			digest, err := binding.BuildDigest()
			if err != nil {
				t.Fatal(err)
			}
			publication := delivery.PublicationDescription{Outputs: []delivery.PublicationOutput{
				{Output: delivery.Output{Name: "chart", Kind: "helm-oci", BuildDigest: build, Digest: manifest, Reference: "registry.example.com/nvke/chart@" + manifest, ChartVersion: "1.2.3"}, ChartArchiveDigest: "sha256:" + strings.Repeat("c", 64)},
				{Output: delivery.Output{Name: "inventory", Kind: "bundle-oci", BuildDigest: digest, Digest: manifest, Derived: binding.Clone()}, DerivedMaterials: []derivedoutput.Material{{Name: "chart", Digest: manifest}}},
			}}
			description, err := releaseview.Describe(publication, releaseview.Options{})
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := releaseview.ArtifactInventory(description, "chart")
			if err != nil {
				t.Fatal(err)
			}
			archive, err := derivedoutput.InventoryArchive(t.Context(), inventory)
			if err != nil {
				t.Fatal(err)
			}
			plan := &factory.Plan{LifecycleOutputs: []factory.LifecycleOutputPlan{{Name: "inventory", Kind: "bundle-oci", BuildDigest: digest, Derived: binding}}}
			if tc.change != nil {
				tc.change(&publication)
			}
			output := publication.Outputs[len(publication.Outputs)-1]
			projected, err := projectQualificationDerived(plan, publication, output)
			if err == nil {
				err = derivedoutput.ValidateInventoryArchive(t.Context(), archive, projected)
			}
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
}

func TestQualificationDerivedInventorySmokeReport(t *testing.T) {
	_, _, policy, report := smokeAggregateFixture(t)
	binding := &derivedoutput.Binding{Encoding: derivedoutput.InventoryV1, RepositoryPrefix: "registry.example.com/nvke", Inputs: []derivedoutput.Input{{Name: "chart", Kind: "helm-oci", BuildDigest: nativeSmokeTestDigest("a")}}}
	digest, err := binding.BuildDigest()
	if err != nil {
		t.Fatal(err)
	}
	output := ArtifactCheckOutput{Name: "inventory", Kind: OutputBundle, Reference: "registry.example.com/nvke/inventory@" + nativeSmokeTestDigest("b"), Subject: nativeSmokeTestDigest("b"), BuildDigest: digest, Derived: binding, Checks: []ArtifactCheckResult{{Name: "derived-inventory", Status: "pass"}}, Overall: "pass"}
	report.ArtifactChecks.Outputs = append(report.ArtifactChecks.Outputs, output)
	sort.Slice(report.ArtifactChecks.Outputs, func(i, j int) bool {
		return report.ArtifactChecks.Outputs[i].Name < report.ArtifactChecks.Outputs[j].Name
	})
	policy.Smoke.NonToolOutputs = append(policy.Smoke.NonToolOutputs, QualificationNonToolOutput{Output: "inventory", Kind: OutputBundle, Reason: "derived-inventory", Components: []string{}})
	policyDigest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	report.PolicyDigest = policyDigest
	description := ToolSmokeDescription{ArtifactChecks: report.ArtifactChecks, Invocation: report.Invocation, Policy: policy, PolicyDigest: policyDigest, Workers: []NativeToolSmokeReport{report}}
	if err := description.Validate(); err != nil {
		t.Fatal(err)
	}
	for i := range report.ArtifactChecks.Outputs {
		if report.ArtifactChecks.Outputs[i].Name == "inventory" {
			report.ArtifactChecks.Outputs[i].Derived = nil
		}
	}
	assertCode(t, report.Validate(), e.CodeVerifyFailed)
}

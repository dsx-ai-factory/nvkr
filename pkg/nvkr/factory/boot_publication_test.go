// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBootPublicationCannotEnterLegacyGraph(t *testing.T) {
	for _, tc := range []struct {
		name    string
		unknown bool
		code    e.Code
	}{
		{name: "missing verifier tool", code: e.CodeTrustPolicyMissing},
		{name: "unknown selected output", unknown: true, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind,
				Metadata: definition.Metadata{Name: "selected"}, Catalog: definition.Catalog{Base: "not-read"},
				Outputs: []definition.DefinitionOutput{
					{Name: "imager", Format: definition.OutputExecutable, Platform: "linux/amd64", Components: []string{"imager.efi"}},
					{Name: "node", Format: definition.OutputDiskImage, Platform: "linux/amd64", Components: []string{"node"}},
				},
			}
			environment := definition.Environment{APIVersion: definition.EnvironmentBootAPIVersion, Kind: definition.EnvironmentKind,
				Metadata: definition.Metadata{Name: "selected"}, UtilityContainer: "golang@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64",
				Signing: &definition.Signing{Mode: "cosign", Identity: "^https://github.com/example/nvkr/.*$", Issuer: "https://token.actions.githubusercontent.com"},
				Publication: &definition.Publication{Mode: "per-output-v1", RepositoryPrefix: "ghcr.io/example/nvkr", CredentialRef: "github:REGISTRY",
					BootSelection: &definition.BootSelection{SchemaVersion: definition.BootSelectionSchemaVersion,
						Disks:   []definition.BootDisk{{Output: "node", Platform: "linux/amd64"}},
						Imagers: []definition.BootImager{{Output: "imager", Member: "imager.efi", Platform: "linux/amd64", Profile: "synthetic-v1"}}},
					ObjectStores: []definition.ObjectStoreDestination{{Name: "boot", Mode: definition.ObjectStoreBootMode, Provider: "s3",
						S3:      &definition.S3ObjectStoreDestination{Region: "us-east-1", Bucket: "synthetic-boot", Prefix: "boot", ExpectedBucketOwner: "123456789012", RoleRef: "github:S3_ROLE"},
						Serving: &definition.BootServing{Profile: definition.BootServingProfile, Origin: "https://boot.example.test", Prefix: "/", ReaderMode: "anonymous"}}},
				},
			}
			if tc.unknown {
				environment.Publication.BootSelection.Disks[0].Output = "sibling"
			}
			calls := 0
			plan, err := compileDocumentsWithLoader(t.Context(), "definition.yaml", input, "environment.yaml", environment, "test",
				func(context.Context, definition.Catalog) (*catalog.Stack, error) { calls++; return nil, nil })
			assertFactoryErrorCode(t, err, tc.code)
			if plan != nil || calls != 0 {
				t.Fatal("selective inputs reached catalog resolution or legacy compilation")
			}
		})
	}
}

func bootPlanFixture(t *testing.T) (*Plan, *Plan) {
	t.Helper()
	base := arcTestPlan(t, "linux/amd64")
	input, environment := base.Definition.Clone(), base.Environment.Clone()
	input.Outputs = append(input.Outputs, definition.DefinitionOutput{Name: "imager", Format: definition.OutputExecutable, Platform: "linux/amd64", Components: []string{"agent"}})
	slices.SortFunc(input.Outputs, func(a, b definition.DefinitionOutput) int { return strings.Compare(a.Name, b.Name) })
	environment.Publication.Mode = "per-output-v1"
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Compile(t.Context(), input, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	environment.APIVersion = definition.EnvironmentBootAPIVersion
	environment.Publication.BootSelection = &definition.BootSelection{SchemaVersion: definition.BootSelectionSchemaVersion,
		Disks:   []definition.BootDisk{{Output: base.Definition.Outputs[0].Name, Platform: "linux/amd64"}},
		Imagers: []definition.BootImager{{Output: "imager", Member: "agent", Platform: "linux/amd64", Profile: "synthetic-v1"}}}
	environment.Publication.ObjectStores = []definition.ObjectStoreDestination{{Name: "boot", Mode: definition.ObjectStoreBootMode, Provider: "s3",
		S3:      &definition.S3ObjectStoreDestination{Region: "us-east-1", Bucket: "synthetic-boot", Prefix: "boot", ExpectedBucketOwner: "123456789012", RoleRef: "github:S3_ROLE"},
		Serving: &definition.BootServing{Profile: definition.BootServingProfile, Origin: "https://boot.example.test", Prefix: "/", ReaderMode: "anonymous"}}}
	plan, err := Compile(t.Context(), input, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	return plan, plain
}

func TestBootPlanOrdersCopyBeforeAggregateAndFinalProof(t *testing.T) {
	plan, plain := bootPlanFixture(t)
	if plan.SchemaVersion != BootPlanSchemaVersion || plain.BootPublication != nil || len(plan.Builds) != len(plain.Builds) {
		t.Fatal("incorrect selective schema or output expansion")
	}
	for i, build := range plan.Builds {
		if build.BuildDigest != plain.Builds[i].BuildDigest {
			t.Fatal("transport selection changed source Build identity")
		}
	}
	nodes, _ := planIndex(plan)
	prepare, assembly, final := nodes["prepare-boot/boot"], nodes["package/release"], nodes["publish-boot/boot"]
	if prepare.ID == "" || final.ID == "" || nodes["publish-object/boot"].ID != "" {
		t.Fatal("selective publication reused whole-mirror node")
	}
	if !slices.Contains(assembly.Inputs, prepare.Outputs[0]) || !slices.Contains(final.Inputs, "verification/release") || !slices.Contains(final.Inputs, prepare.Outputs[0]) {
		t.Fatal("copy, assembly and final publication are not ordered")
	}
	for _, input := range prepare.Inputs {
		if strings.HasSuffix(input, "/release") {
			t.Fatal("preparation depends on eventual aggregate")
		}
	}
	for _, id := range []string{prepare.ID, final.ID} {
		request, isFinal, err := BootPhaseForNode(plan, id)
		if err != nil || isFinal != (id == final.ID) || request.Validate() != nil {
			t.Fatalf("invalid phase %s: %v", id, err)
		}
		request.Selection.Disks[0].Output = "mutated"
		request.Destination.Serving.Origin = "https://foreign.test"
		if err := plan.Validate(); err != nil {
			t.Fatal("phase projection aliases Plan", err)
		}
		_, _, err = ObjectPublicationForNode(plan, id)
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	}
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.SchemaVersion = PlanSchemaVersion },
		func(p *Plan) { p.BootPublication.Destinations[0].FinalReadBytes++ },
		func(p *Plan) { p.BootPublication.Destinations[0].ControlObjects-- },
		func(p *Plan) {
			for i := range p.Graph.Nodes {
				if p.Graph.Nodes[i].ID == assembly.ID {
					p.Graph.Nodes[i].Inputs = slices.DeleteFunc(p.Graph.Nodes[i].Inputs, func(s string) bool { return s == prepare.Outputs[0] })
				}
			}
		},
	} {
		copy, _ := bootPlanFixture(t)
		mutate(copy)
		if err := copy.Validate(); err == nil {
			t.Fatal("mutated selective Plan was admitted")
		}
	}
	if reflect.DeepEqual(plan.EnvironmentDigest, plain.EnvironmentDigest) {
		t.Fatal("selection did not affect Environment identity")
	}
	// Intermediate implementation guard: no generated workflow may start a
	// partial selective Run before the lifecycle publisher is fully integrated.
	bundle, err := RenderGitHub(t.Context(), plan)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if bundle != nil {
		t.Fatal("unfinished route emitted a workflow")
	}
}

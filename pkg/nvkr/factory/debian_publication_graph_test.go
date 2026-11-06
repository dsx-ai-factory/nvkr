// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func debianObjectPublicationInputs(t *testing.T, scan bool) (definition.Definition, definition.Environment, *catalog.Stack) {
	t.Helper()
	d, environment, raw := rawDebianInputs(t)
	reference := referenceTestPlan(t)
	environment.APIVersion = definition.EnvironmentObjectStoresAPIVersion
	environment.Signing = cosignSigningProfile()
	cloned := reference.Environment.Clone()
	environment.Publication = cloned.Publication
	environment.Publication.ObjectStores = []definition.ObjectStoreDestination{{Name: "customer", Provider: "s3", S3: &definition.S3ObjectStoreDestination{Region: "us-east-1", Bucket: "synthetic-artifacts", Prefix: "products/nvke", ExpectedBucketOwner: "123456789012", RoleRef: "github:S3_PUBLISH_ROLE"}}}
	d.CosignTool = reference.Definition.CosignTool
	cat := *raw.Catalog
	cat.Tools = maps.Clone(reference.CatalogSnapshot.Tools)
	cat.Actions = reference.CatalogSnapshot.Actions
	if scan {
		environment.ToolchainAccess.PublicRoots = append(environment.ToolchainAccess.PublicRoots, "example.test/inspection")
		slices.Sort(environment.ToolchainAccess.PublicRoots)
		d.SBOMTool = "scanner"
		disk := diskPublicationPlanFixture(t, "linux/amd64")
		environment.DiskInspectionProfiles = disk.Environment.DiskInspectionProfiles
		profile := environment.DiskInspectionProfiles[environment.UtilityPlatform]
		cat.Tools[d.SBOMTool] = catalog.ToolPin{Format: ToolFormatRaw, URL: "https://example.invalid/scanner", Platform: environment.UtilityPlatform, ExecutablePath: "syft", ArchiveDigest: profile.ScannerDigest, ExecutableDigest: profile.ScannerDigest}
	}
	stack, err := catalog.ApplyStack(&cat)
	if err != nil {
		t.Fatal(err)
	}
	return d, environment, stack
}

func TestRawDebianNormalObjectPublicationGraph(t *testing.T) {
	for _, scan := range []bool{false, true} {
		t.Run(map[bool]string{false: "composition", true: "payload inspection"}[scan], func(t *testing.T) {
			d, environment, stack := debianObjectPublicationInputs(t, scan)
			plan, err := Compile(t.Context(), d, environment, stack, "v0.0.0 (test)")
			if err != nil {
				t.Fatal(err)
			}
			plan.DefinitionPath, plan.EnvironmentPath = "definition.yaml", "environment.yaml"
			if plan.Definition.APIVersion != definition.DefinitionAPIVersion || plan.LifecycleOutputs[0].ArtifactID != "artifact/agent/agent" {
				t.Fatal("publication relabeled the original v5 Debian subject")
			}
			nodes, _ := planIndex(plan)
			carrier := nodes["package/agent"]
			if carrier.PackageKind != PackageDebian || !slices.Contains(carrier.Inputs, "artifact/agent/agent") || !slices.Contains(carrier.Inputs, "result/agent/agent") {
				t.Fatal("Debian carrier omitted its original producer/result join")
			}
			if scan {
				profile := environment.DiskInspectionProfiles[environment.UtilityPlatform]
				if nodes["agent/sbom"].Container != profile.Image || nodes["agent/sbom"].Platform != environment.UtilityPlatform || !slices.Contains(nodes["agent/sbom"].Inputs, "tool/"+d.SBOMTool) {
					t.Fatal("Debian scanner bypassed its exact native tooling profile")
				}
			}
			if _, err := RenderGitHub(t.Context(), plan); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRawDebianPublicationInspectionAdmitsBeforeWork(t *testing.T) {
	for _, fault := range []string{"missing profile", "scanner mismatch"} {
		t.Run(fault, func(t *testing.T) {
			d, environment, stack := debianObjectPublicationInputs(t, true)
			if fault == "missing profile" {
				environment.DiskInspectionProfiles = nil
				d.Catalog.Base = "missing-catalog"
				_, err := CompileDocuments(t.Context(), "definition.yaml", d, "environment.yaml", environment, "test")
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				return
			}
			profile := environment.DiskInspectionProfiles[environment.UtilityPlatform]
			profile.ScannerDigest = "sha256:" + strings.Repeat("8", 64)
			environment.DiskInspectionProfiles[environment.UtilityPlatform] = profile
			_, err := Compile(t.Context(), d, environment, stack, "test")
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
		})
	}
}

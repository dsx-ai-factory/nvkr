// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func TestDiskEvidencePreflightAdmitsOnlySupportedCaptureAndMaterials(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, condition := range []string{"valid", "no acquisition carrier", "unsupported catalog output", "nested disk material", "wrong build"} {
			t.Run(platform+"/"+condition, func(t *testing.T) {
				initial, _ := diskMaterialTraceFixtureForPlatform(t, false, false, platform)
				base := initial.CatalogSnapshot
				root := base.Components["image"]
				packer := *root.Packer
				packer.AcquisitionArchive = "test-acquisitions.tar"
				root.Packer = &packer
				base.Components["image"] = root
				if condition == "nested disk material" {
					// A referenced disk producer is not a second output root.
					// Compile a valid supplier instead of bypassing Plan validation.
					material := root
					material.Materials = nil
					supplierPacker := packer
					supplierPacker.MaterialManifest = nil
					material.Packer = &supplierPacker
					base.Components["agent"] = material
				}
				stack, err := catalog.ApplyStack(base)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := Compile(t.Context(), initial.Definition, initial.Environment, stack, "test")
				if err != nil {
					t.Fatal(err)
				}
				expected := plan.LifecycleOutputs[0]
				want := e.CodeEngineCapability
				if condition == "no acquisition carrier" || condition == "unsupported catalog output" {
					base := plan.CatalogSnapshot
					root := base.Components["image"]
					packer := *root.Packer
					if condition == "no acquisition carrier" {
						packer.AcquisitionArchive = ""
					} else {
						material := base.Components["agent"]
						material.OutputType = "executable"
						base.Components["agent"] = material
					}
					root.Packer = &packer
					base.Components["image"] = root
					stack, err := catalog.ApplyStack(base)
					if condition == "unsupported catalog output" {
						// Unknown output kinds are rejected by the catalog owner,
						// before compilation or disk evidence can perform work.
						assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
						if stack != nil {
							t.Fatal("invalid output kind produced an executable catalog")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					plan, err = Compile(t.Context(), plan.Definition, plan.Environment, stack, "test")
					if err != nil {
						t.Fatal(err)
					}
					expected = plan.LifecycleOutputs[0]
				}
				if condition == "wrong build" {
					expected.BuildDigest = "sha256:" + strings.Repeat("9", 64)
					want = e.CodePipelineDrift
				}
				build, err := admitDiskEvidenceBuild(plan, expected)
				if condition == "valid" {
					if err != nil || build.Platform != platform {
						t.Fatalf("valid frozen build failed: %v", err)
					}
					return
				}
				assertFactoryErrorCode(t, err, want)
				if condition == "nested disk material" && !strings.Contains(err.Error(), "disk material kind \"disk-image\" has no admitted content scanner") {
					t.Fatalf("supplier refusal came from a later missing capability: %v", err)
				}
				// An inert owner/runtime prove the refusal precedes permit and
				// snapshot admission, not an unrelated missing-runtime guard.
				owner := &LocalRunResult{diskClosed: true}
				receipt, err := owner.ReconcileDiskEvidence(t.Context(), plan, expected, &sbom.DiskInspectionRuntime{})
				assertFactoryErrorCode(t, err, want)
				if receipt != nil {
					t.Fatal("rejected preflight issued a receipt")
				}
			})
		}
	}
}

func TestDiskEvidenceRejectsHostileCompletePlanBeforeRuntimeWork(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, condition := range []string{"unsupported platform", "unknown producer kind"} {
			t.Run(platform+"/"+condition, func(t *testing.T) {
				plan, _ := diskMaterialTraceFixtureWithCapture(t, false, false, platform, "none", "acquisitions.tar")
				expected := plan.LifecycleOutputs[0]
				// Caller-modified records still go through the full Plan owner;
				// these do not mint an otherwise-invalid executable authority.
				if condition == "unsupported platform" {
					plan.Builds[0].Platform = "linux/riscv64"
					plan.Builds[0].Graph.Platform = "linux/riscv64"
				} else {
					for index := range plan.Builds[0].Graph.Nodes {
						if plan.Builds[0].Graph.Nodes[index].Component == "agent" && plan.Builds[0].Graph.Nodes[index].OutputType != "" {
							plan.Builds[0].Graph.Nodes[index].OutputType = "executable"
						}
					}
				}
				_, err := admitDiskEvidenceBuild(plan, expected)
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
				owner := &LocalRunResult{diskClosed: true}
				receipt, err := owner.ReconcileDiskEvidence(t.Context(), plan, expected, &sbom.DiskInspectionRuntime{})
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
				if receipt != nil {
					t.Fatal("hostile Plan issued evidence before its complete validation")
				}
			})
		}
	}
}

func TestProductionDiskEvidenceRequiresPrivateScratchAtConsumption(t *testing.T) {
	for _, scratch := range []string{"private-volume", "memory", ""} {
		t.Run(scratch, func(t *testing.T) {
			err := requireProductionDiskScratch(definition.DiskInspectionProfile{ScratchStorage: scratch})
			if scratch == "private-volume" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			}
		})
	}
}

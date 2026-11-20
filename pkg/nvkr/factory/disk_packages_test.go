// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func TestDiskPackageModeCompileAndIdentity(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			before := arcTestPlan(t, platform)
			after := recompileGitHubStabilityPlan(t, before, func(in *githubStabilityInputs) { in.environment.DiskEvidence = sbom.DiskInstalledPackages })
			if after.Builds[0].BuildDigest != before.Builds[0].BuildDigest {
				t.Fatal("observation policy changed logical Build")
			}
			old, _ := before.IdentityDigest()
			current, _ := after.IdentityDigest()
			if old == current {
				t.Fatal("inspection policy did not change Plan")
			}
			if _, err := RenderGitHub(t.Context(), after); err != nil {
				t.Fatal(err)
			}
			start := time.Unix(1, 0)
			deadline, err := diskSelectedEvidenceDeadline(after, after.Builds[0].Name, start, 600, 8)
			if err != nil || !deadline.Equal(start.Add(10*time.Minute)) {
				t.Fatalf("package-only budget %v: %v", deadline, err)
			}
			for _, mode := range []string{sbom.DiskInstalledPackages, sbom.DiskBuildProvenance} {
				t.Run("luks2/"+mode, func(t *testing.T) {
					stack, err := catalog.ApplyStack(after.CatalogSnapshot)
					if err != nil {
						t.Fatal(err)
					}
					root := stack.Catalog.Components["image"]
					root.Disk.Encryption = "luks2"
					root.Packer.Variables = map[string]string{"luks_passphrase": "changeit"}
					stack.Catalog.Components["image"] = root
					stack, err = catalog.ApplyStack(stack.Catalog)
					if err != nil {
						t.Fatal(err)
					}
					environment := after.Environment.Clone()
					environment.DiskEvidenceOverrides = map[string]string{after.Builds[0].Name: mode}
					compiled, err := Compile(t.Context(), after.Definition, environment, stack, "dev (unknown)")
					if mode == sbom.DiskInstalledPackages {
						assertFactoryErrorCode(t, err, e.CodeEngineCapability)
						if compiled != nil {
							t.Fatal("refused FDE returned a Plan")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if githubDiskInspectionRequired(compiled, after.Builds[0].Name) {
						t.Fatal("FDE override still selected inspection")
					}
				})
			}
		})
	}
}

// Controlled documents exercise factory's live-owner join. They are not scanner
// execution evidence; sbom's tests separately exercise issued-inventory projection.
func TestDiskPackageReceiptBindsOwnerAndProfile(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, failure := range []string{"", "changed subject", "changed profile", "closed owner"} {
			t.Run(platform+failure, func(t *testing.T) {
				inspectionPlan := diskPublicationPlanFixture(t, platform)
				plan, owner := diskMaterialTraceFixtureWithPublicationMode(t, false, false, platform, "none", "acquisitions.tar", true, "", func(env *definition.Environment) {
					env.DiskEvidence = sbom.DiskInstalledPackages
					env.ExecutionClasses = inspectionPlan.Environment.Clone().ExecutionClasses
					env.DiskInspectionProfiles = inspectionPlan.Environment.Clone().DiskInspectionProfiles
					env.ToolchainAccess.PublicRoots = append(env.ToolchainAccess.PublicRoots, "example.test/inspection")
					slices.Sort(env.ToolchainAccess.PublicRoots)
				})
				expected := plan.LifecycleOutputs[0]
				_, profile, err := diskPublicationInspectionProfile(plan.Builds[0], plan.Environment)
				if err != nil {
					t.Fatal(err)
				}
				runtime := &sbom.DiskInspectionRuntime{}
				bindings := func(context.Context) (definition.DiskInspectionProfile, error) { return profile, nil }
				receipt, err := owner.reconcileDiskEvidence(t.Context(), plan, expected, runtime, bindings,
					func(_ context.Context, build BuildPlan, trace *diskMaterialTrace, _ *diskPayload, p definition.DiskInspectionProfile) (diskEvidenceScan, error) {
						hashes := map[string]string{}
						for _, member := range trace.description.Members {
							hashes[member.Component] = member.Digest
						}
						documents, err := diskBuildDocuments(build, hashes)
						if err != nil {
							return diskEvidenceScan{}, err
						}
						subject := trace.description.Subject
						bom, err := sbom.NewSubject(build.BuildDigest, subject.Component, subject.Digest)
						if err != nil {
							return diskEvidenceScan{}, err
						}
						bom.Metadata.Component.Type = "operating-system"
						bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "library", Name: "fixture-package", Version: "1", PURL: "pkg:deb/fixture-package@1?arch=all", Properties: []sbom.Property{{Name: "nvkr:packageArchitecture", Value: "all"}, {Name: "nvkr:packageFormat", Value: "deb"}}}}, sbom.DiskInstalledPackagesMethod, p.InspectorDigest)
						if err != nil {
							return diskEvidenceScan{}, err
						}
						bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "nvkr:diskEvidence", Value: sbom.DiskInstalledPackages}, sbom.Property{Name: "nvkr:diskInspection", Value: "installed-packages"}, sbom.Property{Name: "nvkr:diskInspectionRuntimeDigest", Value: p.InspectorDigest}, sbom.Property{Name: "nvkr:diskInspectorDigest", Value: p.InspectorDigest}, sbom.Property{Name: "nvkr:diskCodecDigest", Value: p.CodecDigest})
						slices.SortFunc(bom.Metadata.Props, func(a, b sbom.Property) int { return strings.Compare(a.Name, b.Name) })
						bom, err = sbom.ForSubject(bom, subject.Digest, "build-output", "")
						if err != nil {
							return diskEvidenceScan{}, err
						}
						bom, err = sbom.WithSourceSubject(bom, documents[subject.Component])
						if err != nil {
							return diskEvidenceScan{}, err
						}
						if err := validateReconciledDiskDocument(plan, expected.Name, subject.Component, subject.Digest, bom); err != nil {
							return diskEvidenceScan{}, err
						}
						raw := map[string][]byte{}
						for name, document := range documents {
							raw[name], err = document.Marshal()
							if err != nil {
								return diskEvidenceScan{}, err
							}
						}
						if failure == "changed subject" {
							subject.Digest = "sha256:" + strings.Repeat("f", 64)
						}
						return diskEvidenceScan{subject: subject.Digest, size: subject.Size, profile: p, bom: bom, documents: raw}, nil
					})
				if failure == "changed subject" {
					assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
					if receipt != nil {
						t.Fatal("failed scan issued receipt")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				want := e.Code("")
				if failure == "changed profile" {
					profile.CodecDigest = "sha256:" + strings.Repeat("f", 64)
					want = e.CodeVerifyFailed
				}
				if failure == "closed owner" {
					if err := owner.Close(); err != nil {
						t.Fatal(err)
					}
					want = e.CodeGateUnmet
				}
				_, document, err := owner.exportReconciledDisk(t.Context(), plan, expected, runtime, receipt, io.Discard, bindings)
				if want != "" {
					assertFactoryErrorCode(t, err, want)
					if document != nil {
						t.Fatal("refused export returned evidence")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := sbom.ValidateDiskInstalledPackages(document); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestDiskPackageScannerAdmissionPreservesStrictMatching(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, test := range []struct {
			name, mode string
			overrides  map[string]string
			want       e.Code
		}{
			{name: "strict", want: e.CodeVerifyFailed},
			{name: "packages", mode: sbom.DiskInstalledPackages},
			{name: "strict override", mode: sbom.DiskInstalledPackages, overrides: map[string]string{"node-image": ""}, want: e.CodeVerifyFailed},
			{name: "package override", overrides: map[string]string{"node-image": sbom.DiskInstalledPackages}},
		} {
			t.Run(platform+"/"+test.name, func(t *testing.T) {
				plan := diskPublicationPlanFixture(t, platform)
				plan.Environment.DiskEvidence = test.mode
				plan.Environment.DiskEvidenceOverrides = test.overrides
				plan.Definition.SBOMTool = "test-scanner"
				plan.CatalogSnapshot.Tools["test-scanner"] = catalog.ToolPin{Platform: platform, ExecutableDigest: "sha256:" + strings.Repeat("9", 64)}
				_, profile, err := diskPublicationInspectionProfile(plan.Builds[0], plan.Environment)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateSelectedDiskScanner(plan, platform, profile.ScannerDigest); factoryErrorCode(err) != test.want {
					t.Fatalf("scanner admission: got %v, want %s", err, test.want)
				}
			})
		}
	}
}

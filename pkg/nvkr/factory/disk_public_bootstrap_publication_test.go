// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func publicBootstrapPublicationPlan(t *testing.T, platform, encryption, mode string, variables map[string]string) *Plan {
	t.Helper()
	base := arcTestPlan(t, platform)
	component := base.CatalogSnapshot.Components["image"]
	component.Disk.Encryption = encryption
	component.Packer.Variables = variables
	base.CatalogSnapshot.Components["image"] = component
	environment := base.Environment.Clone()
	environment.DiskEvidence = mode
	stack, err := catalog.ApplyStack(base.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), base.Definition, environment, stack, "dev (unknown)")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = base.DefinitionPath, base.EnvironmentPath
	return plan
}

func TestPublicBootstrapPublicationAdmission(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, test := range []struct {
			name, mode string
			variables  map[string]string
			want       e.Code
		}{
			{"public", sbom.DiskBuildProvenance, map[string]string{"luks_passphrase": "changeit"}, ""},
			{"missing", sbom.DiskBuildProvenance, nil, e.CodeEngineCapability},
			{"private", sbom.DiskBuildProvenance, map[string]string{"luks_passphrase": "must-never-echo"}, e.CodeEngineCapability},
			{"strict", "", map[string]string{"luks_passphrase": "changeit"}, e.CodeEngineCapability},
		} {
			t.Run(platform+"/"+test.name, func(t *testing.T) {
				plan := publicBootstrapPublicationPlan(t, platform, "luks2", test.mode, test.variables)
				bundle, err := RenderGitHub(t.Context(), plan)
				if test.want == "" {
					if err != nil || bundle == nil {
						t.Fatalf("admitted publication: %v", err)
					}
					return
				}
				assertFactoryErrorCode(t, err, test.want)
				if bundle != nil || strings.Contains(err.Error(), "must-never-echo") {
					t.Fatal("rejected publication returned output or exposed input")
				}
				_, artifacts := planIndex(plan)
				wrapper := artifacts["package/"+plan.LifecycleOutputs[0].Name]
				wrapper.Path = filepath.Join(t.TempDir(), "must-not-exist")
				_, err = inspectFactoryDisk(t.Context(), plan, plan.LifecycleOutputs[0], wrapper)
				assertFactoryErrorCode(t, err, test.want)
				if _, err := os.Stat(wrapper.Path); !os.IsNotExist(err) {
					t.Fatalf("refused publication touched wrapper: %v", err)
				}
			})
		}
	}
}

func TestPublicBootstrapPublicationRejectsDuplicateAndQualified(t *testing.T) {
	for _, mutation := range []string{"duplicate", "qualified"} {
		t.Run(mutation, func(t *testing.T) {
			plan := publicBootstrapPublicationPlan(t, "linux/amd64", "luks2", sbom.DiskBuildProvenance, map[string]string{"luks_passphrase": "changeit"})
			for _, producer := range plan.Builds[0].Graph.Nodes {
				if producer.BuilderType != ir.BuilderVMImage {
					continue
				}
				if mutation == "duplicate" {
					producer.Packer.Variables = append(producer.Packer.Variables, producer.Packer.Variables[0])
				} else {
					plan.Qualification = "qualified"
				}
				assertFactoryErrorCode(t, admitDiskPublicationEncryption(plan, producer), e.CodeEngineCapability)
				return
			}
			t.Fatal("missing VM producer")
		})
	}
}

func TestPublishedDiskEncryptionBindsExactPlan(t *testing.T) {
	for _, expectedEncryption := range []string{"none", "luks2"} {
		for _, deliveredEncryption := range []string{"none", "luks2"} {
			t.Run(expectedEncryption+"/"+deliveredEncryption, func(t *testing.T) {
				plan := publicBootstrapPublicationPlan(t, "linux/amd64", expectedEncryption, sbom.DiskBuildProvenance, map[string]string{"luks_passphrase": "changeit"})
				expected := plan.LifecycleOutputs[0]
				_, artifacts := planIndex(plan)
				wrapper := artifacts["package/"+expected.Name]
				wrapper.Path = filepath.Join(t.TempDir(), "wrapper")
				payload := testQCOW2HeaderPayload()
				path := filepath.Join(t.TempDir(), "disk")
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				_, err := delivery.BuildDiskArtifact(t.Context(), wrapper.Path, delivery.DiskArtifactRequest{Path: path,
					Digest: pipeline.FileDigest(payload), Size: int64(len(payload)), Platform: "linux/amd64", Format: "qcow2",
					Encryption: deliveredEncryption, ArtifactType: wrapper.MediaType})
				if err != nil {
					t.Fatal(err)
				}
				observed, err := inspectFactoryDisk(t.Context(), plan, expected, wrapper)
				if expectedEncryption != deliveredEncryption {
					assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
					return
				}
				if err != nil || observed.Encryption != expectedEncryption {
					t.Fatalf("exact encryption binding lost: %+v, %v", observed, err)
				}
			})
		}
	}
}

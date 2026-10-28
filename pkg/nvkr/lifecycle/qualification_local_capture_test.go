// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// This fixture has genuine bundle/producer joins and a synthetic inventory.
// It proves transport binding, not authenticated production or a real scanner.
func writeLocalCaptureFixture(t *testing.T, workspace string, prepared *PreparedRun, payload []byte) Artifact {
	t.Helper()
	artifacts := map[string]factory.Artifact{}
	for _, artifact := range prepared.plan.Graph.Artifacts {
		var err error
		artifact.Path, err = lifecycleArtifactPath(artifact.Path, workspace)
		if err != nil {
			t.Fatal(err)
		}
		artifacts[artifact.ID] = artifact
	}
	source := artifacts["artifact/cli/kubectl"]
	if err := os.MkdirAll(filepath.Dir(source.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source.Path, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	record := fixtureProducerResult(t, prepared.plan, source, source.Path)
	writeCanonicalTestFile(t, artifacts["result/cli/kubectl"].Path, record)
	var slot requiredOutput
	for _, candidate := range prepared.run.Expected {
		if candidate.Name == "cli" {
			slot = candidate
		}
	}
	bundle := artifacts[slot.MemberArtifactID]
	built, err := delivery.BuildBinaryBundle(t.Context(), bundle.Path, delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "cli",
		Files: []delivery.BinaryFileInput{{Name: "kubectl", Path: source.Path, InstallPath: "/usr/local/bin/kubectl", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	bom, err := sbom.NewSubject(slot.BuildDigest, "kubectl", record.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	index := sbom.Index{SchemaVersion: sbom.IndexSchemaVersion, BuildDigest: slot.BuildDigest,
		Entries: []sbom.IndexEntry{{Artifact: "kubectl", ArtifactSHA: record.SHA256, SBOMDigest: digest}}}
	writeCanonicalTestFile(t, filepath.Join(artifacts["sbom/cli"].Path, "index.json"), index)
	writeCanonicalTestFile(t, filepath.Join(artifacts["sbom/cli"].Path, "kubectl.cdx.json"), bom)
	if slot.ArtifactID != slot.MemberArtifactID {
		descriptor, err := delivery.PayloadDescriptor(t.Context(), bundle.Path)
		if err != nil {
			t.Fatal(err)
		}
		request := testReleaseRequest(t,
			[]Artifact{{Name: bundle.Name, Kind: slot.Kind, Digest: descriptor.Digest, BuildDigest: slot.BuildDigest, Components: slot.Components}},
			[]delivery.ObjectInput{{Name: bundle.Name, Kind: bundle.Kind, Path: bundle.Path, Layout: true, ArtifactType: bundle.MediaType, SemanticDigest: built.SemanticDigest, BlobDigest: built.RootDigest}},
			map[string]string{"kubectl": record.SHA256})
		if _, err := delivery.BuildPublicationRoot(t.Context(), artifacts[slot.ArtifactID].Path, request); err != nil {
			t.Fatal(err)
		}
	}
	observed, err := observeOutput(t.Context(), slot, artifacts, prepared.plan, nil, workspace)
	if err != nil {
		t.Fatal(err)
	}
	return observed
}

func TestLocalQualificationCaptureMixedDiskAndExecutable(t *testing.T) {
	for _, mode := range []string{"complete", "ordinary payloads restored at assembly", "changed ordinary producer", "changed ordinary Run subject", "missing ordinary package", "missing ordinary inventory", "missing materials", "substituted materials"} {
		t.Run(mode, func(t *testing.T) {
			missingInventory := mode == "missing ordinary inventory"
			disk, _ := diskLifecycleFixtureWithProducer(t, "mixed", true)
			definition, environment := disk.plan.Definition.Clone(), disk.plan.Environment.Clone()
			definition.Outputs[0].Name = "disk"
			definition.Outputs = append([]DefinitionOutput{{Name: "cli", Platform: "linux/amd64", Format: OutputExecutable, Components: []string{"kubectl"}}}, definition.Outputs...)
			base := *disk.plan.CatalogSnapshot
			base.Components = maps.Clone(base.Components)
			tool := base.Components["nvke-node-non-fde"]
			tool.BuilderType, tool.OutputType, tool.ArtifactPath = catalog.BuilderOCIHermetic, "host-package", "out/kubectl"
			tool.Disk, tool.Packer, tool.BuildRequirements = nil, nil, nil
			tool.BuildCommand = []string{"make", "kubectl"}
			tool.WriteDir = "out"
			base.Components["kubectl"] = tool
			prepared := captureMixedFixtureInputs(t, definition, environment, &base)
			plan, pending := prepared.plan, prepared.run
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(root)
			workspace := ".nvkr/runs/mixed"
			toolBytes := []byte("mixed capture executable fixture")
			toolOutput := writeLocalCaptureFixture(t, workspace, prepared, toolBytes)
			var slot requiredOutput
			for _, candidate := range pending.Expected {
				if candidate.Kind == OutputDiskImage {
					slot = candidate
				}
			}
			payload := make([]byte, 512)
			copy(payload, "QFI\xfb")
			binary.BigEndian.PutUint32(payload[4:8], 3)
			binary.BigEndian.PutUint32(payload[20:24], 9)
			binary.BigEndian.PutUint64(payload[24:32], 1<<20)
			binary.BigEndian.PutUint32(payload[100:104], 104)
			path, err := LocalDiskOutputPath(workspace, slot.Name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			var source factory.Artifact
			var recordPath, inventoryPath string
			for _, artifact := range plan.Graph.Artifacts {
				switch artifact.ID {
				case slot.MemberArtifactID:
					source = artifact
				case slot.ComponentResults[0]:
					recordPath, err = lifecycleArtifactPath(artifact.Path, workspace)
				case "sbom/cli":
					inventoryPath, err = lifecycleArtifactPath(artifact.Path, workspace)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			record := fixtureProducerResult(t, plan, source, path)
			record.EngineClass, record.InputDigest = record.BuilderType, "sha256:"+repeat("7", 64)
			writeCanonicalTestFile(t, recordPath, record)
			observation, err := factory.ObserveRetainedDiskPayload(t.Context(), plan, factory.LifecycleOutputPlan{
				Name: slot.Name, Kind: slot.Kind, ArtifactID: slot.MemberArtifactID, BuildDigest: slot.BuildDigest, Components: slot.Components}, path, recordPath)
			if err != nil {
				t.Fatal(err)
			}
			// Fixture-only completed execution description, never a live receipt.
			observation.Outcome = "succeeded"
			pending.Lane = factory.TierLocalExecution.String()
			run, err := bindObserved(pending, []Artifact{toolOutput, artifactFromDiskObservation(slot, observation)})
			if err != nil {
				t.Fatal(err)
			}
			run, err = bindReleaseContent(plan, run)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "changed ordinary Run subject" {
				run.Observed[0].Digest = "sha256:" + repeat("9", 64)
				run, err = bindReleaseContent(plan, run)
				if err != nil {
					t.Fatal(err)
				}
			}
			if missingInventory {
				if err := os.Rename(inventoryPath, inventoryPath+".saved"); err != nil {
					t.Fatal(err)
				}
			}
			writeCanonicalTestFile(t, filepath.Join(workspace, "lifecycle.json"), run)
			writeCanonicalTestFile(t, filepath.Join(workspace, ".runner", "plan.json"), plan)
			if err := os.WriteFile(filepath.Join(workspace, "compile-inputs.json"), []byte(prepared.inputs), 0o600); err != nil {
				t.Fatal(err)
			}
			inputs, err := loadLocalQualificationInputs(t.Context(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			materials := writeLocalMaterialFixture(t, workspace, inputs)
			if mode == "missing materials" {
				if err := os.Rename(filepath.Join(workspace, localDiskMaterialsFile), filepath.Join(workspace, localDiskMaterialsFile)+".saved"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "substituted materials" {
				materials.Disks[0].PayloadDigest = "sha256:" + repeat("9", 64)
				writeCanonicalTestFile(t, filepath.Join(workspace, localDiskMaterialsFile), materials)
			}
			var producerPath, packagePath string
			for _, artifact := range plan.Graph.Artifacts {
				if artifact.ID != "artifact/cli/kubectl" && artifact.ID != pending.Expected[0].MemberArtifactID {
					continue
				}
				path, err := lifecycleArtifactPath(artifact.Path, workspace)
				if err != nil {
					t.Fatal(err)
				}
				if artifact.ID == "artifact/cli/kubectl" {
					producerPath = path
				} else {
					packagePath = path
				}
			}
			if producerPath == "" || packagePath == "" {
				t.Fatal("mixed fixture requires the original ordinary producer and package")
			}
			ordinaryPayloads := []string{producerPath, packagePath}
			switch mode {
			case "ordinary payloads restored at assembly":
				for _, path := range ordinaryPayloads {
					if err := os.Rename(path, path+".saved"); err != nil {
						t.Fatal(err)
					}
				}
			case "changed ordinary producer":
				if err := os.WriteFile(producerPath, []byte("changed ordinary producer bytes"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "missing ordinary package":
				if err := os.Rename(packagePath, packagePath+".saved"); err != nil {
					t.Fatal(err)
				}
			}
			ordinaryCalls := 0
			held, err := captureLocalQualificationPayloadsWith(t.Context(), workspace, inputs, func(ctx context.Context, plan *factory.Plan, workspace string) (*delivery.Publication, string, error) {
				ordinaryCalls++
				if mode == "ordinary payloads restored at assembly" {
					// The real factory owner must perform the sole ordinary producer and
					// package check. Earlier payload observation would fail this case.
					for _, path := range ordinaryPayloads {
						if err := os.Rename(path+".saved", path); err != nil {
							t.Fatal(err)
						}
					}
				}
				return factory.CaptureWorkspaceOrdinaryPublication(ctx, plan, workspace)
			})
			if mode != "complete" && mode != "ordinary payloads restored at assembly" {
				code := e.CodeVerifyFailed
				if missingInventory {
					code = e.CodeProvenanceMissing
				}
				assertCode(t, err, code)
				if held != nil {
					_ = held.Close()
					t.Fatal("partial ordinary evidence returned a mixed capture")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ordinaryCalls != 1 {
				t.Fatalf("ordinary capture called %d times, want exactly once", ordinaryCalls)
			}
			t.Cleanup(func() { _ = held.Close() })
			if held.materialBytes == "" {
				t.Fatal("capture lost exact material records")
			}
			if held.ordinary == nil || len(held.disks.members) != 1 || held.disks.members[slot.Name] == nil {
				t.Fatal("mixed capture lost a selected output")
			}
			if err := os.Rename(workspace, workspace+".saved"); err != nil {
				t.Fatal(err)
			}
			var executable, image bytes.Buffer
			if _, err := held.ordinary.WriteExecutableMember(t.Context(), "cli", "kubectl", &executable); err != nil {
				t.Fatal(err)
			}
			if _, err := held.disks.members[slot.Name].WriteToContext(t.Context(), &image); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(executable.Bytes(), toolBytes) || !bytes.Equal(image.Bytes(), payload) {
				t.Fatal("mixed capture did not preserve exact payloads")
			}
			checkLocalBuildEvidenceFixture(t, workspace+".saved", inputs, held)
			copySet := *held
			if err := held.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = copySet.ordinary.Outputs()
			assertCode(t, err, e.CodeGateUnmet)
			_, err = copySet.disks.members[slot.Name].WriteToContext(t.Context(), io.Discard)
			assertCode(t, err, e.CodeGateUnmet)
		})
	}
}

func TestLocalQualificationCaptureBindsHeldOrdinaryBytes(t *testing.T) {
	for _, mode := range []string{"exact", "substituted after path check", "late capture failure", "canceled after capture", "missing owner"} {
		t.Run(mode, func(t *testing.T) {
			workspace, prepared, run := localQualificationInputFixture(t)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(root)
			if err := os.MkdirAll(".nvkr/runs", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(workspace, ".nvkr/runs/captured"); err != nil {
				t.Fatal(err)
			}
			workspace = ".nvkr/runs/captured"
			payload := []byte("retained executable fixture")
			run.Observed = []Artifact{writeLocalCaptureFixture(t, workspace, prepared, payload)}
			run, err = bindReleaseContent(prepared.plan, run)
			if err != nil {
				t.Fatal(err)
			}
			writeCanonicalTestFile(t, filepath.Join(workspace, "lifecycle.json"), run)
			inputs, err := loadLocalQualificationInputs(t.Context(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var opened *delivery.Publication
			capture := func(ctx context.Context, plan *factory.Plan, path string) (*delivery.Publication, string, error) {
				if mode == "missing owner" {
					return nil, "", nil
				}
				if mode == "substituted after path check" {
					path = ".nvkr/runs/substitution"
					writeLocalCaptureFixture(t, path, prepared, []byte("different producer and package bytes"))
				}
				owner, root, err := factory.CaptureWorkspaceOrdinaryPublication(ctx, plan, path)
				opened = owner
				if err == nil && mode == "late capture failure" {
					err = e.New(e.CodeVerifyFailed, "fixture late capture error")
				}
				if mode == "canceled after capture" {
					cancel()
				}
				return owner, root, err
			}
			held, err := captureLocalQualificationPayloadsWith(ctx, workspace, inputs, capture)
			if mode != "exact" {
				code := e.CodeVerifyFailed
				switch mode {
				case "canceled after capture":
					code = e.CodeTimeout
				case "missing owner":
					code = e.CodeGateUnmet
				}
				assertCode(t, err, code)
				if held != nil {
					_ = held.Close()
					t.Fatal("failed capture returned a live set")
				}
				if opened != nil {
					_, err := opened.Outputs()
					assertCode(t, err, e.CodeGateUnmet)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			// Remove access to every original payload path. Streaming must use only
			// the private snapshots and retain exactly the delivered member bytes.
			if err := os.Rename(workspace, workspace+".saved"); err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if _, err := held.ordinary.WriteExecutableMember(t.Context(), "cli", "kubectl", &got); err != nil || !bytes.Equal(got.Bytes(), payload) {
				t.Fatalf("held executable changed: %q, %v", got.Bytes(), err)
			}
			checkLocalBuildEvidenceFixture(t, workspace+".saved", inputs, held)
			copyHandle := *held
			if err := held.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = copyHandle.ordinary.Outputs()
			assertCode(t, err, e.CodeGateUnmet)
		})
	}
}

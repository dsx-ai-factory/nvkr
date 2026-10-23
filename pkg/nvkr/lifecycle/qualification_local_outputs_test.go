// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestLocalQualificationPayloadsRejectChangedProducerOrRunSubject(t *testing.T) {
	for _, mode := range []string{"exact", "changed producer", "changed Run subject", "missing package"} {
		t.Run(mode, func(t *testing.T) {
			workspace, prepared, run := localQualificationInputFixture(t)
			artifacts := map[string]factory.Artifact{}
			for _, a := range prepared.plan.Graph.Artifacts {
				if a.Path != "" {
					var err error
					a.Path, err = lifecycleArtifactPath(a.Path, workspace)
					if err != nil {
						t.Fatal(err)
					}
				}
				artifacts[a.ID] = a
			}
			source := artifacts["artifact/cli/kubectl"]
			if err := os.MkdirAll(filepath.Dir(source.Path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source.Path, []byte("fixture executable bytes"), 0o755); err != nil {
				t.Fatal(err)
			}
			record := fixtureProducerResult(t, prepared.plan, source, source.Path)
			writeCanonicalTestFile(t, artifacts["result/cli/kubectl"].Path, record)
			bundle := artifacts[prepared.run.Expected[0].MemberArtifactID]
			if _, err := delivery.BuildBinaryBundle(t.Context(), bundle.Path, delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "cli",
				Files: []delivery.BinaryFileInput{{Name: "kubectl", Path: source.Path, InstallPath: "/usr/local/bin/kubectl", Mode: 0o755}}}); err != nil {
				t.Fatal(err)
			}
			observed, err := observeOutput(t.Context(), prepared.run.Expected[0], artifacts, prepared.plan, nil, workspace)
			if err != nil {
				t.Fatal(err)
			}
			run.Observed = []Artifact{observed}
			run, err = bindReleaseContent(prepared.plan, run)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "changed producer":
				if err := os.WriteFile(source.Path, []byte("substituted executable"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "changed Run subject":
				run.Observed[0].Digest = "sha256:" + repeat("9", 64)
				run, err = bindReleaseContent(prepared.plan, run)
				if err != nil {
					t.Fatal(err)
				}
			case "missing package":
				if err := os.Rename(bundle.Path, bundle.Path+".saved"); err != nil {
					t.Fatal(err)
				}
			}
			writeCanonicalTestFile(t, filepath.Join(workspace, "lifecycle.json"), run)
			inputs, err := loadLocalQualificationInputs(t.Context(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			err = checkLocalQualificationPayloads(t.Context(), workspace, inputs)
			if mode == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestLocalQualificationDiskChecksCommittedPayloadAndRecord(t *testing.T) {
	for _, mode := range []string{"exact", "changed payload", "changed payload and producer record"} {
		t.Run(mode, func(t *testing.T) {
			prepared, exporter := diskLifecycleFixtureWithProducer(t, "disk-check", true)
			payload := make([]byte, 512)
			copy(payload, "QFI\xfb")
			binary.BigEndian.PutUint32(payload[4:8], 3)
			binary.BigEndian.PutUint32(payload[20:24], 9)
			binary.BigEndian.PutUint64(payload[24:32], 1<<20)
			binary.BigEndian.PutUint32(payload[100:104], 104)
			exporter.observation.Digest = pipeline.FileDigest(payload)
			exporter.observation.Size = int64(len(payload))
			pending := prepared.Description()
			pending.Lane = factory.TierLocalExecution.String()
			run, err := bindObserved(&pending, []Artifact{artifactFromDiskObservation(pending.Expected[0], exporter.observation)})
			if err != nil {
				t.Fatal(err)
			}
			run, err = bindReleaseContent(prepared.plan, run)
			if err != nil {
				t.Fatal(err)
			}
			workspace, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path, err := LocalDiskOutputPath(workspace, pending.Expected[0].Name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			var source, record factory.Artifact
			for _, artifact := range prepared.plan.Graph.Artifacts {
				if artifact.ID == pending.Expected[0].MemberArtifactID {
					source = artifact
				}
				if artifact.ID == pending.Expected[0].ComponentResults[0] {
					record = artifact
				}
			}
			result := fixtureProducerResult(t, prepared.plan, source, path)
			result.EngineClass, result.InputDigest = result.BuilderType, "sha256:"+repeat("7", 64)
			if mode != "exact" {
				payload[len(payload)-1] = 1
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed payload and producer record" {
				result.SHA256 = pipeline.FileDigest(payload)
			}
			recordPath, err := lifecycleArtifactPath(record.Path, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "exact" {
				writeCanonicalTestFile(t, recordPath, result)
			}
			inputs := &localQualificationInputs{plan: prepared.plan, run: *run}
			err = checkLocalQualificationPayloads(t.Context(), workspace, inputs)
			if mode == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, e.CodeVerifyFailed)
			}
			var opened []*delivery.DiskArtifact
			capture := func(ctx context.Context, plan *factory.Plan, slot factory.LifecycleOutputPlan, payload string, subject factory.DiskPayloadObservation) (*delivery.DiskArtifact, factory.DiskPayloadObservation, error) {
				owner, observation, err := factory.CaptureRetainedDiskSubject(ctx, plan, slot, payload, subject)
				if owner != nil {
					opened = append(opened, owner)
				}
				return owner, observation, err
			}
			var disks *localQualificationDisks
			if mode == "exact" {
				disks, err = captureLocalQualificationDisks(t.Context(), workspace, inputs)
			} else {
				disks, err = captureLocalQualificationDisksWith(t.Context(), workspace, inputs, capture)
			}
			if mode != "exact" {
				assertCode(t, err, e.CodeVerifyFailed)
				if disks != nil {
					t.Fatal("failed Run match returned disk owners")
				}
				for _, owner := range opened {
					_, err := owner.WriteToContext(t.Context(), io.Discard)
					assertCode(t, err, e.CodeGateUnmet)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = disks.Close() }()
			if len(disks.members) != 1 || disks.members[run.Expected[0].Name] == nil {
				t.Fatal("snapshot lost its named Run slot")
			}
			testLocalCaptureCleanup(t, workspace, inputs)
			writeLocalMaterialFixture(t, workspace, inputs)
			complete, err := captureLocalQualificationPayloads(t.Context(), workspace, inputs)
			if err != nil {
				t.Fatal(err)
			}
			if complete.ordinary != nil || complete.ordinaryRoot != "" || len(complete.disks.members) != 1 {
				t.Fatal("disk-only capture invented an ordinary publication or lost its disk")
			}
			copySet := *complete
			if err := complete.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = copySet.disks.members[run.Expected[0].Name].WriteToContext(t.Context(), io.Discard)
			assertCode(t, err, e.CodeGateUnmet)
			if err := os.WriteFile(path, []byte("changed after capture"), 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if _, err := disks.members[run.Expected[0].Name].WriteToContext(t.Context(), &out); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), payload) {
				t.Fatal("Run-bound snapshot followed replacement bytes")
			}
		})
	}
}

func testLocalCaptureCleanup(t *testing.T, workspace string, inputs *localQualificationInputs) {
	t.Helper()
	for _, mode := range []string{"capture error", "mismatched observation", "canceled after capture"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var opened *delivery.DiskArtifact
			disks, err := captureLocalQualificationDisksWith(ctx, workspace, inputs, func(ctx context.Context, plan *factory.Plan, slot factory.LifecycleOutputPlan, payload string, subject factory.DiskPayloadObservation) (*delivery.DiskArtifact, factory.DiskPayloadObservation, error) {
				owner, observation, err := factory.CaptureRetainedDiskSubject(ctx, plan, slot, payload, subject)
				if err != nil {
					return owner, observation, err
				}
				opened = owner
				switch mode {
				case "capture error":
					return owner, observation, e.New(e.CodeVerifyFailed, "fixture late capture failure")
				case "mismatched observation":
					observation.Size++
				case "canceled after capture":
					cancel()
				}
				return owner, observation, nil
			})
			code := e.CodeVerifyFailed
			if mode == "canceled after capture" {
				code = e.CodeTimeout
			}
			assertCode(t, err, code)
			if disks != nil || opened == nil {
				t.Fatal("failed capture leaked a set or failed before opening fixture")
			}
			_, err = opened.WriteToContext(t.Context(), io.Discard)
			assertCode(t, err, e.CodeGateUnmet)
		})
	}
}

func TestLocalQualificationCapturePreservesOuterPublication(t *testing.T) {
	for _, mode := range []string{"exact", "missing root", "wrong member name", "wrong member media type", "wrong member digest"} {
		t.Run(mode, func(t *testing.T) {
			baseline := prepareBinaryObservationFixture(t)
			definition := baseline.plan.Definition.Clone()
			definition.Catalog.Base = "catalog"
			prepared := captureMixedFixtureInputs(t, definition, baseline.plan.Environment, baseline.plan.CatalogSnapshot)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(root)
			workspace := ".nvkr/runs/mixed"
			toolBytes := []byte("captured outer-publication executable fixture")
			observed := writeLocalCaptureFixture(t, workspace, prepared, toolBytes)
			pending := prepared.Description()
			pending.Lane = factory.TierLocalExecution.String()
			run, err := bindObserved(&pending, []Artifact{observed})
			if err != nil {
				t.Fatal(err)
			}
			run, err = bindReleaseContent(prepared.plan, run)
			if err != nil {
				t.Fatal(err)
			}
			writeCanonicalTestFile(t, filepath.Join(workspace, "lifecycle.json"), run)
			writeCanonicalTestFile(t, filepath.Join(workspace, ".runner", "plan.json"), prepared.plan)
			if err := os.WriteFile(filepath.Join(workspace, "compile-inputs.json"), []byte(prepared.inputs), 0o600); err != nil {
				t.Fatal(err)
			}
			inputs, err := loadLocalQualificationInputs(t.Context(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			slot := inputs.run.Expected[0]
			artifacts, err := localQualificationOutputArtifacts(t.Context(), workspace, inputs)
			if err != nil {
				t.Fatal(err)
			}
			member, outer := artifacts[slot.MemberArtifactID], artifacts[slot.ArtifactID]
			if member.ID == outer.ID {
				t.Fatal("fixture must retain an original enclosing publication")
			}
			if mode != "exact" && mode != "wrong member media type" {
				if err := os.Rename(outer.Path, outer.Path+".saved"); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "wrong member media type":
				for index := range inputs.plan.Graph.Artifacts {
					if inputs.plan.Graph.Artifacts[index].ID == slot.MemberArtifactID {
						inputs.plan.Graph.Artifacts[index].MediaType = "application/x-substituted"
					}
				}
			case "wrong member name", "wrong member digest":
				name, payload := member.Name, toolBytes
				if mode == "wrong member name" {
					name = "sibling"
				} else {
					payload = []byte("foreign ordinary package bytes")
				}
				foreignSource := filepath.Join(workspace, "foreign-executable")
				if err := os.WriteFile(foreignSource, payload, 0o755); err != nil {
					t.Fatal(err)
				}
				foreignPath := filepath.Join(workspace, "foreign-package")
				built, err := delivery.BuildBinaryBundle(t.Context(), foreignPath, delivery.BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: name,
					Files: []delivery.BinaryFileInput{{Name: "kubectl", Path: foreignSource, InstallPath: "/usr/local/bin/kubectl", Mode: 0o755}}})
				if err != nil {
					t.Fatal(err)
				}
				descriptor, err := delivery.PayloadDescriptor(t.Context(), foreignPath)
				if err != nil {
					t.Fatal(err)
				}
				request := testReleaseRequest(t,
					[]Artifact{{Name: name, Kind: slot.Kind, Digest: descriptor.Digest, BuildDigest: slot.BuildDigest, Components: slot.Components}},
					[]delivery.ObjectInput{{Name: name, Kind: member.Kind, Path: foreignPath, Layout: true, ArtifactType: member.MediaType, SemanticDigest: built.SemanticDigest, BlobDigest: built.RootDigest}},
					map[string]string{"kubectl": pipeline.FileDigest(payload)})
				if _, err := delivery.BuildPublicationRoot(t.Context(), outer.Path, request); err != nil {
					t.Fatal(err)
				}
			}
			ordinaryCalls := 0
			held, err := captureLocalQualificationPayloadsWith(t.Context(), workspace, inputs, func(ctx context.Context, plan *factory.Plan, workspace string) (*delivery.Publication, string, error) {
				ordinaryCalls++
				return factory.CaptureWorkspaceOrdinaryPublication(ctx, plan, workspace)
			})
			if mode != "exact" {
				code := e.CodeVerifyFailed
				if mode == "wrong member name" {
					code = e.CodeReleaseIncomplete
				}
				assertCode(t, err, code)
				if held != nil || ordinaryCalls != 0 {
					t.Fatal("changed original publication reached ordinary assembly or returned a capture")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			if ordinaryCalls != 1 {
				t.Fatalf("ordinary capture called %d times, want once", ordinaryCalls)
			}
		})
	}
}

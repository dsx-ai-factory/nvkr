// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func localQualificationInputFixture(t *testing.T) (string, *PreparedRun, *Run) {
	t.Helper()
	_, prepared := capturedLocalFixture(t)
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(parent, prepared.run.Attempt)
	if err := os.MkdirAll(filepath.Join(workspace, ".runner"), 0o700); err != nil {
		t.Fatal(err)
	}
	pending := prepared.Description()
	pending.Lane = factory.TierLocalExecution.String()
	slot := pending.Expected[0]
	complete, err := bindObserved(&pending, []Artifact{{Name: slot.Name, Kind: slot.Kind, BuildDigest: slot.BuildDigest,
		Components: slot.Components, Digest: "sha256:" + repeat("4", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	complete, err = bindReleaseContent(prepared.plan, complete)
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalTestFile(t, filepath.Join(workspace, "lifecycle.json"), complete)
	writeCanonicalTestFile(t, filepath.Join(workspace, ".runner", "plan.json"), prepared.plan)
	if err := os.WriteFile(filepath.Join(workspace, "compile-inputs.json"), []byte(prepared.inputs), 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace, prepared, complete
}

func TestLocalQualificationInputsReplayWithoutProducerCheckout(t *testing.T) {
	workspace, prepared, complete := localQualificationInputFixture(t)
	got, err := loadLocalQualificationInputs(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got.run.PlanDigest != prepared.run.PlanDigest || got.run.ContentSubject != complete.ContentSubject ||
		!bytes.Equal(got.inputBytes, []byte(prepared.inputs)) {
		t.Fatal("saved local input identity changed during replay")
	}
	plan, run, err := InspectLocalRunInputs(t.Context(), workspace)
	if err != nil || plan == nil || run.PlanDigest != got.run.PlanDigest || run.ContentSubject != complete.ContentSubject {
		t.Fatalf("local input inspection changed its frozen metadata: %v", err)
	}
	// This fixture deliberately contains no payloads. Input replay alone must
	// remain a private description, never an artifact or qualification receipt.
	if _, err := os.Stat(filepath.Join(workspace, "artifacts")); !os.IsNotExist(err) {
		t.Fatalf("input replay unexpectedly created payloads: %v", err)
	}
}

func TestLocalQualificationInputsRefuseSubstitutedOrIncompleteRecords(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
		edit func(*testing.T, string, *PreparedRun, *Run)
	}{
		{"pending", e.CodeGateUnmet, func(t *testing.T, root string, p *PreparedRun, _ *Run) {
			writeCanonicalTestFile(t, filepath.Join(root, "lifecycle.json"), p.Description())
		}},
		{"hosted lane", e.CodeGateUnmet, func(t *testing.T, root string, _ *PreparedRun, r *Run) {
			r.Lane = LanePipeline
			writeCanonicalTestFile(t, filepath.Join(root, "lifecycle.json"), r)
		}},
		{"different attempt", e.CodeVerifyFailed, func(t *testing.T, root string, _ *PreparedRun, r *Run) {
			r.Attempt = "other"
			writeCanonicalTestFile(t, filepath.Join(root, "lifecycle.json"), r)
		}},
		{"different definition identity", e.CodePipelineDrift, func(t *testing.T, root string, _ *PreparedRun, r *Run) {
			r.DefinitionDigest = "sha256:" + repeat("9", 64)
			writeCanonicalTestFile(t, filepath.Join(root, "lifecycle.json"), r)
		}},
		{"different environment identity", e.CodePipelineDrift, func(t *testing.T, root string, _ *PreparedRun, r *Run) {
			r.EnvironmentDigest = "sha256:" + repeat("9", 64)
			writeCanonicalTestFile(t, filepath.Join(root, "lifecycle.json"), r)
		}},
		{"different payload mapping", e.CodePipelineDrift, func(t *testing.T, root string, _ *PreparedRun, r *Run) {
			r.Expected[0].MemberArtifactID = "package/other"
			writeCanonicalTestFile(t, filepath.Join(root, "lifecycle.json"), r)
		}},
		{"different Plan identity", e.CodePipelineDrift, func(t *testing.T, root string, _ *PreparedRun, r *Run) {
			r.PlanDigest = "sha256:" + repeat("9", 64)
			writeCanonicalTestFile(t, filepath.Join(root, "lifecycle.json"), r)
		}},
		{"different captured inputs", e.CodePipelineDrift, func(t *testing.T, root string, p *PreparedRun, _ *Run) {
			var snapshot factory.InputSnapshot
			if err := canonical.UnmarshalExact([]byte(p.inputs), &snapshot); err != nil {
				t.Fatal(err)
			}
			definition := p.plan.Definition
			definition.Metadata.Name = "other-product"
			raw, err := canonical.Marshal(definition)
			if err != nil {
				t.Fatal(err)
			}
			for i := range snapshot.Files {
				if snapshot.Files[i].Path == snapshot.DefinitionPath {
					snapshot.Files[i].Data = raw
				}
			}
			writeCanonicalTestFile(t, filepath.Join(root, "compile-inputs.json"), snapshot)
		}},
		{"no content identity", e.CodeGateUnmet, func(t *testing.T, root string, _ *PreparedRun, r *Run) {
			r.ContentSubject = ""
			writeCanonicalTestFile(t, filepath.Join(root, "lifecycle.json"), r)
		}},
		{"unfinished disk commit", e.CodeImmutableViolation, func(t *testing.T, root string, _ *PreparedRun, _ *Run) {
			if err := os.WriteFile(filepath.Join(root, diskAttemptMarker), []byte("pending"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlinked inputs", e.CodeVerifyFailed, func(t *testing.T, root string, _ *PreparedRun, _ *Run) {
			path := filepath.Join(root, "compile-inputs.json")
			if err := os.Rename(path, path+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".saved", path); err != nil {
				t.Fatal(err)
			}
		}},
		{"noncanonical Plan", e.CodePipelineDrift, func(t *testing.T, root string, p *PreparedRun, _ *Run) {
			raw, err := canonical.Marshal(p.plan)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".runner", "plan.json"), append(raw, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, prepared, run := localQualificationInputFixture(t)
			tc.edit(t, root, prepared, run)
			plan, inspected, err := InspectLocalRunInputs(t.Context(), root)
			assertCode(t, err, tc.code)
			if plan != nil || inspected.Attempt != "" {
				t.Fatal("rejected metadata returned prepared inputs")
			}
		})
	}
}

func TestLocalQualificationInputsCanceledBeforeFilesystem(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	plan, run, err := InspectLocalRunInputs(ctx, "missing")
	assertCode(t, err, e.CodeTimeout)
	if plan != nil || run.Attempt != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

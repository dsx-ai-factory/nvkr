// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func localDiagnosticFixture(t *testing.T) (*LocalDiagnosticArtifacts, []byte, string) {
	t.Helper()
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
	payload := []byte("held local executable diagnostic fixture")
	run.Observed = []Artifact{writeLocalCaptureFixture(t, workspace, prepared, payload)}
	run, err = bindReleaseContent(prepared.plan, run)
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalTestFile(t, filepath.Join(workspace, "lifecycle.json"), run)
	owner, err := CaptureLocalDiagnosticArtifacts(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	return owner, payload, workspace
}

func TestLocalDiagnosticArtifactsCaptureAndNestedStream(t *testing.T) {
	owner, payload, workspace := localDiagnosticFixture(t)
	description, err := owner.Description()
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := loadLocalQualificationInputs(t.Context(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if description.Attempt != inputs.run.Attempt || description.Lane != inputs.run.Lane ||
		description.PlanDigest != inputs.run.PlanDigest || description.DefinitionDigest != inputs.run.DefinitionDigest ||
		description.EnvironmentDigest != inputs.run.EnvironmentDigest || description.ContentSubject != inputs.run.ContentSubject ||
		description.CatalogDigest != inputs.plan.CatalogDigest || description.OrdinaryRoot == "" || len(description.Outputs) != 1 {
		t.Fatalf("local diagnostic bindings differ: %+v", description)
	}
	if description.Outputs[0].Reference != "" || description.Outputs[0].Publication != nil || strings.Contains(owner.state.raw, "localhost/") || strings.Contains(owner.state.raw, "invocationID") {
		t.Fatal("local diagnostic exposed synthetic locator or hosted origin")
	}
	original := description.Outputs[0].BuildSubjects[0].Digest
	description.Outputs[0].BuildSubjects[0].Digest = "caller mutation"
	after, err := owner.Description()
	if err != nil || after.Outputs[0].BuildSubjects[0].Digest != original {
		t.Fatal("description mutation reached held owner")
	}
	// Captured executable reads must not reopen the original producer path.
	for _, artifact := range inputs.plan.Graph.Artifacts {
		if artifact.ID == "artifact/cli/kubectl" {
			path, err := lifecycleArtifactPath(artifact.Path, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("producer path replaced after capture"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Payload streaming inside the owning callback must not acquire a held mutex
	// recursively. The complete use/cleanup lifetime remains registered outside it.
	err = owner.WithDiagnosticOperation(t.Context(), func(ctx context.Context, _ LocalDiagnosticDescription) error {
		var output bytes.Buffer
		member, err := owner.WriteExecutableMember(ctx, "cli", "kubectl", "linux/amd64", 1024, &output)
		if err != nil {
			return err
		}
		if !bytes.Equal(output.Bytes(), payload) || member.Size != int64(len(payload)) {
			t.Fatal("held member changed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, output, member, platform string
		limit                          int64
		code                           e.Code
	}{
		{"missing output", "other", "kubectl", "linux/amd64", 1024, e.CodeVerifyFailed},
		{"missing member", "cli", "other", "linux/amd64", 1024, e.CodeVerifyFailed},
		{"wrong platform", "cli", "kubectl", "linux/arm64", 1024, e.CodeEngineCapability},
		{"too small", "cli", "kubectl", "linux/amd64", 1, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			_, err := owner.WriteExecutableMember(t.Context(), tc.output, tc.member, tc.platform, tc.limit, &output)
			assertCode(t, err, tc.code)
			if output.Len() != 0 {
				t.Fatal("rejected member wrote partial output before admission")
			}
		})
	}
}

func TestLocalDiagnosticArtifactsRejectIncompleteOrChangedRun(t *testing.T) {
	for _, mode := range []string{"incomplete", "changed plan", "changed payload"} {
		t.Run(mode, func(t *testing.T) {
			owner, _, workspace := localDiagnosticFixture(t)
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			inputs, err := loadLocalQualificationInputs(t.Context(), workspace)
			if err != nil {
				t.Fatal(err)
			}
			code := e.CodeVerifyFailed
			switch mode {
			case "incomplete":
				inputs.run.Status = RunPending
				inputs.run.Observed = nil
				inputs.run.ContentSubject = ""
				writeCanonicalTestFile(t, filepath.Join(workspace, "lifecycle.json"), inputs.run)
				code = e.CodeGateUnmet
			case "changed plan":
				inputs.run.DefinitionDigest = "sha256:" + repeat("9", 64)
				writeCanonicalTestFile(t, filepath.Join(workspace, "lifecycle.json"), inputs.run)
				code = e.CodePipelineDrift
			case "changed payload":
				for _, artifact := range inputs.plan.Graph.Artifacts {
					if artifact.ID == "artifact/cli/kubectl" {
						path, err := lifecycleArtifactPath(artifact.Path, workspace)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, []byte("different bytes"), 0o755); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			captured, err := CaptureLocalDiagnosticArtifacts(t.Context(), workspace)
			assertCode(t, err, code)
			if captured != nil {
				_ = captured.Close()
				t.Fatal("failed capture issued a live owner")
			}
		})
	}
}

func TestLocalDiagnosticArtifactsCloseCancelsJoinsAndRevokesCopies(t *testing.T) {
	owner, _, _ := localDiagnosticFixture(t)
	copied := *owner
	entered, canceled, cleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-cleanup:
		default:
			close(cleanup)
		}
	}()
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- owner.WithDiagnosticOperation(t.Context(), func(ctx context.Context, _ LocalDiagnosticDescription) error {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-cleanup
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not enter")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- copied.Close() }()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel callback")
	}
	select {
	case <-closeDone:
		t.Fatal("Close released payloads before cleanup joined")
	default:
	}
	if _, err := owner.Description(); err == nil {
		t.Fatal("shallow copy failed to revoke original")
	}
	close(cleanup)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	err := <-operationDone
	assertCode(t, err, e.CodeTimeout)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("revocation lost cancellation cause")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	assertCode(t, copied.WithDiagnosticOperation(t.Context(), func(context.Context, LocalDiagnosticDescription) error {
		t.Fatal("closed callback entered")
		return nil
	}), e.CodeGateUnmet)
}

func TestLocalDiagnosticArtifactsZeroAdmission(t *testing.T) {
	for _, owner := range []*LocalDiagnosticArtifacts{nil, {}} {
		_, err := owner.Description()
		assertCode(t, err, e.CodeGateUnmet)
		assertCode(t, owner.WithDiagnosticOperation(t.Context(), func(context.Context, LocalDiagnosticDescription) error { t.Fatal("zero callback entered"); return nil }), e.CodeGateUnmet)
		_, err = owner.WriteExecutableMember(t.Context(), "cli", "kubectl", "linux/amd64", 1024, &bytes.Buffer{})
		assertCode(t, err, e.CodeGateUnmet)
	}
}

func TestLocalDiagnosticArtifactsContextAdmission(t *testing.T) {
	owner, _, _ := localDiagnosticFixture(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"nil context", nil, e.CodeUsage},
		{"canceled context", canceled, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := owner.WithDiagnosticOperation(tc.ctx, func(context.Context, LocalDiagnosticDescription) error {
				t.Fatal("invalid context entered callback")
				return nil
			})
			assertCode(t, err, tc.code)
		})
	}
	assertCode(t, owner.WithDiagnosticOperation(t.Context(), nil), e.CodeUsage)
	sentinel := e.New(e.CodeUnavailable, "observer failed")
	if err := owner.WithDiagnosticOperation(t.Context(), func(context.Context, LocalDiagnosticDescription) error { return sentinel }); err != sentinel {
		t.Fatalf("coded callback error changed: %v", err)
	}
}

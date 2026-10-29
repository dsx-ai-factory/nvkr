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

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func inspectionDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(path, testRun().Attempt)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func inspectionCompleteRun(t *testing.T) *Run {
	t.Helper()
	run, err := bindObserved(testRun(), []Artifact{{Name: "cli", Kind: OutputExecutable,
		Digest: "sha256:" + strings.Repeat("4", 64), BuildDigest: "sha256:" + strings.Repeat("5", 64),
		Components: []string{"kubectl"}}})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func inspectionDebianRun(t *testing.T, status string) *Run {
	t.Helper()
	run := testRun()
	run.Expected[0].Kind = OutputDebianPackage
	if status == RunFailed {
		return failedRun(run, e.New(e.CodeBuildFailed, "fixture failed"))
	}
	if status == RunComplete {
		complete := inspectionCompleteRun(t)
		complete.Expected[0].Kind = OutputDebianPackage
		complete.Observed[0].Kind = OutputDebianPackage
		return complete
	}
	return run
}

func TestInspectRunMetadataAndMissingRecordRefusal(t *testing.T) {
	for _, test := range []struct {
		name    string
		run     *Run
		marker  bool
		missing bool
	}{
		{"pending", testRun(), false, false},
		{"Debian pending", inspectionDebianRun(t, RunPending), false, false},
		{"Debian complete", inspectionDebianRun(t, RunComplete), false, false},
		{"Debian failed", inspectionDebianRun(t, RunFailed), false, false},
		{"complete", inspectionCompleteRun(t), false, false},
		{"complete with cleanup", inspectionCompleteRun(t), true, false},
		{"failed", failedRun(testRun(), e.New(e.CodeBuildFailed, "private-provider /private/secret token")), false, false},
		{"marker without record", nil, true, true},
		{"reserved directory without record", nil, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := inspectionDirectory(t)
			path := filepath.Join(root, "lifecycle.json")
			var raw []byte
			if test.run != nil {
				var err error
				raw, err = canonical.Marshal(test.run)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0o400); err != nil {
					t.Fatal(err)
				}
			}
			if test.marker {
				if err := os.WriteFile(filepath.Join(root, diskAttemptMarker), nil, 0o400); err != nil {
					t.Fatal(err)
				}
			}
			view, err := InspectRun(t.Context(), path)
			if test.missing {
				assertCode(t, err, e.CodeVerifyFailed)
				if view.RecordAvailable || view.Interrupted || view.Attempt != "" {
					t.Fatal("missing record inferred lifecycle or owner state")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if view.Interrupted || view.CleanupIncomplete != test.marker || view.RecordAvailable != (test.run != nil) {
				t.Fatalf("incorrect attempt description: %+v", view)
			}
			if test.run != nil {
				if view.Status != test.run.Status || len(view.Outputs) != 1 {
					t.Fatalf("wrong membership: %+v", view)
				}
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(raw, after) {
					t.Fatal("inspection changed record bytes", err)
				}
				if test.run.Status == RunComplete && view.Outputs[0].State != "observed" {
					t.Fatal("complete metadata lost its observation")
				}
			}
			encoded, err := canonical.Marshal(view)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private-provider") || strings.Contains(string(encoded), "/private/secret") || strings.Contains(string(encoded), "token") {
				t.Fatal("inspection exposed raw diagnostic text")
			}
		})
	}
}

func TestInspectRunRejectsUnsafeOrSubstitutedRecords(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Run)
		raw    []byte
	}{
		{name: "sibling", mutate: func(run *Run) { run.Observed[0].Name = "sibling" }},
		{name: "wrong build", mutate: func(run *Run) { run.Observed[0].BuildDigest = "sha256:" + strings.Repeat("6", 64) }},
		{name: "wrong content", mutate: func(run *Run) { run.ContentSubject = "sha256:" + strings.Repeat("6", 64) }},
		{name: "unknown lane", mutate: func(run *Run) { run.Lane = "private lane text" }},
		{name: "unsupported qualified lane", mutate: func(run *Run) { run.Lane = LaneQualified }},
		{name: "wrong attempt directory", mutate: func(run *Run) { run.Attempt = "another-attempt" }},
		{name: "pending with observed", mutate: func(run *Run) { run.Status = RunPending }},
		{name: "unknown failure code", mutate: func(run *Run) {
			run.Status = RunFailed
			run.Observed = nil
			run.Failure = &runFailure{Code: "private-code", Message: "private-message"}
		}},
		{name: "empty", raw: []byte{}},
		{name: "oversize", raw: bytes.Repeat([]byte("x"), int(MaxRunRecordBytes+1))},
		{name: "duplicate fields", raw: []byte(`{"schemaVersion":"a","schemaVersion":"b"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := inspectionDirectory(t)
			raw := test.raw
			if test.mutate != nil {
				run := inspectionCompleteRun(t)
				test.mutate(run)
				var err error
				raw, err = canonical.Marshal(run)
				if err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "lifecycle.json")
			if err := os.WriteFile(path, raw, 0o400); err != nil {
				t.Fatal(err)
			}
			_, err := InspectRun(t.Context(), path)
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestInspectRunAdmitsOnlyWriterProducibleLanes(t *testing.T) {
	for _, lane := range []string{LaneUnqualified, LanePipeline, factory.TierLocalExecution.String()} {
		t.Run(lane, func(t *testing.T) {
			run := inspectionCompleteRun(t)
			run.Lane = lane
			root := inspectionDirectory(t)
			raw, err := canonical.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "lifecycle.json")
			if err := os.WriteFile(path, raw, 0o400); err != nil {
				t.Fatal(err)
			}
			view, err := InspectRun(t.Context(), path)
			if err != nil || view.Lane != lane {
				t.Fatal("writer-producible lane refused", lane, err)
			}
		})
	}
}

func TestRunInspectionReadErrorsDoNotExposeIOPaths(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "read failure", true: "canceled"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if canceled {
				cancel()
			}
			err := runInspectionReadError(ctx, &os.PathError{Op: "read", Path: "/private/credential-sentinel", Err: errors.New("provider-secret")})
			want := e.CodeVerifyFailed
			if canceled {
				want = e.CodeTimeout
			}
			assertCode(t, err, want)
			if strings.Contains(err.Error(), "credential-sentinel") || strings.Contains(err.Error(), "provider-secret") {
				t.Fatal("inspection exposed raw I/O cause")
			}
		})
	}
}

func TestInspectRunMissingDirectoryIsDistinctFromSymlinkRefusal(t *testing.T) {
	_, err := InspectRun(t.Context(), filepath.Join(inspectionDirectory(t), "missing", "lifecycle.json"))
	assertCode(t, err, e.CodeVerifyFailed)
	if strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), "unavailable") {
		t.Fatal("missing directory was described as a symlink violation", err)
	}
}

func TestInspectRunRejectsSymlinkAndContextBeforeReading(t *testing.T) {
	root := inspectionDirectory(t)
	path := filepath.Join(root, "lifecycle.json")
	raw, err := canonical.Marshal(inspectionCompleteRun(t))
	if err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(root, "regular-source.json")
	if err := os.WriteFile(regular, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("regular-source.json", path); err != nil {
		t.Fatal(err)
	}
	_, err = InspectRun(t.Context(), path)
	assertCode(t, err, e.CodeVerifyFailed)
	// A valid record through a symlinked ancestor must still be refused. Naming
	// the symlink after the attempt avoids a directory-binding refusal masking it.
	ancestor := inspectionDirectory(t)
	link := filepath.Join(ancestor, filepath.Base(root))
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatal(err)
	}
	_, err = InspectRun(t.Context(), filepath.Join(link, "lifecycle.json"))
	assertCode(t, err, e.CodeVerifyFailed)
	if _, err := InspectRun(t.Context(), path); err != nil {
		t.Fatal("physical path to the same valid record was refused", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = InspectRun(ctx, "missing/lifecycle.json")
	assertCode(t, err, e.CodeTimeout)
	var absentContext context.Context
	_, err = InspectRun(absentContext, path)
	assertCode(t, err, e.CodeUsage)
}

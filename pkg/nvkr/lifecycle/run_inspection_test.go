// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func inspectionDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
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

func TestInspectRunMetadataAndInterruptedAttempts(t *testing.T) {
	for _, test := range []struct {
		name        string
		run         *Run
		marker      bool
		interrupted bool
	}{
		{"pending", testRun(), false, false},
		{"complete", inspectionCompleteRun(t), false, false},
		{"complete with cleanup", inspectionCompleteRun(t), true, false},
		{"failed", failedRun(testRun(), e.New(e.CodeBuildFailed, "private-provider /private/secret token")), false, false},
		{"marker only", nil, true, true},
		{"interrupted before record", nil, false, true},
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
			if err != nil {
				t.Fatal(err)
			}
			if view.Interrupted != test.interrupted || view.CleanupIncomplete != test.marker || view.RecordAvailable != (test.run != nil) {
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
			snapshot := view.Snapshot()
			if snapshot.Mode != "recorded" || !snapshot.StartedAt.IsZero() {
				t.Fatal("inspection manufactured a live clock")
			}
			if len(snapshot.Outputs) > 0 {
				if snapshot.Outputs[0].RecordedDigest != view.Outputs[0].Digest {
					t.Fatal("recorded output identity was lost in presentation")
				}
				snapshot.Outputs[0].Name = "changed"
				if view.Outputs[0].Name == "changed" {
					t.Fatal("snapshot aliases inspection")
				}
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

func TestInspectRunRejectsSymlinkAndContextBeforeReading(t *testing.T) {
	root := inspectionDirectory(t)
	path := filepath.Join(root, "lifecycle.json")
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	_, err := InspectRun(t.Context(), path)
	assertCode(t, err, e.CodeVerifyFailed)
	link := filepath.Join(root, "linked-directory")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	_, err = InspectRun(t.Context(), filepath.Join(link, "lifecycle.json"))
	assertCode(t, err, e.CodeVerifyFailed)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = InspectRun(ctx, "missing/lifecycle.json")
	assertCode(t, err, e.CodeTimeout)
	var absentContext context.Context
	_, err = InspectRun(absentContext, path)
	assertCode(t, err, e.CodeUsage)
}

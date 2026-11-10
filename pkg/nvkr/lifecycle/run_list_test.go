// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// attemptsRoot returns an empty attempts root without symlinks in its path.
func attemptsRoot(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// writeAttempt stores run as name/lifecycle.json under root, with an optional
// cleanup marker, and returns the attempt directory.
func writeAttempt(t *testing.T, root, name string, run *Run, marker bool) string {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if run != nil {
		raw, err := canonical.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "lifecycle.json"), raw, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	if marker {
		if err := os.WriteFile(filepath.Join(directory, diskAttemptMarker), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

// named is run with its attempt renamed, as a second attempt of the same Plan.
func named(run *Run, attempt string) *Run {
	run.Attempt = attempt
	return run
}

// treeBytes snapshots every file under root, so a test can prove it unchanged.
func treeBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			files[path] = "symlink:" + target
			return err
		}
		raw, err := os.ReadFile(path)
		files[path] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestListRunsCompleteAndFailedAttempts(t *testing.T) {
	root := attemptsRoot(t)
	writeAttempt(t, root, "b-failed", named(failedRun(testRun(), e.New(e.CodeBuildFailed, "private /secret detail")), "b-failed"), false)
	writeAttempt(t, root, "a-complete", named(inspectionCompleteRun(t), "a-complete"), true)
	before := treeBytes(t, root)

	list, err := ListRuns(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if list.SchemaVersion != "nvkr.dev/run-list/v1" || list.Authority != "description-only" || len(list.Diagnostics) != 0 {
		t.Fatalf("wrong listing frame: %+v", list)
	}
	attempts := []string{}
	for _, run := range list.Runs {
		attempts = append(attempts, run.Attempt)
	}
	if !slices.Equal(attempts, []string{"a-complete", "b-failed"}) {
		t.Fatalf("runs are not sorted by attempt name: %v", attempts)
	}
	complete, failed := list.Runs[0], list.Runs[1]
	if complete.Status != RunComplete || complete.ObservedOutputs != 1 || complete.RequiredOutputs != 1 || complete.FailureCode != "" ||
		!complete.CleanupIncomplete || complete.Cleanup == "" || complete.Record != filepath.Join(root, "a-complete", "lifecycle.json") {
		t.Fatalf("wrong complete attempt: %+v", complete)
	}
	if failed.Status != RunFailed || failed.ObservedOutputs != 0 || failed.RequiredOutputs != 1 || failed.FailureCode != string(e.CodeBuildFailed) ||
		failed.CleanupIncomplete || failed.Cleanup != "" {
		t.Fatalf("wrong failed attempt: %+v", failed)
	}
	inspected, err := InspectRun(t.Context(), complete.Record)
	if err != nil || inspected.Attempt != "a-complete" {
		t.Fatalf("record locator is not accepted by InspectRun: %v", err)
	}
	again, err := ListRuns(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := canonical.Marshal(list)
	second, _ := canonical.Marshal(again)
	if string(first) != string(second) {
		t.Fatal("listing is not deterministic")
	}
	if !maps.Equal(before, treeBytes(t, root)) {
		t.Fatal("listing changed the attempts root")
	}
}

func TestListRunsEmptyAndMissingRoots(t *testing.T) {
	list, err := ListRuns(t.Context(), attemptsRoot(t))
	if err != nil || len(list.Runs) != 0 || len(list.Diagnostics) != 0 || list.Runs == nil {
		t.Fatalf("an empty root is an empty listing: %+v %v", list, err)
	}
	_, err = ListRuns(t.Context(), filepath.Join(attemptsRoot(t), "missing"))
	assertCodeIs(t, err, e.CodeVerifyFailed, "attempts root is unavailable")

	real := attemptsRoot(t)
	link := filepath.Join(attemptsRoot(t), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	_, err = ListRuns(t.Context(), link)
	assertCodeIs(t, err, e.CodeVerifyFailed, "must exist without symlinks")
	_, err = ListRuns(t.Context(), "")
	assertCodeIs(t, err, e.CodeUsage, "requires an attempts root")
}

func TestListRunsDiagnosesEntriesWithoutRecords(t *testing.T) {
	root := attemptsRoot(t)
	writeAttempt(t, root, "good", named(inspectionCompleteRun(t), "good"), false)
	writeAttempt(t, root, "marker-only", nil, true)
	writeAttempt(t, root, "reserved", nil, false)
	writeAttempt(t, root, "renamed", named(inspectionCompleteRun(t), "someone-else"), false)
	corrupt := writeAttempt(t, root, "corrupt", nil, false)
	if err := os.WriteFile(filepath.Join(corrupt, "lifecycle.json"), []byte(`{"attempt":"corrupt"}`), 0o400); err != nil {
		t.Fatal(err)
	}
	linkedRecord := writeAttempt(t, root, "linked-record", nil, false)
	if err := os.Symlink(filepath.Join(root, "good", "lifecycle.json"), filepath.Join(linkedRecord, "lifecycle.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "good"), filepath.Join(root, "symlinked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray-file"), []byte("not an attempt"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A nested directory inside an attempt is never traversed.
	if err := os.MkdirAll(filepath.Join(root, "good", "nested", "deeper"), 0o700); err != nil {
		t.Fatal(err)
	}
	before := treeBytes(t, root)

	list, err := ListRuns(t.Context(), root)
	assertCodeIs(t, err, e.CodeVerifyFailed, "7 of 8 attempts-root entries have no admissible Run record")
	if len(list.Runs) != 1 || list.Runs[0].Attempt != "good" {
		t.Fatalf("admissible attempts are still listed: %+v", list.Runs)
	}
	got := map[string]string{}
	entries := []string{}
	for _, diagnostic := range list.Diagnostics {
		got[diagnostic.Entry] = diagnostic.Problem
		entries = append(entries, diagnostic.Entry)
		if diagnostic.Code != string(e.CodeVerifyFailed) || diagnostic.Description == "" {
			t.Fatalf("diagnostic lacks a code or description: %+v", diagnostic)
		}
	}
	want := map[string]string{
		"corrupt": runRecordInvalid, "linked-record": runRecordUnsafe, "marker-only": runRecordMarkerOnly,
		"renamed": runRecordMismatch, "reserved": runRecordUnavailable, "stray-file": runEntryNotDirectory, "symlinked": runEntrySymlink,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("diagnostics %v; want %v", got, want)
	}
	if !slices.IsSorted(entries) {
		t.Fatalf("diagnostics are not sorted: %v", entries)
	}
	for _, description := range runProblemDescriptions {
		if strings.Contains(description, "interrupted") {
			t.Fatalf("a diagnostic calls a Run interrupted: %q", description)
		}
	}
	if !maps.Equal(before, treeBytes(t, root)) {
		t.Fatal("listing changed the attempts root")
	}
}

func TestListRunsEntryBoundAndCancellation(t *testing.T) {
	root := attemptsRoot(t)
	for _, name := range []string{"a", "b", "c"} {
		writeAttempt(t, root, name, named(inspectionCompleteRun(t), name), false)
	}
	if list, err := listRuns(t.Context(), root, 3); err != nil || len(list.Runs) != 3 {
		t.Fatalf("a root at the bound is listed: %+v %v", list, err)
	}
	list, err := listRuns(t.Context(), root, 2)
	assertCodeIs(t, err, e.CodeVerifyFailed, "more than 2 entries")
	if list.Runs != nil {
		t.Fatal("a root over the bound returned a partial listing")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	list, err = ListRuns(canceled, root)
	assertCodeIs(t, err, e.CodeTimeout, "canceled")
	if list.Runs != nil {
		t.Fatal("a canceled listing returned runs")
	}
}

func assertCodeIs(t *testing.T, err error, code e.Code, text string) {
	t.Helper()
	assertCode(t, err, code)
	if !strings.Contains(err.Error(), text) {
		t.Fatalf("error %q does not say %q", err, text)
	}
}

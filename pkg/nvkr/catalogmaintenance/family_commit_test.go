// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestFamilyCommitFailureAndConcurrentEdit(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		fail                          int
		cancel, editNext, editWritten bool
	}{
		{name: "success"}, {name: "first write", fail: 1}, {name: "second write", fail: 2},
		{name: "cancellation after first write", cancel: true}, {name: "concurrent next file", editNext: true},
		{name: "concurrent written file", fail: 2, editWritten: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.Mkdir("inputs", 0700); err != nil {
				t.Fatal(err)
			}
			snapshot := &familySnapshot{files: map[string]familyFileState{}}
			var changes []familyChange
			for _, name := range []string{"inputs/a", "inputs/b"} {
				before, after := []byte("before-"+name), []byte("after-"+name)
				if err := os.WriteFile(name, before, 0644); err != nil {
					t.Fatal(err)
				}
				snapshot.files[name] = familyFileState{digest: sha256.Sum256(before), mode: 0644}
				changes = append(changes, familyChange{path: name, before: before, after: after, exists: true, mode: 0644})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			err := commitFamilyChangesWithRename(ctx, snapshot, changes, []string{".family.lease"}, func(from, to string) error {
				calls++
				if calls == tc.fail {
					return fmt.Errorf("injected rename failure")
				}
				if err := os.Rename(from, to); err != nil {
					return err
				}
				if calls == 2 && tc.cancel {
					cancel()
				}
				if calls == 1 {

					if tc.editNext {
						if err := os.WriteFile("inputs/b", []byte("external"), 0644); err != nil {
							return err
						}
					}
					if tc.editWritten {
						if err := os.WriteFile("inputs/a", []byte("external"), 0644); err != nil {
							return err
						}
					}
				}
				return nil
			})
			if tc.name == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("commit failure reported success")
				}
				if tc.cancel {
					assertCode(t, err, e.CodeTimeout)
				}
				coded, ok := err.(*e.Error)
				if !ok || (coded.Code != e.CodeWriteConflict && coded.Code != e.CodeTimeout && coded.Code != e.CodeInternal) {
					t.Fatalf("uncoded commit failure: %v", err)
				}
			}
			for _, change := range changes {
				got, err := os.ReadFile(change.path)
				if err != nil {
					t.Fatal(err)
				}
				want := change.before
				if tc.name == "success" {
					want = change.after
				}
				if tc.editNext && change.path == "inputs/b" || tc.editWritten && change.path == "inputs/a" {
					want = []byte("external")
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("%s=%q, want %q", change.path, got, want)
				}
			}
			backups, err := filepath.Glob("inputs/.nvkr-family-before-*")
			if err != nil {
				t.Fatal(err)
			}
			if tc.editWritten && len(backups) != 1 {
				t.Fatal("must retain only the conflicting member's recovery backup")
			}
			if !tc.editWritten && len(backups) != 0 {
				t.Fatal("completed rollback left backup files")
			}
		})
	}
}

func TestFamilyCommitNewOutputNoReplace(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(fmt.Sprint(occupied), func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.Mkdir("inputs", 0700); err != nil {
				t.Fatal(err)
			}
			if occupied {
				writeFixture(t, "inputs/new", []byte("external"))
			}
			snapshot := &familySnapshot{files: map[string]familyFileState{}}
			err := commitFamilyChanges(t.Context(), snapshot, []familyChange{{path: "inputs/new", after: []byte("candidate"), mode: 0644}}, nil)
			if occupied {
				assertCode(t, err, e.CodeWriteConflict)
				if string(readFixture(t, "inputs/new")) != "external" {
					t.Fatal("replaced external file")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if string(readFixture(t, "inputs/new")) != "candidate" {
					t.Fatal("new output lost")
				}
			}
		})
	}
}

func TestFamilyReplacementRetainsDisplacedEdits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after bool
	}{
		{name: "ordinary replacement"}, {name: "edit before withdrawal", before: true}, {name: "save after withdrawal", after: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeFixture(t, "target", []byte("original"))
			writeFixture(t, "candidate", []byte("replacement"))
			writeFixture(t, "backup", []byte("original"))
			keep, err := replaceFamilyFile(t.Context(), "target", "candidate", "backup", []byte("original"), 0600, func(from, to string) error {
				if tc.before {
					writeFixture(t, "target", []byte("external"))
				}
				if err := os.Rename(from, to); err != nil {
					return err
				}
				if tc.after {
					writeFixture(t, "target", []byte("external"))
				}
				return nil
			})
			want := "replacement"
			if tc.before || tc.after {
				want = "external"
				assertCode(t, err, e.CodeWriteConflict)
			} else if err != nil {
				t.Fatal(err)
			}
			if string(readFixture(t, "target")) != want {
				t.Fatal("lost target bytes")
			}
			if tc.after && (!keep || string(readFixture(t, "backup")) != "original") {
				t.Fatal("lost displaced original")
			}
		})
	}
}

func TestFamilyCommitRechecksUnchangedInputs(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFixture(t, "definition", []byte("captured input"))
	writeFixture(t, "output", []byte("original"))
	snapshot := &familySnapshot{files: map[string]familyFileState{
		"definition": {digest: sha256.Sum256([]byte("captured input")), mode: 0600},
		"output":     {digest: sha256.Sum256([]byte("original")), mode: 0600},
	}}
	err := commitFamilyChangesWithRename(t.Context(), snapshot, []familyChange{{path: "output", before: []byte("original"), after: []byte("candidate"), exists: true, mode: 0600}}, nil, func(from, to string) error {
		writeFixture(t, "definition", []byte("external edit"))
		return os.Rename(from, to)
	})
	assertCode(t, err, e.CodeWriteConflict)
	if string(readFixture(t, "definition")) != "external edit" || string(readFixture(t, "output")) != "original" {
		t.Fatal("final conflict did not preserve edit and roll back output")
	}
}

func TestFamilyCommitPreservesAlreadyOpenWriter(t *testing.T) {
	t.Chdir(t.TempDir())
	snapshot := &familySnapshot{files: map[string]familyFileState{}}
	var changes []familyChange
	for _, name := range []string{"a", "b"} {
		before, after := []byte("original-"+name), []byte("candidate-"+name)
		writeFixture(t, name, before)
		snapshot.files[name] = familyFileState{digest: sha256.Sum256(before), mode: 0600}
		changes = append(changes, familyChange{path: name, before: before, after: after, exists: true, mode: 0600})
	}
	writer, err := os.OpenFile("a", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	calls := 0
	err = commitFamilyChangesWithRename(t.Context(), snapshot, changes, nil, func(from, to string) error {
		calls++
		if calls == 2 {
			if _, err := writer.WriteAt([]byte("external-a"), 0); err != nil {
				return err
			}
		}
		return os.Rename(from, to)
	})
	assertCode(t, err, e.CodeWriteConflict)
	if string(readFixture(t, "a")) != "external-a" || string(readFixture(t, "b")) != "original-b" {
		t.Fatal("lost displaced inode edit or failed rollback")
	}
}

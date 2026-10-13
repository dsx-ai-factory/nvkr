// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func rawPin(raw string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw))) }

func requireCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func resultFile(t *testing.T, root, name, raw string) FileInput {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return FileInput{Name: name, ExpectedDigest: rawPin(raw)}
}

func TestCaptureHoldsSelectedExactBytes(t *testing.T) {
	root := t.TempDir()
	failed := `<testsuite tests="1" failures="1"><testcase name="failed"><failure/></testcase></testsuite>`
	a := resultFile(t, root, "results/a.xml", failed)
	z := resultFile(t, root, "results/z.xml", "original bytes")
	_ = resultFile(t, root, "private.log", "PRIVATE: excluded from capture")
	snapshot, err := Capture(t.Context(), root, []FileInput{z, a})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	files, err := snapshot.Files(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name != a.Name || files[0].Digest != a.ExpectedDigest || files[0].Size != int64(len(failed)) {
		t.Fatalf("unexpected captured files: %+v", files)
	}
	files[0].Name = "caller mutation"
	files, err = snapshot.Files(t.Context())
	if err != nil || files[0].Name != a.Name {
		t.Fatalf("description changed: %+v, %v", files, err)
	}
	// Replace the original pathname with a different inode after capture.
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("substituted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(root, a.Name)); err != nil {
		t.Fatal(err)
	}
	raw, err := snapshot.Read(t.Context(), a.Name)
	if err != nil || string(raw) != failed {
		t.Fatalf("captured bytes = %q, %v", raw, err)
	}
	raw[0] = '!'
	again, err := snapshot.Read(t.Context(), a.Name)
	if err != nil || string(again) != failed {
		t.Fatalf("caller changed held bytes: %q, %v", again, err)
	}
	_, err = snapshot.Read(t.Context(), "private.log")
	requireCode(t, err, e.CodeVerifyFailed)
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = snapshot.Read(t.Context(), a.Name)
	requireCode(t, err, e.CodeVerifyFailed)
	_, err = snapshot.Files(t.Context())
	requireCode(t, err, e.CodeVerifyFailed)
}

func TestCaptureAdmissionBeforeRootAccess(t *testing.T) {
	valid := FileInput{Name: "results.xml", ExpectedDigest: rawPin("bytes")}
	for _, tc := range []struct {
		name   string
		inputs []FileInput
		code   e.Code
	}{
		{"empty selection", nil, e.CodeUsage},
		{"too many files", make([]FileInput, MaxFiles+1), e.CodeUsage},
		{"duplicate", []FileInput{valid, valid}, e.CodeUnpinnedInput},
		{"missing pin", []FileInput{{Name: valid.Name}}, e.CodeUnpinnedInput},
		{"malformed pin", []FileInput{{Name: valid.Name, ExpectedDigest: "sha256:abc"}}, e.CodeUnpinnedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := Capture(t.Context(), filepath.Join(t.TempDir(), "absent"), tc.inputs)
			if snapshot != nil {
				t.Fatal("rejection returned a snapshot")
			}
			requireCode(t, err, tc.code)
		})
	}
	for _, name := range []string{"", "/absolute", "../escape", "x/../escape", "./result", "x//result", "x/", "x\\result", "result\x00", "result\n", "result xml", strings.Repeat("x", maxPathBytes+1), strings.Repeat("x/", maxPathDepth) + "result"} {
		t.Run(fmt.Sprintf("path %q", name), func(t *testing.T) {
			snapshot, err := Capture(t.Context(), filepath.Join(t.TempDir(), "absent"), []FileInput{{Name: name, ExpectedDigest: valid.ExpectedDigest}})
			if snapshot != nil {
				t.Fatal("unsafe path returned a snapshot")
			}
			requireCode(t, err, e.CodeUnpinnedInput)
		})
	}
}

func TestCaptureRejectsInvalidFilesWithoutPartialSnapshot(t *testing.T) {
	for _, kind := range []string{"wrong pin", "missing", "directory", "file symlink", "parent symlink", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			first := resultFile(t, root, "a.xml", "first")
			bad := FileInput{Name: "z.xml", ExpectedDigest: rawPin("bad")}
			path := filepath.Join(root, bad.Name)
			switch kind {
			case "wrong pin":
				_ = resultFile(t, root, bad.Name, "other bytes")
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "file symlink":
				if err := os.Symlink(filepath.Join(root, first.Name), path); err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				if err := os.Symlink(root, filepath.Join(root, "z")); err != nil {
					t.Fatal(err)
				}
				bad.Name = "z/a.xml"
				bad.ExpectedDigest = first.ExpectedDigest
			case "oversize":
				_ = resultFile(t, root, bad.Name, "bad")
				if err := os.Truncate(path, MaxFileBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := Capture(t.Context(), root, []FileInput{first, bad})
			if snapshot != nil {
				t.Fatal("failed file returned partial snapshot")
			}
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestCaptureRemainingBudgetAndRootSymlink(t *testing.T) {
	root := t.TempDir()
	input := resultFile(t, root, "result.xml", "four")
	directory, _, err := securepath.OpenDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	for _, remaining := range []int64{0, 3} {
		held, err := captureFile(t.Context(), directory, input, remaining)
		if held != nil {
			_ = held.Close()
			t.Fatal("over-budget capture succeeded")
		}
		requireCode(t, err, e.CodeVerifyFailed)
	}
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Capture(t.Context(), link, []FileInput{input})
	if snapshot != nil {
		t.Fatal("symlink root succeeded")
	}
	requireCode(t, err, e.CodeVerifyFailed)
}

func TestCapturePathRecheckRejectsReplacement(t *testing.T) {
	for _, replace := range []string{"file", "parent"} {
		t.Run(replace, func(t *testing.T) {
			root := t.TempDir()
			input := resultFile(t, root, "results/result.xml", "bytes")
			directory, _, err := securepath.OpenDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			parent, parentInfo, err := securepath.OpenChild(directory, "results", true)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			source, original, err := securepath.OpenChild(parent, "result.xml", false)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			if replace == "file" {
				_ = resultFile(t, root, "replacement.xml", "bytes")
				if err := os.Rename(filepath.Join(root, "replacement.xml"), filepath.Join(root, input.Name)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(filepath.Join(root, "results"), filepath.Join(root, "old-results")); err != nil {
					t.Fatal(err)
				}
				_ = resultFile(t, root, input.Name, "bytes")
			}
			err = recheckCapturePath(t.Context(), directory, strings.Split(input.Name, "/"), []os.FileInfo{parentInfo}, original)
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestCaptureCancellationAndConcurrentClose(t *testing.T) {
	root := t.TempDir()
	input := resultFile(t, root, "result.xml", "bytes")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		ctx  context.Context
		code e.Code
	}{{nil, e.CodeUsage}, {canceled, e.CodeTimeout}} {
		snapshot, err := Capture(tc.ctx, root, []FileInput{input})
		if snapshot != nil {
			t.Fatal("invalid context returned a snapshot")
		}
		requireCode(t, err, tc.code)
	}
	snapshot, err := Capture(t.Context(), root, []FileInput{input})
	if err != nil {
		t.Fatal(err)
	}
	_, err = snapshot.Read(canceled, input.Name)
	requireCode(t, err, e.CodeTimeout)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 10 {
				raw, err := snapshot.Read(t.Context(), input.Name)
				if err != nil {
					requireCode(t, err, e.CodeVerifyFailed)
				} else if string(raw) != "bytes" {
					t.Error("read returned substituted bytes")
				}
			}
			if err := snapshot.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	for _, file := range snapshot.files {
		if _, err := file.file.ReadAllContext(t.Context(), MaxFileBytes); err == nil {
			t.Fatal("close left an open held file")
		}
	}
	var absent *Snapshot
	_, err = absent.Read(t.Context(), input.Name)
	requireCode(t, err, e.CodeVerifyFailed)
	if err := absent.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = (&Snapshot{}).Files(t.Context())
	requireCode(t, err, e.CodeVerifyFailed)
}

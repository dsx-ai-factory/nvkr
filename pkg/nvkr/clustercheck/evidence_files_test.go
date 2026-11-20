// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package clustercheck

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func requireCoded(t *testing.T, err error, want e.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error, got nil", want)
	}
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("expected %s error, got %v", want, err)
	}
}

func evidenceTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func openWriter(t *testing.T, root string, maxPayloadBytes int64) *EvidenceFiles {
	t.Helper()
	files, err := OpenEvidenceFiles(context.Background(), root, maxPayloadBytes)
	if err != nil {
		t.Fatalf("opening evidence files: %v", err)
	}
	t.Cleanup(func() { _ = files.Close() })
	return files
}

func directoryEntries(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading evidence directory: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestOpenEvidenceFilesRequiresNewPrivateDirectory(t *testing.T) {
	t.Run("creates a new private directory", func(t *testing.T) {
		root := filepath.Join(evidenceTempDir(t), "nested", "evidence")
		files := openWriter(t, root, 1<<20)
		if files.Directory() != root {
			t.Fatalf("directory %q does not match requested root %q", files.Directory(), root)
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("evidence directory is not a private 0700 directory: %v %v", info, err)
		}
	})
	t.Run("refuses an existing directory and leaves it intact", func(t *testing.T) {
		root := filepath.Join(evidenceTempDir(t), "evidence")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatalf("preparing existing directory: %v", err)
		}
		keep := filepath.Join(root, "prior")
		if err := os.WriteFile(keep, []byte("prior evidence"), 0o600); err != nil {
			t.Fatalf("writing prior evidence: %v", err)
		}
		_, err := OpenEvidenceFiles(context.Background(), root, 1<<20)
		requireCoded(t, err, e.CodeWriteConflict)
		raw, readErr := os.ReadFile(keep)
		if readErr != nil || string(raw) != "prior evidence" {
			t.Fatalf("existing directory was disturbed: %q %v", raw, readErr)
		}
	})
	t.Run("refuses an existing file at the destination", func(t *testing.T) {
		root := filepath.Join(evidenceTempDir(t), "evidence")
		if err := os.WriteFile(root, []byte("occupied"), 0o600); err != nil {
			t.Fatalf("writing destination file: %v", err)
		}
		_, err := OpenEvidenceFiles(context.Background(), root, 1<<20)
		requireCoded(t, err, e.CodeWriteConflict)
	})
	t.Run("refuses invalid openings", func(t *testing.T) {
		cases := map[string]struct {
			root            string
			maxPayloadBytes int64
		}{
			"empty root":       {"", 1 << 20},
			"control rune":     {"evi\x01dence", 1 << 20},
			"unpadded spacing": {" evidence ", 1 << 20},
			"non-canonical":    {strings.TrimSuffix(evidenceTempDir(t), string(filepath.Separator)) + "/./evidence", 1 << 20},
			"zero byte bound":  {filepath.Join(evidenceTempDir(t), "evidence"), 0},
			"negative bounds":  {filepath.Join(evidenceTempDir(t), "evidence"), -1},
			"directory dot":    {".", 1 << 20},
			"relative root":    {"evidence", 1 << 20},
			"nil context":      {filepath.Join(evidenceTempDir(t), "evidence"), 1 << 20},
		}
		for name, testCase := range cases {
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				if name == "nil context" {
					ctx = nil
				}
				_, err := OpenEvidenceFiles(ctx, testCase.root, testCase.maxPayloadBytes)
				requireCoded(t, err, e.CodeUsage)
			})
		}
	})
}

func TestOpenEvidenceFilesRefusesLinkedParent(t *testing.T) {
	parent, outside := evidenceTempDir(t), evidenceTempDir(t)
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, err := OpenEvidenceFiles(context.Background(), filepath.Join(link, "evidence"), 8)
	requireCoded(t, err, e.CodeImmutableViolation)
	if entries := directoryEntries(t, outside); len(entries) != 0 {
		t.Fatalf("wrote through a linked parent: %v", entries)
	}
}

func TestWriteComponentPreservesExactBytes(t *testing.T) {
	root := filepath.Join(evidenceTempDir(t), "evidence")
	files := openWriter(t, root, 1<<20)
	first := []byte{0x00, 0x01, 0xfe, 0xff, 0x0a}
	second := []byte("report body\n")
	for _, component := range []struct {
		name    string
		payload []byte
	}{
		{"inventory.json", first},
		{"agent.log", second},
	} {
		if err := files.WriteComponent(context.Background(), component.name, strings.NewReader(string(component.payload))); err != nil {
			t.Fatalf("writing %s: %v", component.name, err)
		}
		raw, err := os.ReadFile(filepath.Join(root, component.name))
		if err != nil {
			t.Fatalf("reading %s: %v", component.name, err)
		}
		if string(raw) != string(component.payload) {
			t.Fatalf("component %s bytes changed: wrote %q read %q", component.name, component.payload, raw)
		}
	}
	entries := directoryEntries(t, root)
	if len(entries) != 2 || entries[0] != "agent.log" || entries[1] != "inventory.json" {
		t.Fatalf("unexpected directory entries after success: %v", entries)
	}
	info, err := os.Stat(filepath.Join(root, "inventory.json"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("published component is not a private regular file: %v %v", info, err)
	}
}

func TestWriteComponentRefusesOverwrite(t *testing.T) {
	root := filepath.Join(evidenceTempDir(t), "evidence")
	files := openWriter(t, root, 1<<20)
	original := "original bytes"
	if err := files.WriteComponent(context.Background(), "report.json", strings.NewReader(original)); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	err := files.WriteComponent(context.Background(), "report.json", strings.NewReader("replacement"))
	requireCoded(t, err, e.CodeWriteConflict)
	raw, readErr := os.ReadFile(filepath.Join(root, "report.json"))
	if readErr != nil || string(raw) != original {
		t.Fatalf("overwrite replaced completed component: %q %v", raw, readErr)
	}
	if entries := directoryEntries(t, root); len(entries) != 1 {
		t.Fatalf("refused overwrite left entries: %v", entries)
	}
}

func TestWriteComponentRefusesUnsafeNames(t *testing.T) {
	files := openWriter(t, filepath.Join(evidenceTempDir(t), "evidence"), 1<<20)
	longName := strings.Repeat("a", maxComponentNameBytes+1)
	cases := map[string]string{
		"empty":           "",
		"dot":             ".",
		"dot dot":         "..",
		"separator":       "nested/report.json",
		"traversal":       "../escape",
		"absolute":        "/etc/passwd",
		"windows escape":  `nested\report.json`,
		"reserved prefix": incompletePrefix + "report.json",
		"hidden name":     ".report.json",
		"control rune":    "report\x01.json",
		"overlong":        longName,
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			before := directoryEntries(t, files.Directory())
			err := files.WriteComponent(context.Background(), value, strings.NewReader("payload"))
			requireCoded(t, err, e.CodeUsage)
			if after := directoryEntries(t, files.Directory()); len(after) != len(before) {
				t.Fatalf("unsafe name %q left entries: %v", value, after)
			}
		})
	}
	t.Run("admits the 200-byte boundary name", func(t *testing.T) {
		name := strings.Repeat("a", maxComponentNameBytes)
		if err := files.WriteComponent(context.Background(), name, strings.NewReader("payload")); err != nil {
			t.Fatalf("writing boundary name: %v", err)
		}
	})
	t.Run("refuses a closed writer", func(t *testing.T) {
		closed, err := OpenEvidenceFiles(context.Background(), filepath.Join(evidenceTempDir(t), "closed"), 1<<20)
		if err != nil {
			t.Fatalf("opening closed writer: %v", err)
		}
		if err := closed.Close(); err != nil {
			t.Fatalf("closing writer: %v", err)
		}
		if err := closed.WriteComponent(context.Background(), "report.json", strings.NewReader("payload")); err != nil {
			requireCoded(t, err, e.CodeUsage)
		} else {
			t.Fatal("closed writer accepted a component")
		}
	})
}

func TestWriteComponentRefusesSymlinkDestination(t *testing.T) {
	root := filepath.Join(evidenceTempDir(t), "evidence")
	outside := filepath.Join(evidenceTempDir(t), "outside.txt")
	if err := os.WriteFile(outside, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("writing outside file: %v", err)
	}
	files := openWriter(t, root, 1<<20)
	if err := os.Symlink(outside, filepath.Join(root, "report.json")); err != nil {
		t.Fatalf("planting symlink: %v", err)
	}
	err := files.WriteComponent(context.Background(), "report.json", strings.NewReader("payload"))
	requireCoded(t, err, e.CodeWriteConflict)
	raw, readErr := os.ReadFile(outside)
	if readErr != nil || string(raw) != "sentinel" {
		t.Fatalf("symlink destination was followed: %q %v", raw, readErr)
	}
	info, statErr := os.Lstat(filepath.Join(root, "report.json"))
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink destination was replaced: %v %v", info, statErr)
	}
}

func TestWriteComponentBoundsPayloadSize(t *testing.T) {
	root := filepath.Join(evidenceTempDir(t), "evidence")
	files := openWriter(t, root, 16)
	// Exactly 16 bytes: at the bound succeeds, one byte more is refused.
	if err := files.WriteComponent(context.Background(), "kept.txt", strings.NewReader("0123456789abcdef")); err != nil {
		t.Fatalf("boundary-size write: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "kept.txt"))
	if err != nil || string(raw) != "0123456789abcdef" {
		t.Fatalf("boundary-size component bytes changed: %q %v", raw, err)
	}
	err = files.WriteComponent(context.Background(), "large.log", strings.NewReader("0123456789abcdefg"))
	requireCoded(t, err, e.CodeUsage)
	if entries := directoryEntries(t, root); len(entries) != 2 {
		t.Fatalf("unexpected entries after oversize refusal: %v", entries)
	}
}

func TestWriteComponentLeavesPartialWritesDistinguishable(t *testing.T) {
	root := filepath.Join(evidenceTempDir(t), "evidence")
	files := openWriter(t, root, 1<<20)
	kept := "complete component bytes"
	if err := files.WriteComponent(context.Background(), "kept.txt", strings.NewReader(kept)); err != nil {
		t.Fatalf("writing kept component: %v", err)
	}
	failing := errors.New("synthetic source failure")
	err := files.WriteComponent(context.Background(), "broken.log",
		io.MultiReader(strings.NewReader("partial"), &failingReader{err: failing}))
	requireCoded(t, err, e.CodeUnavailable)
	entries := directoryEntries(t, root)
	if len(entries) != 2 {
		t.Fatalf("unexpected entries after partial write: %v", entries)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "broken.log")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed component published a member name: %v", statErr)
	}
	staged, readErr := os.ReadFile(filepath.Join(root, incompletePrefix+"broken.log"))
	if readErr != nil || string(staged) != "partial" {
		t.Fatalf("partial write not distinguishable: %q %v", staged, readErr)
	}
	raw, readErr := os.ReadFile(filepath.Join(root, "kept.txt"))
	if readErr != nil || string(raw) != kept {
		t.Fatalf("completed component disturbed by partial write: %q %v", raw, readErr)
	}
}

func TestWriteComponentRefusesMovedEvidenceDirectory(t *testing.T) {
	parent := evidenceTempDir(t)
	root := filepath.Join(parent, "evidence")
	moved := filepath.Join(parent, "moved")
	files := openWriter(t, root, 1<<20)
	payload := readerFunc(func(buffer []byte) (int, error) {
		if err := os.Rename(root, moved); err != nil {
			t.Fatalf("moving evidence directory: %v", err)
		}
		return copy(buffer, "payload"), io.EOF
	})
	err := files.WriteComponent(context.Background(), "report.json", payload)
	requireCoded(t, err, e.CodeImmutableViolation)
	if _, err := os.Lstat(filepath.Join(moved, "report.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published a component after its directory moved: %v", err)
	}
	want := incompletePrefix + "report.json"
	if entries := directoryEntries(t, moved); len(entries) != 1 || entries[0] != want {
		t.Fatalf("staged bytes were lost: %v", entries)
	}
}

type readerFunc func([]byte) (int, error)

func (read readerFunc) Read(buffer []byte) (int, error) { return read(buffer) }

func TestWriteComponentObservesContext(t *testing.T) {
	files := openWriter(t, filepath.Join(evidenceTempDir(t), "evidence"), 1<<20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := files.WriteComponent(ctx, "report.json", strings.NewReader("payload"))
	requireCoded(t, err, e.CodeTimeout)
}

func TestWriteComponentStopsBeforePublishing(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
		code   e.Code
	}{
		{"reader cancels before EOF", true, e.CodeTimeout},
		{"reader makes no progress", false, e.CodeUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := openWriter(t, filepath.Join(evidenceTempDir(t), "evidence"), 8)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := readerFunc(func(buffer []byte) (int, error) {
				if test.cancel {
					cancel()
					return copy(buffer, "payload"), io.EOF
				}
				return 0, nil
			})
			requireCoded(t, files.WriteComponent(ctx, "report.json", reader), test.code)
			if got := directoryEntries(t, files.Directory()); len(got) != 1 || got[0] != incompletePrefix+"report.json" {
				t.Fatalf("unexpected files after interrupted write: %v", got)
			}
		})
	}
}

func TestPublishComponentLeavesMarkerWhenSyncFails(t *testing.T) {
	files := openWriter(t, filepath.Join(evidenceTempDir(t), "evidence"), 8)
	stage := incompletePrefix + "report.json"
	if err := files.directory.WriteFile(stage, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := files.directory.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	requireCoded(t, files.publishComponent(directory, stage, "report.json"), e.CodeImmutableViolation)
	if got := directoryEntries(t, files.Directory()); len(got) != 2 || got[0] != stage || got[1] != "report.json" {
		t.Fatalf("missing marker for unsynced publication: %v", got)
	}
}

type failingReader struct {
	err error
}

func (reader *failingReader) Read([]byte) (int, error) {
	return 0, reader.err
}

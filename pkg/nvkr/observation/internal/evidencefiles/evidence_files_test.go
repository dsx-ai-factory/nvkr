// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package evidencefiles

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func requireCoded(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("expected %s error, got %v", want, err)
	}
}

func requireEvidenceBytes(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatalf("reading %s: got %q (%v), want %q", path, raw, err, want)
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

func requireEntries(t *testing.T, root string, want ...string) {
	t.Helper()
	if got := directoryEntries(t, root); !slices.Equal(got, want) {
		t.Fatalf("directory %q entries: got %v, want %v", root, got, want)
	}
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
	t.Run("preserves existing destinations", func(t *testing.T) {
		parent := evidenceTempDir(t)
		directory, file := filepath.Join(parent, "occupied-dir"), filepath.Join(parent, "occupied-file")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		for root, sentinel := range map[string]string{directory: filepath.Join(directory, "prior"), file: file} {
			if err := os.WriteFile(sentinel, []byte("prior evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := OpenEvidenceFiles(t.Context(), root, 1<<20)
			requireCoded(t, err, e.CodeWriteConflict)
			requireEvidenceBytes(t, sentinel, "prior evidence")
		}
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
			"filesystem root":  {string(filepath.Separator), 1 << 20},
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
	_, err := OpenEvidenceFiles(t.Context(), filepath.Join(link, "evidence"), 8)
	requireCoded(t, err, e.CodeUsage)
	if !strings.Contains(err.Error(), `"linked"`) {
		t.Fatalf("error omitted the aliased component: %v", err)
	}
	requireEntries(t, outside)
}

func TestOpenEvidenceFilesRefusesSharedWritableParent(t *testing.T) {
	for _, test := range []struct {
		name string
		mode os.FileMode
		path string
	}{
		{"group writable", 0o730, "evidence"},
		{"world writable", 0o703, "nested/evidence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := evidenceTempDir(t)
			if err := os.Chmod(parent, test.mode); err != nil {
				t.Fatal(err)
			}
			_, err := OpenEvidenceFiles(t.Context(), filepath.Join(parent, test.path), 8)
			requireCoded(t, err, e.CodeImmutableViolation)
			requireEntries(t, parent)
		})
	}
}

func TestWriteComponentPreservesExactBytes(t *testing.T) {
	root := filepath.Join(evidenceTempDir(t), "evidence")
	files := openWriter(t, root, 1<<20)
	for _, component := range []struct {
		name    string
		payload string
	}{
		{"admitted-report.bin", "\x00\x01\xfe\xff\n"},
		{"observation.json", "{\"coverage\":\"partial\"}\n"},
	} {
		if err := files.WriteComponent(t.Context(), component.name, strings.NewReader(component.payload)); err != nil {
			t.Fatalf("writing %s: %v", component.name, err)
		}
		requireEvidenceBytes(t, filepath.Join(root, component.name), component.payload)
	}
	requireEntries(t, root, "admitted-report.bin", "observation.json")
	info, err := os.Stat(filepath.Join(root, "admitted-report.bin"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("published component is not a private regular file: %v %v", info, err)
	}
	t.Run("rejects a different stage identity", func(t *testing.T) {
		const name = "pending.json"
		stage := incompletePrefix + name
		if err := files.directory.WriteFile(stage, []byte("candidate"), 0o600); err != nil {
			t.Fatal(err)
		}
		requireCoded(t, files.publishStagedComponent(t.Context(), stage, name, info), e.CodeImmutableViolation)
		requireEntries(t, root, stage, "admitted-report.bin", "observation.json")
		requireEvidenceBytes(t, filepath.Join(root, stage), "candidate")
	})
}

func TestWriteComponentRefusesOverwrite(t *testing.T) {
	root := filepath.Join(evidenceTempDir(t), "evidence")
	files := openWriter(t, root, 1<<20)
	original := "original bytes"
	if err := files.WriteComponent(context.Background(), "report.json", strings.NewReader(original)); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	requireCoded(t, files.WriteComponent(t.Context(), "report.json", strings.NewReader("replacement")), e.CodeWriteConflict)
	requireEvidenceBytes(t, filepath.Join(root, "report.json"), original)
	requireEntries(t, root, "report.json")
}

func TestWriteComponentRefusesUnsafeNames(t *testing.T) {
	files := openWriter(t, filepath.Join(evidenceTempDir(t), "evidence"), 1<<20)
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
		"leading space":   " report.json",
		"trailing space":  "report.json ",
		"control rune":    "report\x01.json",
		"overlong":        strings.Repeat("a", maxComponentNameBytes+1),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			before := directoryEntries(t, files.Directory())
			requireCoded(t, files.WriteComponent(t.Context(), value, strings.NewReader("payload")), e.CodeUsage)
			requireEntries(t, files.Directory(), before...)
		})
	}
	t.Run("admits the 200-byte boundary name", func(t *testing.T) {
		name := strings.Repeat("a", maxComponentNameBytes)
		if err := files.WriteComponent(context.Background(), name, strings.NewReader("payload")); err != nil {
			t.Fatalf("writing boundary name: %v", err)
		}
	})
	t.Run("refuses a closed writer", func(t *testing.T) {
		closed := openWriter(t, filepath.Join(evidenceTempDir(t), "closed"), 1<<20)
		if err := closed.Close(); err != nil {
			t.Fatalf("closing writer: %v", err)
		}
		requireCoded(t, closed.WriteComponent(t.Context(), "report.json", strings.NewReader("payload")), e.CodeUsage)
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
	requireCoded(t, files.WriteComponent(t.Context(), "report.json", strings.NewReader("payload")), e.CodeWriteConflict)
	requireEvidenceBytes(t, outside, "sentinel")
	info, statErr := os.Lstat(filepath.Join(root, "report.json"))
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink destination was replaced: %v %v", info, statErr)
	}
}

func TestWriteComponentBoundsPayloadSize(t *testing.T) {
	root := filepath.Join(evidenceTempDir(t), "evidence")
	files := openWriter(t, root, 16)
	if err := files.WriteComponent(context.Background(), "kept.txt", strings.NewReader("0123456789abcdef")); err != nil {
		t.Fatalf("boundary-size write: %v", err)
	}
	requireEvidenceBytes(t, filepath.Join(root, "kept.txt"), "0123456789abcdef")
	requireCoded(t, files.WriteComponent(t.Context(), "large.log", strings.NewReader("0123456789abcdefg")), e.CodeUsage)
	requireEntries(t, root, incompletePrefix+"large.log", "kept.txt")
}

func TestWriteComponentLeavesPartialWritesDistinguishable(t *testing.T) {
	root := filepath.Join(evidenceTempDir(t), "evidence")
	files := openWriter(t, root, 1<<20)
	kept := "complete component bytes"
	if err := files.WriteComponent(context.Background(), "kept.txt", strings.NewReader(kept)); err != nil {
		t.Fatalf("writing kept component: %v", err)
	}
	payload := io.MultiReader(strings.NewReader("partial"), iotest.ErrReader(errors.New("synthetic source failure")))
	requireCoded(t, files.WriteComponent(t.Context(), "broken.log", payload), e.CodeUnavailable)
	requireEntries(t, root, incompletePrefix+"broken.log", "kept.txt")
	requireEvidenceBytes(t, filepath.Join(root, incompletePrefix+"broken.log"), "partial")
	requireEvidenceBytes(t, filepath.Join(root, "kept.txt"), kept)
}

func TestWriteComponentRefusesEvidenceDirectoryChange(t *testing.T) {
	for _, change := range []string{"moved", "replaced", "permissions"} {
		t.Run(change, func(t *testing.T) {
			parent := evidenceTempDir(t)
			root, retained := filepath.Join(parent, "evidence"), filepath.Join(parent, "moved")
			files := openWriter(t, root, 8)
			payload := readerFunc(func(buffer []byte) (int, error) {
				if change == "permissions" {
					retained = root
					if err := os.Chmod(root, 0o750); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Rename(root, retained); err != nil {
						t.Fatal(err)
					}
					if change == "replaced" {
						if err := os.Mkdir(root, 0o700); err != nil {
							t.Fatal(err)
						}
					}
				}
				return copy(buffer, "payload"), io.EOF
			})
			requireCoded(t, files.WriteComponent(t.Context(), "report.json", payload), e.CodeImmutableViolation)
			requireEntries(t, retained, incompletePrefix+"report.json")
			if change == "replaced" {
				requireEntries(t, root)
			}
		})
	}
}

type readerFunc func([]byte) (int, error)

func (read readerFunc) Read(buffer []byte) (int, error) { return read(buffer) }

func TestWriteComponentObservesContext(t *testing.T) {
	files := openWriter(t, filepath.Join(evidenceTempDir(t), "evidence"), 1<<20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	requireCoded(t, files.WriteComponent(ctx, "report.json", strings.NewReader("payload")), e.CodeTimeout)
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
			requireEntries(t, files.Directory(), incompletePrefix+"report.json")
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
	requireEntries(t, files.Directory(), stage, "report.json")
}

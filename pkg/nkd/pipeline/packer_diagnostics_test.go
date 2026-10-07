// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

type packerDiagnosticTarEntry struct {
	name     string
	typeflag byte
	payload  []byte
	link     string
}

func TestPackerDiagnosticSinkIssuesOnlyOneSealedRoute(t *testing.T) {
	for _, mode := range []string{"sealed", "open", "failed sealing", "failed target sealing", "multiple routes", "absent"} {
		t.Run(mode, func(t *testing.T) {
			root, sink, target, manifest := newPackerDiagnosticFixture(t, "plan/retained")
			defer sink.Close()
			defer target.close()
			if sink.RetainedPath() != "" {
				t.Fatal("open sink issued a transport route")
			}
			switch mode {
			case "open":
				return
			case "failed sealing":
				if err := sink.packer.Close(); err != nil {
					t.Fatal(err)
				}
			case "failed target sealing":
				if err := target.root.Close(); err != nil {
					t.Fatal(err)
				}
			case "multiple routes":
				other, err := sink.start(t.Context(), "plan/other", manifest, fmt.Sprintf("sha256:%x", sha256.Sum256(manifest)))
				if err != nil {
					t.Fatal(err)
				}
				if err := other.close(); err != nil {
					t.Fatal(err)
				}
			case "absent":
				if (*PackerDiagnosticSink)(nil).RetainedPath() != "" {
					t.Fatal("absent sink issued a path")
				}
				return
			}
			if err := target.close(); mode == "failed target sealing" {
				if err == nil {
					t.Fatal("wanted target sealing failure")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			err := sink.Close()
			if mode == "failed sealing" {
				if err == nil {
					t.Fatal("wanted sealing failure")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			route := sink.RetainedPath()
			if mode != "sealed" {
				if route != "" {
					t.Fatal("unsealed/ambiguous sink issued a path")
				}
				return
			}
			if !strings.HasPrefix(route, "diagnostics/packer/run-") || strings.ContainsAny(route, "\r\n") {
				t.Fatalf("unsafe issued route %q", route)
			}
			info, err := os.Stat(filepath.Join(root, route, packerDiagnosticManifestName))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o444 {
				t.Fatalf("issued route lacks sealed manifest: %v", err)
			}
		})
	}
}

func TestPackerDiagnosticSinkRetainsExactManifestAndRegularLogs(t *testing.T) {
	root, sink, target, manifest := newPackerDiagnosticFixture(t, "plan/success")
	archive := newHeldPackerDiagnosticArchive(t, []packerDiagnosticTarEntry{
		{name: "./", typeflag: tar.TypeDir},
		{name: "serial", typeflag: tar.TypeDir},
		{name: "serial/console.log", typeflag: tar.TypeReg, payload: []byte("booted\n")},
		{name: "packer.log", typeflag: tar.TypeReg, payload: []byte("complete\n")},
	})
	if err := target.retainBuildLogs(t.Context(), archive); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := target.close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	routes, err := filepath.Glob(filepath.Join(root, packerDiagnosticDirectory,
		packerDiagnosticProducerDir, "run-*"))
	if err != nil || len(routes) != 1 {
		t.Fatalf("diagnostic routes = %#v, error=%v", routes, err)
	}
	retainedManifest, err := os.ReadFile(filepath.Join(routes[0], packerDiagnosticManifestName))
	if err != nil || !bytes.Equal(retainedManifest, manifest) {
		t.Fatalf("retained manifest = %q, error=%v", retainedManifest, err)
	}
	for relative, want := range map[string]string{
		"packer.log": "complete\n", "serial/console.log": "booted\n",
	} {
		raw, err := os.ReadFile(filepath.Join(routes[0], packerDiagnosticLogsName, filepath.FromSlash(relative)))
		if err != nil || string(raw) != want {
			t.Fatalf("retained log %s = %q, error=%v", relative, raw, err)
		}
	}
	for _, relative := range []string{"", packerDiagnosticLogsName, packerDiagnosticLogsName + "/serial", packerDiagnosticManifestName} {
		info, err := os.Stat(filepath.Join(routes[0], relative))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o555)
		if relative == packerDiagnosticManifestName {
			want = 0o444
		}
		if info.Mode().Perm() != want {
			t.Fatalf("retained %q mode = %#o, want %#o", relative, info.Mode().Perm(), want)
		}
	}
}

func TestPackerDiagnosticSinkRejectsManifestDigestMismatchBeforeCreatingOutput(t *testing.T) {
	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewPackerDiagnosticSink(t.Context(), directory)
	_ = directory.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	manifest := []byte(`{"schema":"nkd.dev/build-materials/v2","materials":[]}` + "\n")
	_, err = sink.start(t.Context(), "plan/digest-mismatch", manifest, "sha256:"+repeatHex("0"))
	if code(err) != e.CodeUnverifiedRef {
		t.Fatalf("manifest digest mismatch error = %v, want %s", err, e.CodeUnverifiedRef)
	}
	if _, statErr := os.Stat(filepath.Join(root, packerDiagnosticDirectory)); !os.IsNotExist(statErr) {
		t.Fatalf("manifest digest mismatch created diagnostic output: %v", statErr)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if sink.RetainedPath() != "" {
		t.Fatal("failed retention issued a transport route")
	}
}

func TestPackerDiagnosticArchiveRejectsUnsafeEntriesAndCleansStaging(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries []packerDiagnosticTarEntry
	}{
		{name: "traversal", entries: []packerDiagnosticTarEntry{{name: "../escape", typeflag: tar.TypeReg, payload: []byte("x")}}},
		{name: "absolute", entries: []packerDiagnosticTarEntry{{name: "/escape", typeflag: tar.TypeReg, payload: []byte("x")}}},
		{name: "symlink", entries: []packerDiagnosticTarEntry{{name: "link", typeflag: tar.TypeSymlink, link: "target"}}},
		{name: "hard link", entries: []packerDiagnosticTarEntry{{name: "hard", typeflag: tar.TypeLink, link: "target"}}},
		{name: "character device", entries: []packerDiagnosticTarEntry{{name: "device", typeflag: tar.TypeChar}}},
		{name: "fifo", entries: []packerDiagnosticTarEntry{{name: "fifo", typeflag: tar.TypeFifo}}},
		{name: "duplicate", entries: []packerDiagnosticTarEntry{
			{name: "same.log", typeflag: tar.TypeReg, payload: []byte("one")},
			{name: "same.log", typeflag: tar.TypeReg, payload: []byte("two")},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, sink, target, _ := newPackerDiagnosticFixture(t, "plan/"+test.name)
			archive := newHeldPackerDiagnosticArchive(t, test.entries)
			err := target.retainBuildLogs(t.Context(), archive)
			if code(err) != e.CodeVerifyFailed {
				t.Fatalf("unsafe archive error = %v, want %s", err, e.CodeVerifyFailed)
			}
			_ = archive.Close()
			_ = target.close()
			_ = sink.Close()
			assertNoPackerDiagnosticStages(t, root)
			assertOnlyPackerDiagnosticManifests(t, root)
		})
	}
}

func TestPackerDiagnosticArchiveEnforcesCountAndSizeBounds(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries []packerDiagnosticTarEntry
		limits  packerDiagnosticLimits
		text    string
	}{
		{
			name: "entry count", entries: []packerDiagnosticTarEntry{
				{name: "one.log", typeflag: tar.TypeReg}, {name: "two.log", typeflag: tar.TypeReg},
			}, limits: packerDiagnosticLimits{entries: 1, files: 2, fileBytes: 8, payloadBytes: 8}, text: "entry bound",
		},
		{
			name: "file count", entries: []packerDiagnosticTarEntry{
				{name: "one.log", typeflag: tar.TypeReg}, {name: "two.log", typeflag: tar.TypeReg},
			}, limits: packerDiagnosticLimits{entries: 2, files: 1, fileBytes: 8, payloadBytes: 8}, text: "file bound",
		},
		{
			name: "per-file bytes", entries: []packerDiagnosticTarEntry{
				{name: "large.log", typeflag: tar.TypeReg, payload: []byte("four")},
			}, limits: packerDiagnosticLimits{entries: 1, files: 1, fileBytes: 3, payloadBytes: 8}, text: "byte bounds",
		},
		{
			name: "aggregate bytes", entries: []packerDiagnosticTarEntry{
				{name: "one.log", typeflag: tar.TypeReg, payload: []byte("aa")},
				{name: "two.log", typeflag: tar.TypeReg, payload: []byte("bb")},
			}, limits: packerDiagnosticLimits{entries: 2, files: 2, fileBytes: 2, payloadBytes: 3}, text: "byte bounds",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, sink, target, _ := newPackerDiagnosticFixture(t, "plan/bounds/"+test.name)
			archive := newHeldPackerDiagnosticArchive(t, test.entries)
			err := target.retainBuildLogsWithLimits(t.Context(), archive, test.limits)
			if code(err) != e.CodeVerifyFailed || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("bounds error = %v, want %s containing %q", err, e.CodeVerifyFailed, test.text)
			}
			_ = archive.Close()
			_ = target.close()
			_ = sink.Close()
		})
	}
}

func TestPackerDiagnosticRetentionHandlesCancellationNoLogsAndNoReplace(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, string, *PackerDiagnosticSink, *packerDiagnosticTarget)
	}{
		{name: "canceled", run: func(t *testing.T, root string, _ *PackerDiagnosticSink, target *packerDiagnosticTarget) {
			archive := newHeldPackerDiagnosticArchive(t, []packerDiagnosticTarEntry{{
				name: "serial.log", typeflag: tar.TypeReg, payload: []byte("serial")},
			})
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := target.retainBuildLogs(ctx, archive); code(err) != e.CodeTimeout {
				t.Fatalf("canceled retention error = %v, want %s", err, e.CodeTimeout)
			}
			_ = archive.Close()
			assertNoPackerDiagnosticLogs(t, root)
		}},
		{name: "build failed before logs existed", run: func(t *testing.T, root string, _ *PackerDiagnosticSink, target *packerDiagnosticTarget) {
			archive := newHeldPackerDiagnosticArchive(t, []packerDiagnosticTarEntry{{name: "./", typeflag: tar.TypeDir}})
			defer archive.Close()
			if err := target.retainBuildLogs(t.Context(), archive); err != nil {
				t.Fatal(err)
			}
			assertNoPackerDiagnosticLogs(t, root)
			assertNoPackerDiagnosticStages(t, root)
		}},
		{name: "logs are no-replace", run: func(t *testing.T, root string, _ *PackerDiagnosticSink, target *packerDiagnosticTarget) {
			first := newHeldPackerDiagnosticArchive(t, []packerDiagnosticTarEntry{{
				name: "serial.log", typeflag: tar.TypeReg, payload: []byte("first")},
			})
			if err := target.retainBuildLogs(t.Context(), first); err != nil {
				t.Fatal(err)
			}
			_ = first.Close()
			second := newHeldPackerDiagnosticArchive(t, []packerDiagnosticTarEntry{{
				name: "serial.log", typeflag: tar.TypeReg, payload: []byte("second")},
			})
			if err := target.retainBuildLogs(t.Context(), second); code(err) != e.CodeImmutableViolation {
				t.Fatalf("no-replace error = %v, want %s", err, e.CodeImmutableViolation)
			}
			_ = second.Close()
			matches, _ := filepath.Glob(filepath.Join(root, packerDiagnosticDirectory,
				packerDiagnosticProducerDir, "run-*", packerDiagnosticLogsName, "serial.log"))
			if len(matches) != 1 {
				t.Fatalf("retained serial logs = %#v", matches)
			}
			raw, err := os.ReadFile(matches[0])
			if err != nil || string(raw) != "first" {
				t.Fatalf("no-replace changed original log to %q, error=%v", raw, err)
			}
			assertNoPackerDiagnosticStages(t, root)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, sink, target, manifest := newPackerDiagnosticFixture(t, "plan/state/"+test.name)
			test.run(t, root, sink, target)
			if err := target.close(); err != nil {
				t.Fatal(err)
			}
			if err := sink.Close(); err != nil {
				t.Fatal(err)
			}
			matches, _ := filepath.Glob(filepath.Join(root, packerDiagnosticDirectory,
				packerDiagnosticProducerDir, "run-*", packerDiagnosticManifestName))
			if len(matches) != 1 {
				t.Fatalf("retained manifests = %#v", matches)
			}
			raw, err := os.ReadFile(matches[0])
			if err != nil || !bytes.Equal(raw, manifest) {
				t.Fatalf("retained exact manifest = %q, error=%v", raw, err)
			}
		})
	}
}

func TestPackerDiagnosticSinkAdmissionFailures(t *testing.T) {
	for _, name := range []string{"nil context", "canceled context", "nil root", "regular file", "closed root"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			directory, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			ctx := t.Context()
			want := e.CodeEngineCapability
			switch name {
			case "nil context":
				ctx = nil
				want = e.CodeUsage
			case "canceled context":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
				want = e.CodeTimeout
			case "nil root":
				_ = directory.Close()
				directory = nil
			case "regular file":
				_ = directory.Close()
				directory, err = os.Create(filepath.Join(root, "file"))
				if err != nil {
					t.Fatal(err)
				}
				defer directory.Close()
			case "closed root":
				_ = directory.Close()
			}
			sink, err := NewPackerDiagnosticSink(ctx, directory)
			if sink != nil || code(err) != want {
				t.Fatalf("sink=%v error=%v, want %s", sink, err, want)
			}
			if _, err := os.Stat(filepath.Join(root, packerDiagnosticDirectory)); !os.IsNotExist(err) {
				t.Fatalf("failed admission created diagnostics: %v", err)
			}
		})
	}
}

func TestPackerDiagnosticSinkRejectsConflictsAndClosedState(t *testing.T) {
	for _, name := range []string{"nil sink", "closed sink", "duplicate subject", "existing directory", "existing file", "existing symlink"} {
		t.Run(name, func(t *testing.T) {
			root, sink, target, manifest := newPackerDiagnosticFixture(t, "plan/one")
			defer sink.Close()
			defer target.close()
			want := e.CodeEngineCapability
			subject := "plan/two"
			selected := sink
			switch name {
			case "nil sink":
				selected = nil
			case "closed sink":
				_ = sink.Close()
			case "duplicate subject":
				subject = "plan/one"
				want = e.CodeImmutableViolation
			default:
				// Use a fresh held root so ensureRoots must handle the conflict.
				other := t.TempDir()
				path := filepath.Join(other, packerDiagnosticDirectory)
				var err error
				switch name {
				case "existing directory":
					err = os.Mkdir(path, 0o700)
				case "existing file":
					err = os.WriteFile(path, []byte("unchanged"), 0o600)
				case "existing symlink":
					err = os.Symlink(root, path)
				}
				if err != nil {
					t.Fatal(err)
				}
				directory, err := os.Open(other)
				if err != nil {
					t.Fatal(err)
				}
				selected, err = NewPackerDiagnosticSink(t.Context(), directory)
				_ = directory.Close()
				if err != nil {
					t.Fatal(err)
				}
				defer selected.Close()
				want = e.CodeImmutableViolation
			}
			got, err := selected.start(t.Context(), subject, manifest, fmt.Sprintf("sha256:%x", sha256.Sum256(manifest)))
			if got != nil || code(err) != want {
				t.Fatalf("target=%v error=%v, want %s", got, err, want)
			}
			assertOnlyPackerDiagnosticManifests(t, root)
		})
	}
}

func TestPackerDiagnosticSinkRoutesDistinctSubjects(t *testing.T) {
	root, sink, first, manifest := newPackerDiagnosticFixture(t, "plan/one")
	defer sink.Close()
	defer first.close()
	second, err := sink.start(t.Context(), "plan/two", manifest, fmt.Sprintf("sha256:%x", sha256.Sum256(manifest)))
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	routes, err := filepath.Glob(filepath.Join(root, packerDiagnosticDirectory, packerDiagnosticProducerDir, "run-*"))
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes=%v error=%v", routes, err)
	}
}

func TestPackerDiagnosticClosedTargetCannotRecordIncomplete(t *testing.T) {
	root, sink, target, _ := newPackerDiagnosticFixture(t, "plan/closed-marker")
	defer sink.Close()
	if err := target.close(); err != nil {
		t.Fatal(err)
	}
	if err := target.recordIncomplete(t.Context(), e.New(e.CodeVerifyFailed, "logs missing")); code(err) != e.CodeEngineCapability {
		t.Fatalf("closed target marker error=%v, want %s", err, e.CodeEngineCapability)
	}
	assertOnlyPackerDiagnosticManifests(t, root)
}

type cancelAfterDiagnosticWrite struct {
	context.Context
	cancel context.CancelFunc
	root   string
}

func (ctx cancelAfterDiagnosticWrite) Err() error {
	files, _ := filepath.Glob(filepath.Join(ctx.root, packerDiagnosticDirectory, packerDiagnosticProducerDir,
		"run-*", ".logs-*", "first.log"))
	if len(files) == 1 {
		if raw, err := os.ReadFile(files[0]); err == nil && string(raw) == "first\n" {
			ctx.cancel()
		}
	}
	return ctx.Context.Err()
}

func TestPackerDiagnosticCancellationDuringExtractionRemovesPartialLogs(t *testing.T) {
	root, sink, target, _ := newPackerDiagnosticFixture(t, "plan/cancel-during-extraction")
	defer sink.Close()
	defer target.close()
	archive := newHeldPackerDiagnosticArchive(t, []packerDiagnosticTarEntry{
		{name: "first.log", typeflag: tar.TypeReg, payload: []byte("first\n")},
		{name: "second.log", typeflag: tar.TypeReg, payload: []byte("second\n")},
	})
	defer archive.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := target.retainBuildLogs(cancelAfterDiagnosticWrite{Context: ctx, cancel: cancel, root: root}, archive)
	if code(err) != e.CodeTimeout || ctx.Err() == nil {
		t.Fatalf("mid-extraction cancellation error=%v", err)
	}
	assertNoPackerDiagnosticStages(t, root)
	assertOnlyPackerDiagnosticManifests(t, root)
}

func assertOnlyPackerDiagnosticManifests(t *testing.T, root string) {
	t.Helper()
	files := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			if entry.Name() != packerDiagnosticManifestName || !entry.Type().IsRegular() {
				t.Errorf("unexpected retained entry outside manifest: %s", path)
			}
			files++
		}
		return nil
	})
	if err != nil || files != 1 {
		t.Fatalf("retained files=%d error=%v, want one manifest", files, err)
	}
}

func newPackerDiagnosticFixture(t *testing.T, subject string) (string, *PackerDiagnosticSink,
	*packerDiagnosticTarget, []byte,
) {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() { makePackerDiagnosticsRemovable(root) })
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := NewPackerDiagnosticSink(t.Context(), directory)
	_ = directory.Close()
	if err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schema":"nkd.dev/build-materials/v2","materials":[]}` + "\n")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	target, err := sink.start(t.Context(), subject, manifest, digest)
	if err != nil {
		_ = sink.Close()
		t.Fatal(err)
	}
	return root, sink, target, manifest
}

func newHeldPackerDiagnosticArchive(t *testing.T, entries []packerDiagnosticTarEntry) *heldfile.File {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name, Typeflag: entry.typeflag, Linkname: entry.link,
			Mode: 0o600, Size: int64(len(entry.payload)),
		}
		if entry.typeflag == tar.TypeDir {
			header.Mode = 0o700
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.payload) != 0 {
			if _, err := writer.Write(entry.payload); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	held, err := heldfile.FromBytesContext(t.Context(), raw.Bytes(), heldfile.Options{
		Label: "test-packer-diagnostic-archive", MaxBytes: maxPackerDiagnosticArchiveBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func assertNoPackerDiagnosticLogs(t *testing.T, root string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, packerDiagnosticDirectory,
		packerDiagnosticProducerDir, "run-*", packerDiagnosticLogsName))
	if err != nil || len(matches) != 0 {
		t.Fatalf("unexpected retained Packer logs %#v, error=%v", matches, err)
	}
}

func assertNoPackerDiagnosticStages(t *testing.T, root string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, packerDiagnosticDirectory,
		packerDiagnosticProducerDir, "run-*", ".logs-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("unexpected Packer diagnostic staging paths %#v, error=%v", matches, err)
	}
}

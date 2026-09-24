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
	for _, relative := range []string{"", packerDiagnosticLogsName, packerDiagnosticManifestName} {
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
			logs, globErr := filepath.Glob(filepath.Join(root, packerDiagnosticDirectory,
				packerDiagnosticProducerDir, "run-*", ".logs-*"))
			if globErr != nil || len(logs) != 0 {
				t.Fatalf("unsafe archive left staging paths %#v, error=%v", logs, globErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, "escape")); statErr == nil {
				t.Fatal("unsafe archive wrote outside its diagnostic route")
			}
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
		{name: "build failed before logs existed", run: func(t *testing.T, root string, _ *PackerDiagnosticSink, _ *packerDiagnosticTarget) {
			assertNoPackerDiagnosticLogs(t, root)
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

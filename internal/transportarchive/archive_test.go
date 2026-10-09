// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package transportarchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func TestCopyExact(t *testing.T) {
	for _, test := range []struct {
		name     string
		source   string
		size     int64
		cancel   bool
		want     string
		wantCode nkderr.Code
	}{
		{name: "exact", source: "payload", size: 7, want: "payload"},
		{name: "leaves trailing bytes", source: "payload-next", size: 7, want: "payload"},
		{name: "empty", size: 0},
		{name: "short", source: "x", size: 2, want: "x", wantCode: nkderr.CodeVerifyFailed},
		{name: "negative", size: -1, wantCode: nkderr.CodeVerifyFailed},
		{name: "canceled", source: "payload", size: 7, cancel: true, wantCode: nkderr.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.cancel {
				cancel()
			}
			source := strings.NewReader(test.source)
			var destination bytes.Buffer
			written, err := CopyExact(ctx, &destination, source, test.size)
			var coded *nkderr.Error
			if test.wantCode == "" {
				if err != nil || written != test.size || int64(source.Len()) != int64(len(test.source))-test.size {
					t.Fatalf("written=%d remaining=%d error=%v", written, source.Len(), err)
				}
			} else if !errors.As(err, &coded) || coded.Code != test.wantCode {
				t.Fatalf("error=%v, want %s", err, test.wantCode)
			}
			if destination.String() != test.want {
				t.Fatalf("copied=%q, want %q", destination.String(), test.want)
			}
		})
	}
}

func TestTransportArchiveRoundTripIsDeterministicAndExact(t *testing.T) {
	producer := t.TempDir()
	if err := os.MkdirAll(filepath.Join(producer, ".nkd/work/out/tool/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(producer, ".nkd/work/out/tool/bin/tool")
	if err := os.WriteFile(program, []byte("exact bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bin/tool", filepath.Join(producer, ".nkd/work/out/tool/current")); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(producer, ".nkd/work/results/build-tool.json")
	if err := os.MkdirAll(filepath.Dir(result), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result, []byte(`{"sha256":"sha256:abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{".nkd/work/out/tool", ".nkd/work/results/build-tool.json"}
	one := filepath.Join(producer, ".carrier", "one.tar")
	two := filepath.Join(producer, ".carrier", "two.tar")
	if err := Pack(context.Background(), one, producer, paths); err != nil {
		t.Fatal(err)
	}
	if err := Pack(context.Background(), two, producer, paths); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(one)
	b2, _ := os.ReadFile(two)
	if !bytes.Equal(b1, b2) {
		t.Fatal("deterministic transport carrier changed for identical inputs")
	}

	consumer := t.TempDir()
	if err := Unpack(context.Background(), one, consumer, paths); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(consumer, ".nkd/work/out/tool/bin/tool"))
	if err != nil || string(got) != "exact bytes\n" {
		t.Fatalf("transported bytes = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(consumer, ".nkd/work/out/tool/bin/tool"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("executable mode was not preserved: %v %v", info, err)
	}
	link, err := os.Readlink(filepath.Join(consumer, ".nkd/work/out/tool/current"))
	if err != nil || link != "bin/tool" {
		t.Fatalf("safe relative symlink was not preserved: %q %v", link, err)
	}
	if err := Unpack(context.Background(), one, consumer, paths); err == nil {
		t.Fatal("destination overwrite must fail closed")
	}
	if err := Pack(context.Background(), one, producer, paths); err == nil {
		t.Fatal("archive overwrite must fail closed")
	}
}

func TestTransportArchiveModesIgnoreProcessUmask(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	outputs := t.TempDir()
	for _, mask := range []string{"022", "077"} {
		t.Run(mask, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestTransportArchiveModesHelper$", "-test.timeout=20s")
			command.Env = append(os.Environ(), "NKD_TRANSPORT_UMASK="+mask,
				"NKD_TRANSPORT_OUTPUT="+filepath.Join(outputs, mask))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("isolated umask %s extraction: %v\n%s", mask, err, output)
			}
		})
	}
	if t.Failed() {
		return
	}
	one, err := os.ReadFile(filepath.Join(outputs, "022", "carrier.tar"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := os.ReadFile(filepath.Join(outputs, "077", "carrier.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) {
		t.Fatal("file-only Helm chart repacking changed with the extracting process's umask")
	}
}

func TestTransportArchiveModesHelper(t *testing.T) {
	maskText := os.Getenv("NKD_TRANSPORT_UMASK")
	if maskText == "" {
		t.Skip("isolated subprocess helper")
	}
	mask, err := strconv.ParseUint(maskText, 8, 32)
	if err != nil {
		t.Fatal(err)
	}
	// This helper is the sole test selected in its child process. Never change
	// the parent's umask: other tests and goroutines share that process setting.
	previous := unix.Umask(int(mask))
	defer unix.Umask(previous)
	root := os.Getenv("NKD_TRANSPORT_OUTPUT")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "chart.tgz")
	writeTestTarGz(t, archive, []*tar.Header{
		{Name: "chart/Chart.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		{Name: "chart/templates/deployment.yaml", Typeflag: tar.TypeReg, Mode: 0o640, Size: 4},
		{Name: "chart/bin/hook", Typeflag: tar.TypeReg, Mode: 0o755, Size: 4},
	})
	destination := filepath.Join(root, "extracted")
	if _, err := DetectAndUnpackChartRoot(t.Context(), archive, destination); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]fs.FileMode{
		"chart": 0o755, "chart/templates": 0o755, "chart/bin": 0o755,
		"chart/Chart.yaml": 0o644, "chart/templates/deployment.yaml": 0o640,
		"chart/bin/hook": 0o755,
	} {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("mode of %s = %04o, want %04o", name, info.Mode().Perm(), mode)
		}
	}
	if err := Pack(t.Context(), filepath.Join(root, "carrier.tar"), root, []string{"extracted/chart"}); err != nil {
		t.Fatal(err)
	}
	// A later explicit directory header overrides its earlier implicit default.
	archive = filepath.Join(root, "restricted.tar")
	writeTestTar(t, archive, []*tar.Header{
		{Name: "private/first", Typeflag: tar.TypeDir, Mode: 0o700},
		{Name: "private/first/file", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		{Name: "private/closed/file", Typeflag: tar.TypeReg, Mode: 0o400, Size: 4},
		{Name: "private/closed", Typeflag: tar.TypeDir, Mode: 0o700},
		{Name: "private", Typeflag: tar.TypeDir, Mode: 0o750},
	})
	destination = filepath.Join(root, "restricted")
	if err := Unpack(t.Context(), archive, destination, []string{"private"}); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]fs.FileMode{
		"private": 0o750, "private/closed": 0o700, "private/closed/file": 0o400,
		"private/first": 0o700, "private/first/file": 0o644,
	} {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("mode of %s = %04o, want %04o", name, info.Mode().Perm(), mode)
		}
	}
}

func TestUnpackOpenUsesHeldArchiveAfterPathReplacement(t *testing.T) {
	producer := t.TempDir()
	if err := os.WriteFile(filepath.Join(producer, "artifact"), []byte("held bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(producer, "carrier.tar")
	if err := Pack(t.Context(), archive, producer, []string{"artifact"}); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := os.Rename(archive, filepath.Join(producer, "moved.tar")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := UnpackOpen(t.Context(), held, destination, []string{"artifact"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "artifact"))
	if err != nil || string(got) != "held bytes" {
		t.Fatalf("held archive output = %q, %v", got, err)
	}
}

func TestUnpackOpenRejectsInvalidHeldArchives(t *testing.T) {
	for _, test := range []struct {
		name        string
		open        func(*testing.T) *os.File
		wantMessage string
	}{
		{name: "absent", open: func(*testing.T) *os.File { return nil }, wantMessage: "is absent"},
		{name: "directory", open: func(t *testing.T) *os.File {
			file, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return file
		}, wantMessage: "not a regular file"},
		{name: "fifo", open: func(t *testing.T) *os.File {
			path := filepath.Join(t.TempDir(), "archive.fifo")
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			return os.NewFile(uintptr(fd), path)
		}, wantMessage: "not a regular file"},
		{name: "oversized", open: func(t *testing.T) *os.File {
			path := filepath.Join(t.TempDir(), "oversized.tar")
			file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(maxTransportBytes + 1); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			return file
		}, wantMessage: fmt.Sprint(maxTransportBytes)},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := test.open(t)
			if archive != nil {
				defer archive.Close()
			}
			err := UnpackOpen(t.Context(), archive, t.TempDir(), []string{"artifact"})
			if transportCode(err) != nkderr.CodeVerifyFailed {
				t.Fatalf("invalid held archive returned %v, want verify-failed", err)
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("invalid held archive error %q does not contain %q", err, test.wantMessage)
			}
		})
	}
}

func TestTransportArchiveRejectsUntrustedTarStructures(t *testing.T) {
	tests := []struct {
		name    string
		headers []*tar.Header
	}{
		{"traversal", []*tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Size: 1}}},
		{"absolute", []*tar.Header{{Name: "/escape", Typeflag: tar.TypeReg, Size: 1}}},
		{"directory-alias", []*tar.Header{{Name: "allowed", Typeflag: tar.TypeDir}, {Name: "allowed/", Typeflag: tar.TypeDir}}},
		{"directory-traversal", []*tar.Header{{Name: "allowed/../escape/", Typeflag: tar.TypeDir}}},
		{"directory-repeated-slash", []*tar.Header{{Name: "allowed//", Typeflag: tar.TypeDir}}},
		{"unexpected", []*tar.Header{{Name: "other/file", Typeflag: tar.TypeReg, Size: 1}}},
		{"hard-link", []*tar.Header{{Name: "allowed/hard", Typeflag: tar.TypeLink, Linkname: "allowed/file"}}},
		{"absolute-symlink", []*tar.Header{{Name: "allowed/link", Typeflag: tar.TypeSymlink, Linkname: "/escape"}}},
		{"symlink-parent-directory", []*tar.Header{
			{Name: "allowed/link", Typeflag: tar.TypeSymlink, Linkname: "inside"},
			{Name: "allowed/link/subdir", Typeflag: tar.TypeDir},
		}},
		{"symlink-parent-link", []*tar.Header{
			{Name: "allowed/link", Typeflag: tar.TypeSymlink, Linkname: "inside"},
			{Name: "allowed/link/child", Typeflag: tar.TypeSymlink, Linkname: "target"},
		}},
		{"escaping-symlink", []*tar.Header{{Name: "allowed/link", Typeflag: tar.TypeSymlink, Linkname: "../../escape"}}},
		{"symlink-parent", []*tar.Header{
			{Name: "allowed/dir", Typeflag: tar.TypeSymlink, Linkname: "inside"},
			{Name: "allowed/dir/file", Typeflag: tar.TypeReg, Size: 1},
		}},
		{"duplicate", []*tar.Header{
			{Name: "allowed/file", Typeflag: tar.TypeReg, Size: 1},
			{Name: "allowed/file", Typeflag: tar.TypeReg, Size: 1},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "bad.tar")
			writeTestTar(t, archive, tc.headers)
			root := t.TempDir()
			if err := Unpack(context.Background(), archive, root, []string{"allowed"}); transportCode(err) != nkderr.CodeVerifyFailed {
				t.Fatalf("untrusted %s carrier error = %v, want verify-failed", tc.name, err)
			}
			if _, err := os.Lstat(filepath.Join(root, "allowed")); !os.IsNotExist(err) {
				t.Fatalf("failed extraction published partial destination: %v", err)
			}
		})
	}
}

func TestPackRejectsEscapingSymlinkAndCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "allowed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", filepath.Join(root, "allowed", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := Pack(context.Background(), filepath.Join(root, ".carrier", "bad.tar"), root, []string{"allowed"}); err == nil {
		t.Fatal("escaping source symlink was accepted")
	}
	if err := os.Remove(filepath.Join(root, "allowed", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "allowed", "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Pack(ctx, filepath.Join(root, ".carrier", "canceled.tar"), root, []string{"allowed"}); transportCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("canceled pack error = %v, want retryable timeout", err)
	}
	archive := filepath.Join(root, ".carrier", "ok.tar")
	if err := Pack(context.Background(), archive, root, []string{"allowed"}); err != nil {
		t.Fatal(err)
	}
	if err := Unpack(ctx, archive, t.TempDir(), []string{"allowed"}); transportCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("canceled unpack error = %v, want retryable timeout", err)
	}
}

func TestTransportRejectsNilAndCanceledContextBeforeIO(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing-root")
	missingArchive := filepath.Join(t.TempDir(), "missing.tar")

	//nolint:staticcheck // Passing nil is the admission behavior under test.
	if err := Pack(nil, missingArchive, missingRoot, []string{"allowed"}); transportCode(err) != nkderr.CodeUsage {
		t.Fatalf("nil pack context error = %v, want usage", err)
	}
	//nolint:staticcheck // Passing nil is the admission behavior under test.
	if err := Unpack(nil, missingArchive, missingRoot, []string{"allowed"}); transportCode(err) != nkderr.CodeUsage {
		t.Fatalf("nil unpack context error = %v, want usage", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Pack(ctx, missingArchive, missingRoot, []string{"allowed"}); transportCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("pre-canceled pack error = %v, want retryable timeout before missing-root I/O", err)
	}
	if err := Unpack(ctx, missingArchive, missingRoot, []string{"allowed"}); transportCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("pre-canceled unpack error = %v, want retryable timeout before missing-archive I/O", err)
	}
	if _, err := os.Lstat(missingArchive); !os.IsNotExist(err) {
		t.Fatalf("rejected transport operation touched archive: %v", err)
	}
}

func TestUnpackRejectsSymlinkAndFIFOArchiveBeforeRootIO(t *testing.T) {
	valid := filepath.Join(t.TempDir(), "valid.tar")
	writeTestTar(t, valid, []*tar.Header{{Name: "allowed", Typeflag: tar.TypeDir, Mode: 0o755}})
	for _, test := range []struct {
		name  string
		setup func(string) error
	}{
		{name: "symlink", setup: func(path string) error { return os.Symlink(valid, path) }},
		{name: "fifo", setup: func(path string) error { return unix.Mkfifo(path, 0o600) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "carrier")
			if err := test.setup(archive); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "must-not-be-created")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := Unpack(ctx, archive, root, []string{"allowed"}); transportCode(err) != nkderr.CodeVerifyFailed {
				t.Fatalf("%s archive returned %v, want verify-failed", test.name, err)
			}
			if ctx.Err() != nil {
				t.Fatalf("%s archive blocked until context expiration", test.name)
			}
			if _, err := os.Lstat(root); !os.IsNotExist(err) {
				t.Fatalf("rejected %s archive touched extraction root: %v", test.name, err)
			}
		})
	}
}

func TestTransportSnapshotKeepsHeldArchiveAcrossPathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "carrier.tar")
	if err := os.WriteFile(path, []byte("held carrier bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, info, err := securepath.OpenRegular(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, snapshot, err := snapshotTransport(context.Background(), held, info,
		filepath.Join(t.TempDir(), "snapshot"))
	if err != nil {
		t.Fatalf("held transport snapshot followed replacement path: %v", err)
	}
	defer snapshot.Close()
	current, currentInfo, err := stableTransportDigest(context.Background(), held, info)
	if err != nil {
		t.Fatalf("held transport recheck followed replacement path: %v", err)
	}
	if current != digest || !os.SameFile(info, currentInfo) {
		t.Fatalf("held transport identity changed: initial %s current %s", digest, current)
	}
}

func TestPackRejectsAggregateByteOverflowBeforePublication(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed")
	if err := os.Mkdir(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(allowed, "sparse")
	file, err := os.OpenFile(oversized, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxTransportBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, ".carrier", "oversized.tar")
	if err := Pack(context.Background(), archive, root,
		[]string{"allowed"}); transportCode(err) != nkderr.CodeReleaseIncomplete {
		t.Fatalf("oversized transport returned %v, want release-incomplete", err)
	}
	if _, err := os.Lstat(filepath.Dir(archive)); !os.IsNotExist(err) {
		t.Fatalf("oversized transport touched publication directory: %v", err)
	}
}

func TestTransportRootListHasAnAdmissionBound(t *testing.T) {
	paths := make([]string, maxTransportRoots+1)
	for index := range paths {
		paths[index] = fmt.Sprintf("root-%05d", index)
	}
	if _, _, err := transportRoots(t.TempDir(), paths); transportCode(err) != nkderr.CodeUsage {
		t.Fatalf("oversized transport root list returned %v, want usage", err)
	}
}

func TestPackRejectsSymlinkPublicationParent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "allowed"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".carrier")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, ".carrier", "escaped.tar")
	if err := Pack(context.Background(), archive, root,
		[]string{"allowed"}); transportCode(err) != nkderr.CodeImmutableViolation {
		t.Fatalf("symlink publication parent returned %v, want immutable-violation", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "escaped.tar")); !os.IsNotExist(err) {
		t.Fatalf("symlink publication parent redirected archive output: %v", err)
	}
}

func TestUnpackRejectsSymlinkDestinationParent(t *testing.T) {
	archiveRoot := t.TempDir()
	archive := filepath.Join(archiveRoot, "carrier.tar")
	writeTestTar(t, archive, []*tar.Header{{Name: "link/out", Typeflag: tar.TypeDir, Mode: 0o755}})
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := Unpack(context.Background(), archive, root,
		[]string{"link/out"}); transportCode(err) != nkderr.CodeImmutableViolation {
		t.Fatalf("symlink destination parent returned %v, want immutable-violation", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "out")); !os.IsNotExist(err) {
		t.Fatalf("symlink destination parent redirected extraction: %v", err)
	}
}

func TestPublishTransportRootsRollsBackOrdinaryFailure(t *testing.T) {
	stage := t.TempDir()
	root := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(stage, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	publish := func(source, destination string) error {
		calls++
		if calls == 2 {
			return errors.New("injected second-root publication failure")
		}
		return publishTransportNoReplace(source, destination)
	}
	err := publishTransportRoots(context.Background(), stage, root, []string{"a", "b"}, publish)
	if transportCode(err) != nkderr.CodeImmutableViolation {
		t.Fatalf("injected publication failure returned %v, want immutable-violation", err)
	}
	for _, name := range []string{"a", "b"} {
		if _, statErr := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(statErr) {
			t.Fatalf("failed multi-root publication left %s behind: %v", name, statErr)
		}
	}
}

type cancelingTransportWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
	writes int
}

func (writer *cancelingTransportWriter) Write(p []byte) (int, error) {
	n, err := writer.Buffer.Write(p)
	writer.writes++
	if writer.writes == 1 {
		writer.cancel()
	}
	return n, err
}

func TestCopyTransportStopsAtBoundedChunkOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelingTransportWriter{cancel: cancel}
	source := bytes.NewReader(bytes.Repeat([]byte{'x'}, 3*transportCopyBufferSize))

	written, err := copyTransport(ctx, writer, source)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copy error = %v, want context cancellation", err)
	}
	if written != transportCopyBufferSize || int64(writer.Len()) != transportCopyBufferSize {
		t.Fatalf("copy wrote %d bytes (sink %d), want one bounded %d-byte chunk", written, writer.Len(), transportCopyBufferSize)
	}
}

func TestTransportEnumerationAppliesBoundBeforeRetainingOverflow(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "allowed"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, "allowed", name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rootDir, _, err := securepath.OpenDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootDir.Close()

	if _, err := enumerateTransportEntries(context.Background(), rootDir, []string{"allowed"}, 2); err == nil || !strings.Contains(err.Error(), "2-entry bound") {
		t.Fatalf("bounded enumeration error = %v, want entry-bound failure", err)
	}

	entries, err := enumerateTransportEntries(context.Background(), rootDir, []string{"allowed"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("enumerated %d entries, want declared root plus two files", len(entries))
	}
}

func TestTransportEnumerationPreservesGitExclusionBoundary(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"allowed/.git", "allowed/nested/.git"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "allowed/.git/config"), []byte("excluded"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "allowed/nested/.git/kept"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, ".carrier", "carrier.tar")
	if err := Pack(context.Background(), archive, root, []string{"allowed"}); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := Unpack(context.Background(), archive, destination, []string{"allowed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "allowed/.git")); !os.IsNotExist(err) {
		t.Fatalf("declared root's administrative database crossed transport: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(destination, "allowed/nested/.git/kept")); err != nil || string(got) != "kept" {
		t.Fatalf("nested .git path changed historical transport behavior: %q, %v", got, err)
	}
}

func transportCode(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func TestDetectTransportChartRootDiscoversTheSingleTopLevelDirectory(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers []*tar.Header
		gzip    bool
		root    string
		code    nkderr.Code
	}{
		{name: "single root", headers: []*tar.Header{
			{Name: "postgresql", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "postgresql/Chart.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		}, root: "postgresql"},
		{name: "single root, gzip", headers: []*tar.Header{
			{Name: "postgresql", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "postgresql/Chart.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
		}, gzip: true, root: "postgresql"},
		{name: "multiple roots", headers: []*tar.Header{
			{Name: "helm", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "other", Typeflag: tar.TypeDir, Mode: 0o755},
		}, code: nkderr.CodeVerifyFailed},
		{name: "empty archive", headers: nil, code: nkderr.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "chart.archive")
			if test.gzip {
				writeTestTarGz(t, archive, test.headers)
			} else {
				writeTestTar(t, archive, test.headers)
			}
			root, err := DetectTransportChartRoot(context.Background(), archive)
			if test.code != "" {
				if transportCode(err) != test.code {
					t.Fatalf("got %v, want code %s", err, test.code)
				}
				return
			}
			if err != nil || root != test.root {
				t.Fatalf("root=%q err=%v, want %q", root, err, test.root)
			}
		})
	}
}

// Only DetectAndUnpackChartRoot (the Helm chart-root discovery path) accepts a
// gzip-compressed carrier; every other extraction entry point requires plain
// tar, so this widened decompression exposure and its budget stay scoped to
// the one caller that actually needs it.
func TestDetectAndUnpackChartRootAcceptsAGzipCompressedCarrier(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "chart.tgz")
	writeTestTarGz(t, archive, []*tar.Header{
		{Name: "postgresql", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "postgresql/Chart.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
	})
	root := filepath.Join(t.TempDir(), "extracted")
	chartRoot, err := DetectAndUnpackChartRoot(context.Background(), archive, root)
	if err != nil {
		t.Fatal(err)
	}
	if chartRoot != "postgresql" {
		t.Fatalf("chartRoot = %q, want postgresql", chartRoot)
	}
	if info, err := os.Stat(filepath.Join(root, "postgresql", "Chart.yaml")); err != nil || info.IsDir() {
		t.Fatalf("extracted gzip carrier: %v", err)
	}
}

// Plain Unpack/UnpackOpen must reject a gzip-compressed carrier rather than
// silently widening every caller's decompression exposure; only the dedicated
// Helm chart-root path opts into compression.
func TestUnpackRejectsAGzipCompressedCarrier(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "chart.tgz")
	writeTestTarGz(t, archive, []*tar.Header{
		{Name: "postgresql", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "postgresql/Chart.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4},
	})
	root := filepath.Join(t.TempDir(), "extracted")
	err := Unpack(context.Background(), archive, root, []string{"postgresql"})
	if transportCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("got %v, want a fail-closed rejection of the compressed carrier", err)
	}
}

// A compressed carrier only passes its on-disk size through the caller's size
// precondition, so the decompressed stream carries its own aggregate budget.
// The budget is exercised at a small size here; production uses
// maxHelmTransportDecompressedBytes for the gzip lane and maxTransportBytes
// for every plain-tar lane.
func TestTransportLimitReaderFailsClosedPastItsBudget(t *testing.T) {
	t.Parallel()
	reader := &transportLimitReader{reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, 16)), remaining: 8}
	consumed, err := io.Copy(io.Discard, reader)
	if err == nil {
		t.Fatalf("expected the budget to fail closed, got %d bytes and no error", consumed)
	}
	if consumed != 8 {
		t.Fatalf("consumed = %d, want the budget of 8", consumed)
	}
	if !strings.Contains(err.Error(), "decompressed bound") {
		t.Fatalf("err = %v", err)
	}
}

// A stream of exactly the budget is not over budget: the reader must not
// reject a maximum-size but otherwise legal input merely because its counter
// reached zero.
func TestTransportLimitReaderAcceptsExactlyItsBudget(t *testing.T) {
	t.Parallel()
	reader := &transportLimitReader{reader: bytes.NewReader(bytes.Repeat([]byte{'x'}, 8)), remaining: 8}
	consumed, err := io.Copy(io.Discard, reader)
	if err != nil {
		t.Fatalf("exact-budget stream rejected: %v", err)
	}
	if consumed != 8 {
		t.Fatalf("consumed = %d, want 8", consumed)
	}
}

// Both DetectTransportChartRoot and Unpack read through openTransportTarSource,
// so wiring the budget there covers discovery and extraction alike, including
// bytes tar.Reader.Next skips between entries.
func TestOpenTransportTarSourceBoundsBothCarrierShapes(t *testing.T) {
	t.Parallel()
	wantBudget := map[string]int64{"plain": maxTransportBytes, "gzip": maxHelmTransportDecompressedBytes}
	for name, path := range map[string]string{
		"plain": filepath.Join(t.TempDir(), "carrier.tar"),
		"gzip":  filepath.Join(t.TempDir(), "carrier.tgz"),
	} {
		headers := []*tar.Header{{Name: "demo/", Typeflag: tar.TypeDir, Mode: 0o755}}
		if name == "gzip" {
			writeTestTarGz(t, path, headers)
		} else {
			writeTestTar(t, path, headers)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		source, closeSource, err := openTransportTarSource(file, true)
		if err != nil {
			t.Fatal(err)
		}
		limited, ok := source.(*transportLimitReader)
		if !ok {
			t.Fatalf("%s carrier is unbounded: %T", name, source)
		}
		if limited.remaining != wantBudget[name] {
			t.Fatalf("%s carrier budget = %d, want %d", name, limited.remaining, wantBudget[name])
		}
		closeSource()
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func writeTestTar(t *testing.T, path string, headers []*tar.Header) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(file)
	for _, header := range headers {
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte{'x'}, int(header.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTestTarGz(t *testing.T, path string, headers []*tar.Header) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, header := range headers {
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte{'x'}, int(header.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

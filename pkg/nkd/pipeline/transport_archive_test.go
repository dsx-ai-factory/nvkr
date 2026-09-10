package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

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
	if err := PackTransport(context.Background(), one, producer, paths); err != nil {
		t.Fatal(err)
	}
	if err := PackTransport(context.Background(), two, producer, paths); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(one)
	b2, _ := os.ReadFile(two)
	if !bytes.Equal(b1, b2) {
		t.Fatal("deterministic transport carrier changed for identical inputs")
	}

	consumer := t.TempDir()
	if err := UnpackTransport(context.Background(), one, consumer, paths); err != nil {
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
	if err := UnpackTransport(context.Background(), one, consumer, paths); err == nil {
		t.Fatal("destination overwrite must fail closed")
	}
	if err := PackTransport(context.Background(), one, producer, paths); err == nil {
		t.Fatal("archive overwrite must fail closed")
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
			if err := UnpackTransport(context.Background(), archive, root, []string{"allowed"}); err == nil {
				t.Fatalf("untrusted %s carrier was accepted", tc.name)
			}
			if _, err := os.Lstat(filepath.Join(root, "allowed")); !os.IsNotExist(err) {
				t.Fatalf("failed extraction published partial destination: %v", err)
			}
		})
	}
}

func TestPackTransportRejectsEscapingSymlinkAndCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "allowed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", filepath.Join(root, "allowed", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := PackTransport(context.Background(), filepath.Join(root, ".carrier", "bad.tar"), root, []string{"allowed"}); err == nil {
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
	if err := PackTransport(ctx, filepath.Join(root, ".carrier", "canceled.tar"), root, []string{"allowed"}); transportCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("canceled pack error = %v, want retryable timeout", err)
	}
	archive := filepath.Join(root, ".carrier", "ok.tar")
	if err := PackTransport(context.Background(), archive, root, []string{"allowed"}); err != nil {
		t.Fatal(err)
	}
	if err := UnpackTransport(ctx, archive, t.TempDir(), []string{"allowed"}); transportCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("canceled unpack error = %v, want retryable timeout", err)
	}
}

func TestTransportRejectsNilAndCanceledContextBeforeIO(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing-root")
	missingArchive := filepath.Join(t.TempDir(), "missing.tar")

	//nolint:staticcheck // Passing nil is the admission behavior under test.
	if err := PackTransport(nil, missingArchive, missingRoot, []string{"allowed"}); transportCode(err) != nkderr.CodeUsage {
		t.Fatalf("nil pack context error = %v, want usage", err)
	}
	//nolint:staticcheck // Passing nil is the admission behavior under test.
	if err := UnpackTransport(nil, missingArchive, missingRoot, []string{"allowed"}); transportCode(err) != nkderr.CodeUsage {
		t.Fatalf("nil unpack context error = %v, want usage", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := PackTransport(ctx, missingArchive, missingRoot, []string{"allowed"}); transportCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("pre-canceled pack error = %v, want retryable timeout before missing-root I/O", err)
	}
	if err := UnpackTransport(ctx, missingArchive, missingRoot, []string{"allowed"}); transportCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("pre-canceled unpack error = %v, want retryable timeout before missing-archive I/O", err)
	}
	if _, err := os.Lstat(missingArchive); !os.IsNotExist(err) {
		t.Fatalf("rejected transport operation touched archive: %v", err)
	}
}

func TestUnpackTransportRejectsSymlinkAndFIFOArchiveBeforeRootIO(t *testing.T) {
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
			if err := UnpackTransport(ctx, archive, root, []string{"allowed"}); transportCode(err) != nkderr.CodeVerifyFailed {
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
	held, info, err := openRegularNoFollow(path)
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

func TestPackTransportRejectsAggregateByteOverflowBeforePublication(t *testing.T) {
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
	if err := PackTransport(context.Background(), archive, root,
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

func TestPackTransportRejectsSymlinkPublicationParent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "allowed"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".carrier")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, ".carrier", "escaped.tar")
	if err := PackTransport(context.Background(), archive, root,
		[]string{"allowed"}); transportCode(err) != nkderr.CodeImmutableViolation {
		t.Fatalf("symlink publication parent returned %v, want immutable-violation", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "escaped.tar")); !os.IsNotExist(err) {
		t.Fatalf("symlink publication parent redirected archive output: %v", err)
	}
}

func TestUnpackTransportRejectsSymlinkDestinationParent(t *testing.T) {
	archiveRoot := t.TempDir()
	archive := filepath.Join(archiveRoot, "carrier.tar")
	writeTestTar(t, archive, []*tar.Header{{Name: "link/out", Typeflag: tar.TypeDir, Mode: 0o755}})
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := UnpackTransport(context.Background(), archive, root,
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
	rootDir, _, err := openDirectoryNoFollow(root)
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
	if err := PackTransport(context.Background(), archive, root, []string{"allowed"}); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := UnpackTransport(context.Background(), archive, destination, []string{"allowed"}); err != nil {
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

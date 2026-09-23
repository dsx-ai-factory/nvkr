package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestMaterializeSourceAppliesAuthenticatedPatchPrivately(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work", "src")
	dest := "repo-70d3cc986aa8"
	source := filepath.Join(root, dest)
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fetch := ir.Node{ID: "fetch-r", Dest: dest, Commit: goodSourceCommit, TreeHash: "tree"}
	vendorDigest, err := fetchVendorSpecDigest(fetch)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := sourceTreeDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath, err := sourceReceiptPath(root, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSourceReceipt(receiptPath, sourceReceipt{
		Schema: sourceReceiptSchema, Commit: fetch.Commit, TreeHash: fetch.TreeHash,
		VendorSpecDigest: vendorDigest, ClosureDigest: closure,
	}); err != nil {
		t.Fatal(err)
	}

	patch := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n"
	patchDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(patch)))
	build := ir.Node{
		ID: "build-r", Component: "r", SourceDest: dest,
		SourceCommit: fetch.Commit, SourceTreeHash: fetch.TreeHash, VendorSpecDigest: vendorDigest,
		Env:     []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "123"}},
		Patches: []ir.PatchRef{{Name: "patches/fix.patch", SHA256: patchDigest, Content: patch}},
	}
	var progress bytes.Buffer
	snapshot, err := materializeSource(build, RunOptions{
		SourceRoot: root, progress: &progressSink{w: &progress},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.cleanup()
	got, err := os.ReadFile(filepath.Join(snapshot.Dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" || snapshot.InputDigest == "" {
		t.Fatalf("patch was not applied and identified: content=%q inputDigest=%q", got, snapshot.InputDigest)
	}
	original, _ := os.ReadFile(filepath.Join(source, "a.txt"))
	if string(original) != "old\n" {
		t.Fatalf("materialization mutated the fetched source: %q", original)
	}
	for _, phase := range []string{
		"checkout closure re-hash verified in ",
		"private normalized snapshot copied in ",
		"private snapshot re-hash verified in ",
		"final patched-input hash computed in ",
		"source preparation complete in ",
	} {
		if !strings.Contains(progress.String(), phase) {
			t.Errorf("source progress omitted %q: %s", phase, progress.String())
		}
	}
}

func TestMaterializeSourceAppliesAuthenticatedNewFilePatch(t *testing.T) {
	repository := t.TempDir()
	if output, err := exec.Command("git", "-C", repository, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("initializing enclosing repository: %v: %s", err, output)
	}
	t.Chdir(repository)
	// Production run snapshots live beneath the nke-recipes checkout. The patch
	// application must therefore remain rooted at the private snapshot instead of
	// inheriting repository context from an enclosing worktree.
	root := filepath.Join(".nkd", "work", "src")
	dest := "repo-70d3cc986aa8"
	source := filepath.Join(root, dest)
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "existing.txt"), []byte("pinned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fetch := ir.Node{ID: "fetch-r", Dest: dest, Commit: goodSourceCommit, TreeHash: "tree"}
	vendorDigest, err := fetchVendorSpecDigest(fetch)
	if err != nil {
		t.Fatal(err)
	}
	closure, err := sourceTreeDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath, err := sourceReceiptPath(root, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSourceReceipt(receiptPath, sourceReceipt{
		Schema: sourceReceiptSchema, Commit: fetch.Commit, TreeHash: fetch.TreeHash,
		VendorSpecDigest: vendorDigest, ClosureDigest: closure,
	}); err != nil {
		t.Fatal(err)
	}

	patch := "diff --git a/scripts/generated.sh b/scripts/generated.sh\n" +
		"new file mode 100755\n" +
		"index 0000000..4163036\n" +
		"--- /dev/null\n" +
		"+++ b/scripts/generated.sh\n" +
		"@@ -0,0 +1,2 @@\n" +
		"+#!/bin/sh\n" +
		"+echo generated\n"
	build := ir.Node{
		ID: "build-r", Component: "r", SourceDest: dest,
		SourceCommit: fetch.Commit, SourceTreeHash: fetch.TreeHash, VendorSpecDigest: vendorDigest,
		Patches: []ir.PatchRef{{
			Name: "patches/add-generated-script.patch", SHA256: digestBytes([]byte(patch)), Content: patch,
		}},
	}
	snapshot, err := materializeSource(build, RunOptions{SourceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.cleanup()
	generated := filepath.Join(snapshot.Dir, "scripts", "generated.sh")
	info, err := os.Stat(generated)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("new patched file mode = %04o, want 0755", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(source, "scripts", "generated.sh")); !os.IsNotExist(err) {
		t.Fatalf("new patched file reached fetched source: %v", err)
	}
}

func TestMaterializeSourceRejectsDriftAfterFetch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src")
	dest := "repo-70d3cc986aa8"
	source := filepath.Join(root, dest)
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a.txt"), []byte("pinned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fetch := ir.Node{ID: "fetch-r", Dest: dest, Commit: goodSourceCommit, TreeHash: "tree"}
	vendorDigest, _ := fetchVendorSpecDigest(fetch)
	closure, _ := sourceTreeDigest(source)
	receiptPath, _ := sourceReceiptPath(root, dest)
	if err := writeSourceReceipt(receiptPath, sourceReceipt{
		Schema: sourceReceiptSchema, Commit: fetch.Commit, TreeHash: fetch.TreeHash,
		VendorSpecDigest: vendorDigest, ClosureDigest: closure,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "untracked-input"), []byte("tamper"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := materializeSource(ir.Node{
		Component: "r", SourceDest: dest, SourceCommit: fetch.Commit,
		SourceTreeHash: fetch.TreeHash, VendorSpecDigest: vendorDigest,
	}, RunOptions{SourceRoot: root})
	if code(err) != nkderr.CodeBuildFailed {
		t.Fatalf("source drift must fail closed, got %v", err)
	}
}

func TestReadSourceReceiptTreatsMissingParentAsFreshCache(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fresh", "src")
	receiptPath, err := sourceReceiptPath(root, "source-"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(context.Background(), receiptPath)
	if err != nil {
		t.Fatalf("fresh receipt cache returned an error: %v", err)
	}
	if exists || receipt != (sourceReceipt{}) {
		t.Fatalf("fresh receipt cache returned material: exists=%t receipt=%+v", exists, receipt)
	}
}

func TestApplyPatchesRejectsForgedContent(t *testing.T) {
	dir := t.TempDir()
	n := ir.Node{Component: "r", Patches: []ir.PatchRef{{
		Name: "fix.patch", SHA256: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Content: "forged",
	}}}
	if err := applyPatches(n, dir, RunOptions{}); code(err) != nkderr.CodeBuildFailed {
		t.Fatalf("forged in-IR patch content must fail closed, got %v", err)
	}
}

func TestSourceClosureRejectsEscapingSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", filepath.Join(root, "nested", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceTreeDigest(root); err == nil {
		t.Fatal("source closure accepted a symlink escaping the authenticated root")
	}
	if err := copySourceTree(root, t.TempDir(), 0); err == nil {
		t.Fatal("source materializer copied a symlink escaping the authenticated root")
	}
}

func TestSourceInspectionRejectsDirectorySwappedToHostSymlink(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "source")
	if err := os.Mkdir(original, 0o755); err != nil {
		t.Fatal(err)
	}
	rootDir, _, err := securepath.OpenDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootDir.Close()
	names, err := sourceChildNames(rootDir)
	if err != nil || len(names) != 1 || names[0] != "source" {
		t.Fatalf("source enumeration fixture failed: %v %v", names, err)
	}

	// The enumerated entry was a directory. Replace its pathname before inspection
	// with a symlink to an external directory. Descriptor-relative inspection must
	// ignore the stale DirEntry type and must never traverse the replacement.
	if err := os.Rename(original, original+"-old"); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	canary := filepath.Join(external, "host-canary")
	if err := os.WriteFile(canary, []byte("must-not-import"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, original); err != nil {
		t.Fatal(err)
	}
	if _, err := securepath.InspectChild(rootDir, names[0], names[0]); err == nil {
		t.Fatal("source inspection followed a directory pathname swapped to a host symlink")
	}
	destination := t.TempDir()
	if err := copySourceTree(root, destination, 0); err == nil {
		t.Fatal("source snapshot copied through a swapped host symlink")
	}
	if _, err := os.Stat(filepath.Join(destination, "source", "host-canary")); !os.IsNotExist(err) {
		t.Fatalf("host canary reached the private source snapshot: %v", err)
	}
}

func TestSourceClosureBindsEmptyDirectoriesAndModes(t *testing.T) {
	root := t.TempDir()
	base, err := sourceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(root, "build-visible-empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	withDirectory, err := sourceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if withDirectory == base {
		t.Fatal("build-visible empty directory was omitted from source closure identity")
	}
	if err := os.Chmod(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	withChangedMode, err := sourceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if withChangedMode == withDirectory {
		t.Fatal("empty-directory permission change was omitted from source closure identity")
	}
	file := filepath.Join(root, "build-visible-file")
	if err := os.WriteFile(file, []byte("input\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o755); err != nil {
		t.Fatal(err)
	}
	withExecutable, err := sourceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	withNonExecutable, err := sourceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if withNonExecutable == withExecutable {
		t.Fatal("file permission change was omitted from source closure identity")
	}
}

func TestSourceClosureUsesVersionedCanonicalTreeManifest(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o710); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(empty, 0o710); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "payload")
	payloadBytes := []byte("pinned source bytes\n")
	if err := os.WriteFile(payload, payloadBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(payload, 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "current")
	if err := os.Symlink("payload", link); err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}

	manifest, err := sourceTreeManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(payloadBytes))
	want := sourceClosureManifest{
		SchemaVersion: sourceClosureSchema,
		Entries: []sourceClosureEntry{
			{Path: "current", Type: "symlink", Mode: fmt.Sprintf("%04o", linkInfo.Mode().Perm()), Target: "payload"},
			{Path: "empty", Type: "directory", Mode: "0710"},
			{Path: "payload", Type: "file", Mode: "0640", ContentDigest: payloadDigest},
		},
	}
	if !reflect.DeepEqual(manifest, want) {
		t.Fatalf("source closure manifest mismatch:\n got: %#v\nwant: %#v", manifest, want)
	}
	gotDigest, err := sourceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := canonical.Digest(canonical.KindSourceClosure, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("source closure does not use canonical domain digest: got %s want %s", gotDigest, wantDigest)
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("empty", link); err != nil {
		t.Fatal(err)
	}
	retargeted, err := sourceTreeDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if retargeted == gotDigest {
		t.Fatal("symlink target change was omitted from source closure identity")
	}
}

func TestSourceClosureManifestCanonicalGolden(t *testing.T) {
	manifest := sourceClosureManifest{
		SchemaVersion: sourceClosureSchema,
		Entries: []sourceClosureEntry{
			{Path: "bin", Type: "directory", Mode: "0755"},
			{Path: "bin/tool", Type: "file", Mode: "0755", ContentDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{Path: "current", Type: "symlink", Mode: "0777", Target: "bin/tool"},
		},
	}
	got, err := canonical.Digest(canonical.KindSourceClosure, manifest)
	if err != nil {
		t.Fatal(err)
	}
	const want = "sha256:f3d660a528da4b8914c6f77fd94117e09c2b3f5f33e0ffa73099650641d81099"
	if got != want {
		t.Fatalf("source closure canonical golden changed: got %s want %s", got, want)
	}
}

const goodSourceCommit = "70d3cc986aa8221cd1dfb1121852688902d3bf53"

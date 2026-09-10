package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func sha256Of(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// downloadVerify is fail-closed: it accepts a matching artifact, and on a digest
// mismatch it errors (CodeVerifyFailed) without publishing the poisoned temporary
// bytes or disturbing a prior destination. A non-200 response is a fetch failure.
func TestDownloadVerify(t *testing.T) {
	body := []byte("the-real-pinned-artifact-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing") {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()

	// happy path
	dest := filepath.Join(dir, "ok")
	sum, size, err := downloadVerify(context.Background(), srv.URL+"/tool", dir, "ok", sha256Of(body))
	if err != nil {
		t.Fatalf("matching download must succeed: %v", err)
	}
	if sum != sha256Of(body) || size != int64(len(body)) {
		t.Fatalf("wrong sum/size: %s %d", sum, size)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("verified file must remain: %v", err)
	}

	// digest mismatch -> fail closed + poisoned temp removed + prior file retained
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("previous-good-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = downloadVerify(context.Background(), srv.URL+"/tool", dir, "bad", sha256Of([]byte("something else")))
	if code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("mismatch must fail closed with CodeVerifyFailed, got %v", err)
	}
	if got, err := os.ReadFile(bad); err != nil || string(got) != "previous-good-bytes" {
		t.Fatalf("mismatch disturbed prior destination: %q %v", got, err)
	}

	// non-200 -> fetch failure
	if _, _, err := downloadVerify(context.Background(), srv.URL+"/missing", dir, "x", sha256Of(body)); code(err) != nkderr.CodeFetchFailed {
		t.Fatalf("404 must fail with CodeFetchFailed, got %v", err)
	}
}

func TestDownloadVerifyNeverFollowsDestinationSymlinksOrPublishesPartialBytes(t *testing.T) {
	body := []byte("the-real-pinned-artifact-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/partial") {
			w.Header().Set("Content-Length", "4096")
			_, _ = w.Write(body[:4])
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	root := t.TempDir()
	canaryDir := t.TempDir()
	canary := filepath.Join(canaryDir, "host-canary")
	if err := os.WriteFile(canary, []byte("do-not-overwrite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canary, filepath.Join(root, "artifact")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := downloadVerify(context.Background(), srv.URL+"/tool", root, "artifact", sha256Of(body)); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(canary); err != nil || string(got) != "do-not-overwrite" {
		t.Fatalf("download followed destination symlink and changed host target: %q %v", got, err)
	}
	if info, err := os.Lstat(filepath.Join(root, "artifact")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("verified bytes did not replace the symlink itself: %v %v", info, err)
	}

	previous := filepath.Join(root, "partial")
	if err := os.WriteFile(previous, []byte("previous-good-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := downloadVerify(context.Background(), srv.URL+"/partial", root, "partial", sha256Of(body)); code(err) != nkderr.CodeFetchFailed {
		t.Fatalf("partial response must fail without publishing bytes: %v", err)
	}
	if got, err := os.ReadFile(previous); err != nil || string(got) != "previous-good-bytes" {
		t.Fatalf("partial response changed the prior destination: %q %v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".nkd-download-") {
			t.Fatalf("failed download left private temporary file %q", entry.Name())
		}
	}

	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "redirect")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := downloadVerify(context.Background(), srv.URL+"/tool", root, "redirect/artifact", sha256Of(body)); code(err) != nkderr.CodeFetchFailed {
		t.Fatalf("symlinked parent was not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(external, "artifact")); !os.IsNotExist(err) {
		t.Fatalf("download escaped through a symlinked parent: %v", err)
	}
}

func TestDownloadVerifyCancellationNeverPublishes(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing-root")
	if _, _, err := downloadVerify(nil, "https://example.invalid/artifact", missingRoot, "artifact", sha256Of(nil)); code(err) != nkderr.CodeUsage { //nolint:staticcheck // Nil admission is the behavior under test.
		t.Fatalf("nil download context error = %v, want usage", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := downloadVerify(ctx, "https://example.invalid/artifact", missingRoot, "artifact", sha256Of(nil)); code(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("pre-canceled download error = %v, want retryable timeout", err)
	}
	if _, err := os.Stat(missingRoot); !os.IsNotExist(err) {
		t.Fatalf("pre-canceled download touched its missing root: %v", err)
	}

	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = w.Write([]byte("first-chunk"))
		if flush, ok := w.(http.Flusher); ok {
			flush.Flush()
		}
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "artifact")
	if err := os.WriteFile(destination, []byte("previous-good"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, err := downloadVerify(ctx, server.URL, root, "artifact", sha256Of([]byte("never-completes")))
		result <- err
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("download did not begin")
	}
	select {
	case err := <-result:
		if code(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
			t.Fatalf("mid-download cancellation error = %v, want retryable timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled download did not return")
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "previous-good" {
		t.Fatalf("canceled download changed prior destination: %q %v", got, err)
	}
}

// The runner over a pull-only (binary) graph: it downloads the artifact, verifies it
// against the pin, and the terminal verify re-hashes it. A tampered pin fails closed.
func TestRunPullLaneRawBinary(t *testing.T) {
	body := []byte("kubectl-like-binary")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()

	mk := func(digest string) *ir.Graph {
		return &ir.Graph{SchemaVersion: ir.SchemaVersion, RecipeID: "p",
			Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"}, Nodes: []ir.Node{
				{ID: "pull-tool", Kind: ir.KindPull, Component: "tool", OutputType: "raw",
					Network: ir.NetworkDefault, Platform: "linux/amd64", Artifact: srv.URL + "/tool",
					Digest: digest, ArtifactPath: "tool", Version: "1.0.0"},
				{ID: "verify", Kind: ir.KindVerify, Needs: []string{"pull-tool"}},
			}}
	}
	root := filepath.Join(t.TempDir(), "src")

	// correct pin: acquire + verify pass, and the artifact digest equals the pin.
	validGraph := mk(sha256Of(body))
	rep, err := Local{}.Run(validGraph, mustGraphDigest(t, validGraph), RunOptions{Ctx: context.Background(), SourceRoot: root})
	if err != nil {
		t.Fatalf("valid pull must succeed: %v", err)
	}
	if len(rep.Artifacts) != 1 || rep.Artifacts[0].SHA256 != sha256Of(body) {
		t.Fatalf("artifact digest not recorded: %+v", rep.Artifacts)
	}

	// tampered pin: the served bytes no longer match -> fail closed.
	tamperedGraph := mk(sha256Of([]byte("tampered")))
	_, err = Local{}.Run(tamperedGraph, mustGraphDigest(t, tamperedGraph), RunOptions{Ctx: context.Background(), SourceRoot: filepath.Join(t.TempDir(), "src")})
	if code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("tampered pin must fail closed, got %v", err)
	}
}

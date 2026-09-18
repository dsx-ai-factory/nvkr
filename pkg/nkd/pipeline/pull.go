package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"golang.org/x/sys/unix"
)

// The no-compile (prebuilt) lane. A pull node acquires a pinned, content-addressed
// artifact instead of building it from source (NKD-FR-BLD-050). It is fail-closed: the
// acquired bytes MUST hash to the node's pinned Digest or the whole run stops
// (CodeVerifyFailed). This lane trades the from-source guarantee — "the same inputs
// rebuild the same bytes" — for a content-addressed one — "exactly these pinned bytes";
// it is deliberately NOT a byte reproduction of a build, and claims none.

// pull acquires one prebuilt artifact: an OCI image (pulled by digest) or an https
// binary (downloaded and hash-verified).
func (l Local) pull(n ir.Node, opts RunOptions) (*ArtifactResult, error) {
	if n.Digest == "" {
		return nil, e.New(e.CodeUnpinnedSource, "pull %s: no pinned artifact digest", n.Component)
	}
	if n.OutputType == "oci-image" {
		return l.pullImage(n, opts)
	}
	return l.pullBinary(n, opts)
}

// pullImage acquires a complete OCI image/layout by its pinned manifest digest without
// a daemon or mounted runtime socket. The registry adapter writes into a private
// temporary layout, verifies the complete descriptor/blob closure, and publishes the
// directory atomically. Terminal verification walks the layout again with no network.
func (l Local) pullImage(n ir.Node, opts RunOptions) (*ArtifactResult, error) {
	ref := n.Artifact + "@" + n.Digest
	dir := prebuiltDir(opts, n)
	layoutPath := filepath.Join(dir, sanitizeName(n.Component)+".oci")
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[pull:%s] acquire complete OCI closure %s -> %s (verify %s)\n", n.Component, ref, layoutPath, n.Digest)
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "creating OCI transport dir for %s", n.Component)
	}
	fmt.Fprintf(opts.Stdout, "\n=== pull %s (image %s) ===\n", n.Component, ref)
	got, size, err := opts.OCI.Acquire(opts.ctx(), ref, layoutPath)
	if err != nil {
		return nil, err
	}
	if got != n.Digest {
		return nil, e.New(e.CodeVerifyFailed, "OCI adapter returned digest %s for %s, want %s", got, ref, n.Digest)
	}
	return &ArtifactResult{
		Component:     n.Component,
		OutputType:    n.OutputType,
		Qualification: ArtifactProfileFreeAcquisition,
		EngineClass:   ir.EnginePrebuiltAcquire,
		Path:          ref,
		Ref:           ref,
		TransportPath: layoutPath,
		SHA256:        n.Digest,
		Size:          size,
		Packaged:      true,
	}, nil
}

// pullBinary downloads an https binary artifact, verifies its sha256 against the pin
// (fail closed), and — for a host-package delivery — assembles it into a reproducible
// .deb exactly as the from-source lane does. A raw delivery keeps the binary digest
// (which equals the pin).
func (l Local) pullBinary(n ir.Node, opts RunOptions) (*ArtifactResult, error) {
	dir := prebuiltDir(opts, n)
	name := n.ArtifactPath
	if name == "" {
		name = n.Component
	}
	dest, err := pathWithin(dir, name)
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "resolving prebuilt artifact path for %s", n.Component)
	}

	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[pull:%s] GET %s -> %s (verify sha256 %s)\n",
			n.Component, n.Artifact, dest, abbrev(strings.TrimPrefix(n.Digest, "sha256:")))
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "creating fetch dir for %s", n.Component)
	}
	fmt.Fprintf(opts.Stdout, "\n=== pull %s (%s) ===\n", n.Component, n.Artifact)
	sum, size, err := downloadVerify(opts.ctx(), n.Artifact, dir, name, n.Digest)
	if err != nil {
		return nil, err
	}
	res := &ArtifactResult{
		Component:     n.Component,
		OutputType:    n.OutputType,
		Qualification: ArtifactProfileFreeAcquisition,
		EngineClass:   ir.EnginePrebuiltAcquire,
		Path:          dest,
		SHA256:        sum,
		Size:          size,
		AbsPath:       dest,
	}
	if opts.Package && n.OutputType == "host-package" {
		if err := packageArtifact(n, dest, opts, res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// downloadVerify streams url into a private sibling while hashing, and publishes it
// only after the bytes match want (sha256:<hex>) and the file is durable. The parent
// path is held descriptor-relative with O_NOFOLLOW; renameat replaces a pre-existing
// destination symlink itself instead of following it to a host target. Every error
// unlinks only the private temporary file and leaves any prior destination untouched.
func downloadVerify(ctx context.Context, url, root, relative, want string) (string, int64, error) {
	if ctx == nil {
		return "", 0, e.New(e.CodeUsage, "downloading a pinned artifact requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, e.Wrap(e.CodeTimeout, err, "downloading %s canceled", url)
	}
	dest := filepath.Join(root, relative)
	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "constructing GET %s", url)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, mapCancellation(ctx, e.Wrap(e.CodeFetchFailed, err, "GET %s", url), "downloading "+url)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, e.New(e.CodeFetchFailed, "GET %s: HTTP %s", url, resp.Status)
	}
	parent, finalName, err := securepath.CreateParentBeneath(root, relative)
	if err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "resolving safe download destination %s", dest)
	}
	defer parent.Close()
	f, temporaryName, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "creating private download for %s", dest)
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		_ = unix.Unlinkat(int(parent.Fd()), temporaryName, 0)
	}()
	h := sha256.New()
	size, copyErr := io.CopyBuffer(io.MultiWriter(f, h), pipelineContextReader{ctx: ctx, source: resp.Body}, make([]byte, 128<<10))
	if copyErr != nil {
		return "", 0, mapCancellation(ctx, e.Wrap(e.CodeFetchFailed, copyErr, "downloading %s", url), "downloading "+url)
	}
	if err := ctx.Err(); err != nil {
		return "", 0, e.Wrap(e.CodeTimeout, err, "downloading %s canceled", url)
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got != want {
		return "", 0, e.New(e.CodeVerifyFailed,
			"artifact digest mismatch for %s: expected %s, got %s (fail closed; the pinned artifact was not served)", url, want, got)
	}
	if err := f.Chmod(0o755); err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "setting verified download mode for %s", dest)
	}
	if err := f.Sync(); err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "syncing private download for %s", dest)
	}
	if err := ctx.Err(); err != nil {
		return "", 0, e.Wrap(e.CodeTimeout, err, "publishing downloaded artifact %s canceled", dest)
	}
	if err := f.Close(); err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "closing private download for %s", dest)
	}
	closed = true
	if err := unix.Renameat(int(parent.Fd()), temporaryName, int(parent.Fd()), finalName); err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "publishing verified download %s", dest)
	}
	if err := unix.Fsync(int(parent.Fd())); err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "syncing verified download directory for %s", dest)
	}
	return got, size, nil
}

// prebuiltDir is the per-component download directory, a sibling of the source root
// (.nkd/work/fetch/<component>) so acquired artifacts live under the same work tree as
// from-source checkouts.
func prebuiltDir(opts RunOptions, n ir.Node) string {
	return filepath.Join(filepath.Dir(opts.SourceRoot), "fetch", sanitizeName(n.Component))
}

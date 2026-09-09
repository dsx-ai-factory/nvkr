// Package transport defines operator-swappable publication adapters. Authorization
// belongs to release; this package accepts only a cleared artifact capability and
// rechecks bytes at the transport seam.
package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

// Target names a destination and a credential reference, never a secret value.
type Target struct {
	Ref     string
	CredRef string
}

// OCIRegistry publishes digest-verified bytes and referrers. Adapters must honor ctx,
// rehash bytes against the supplied digest, atomically create destinations, and reject
// an existing destination rather than overwrite it.
type OCIRegistry interface {
	Push(ctx context.Context, target Target, artifactPath, digest string) (ref string, err error)
	AttachReferrer(ctx context.Context, target Target, subjectDigest, artifactType, blobPath, blobDigest string) error
}

// PackageRepo publishes a host package only after checking its authorized digest.
// Implementations follow the same context, atomic-create, and no-overwrite contract.
type PackageRepo interface {
	Publish(ctx context.Context, target Target, packagePath, digest string) error
}

// ObjectStore stores a reader under a key after checking its authorized digest.
// Put follows the same context, atomic-create, and no-overwrite contract.
type ObjectStore interface {
	Put(ctx context.Context, target Target, key, digest string, r io.Reader) error
	Get(ctx context.Context, target Target, key string, w io.Writer) error
}

// Seams bundles the three publication adapters. Nil values resolve to atomic,
// no-overwrite filesystem adapters.
type Seams struct {
	OCI     OCIRegistry
	Package PackageRepo
	Object  ObjectStore
}

// Resolve fills absent adapters with filesystem implementations.
func (seams Seams) Resolve() Seams {
	if seams.OCI == nil {
		seams.OCI = fsOCI{}
	}
	if seams.Package == nil {
		seams.Package = fsPackage{}
	}
	if seams.Object == nil {
		seams.Object = fsObject{}
	}
	return seams
}

type fsOCI struct{}

func (fsOCI) Push(ctx context.Context, target Target, artifactPath, digest string) (string, error) {
	if !isDigest(digest) {
		return "", e.New(e.CodeUnverifiedRef, "OCI push digest %q is malformed", digest)
	}
	if err := atomicCopyPath(ctx, target.Ref, safeName(digest), artifactPath, digest); err != nil {
		return "", err
	}
	return target.Ref + "@" + digest, nil
}

func (fsOCI) AttachReferrer(ctx context.Context, target Target, subjectDigest, artifactType, blobPath, blobDigest string) error {
	if !isDigest(subjectDigest) || !isDigest(blobDigest) {
		return e.New(e.CodeUnverifiedRef, "OCI referrer requires immutable subject and blob digests")
	}
	if strings.TrimSpace(artifactType) == "" {
		return e.New(e.CodeInvalidCatalog, "OCI referrer artifact type is required")
	}
	dir := filepath.Join(target.Ref, "referrers", safeName(subjectDigest))
	return atomicCopyPath(ctx, dir, safeName(artifactType), blobPath, blobDigest)
}

type fsPackage struct{}

func (fsPackage) Publish(ctx context.Context, target Target, packagePath, digest string) error {
	if !isDigest(digest) {
		return e.New(e.CodeUnverifiedRef, "package digest %q is malformed", digest)
	}
	return atomicCopyPath(ctx, target.Ref, safeName(filepath.Base(packagePath)), packagePath, digest)
}

type fsObject struct{}

func (fsObject) Put(ctx context.Context, target Target, key, digest string, reader io.Reader) error {
	if !isDigest(digest) {
		return e.New(e.CodeUnverifiedRef, "object digest %q is malformed", digest)
	}
	return atomicWrite(ctx, target.Ref, safeName(key), reader, digest)
}

func (fsObject) Get(ctx context.Context, target Target, key string, writer io.Writer) error {
	if err := transportContextErr(ctx); err != nil {
		return err
	}
	file, err := os.Open(filepath.Join(target.Ref, safeName(key)))
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "object store open %s", key)
	}
	defer file.Close()
	if _, err := io.Copy(writer, contextReader{ctx: ctx, reader: file}); err != nil {
		if ctx.Err() != nil {
			return transportContextErr(ctx)
		}
		return e.Wrap(e.CodeInternal, err, "object store read %s", key)
	}
	return nil
}

func atomicCopyPath(ctx context.Context, dir, name, src, expectedDigest string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "reading %s", src)
	}
	if !info.Mode().IsRegular() {
		return e.New(e.CodeVerifyFailed, "publication source %q is not a regular file", src)
	}
	file, err := os.Open(src)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening %s", src)
	}
	defer file.Close()
	return atomicWrite(ctx, dir, name, file, expectedDigest)
}

// atomicWrite stages and hashes bytes in the destination directory, then links the
// completed file into place. Link provides atomic create-without-overwrite semantics.
func atomicWrite(ctx context.Context, dir, name string, reader io.Reader, expectedDigest string) error {
	if err := transportContextErr(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(dir) == "" {
		return e.New(e.CodeUsage, "transport target is required")
	}
	if !isDigest(expectedDigest) {
		return e.New(e.CodeUnverifiedRef, "transport expected digest %q is malformed", expectedDigest)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return e.Wrap(e.CodeInternal, err, "creating transport target %s", dir)
	}
	destination := filepath.Join(dir, safeName(name))
	if _, err := os.Lstat(destination); err == nil {
		return e.New(e.CodeImmutableViolation, "refusing to overwrite published object %s", destination)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return e.Wrap(e.CodeInternal, err, "checking published object %s", destination)
	}
	tmp, err := os.CreateTemp(dir, ".nkd-publish-*.tmp")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating publication temp file")
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, hash), contextReader{ctx: ctx, reader: reader})
	if copyErr == nil {
		copyErr = tmp.Sync()
	}
	closeErr := tmp.Close()
	if copyErr != nil {
		if ctx.Err() != nil {
			return transportContextErr(ctx)
		}
		return e.Wrap(e.CodeInternal, copyErr, "staging publication %s", destination)
	}
	if closeErr != nil {
		return e.Wrap(e.CodeInternal, closeErr, "closing publication temp file")
	}
	got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if got != expectedDigest {
		return e.New(e.CodeVerifyFailed, "transport digest mismatch for %s: expected %s, got %s", destination, expectedDigest, got)
	}
	if err := transportContextErr(ctx); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return e.Wrap(e.CodeInternal, err, "setting publication mode")
	}
	if err := os.Link(tmpPath, destination); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return e.New(e.CodeImmutableViolation, "refusing to overwrite published object %s", destination)
		}
		return e.Wrap(e.CodeInternal, err, "atomically publishing %s", destination)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-reader.ctx.Done():
		return 0, reader.ctx.Err()
	default:
		return reader.reader.Read(buffer)
	}
}

func transportContextErr(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeTimeout, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "transport canceled")
	}
	return nil
}

func isDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	raw := strings.TrimPrefix(value, "sha256:")
	if strings.ToLower(raw) != raw {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func safeName(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			out.WriteRune(r)
		} else {
			out.WriteRune('_')
		}
	}
	name := out.String()
	if name == "" || name == "." || name == ".." {
		return "_"
	}
	return name
}

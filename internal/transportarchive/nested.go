// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package transportarchive

import (
	"archive/tar"
	"context"
	"io"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// UnpackNestedStream extracts one exact plain-TAR member directly from an outer
// plain-TAR carrier. The member archive is never written to disk. All extracted
// inner roots stay private until the complete outer size, digest and source EOF
// pass verification. The caller authenticates the outer integrity binding and
// owns cancellation/lifetime of source; allowed paths describe permitted inner
// roots and confer no artifact or release authority.
func UnpackNestedStream(ctx context.Context, source io.Reader, size int64, digest, member, root string, allowed []string) error {
	stream, verifyOuter, err := boundedTransportStream(ctx, source, size, digest)
	if err != nil {
		return err
	}
	rootAbs, cleanAllowed, err := transportRoots(root, allowed)
	if err != nil {
		return err
	}
	if _, ok := safeArchiveName(member, []string{member}); !ok {
		return e.New(e.CodeUsage, "nested transport requires an exact canonical member path")
	}
	outer := tar.NewReader(stream)
	header, err := outer.Next()
	if err != nil {
		return transportError(ctx, e.CodeVerifyFailed, err, "reading nested transport member")
	}
	if header.Name != member || header.Typeflag != tar.TypeReg || header.Linkname != "" ||
		header.Size <= 0 || header.Size > size {
		return e.New(e.CodeVerifyFailed, "nested transport differs from its exact regular archive member")
	}
	for key := range header.PAXRecords {
		if strings.HasPrefix(key, "SCHILY.xattr.") {
			return e.New(e.CodeVerifyFailed, "nested transport member cannot carry extended attributes")
		}
		if strings.HasPrefix(key, "GNU.sparse.") {
			return e.New(e.CodeVerifyFailed, "nested transport member cannot be sparse")
		}
	}
	inner := &io.LimitedReader{R: outer, N: header.Size}
	verify := func() error {
		if inner.N != 0 {
			return e.New(e.CodeVerifyFailed, "nested transport member is truncated")
		}
		if _, err := outer.Next(); err != io.EOF {
			if err == nil {
				return e.New(e.CodeVerifyFailed, "nested transport contains an additional member")
			}
			return transportError(ctx, e.CodeVerifyFailed, err, "finishing nested transport members")
		}
		// TAR end markers are not source EOF. Authenticate trailing bytes and
		// the final chunk's verifier/close before publishing any inner root.
		if _, err := copyTransport(ctx, io.Discard, stream); err != nil {
			return transportError(ctx, e.CodeVerifyFailed, err, "finishing nested transport archive")
		}
		return verifyOuter()
	}
	return unpackSource(ctx, inner, rootAbs, cleanAllowed, false, verify)
}

// PublishRestoredRoots moves exact restored roots from an invocation-owned
// staging tree into destination without replacement. It reuses the transport
// owner's checked parent handling, durability and rollback; it does not issue
// an artifact receipt or independently authenticate the staged bytes.
func PublishRestoredRoots(ctx context.Context, staging, destination string, allowed []string) error {
	if err := requireTransportContext(ctx, "publishing restored transport roots"); err != nil {
		return err
	}
	root, paths, err := transportRoots(destination, allowed)
	if err != nil {
		return err
	}
	stage, _, err := transportRoots(staging, paths)
	if err != nil {
		return err
	}
	return publishTransportRoots(ctx, stage, root, paths, publishTransportNoReplace)
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package transportarchive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// UnpackStream applies Unpack's bounded plain-TAR extraction to an ordered byte
// stream without first writing another archive. It checks the exact size, SHA-256
// digest and source EOF before publishing any extracted root. Digest is transport
// integrity only; callers must authenticate its binding to the intended carrier.
// The caller owns source's lifetime and cancellation of any blocking source I/O.
func UnpackStream(ctx context.Context, source io.Reader, size int64, digest, root string, allowed []string) error {
	stream, verify, err := boundedTransportStream(ctx, source, size, digest)
	if err != nil {
		return err
	}
	rootAbs, cleanAllowed, err := transportRoots(root, allowed)
	if err != nil {
		return err
	}
	return unpackSource(ctx, stream, rootAbs, cleanAllowed, false, verify)
}

// boundedTransportStream prepares exact-byte checking without reading source or
// touching a filesystem. Its verifier must run only after draining the stream.
func boundedTransportStream(ctx context.Context, source io.Reader, size int64, digest string) (io.Reader, func() error, error) {
	if err := requireTransportContext(ctx, "unpacking streamed transport"); err != nil {
		return nil, nil, err
	}
	if source == nil || size < 0 || size > maxTransportBytes {
		return nil, nil, e.New(e.CodeVerifyFailed, "streamed transport requires a source within the archive size bound")
	}
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	decoded, err := hex.DecodeString(hexDigest)
	if err != nil || len(decoded) != sha256.Size || digest != "sha256:"+hex.EncodeToString(decoded) {
		return nil, nil, e.New(e.CodeVerifyFailed, "streamed transport requires an exact SHA-256 digest")
	}
	raw := &io.LimitedReader{R: source, N: size}
	hash := sha256.New()
	stream := io.TeeReader(transportContextReader{ctx: ctx, reader: raw}, hash)
	verify := func() error {
		if raw.N != 0 {
			return e.New(e.CodeVerifyFailed, "streamed transport archive is truncated")
		}
		// A LimitedReader manufactures EOF at its bound. Probe the actual source
		// so neither extra bytes nor a final read/verification/close failure can
		// be mistaken for successful completion of the authenticated stream.
		var extra [1]byte
		n, err := io.ReadFull(transportContextReader{ctx: ctx, reader: source}, extra[:])
		if n != 0 {
			return e.New(e.CodeVerifyFailed, "streamed transport archive exceeds its exact size")
		}
		if err != io.EOF {
			return transportError(ctx, e.CodeVerifyFailed, err, "finishing streamed transport source")
		}
		if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != digest {
			return e.New(e.CodeVerifyFailed, "streamed transport archive digest mismatch")
		}
		return nil
	}
	return stream, verify, nil
}

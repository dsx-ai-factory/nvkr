// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"golang.org/x/net/http2"
)

const maxOCIArchiveBytes int64 = 64 << 20

// downloadOCIBlobVerify negotiates anonymous registry authentication, then reads
// only the exact blob digest. It never resolves a tag or requests an image manifest.
// The resulting .tgz bytes use the same durable, no-follow publication path as
// HTTPS downloads; no decompression or repackaging can change their identity.
func downloadOCIBlobVerify(ctx context.Context, artifact, root, relative, want string) (string, int64, error) {
	if ctx == nil {
		return "", 0, e.New(e.CodeUsage, "OCI blob acquisition requires a context")
	}
	if err := ctx.Err(); err != nil {
		return "", 0, e.Wrap(e.CodeTimeout, err, "OCI blob acquisition canceled")
	}
	ref, err := ir.ParseOCIBlobArtifact(artifact, want)
	if err != nil {
		return "", 0, err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
	}
	roundTripper, err := transport.NewWithContext(ctx, ref.Registry, authn.Anonymous,
		ociBlobTransport(remote.DefaultTransport), []string{ref.Scope("pull")})
	if err != nil {
		return "", 0, mapCancellation(ctx, e.Wrap(e.CodeFetchFailed, err, "authenticating OCI blob transport %s", artifact), "OCI blob acquisition")
	}
	location := url.URL{
		Scheme: ref.Scheme(), Host: ref.RegistryStr(),
		Path: "/v2/" + ref.RepositoryStr() + "/blobs/" + ref.DigestStr(),
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return "", 0, e.Wrap(e.CodeFetchFailed, err, "constructing OCI blob request")
	}
	request.Header.Set("User-Agent", "nvkr/oci-blob-acquire")
	request.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{Transport: roundTripper}
	response, err := client.Do(request)
	if err != nil {
		return "", 0, mapCancellation(ctx, e.Wrap(e.CodeFetchFailed, err, "fetching OCI blob %s", artifact), "OCI blob acquisition")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", 0, e.New(e.CodeFetchFailed, "OCI blob %s: HTTP %s", artifact, response.Status)
	}
	if response.ContentLength > maxOCIArchiveBytes {
		return "", 0, e.New(e.CodeFetchFailed, "OCI blob %s exceeds the %d-byte archive limit", artifact, maxOCIArchiveBytes)
	}
	return writeVerifiedDownload(ctx, response.Body, artifact, root, relative, want, maxOCIArchiveBytes)
}

// Registry pings and anonymous token requests are reads. Retry a peer's transient
// stream failure before any body is published; integrity checks are outside this
// transport and can never authorize another attempt. The pinned library owns the
// three-attempt backoff, and every request retains its acquisition context.
func ociBlobTransport(base http.RoundTripper) http.RoundTripper {
	return transport.NewRetry(base, transport.WithRetryPredicate(func(err error) bool {
		var stream http2.StreamError
		return errors.As(err, &stream) && stream.StreamID != 0 && stream.Code == http2.ErrCodeInternal &&
			stream.Cause != nil && stream.Cause.Error() == "received from peer"
	}))
}

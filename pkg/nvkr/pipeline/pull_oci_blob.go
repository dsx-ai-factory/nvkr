// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"net/http"
	"net/url"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	roundTripper, err := transport.NewWithContext(ctx, ref.Registry, authn.Anonymous,
		remote.DefaultTransport, []string{ref.Scope("pull")})
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
	client := &http.Client{Transport: roundTripper, Timeout: 10 * time.Minute}
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

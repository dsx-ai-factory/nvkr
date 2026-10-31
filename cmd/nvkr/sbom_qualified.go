// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"
)

const (
	maxExternalEvidenceBytes int64 = 64 << 20
	externalEvidenceTimeout        = 5 * time.Minute
)

// commandEvidenceReader is a transport adapter only. It returns bytes; the SBOM
// module verifies those bytes and their artifact subject against the catalog's
// exact bindings. The reader cannot return a success assertion or a digest claim.
type commandEvidenceReader struct {
	client *http.Client
}

func (reader commandEvidenceReader) ReadEvidence(ctx context.Context, ref string) ([]byte, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "external evidence retrieval requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "external evidence retrieval canceled")
	}
	switch {
	case strings.HasPrefix(ref, "file://"):
		path := strings.TrimPrefix(ref, "file://")
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return nil, e.Wrap(e.CodeUnavailable, err, "opening external evidence %s", ref)
		}
		file := os.NewFile(uintptr(fd), path)
		if file == nil {
			_ = unix.Close(fd)
			return nil, e.New(e.CodeInternal, "holding external evidence %s", ref)
		}
		defer func() { _ = file.Close() }()
		before, err := file.Stat()
		if err != nil {
			return nil, e.Wrap(e.CodeUnavailable, err, "stating external evidence %s", ref)
		}
		pathInfo, pathErr := os.Lstat(path)
		if pathErr != nil || !before.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
			!os.SameFile(before, pathInfo) || before.Size() < 0 || before.Size() > maxExternalEvidenceBytes {
			return nil, e.New(e.CodeUnavailable, "external evidence %s must be a regular file no larger than %d bytes", ref, maxExternalEvidenceBytes)
		}
		raw, err := readBoundedEvidence(ctx, file)
		if err != nil {
			return nil, err
		}
		after, statErr := file.Stat()
		afterPath, pathErr := os.Lstat(path)
		if statErr != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(before, afterPath) ||
			before.Size() != int64(len(raw)) || after.Size() != before.Size() ||
			before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
			return nil, e.New(e.CodeUnavailable, "external evidence %s changed while being read", ref)
		}
		return raw, nil
	case strings.HasPrefix(ref, "https://"):
		client := reader.client
		if client == nil {
			client = newEvidenceHTTPClient()
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
		if err != nil {
			return nil, e.Wrap(e.CodeUnavailable, err, "constructing external evidence request")
		}
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "reading external evidence %s", ref)
			}
			return nil, e.Wrap(e.CodeUnavailable, err, "reading external evidence %s", ref)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, e.New(e.CodeUnavailable, "reading external evidence %s returned HTTP %d", ref, response.StatusCode)
		}
		if response.ContentLength > maxExternalEvidenceBytes {
			return nil, e.New(e.CodeUnavailable, "external evidence %s exceeds %d bytes", ref, maxExternalEvidenceBytes)
		}
		return readBoundedEvidence(ctx, response.Body)
	default:
		return nil, e.New(e.CodeUnsupported,
			"external evidence reference %q needs an https:// or file:// reader adapter", ref)
	}
}

func newEvidenceHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.IdleConnTimeout = 30 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return &http.Client{Transport: transport, Timeout: externalEvidenceTimeout}
}

func readBoundedEvidence(ctx context.Context, reader io.Reader) ([]byte, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "reading external evidence requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "reading external evidence canceled")
	}
	raw, err := io.ReadAll(io.LimitReader(evidenceContextReader{ctx: ctx, source: reader}, maxExternalEvidenceBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "reading external evidence canceled")
		}
		return nil, e.Wrap(e.CodeUnavailable, err, "reading external evidence")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "reading external evidence canceled")
	}
	if int64(len(raw)) > maxExternalEvidenceBytes {
		return nil, e.New(e.CodeUnavailable, "external evidence exceeds %d bytes", maxExternalEvidenceBytes)
	}
	return raw, nil
}

type evidenceContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader evidenceContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}

// evidenceBundleVerifier selects the pinned cosign verifier for signed upstream
// evidence. Without --cosign-digest it is nil: signed upstream provenance then
// fails closed, while unsigned digest-pinned declarations behave as before.
func evidenceBundleVerifier(c *cli.Context) (sbom.EvidenceBundleVerifier, error) {
	digest := strings.TrimSpace(c.String("cosign-digest"))
	if digest == "" {
		return nil, nil
	}
	return sign.NewCosignEvidenceVerifier(strings.TrimSpace(c.String("cosign")), digest, nil)
}

func buildVerifiedSBOM(ctx context.Context, resolved *buildgraph.Resolved, buildDigest string,
	artifactHashes map[string]string, verifier sbom.EvidenceBundleVerifier,
) (*sbom.VerifiedSet, error) {
	return sbom.BuildVerified(ctx, resolved, buildDigest, artifactHashes, commandEvidenceReader{}, verifier)
}

func buildDiagnosticSelectedSBOM(ctx context.Context, resolved *buildgraph.Resolved, buildDigest string,
	artifactHashes map[string]string, verifier sbom.EvidenceBundleVerifier,
) (*sbom.VerifiedSet, error) {
	return sbom.BuildDiagnosticSelected(ctx, resolved, buildDigest, artifactHashes, commandEvidenceReader{}, verifier)
}

func buildVerifiedSBOMFromReport(ctx context.Context, resolved *buildgraph.Resolved,
	report *pipeline.Report, verifier sbom.EvidenceBundleVerifier,
) (*sbom.VerifiedSet, error) {
	return sbom.BuildVerifiedFromReport(ctx, resolved, report, commandEvidenceReader{}, verifier)
}

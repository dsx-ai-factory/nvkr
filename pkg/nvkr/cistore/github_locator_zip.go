// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func (g *githubLocatorClient) downloadLocator(ctx context.Context, request Request, artifact githubLocatorArtifact) ([]byte, error) {
	location, err := g.apiGET(ctx, "/repos/"+request.Repository+"/actions/artifacts/"+string(artifact.ID)+"/zip")
	if err != nil {
		return nil, err
	}
	if location.status != http.StatusFound {
		return nil, githubLocatorStatusError(ctx, location.status)
	}
	if len(location.header.Values("Location")) != 1 {
		return nil, e.New(e.CodeVerifyFailed, "CI locator ZIP requires one provider download location")
	}
	address, err := githubLocatorDownloadURL(location.header.Get("Location"))
	if err != nil {
		return nil, err
	}
	// Construct a new credential-free request. Neither the API Authorization
	// header nor a cookie jar can follow the signed storage URL; redirects stop.
	download, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return nil, e.New(e.CodeInternal, "creating CI locator ZIP request failed")
	}
	response, err := g.get(ctx, download, maxGitHubLocatorZIPBytes)
	if err != nil {
		return nil, err
	}
	if response.status != http.StatusOK {
		return nil, githubLocatorStatusError(ctx, response.status)
	}
	hash := sha256.Sum256(response.body)
	if strconv.Itoa(len(response.body)) != string(artifact.Size) || "sha256:"+hex.EncodeToString(hash[:]) != artifact.Digest {
		return nil, e.New(e.CodeVerifyFailed, "CI locator ZIP differs from its provider digest or size")
	}
	request.Name = artifact.Name
	return unpackGitHubLocator(ctx, response.body, request)
}

func unpackGitHubLocator(ctx context.Context, raw []byte, request Request) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, safeIOError(ctx, e.CodeTimeout, "CI locator ZIP decoding canceled")
	}
	if len(raw) == 0 || len(raw) > maxGitHubLocatorZIPBytes {
		return nil, e.New(e.CodeVerifyFailed, "CI locator ZIP exceeds its byte bound")
	}
	// The entire ZIP, including the central directory, is bounded before the
	// standard parser allocates entries. Only one regular member is admitted.
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(archive.File) != 1 {
		return nil, e.New(e.CodeVerifyFailed, "CI locator ZIP must contain exactly one regular locator")
	}
	member := archive.File[0]
	if member.Name != request.Name+locatorSuffix || !member.Mode().IsRegular() || member.UncompressedSize64 == 0 || member.UncompressedSize64 > MaxMetadataRecordBytes {
		return nil, e.New(e.CodeVerifyFailed, "CI locator ZIP member has invalid name, type or size")
	}
	file, err := member.Open()
	if err != nil {
		return nil, e.New(e.CodeVerifyFailed, "opening CI locator ZIP member failed")
	}
	locator, readErr := io.ReadAll(io.LimitReader(file, MaxMetadataRecordBytes+1))
	closeErr := file.Close()
	if ctx.Err() != nil {
		return nil, safeIOError(ctx, e.CodeTimeout, "CI locator ZIP decoding canceled")
	}
	if readErr != nil || closeErr != nil || len(locator) > MaxMetadataRecordBytes || uint64(len(locator)) != member.UncompressedSize64 {
		return nil, e.New(e.CodeVerifyFailed, "CI locator ZIP member integrity failed")
	}
	description, err := DecodeLocator(locator)
	if err != nil {
		return nil, e.New(e.CodeVerifyFailed, "CI locator ZIP member is not a canonical locator")
	}
	if err := description.Validate(request); err != nil {
		return nil, e.New(e.CodeVerifyFailed, "CI locator ZIP member differs from the exact source request")
	}
	return locator, nil
}

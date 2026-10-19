// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/url"
	"strconv"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// DownloadDevelopmentMetadata recovers one original public control carrier from
// a provider-authenticated development attempt. The Plan selects its exact name,
// carrier member and restored root. These bytes remain untrusted transport.
func (g *GitHub) DownloadDevelopmentMetadata(ctx context.Context, source *SourceRun, name, member, root, destination string) error {
	if err := admitContext(ctx); err != nil {
		return err
	}
	origin, err := source.Description()
	if err != nil {
		return err
	}
	if origin.Branch == "" {
		return e.New(e.CodeEngineCapability, "development metadata recovery requires a development source")
	}
	if g == nil || g.client == nil || g.api != "https://api.github.com" {
		return e.New(e.CodeEngineCapability, "development metadata transport is unavailable")
	}
	request := cistore.Request{Repository: origin.Repository, RunID: origin.RunID, Attempt: origin.Attempt, Name: name}
	if err := cistore.ValidateGitHubRequest(ctx, request); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var listing qualificationArtifactList
	if err := g.get(ctx, "/repos/"+origin.Repository+"/actions/runs/"+origin.RunID+"/artifacts?name="+url.QueryEscape(name)+"&per_page=100", &listing); err != nil {
		return err
	}
	count := len(listing.Artifacts)
	if count == 0 || count > 100 || string(listing.TotalCount) != strconv.Itoa(count) {
		return e.New(e.CodeProvenanceMissing, "development recovery requires a complete bounded carrier selection")
	}
	ids := make(map[string]bool, count)
	var total int64
	for _, artifact := range listing.Artifacts {
		if err := artifact.validate(name, origin); err != nil {
			return err
		}
		id := string(artifact.ID)
		if ids[id] {
			return e.New(e.CodeVerifyFailed, "development carrier artifact ID is repeated")
		}
		ids[id] = true
		size, _ := strconv.ParseInt(string(artifact.Size), 10, 64)
		total += size
		if total > 128<<20 {
			return e.New(e.CodeVerifyFailed, "development carrier selection exceeds its aggregate bound")
		}
	}
	var selected cistore.Locator
	var latest uint64
	seen := make(map[uint64]bool, count)
	current, _ := strconv.ParseUint(request.Attempt, 10, 64)
	for _, artifact := range listing.Artifacts {
		locator, err := g.downloadDevelopmentLocator(ctx, source, origin, name, artifact)
		if err != nil {
			return err
		}
		candidate := request
		candidate.Attempt = locator.Request.Attempt
		if err := locator.Validate(candidate); err != nil {
			return err
		}
		attempt, _ := strconv.ParseUint(candidate.Attempt, 10, 64)
		if attempt > current || seen[attempt] {
			return e.New(e.CodeVerifyFailed, "development carrier has a future or ambiguous source attempt")
		}
		seen[attempt] = true
		if attempt > latest {
			selected, latest = locator, attempt
		}
	}
	if err := source.Require(); err != nil {
		return err
	}
	if err := cistore.DownloadNested(ctx, selected.Request, selected, destination, cistore.NestedArchive{Member: member, Allowed: []string{root}}, cistore.Options{}); err != nil {
		return err
	}
	return source.Require()
}

// Every candidate is provider-authenticated before selecting its original attempt.
func (g *GitHub) downloadDevelopmentLocator(ctx context.Context, source *SourceRun, origin Description, name string, artifact qualificationArtifact) (cistore.Locator, error) {
	archive, err := g.downloadArtifactArchive(ctx, source.Require, origin.Repository, artifact.ID, 40<<20)
	if err != nil {
		return cistore.Locator{}, err
	}
	digest := sha256.Sum256(archive)
	size, err := strconv.ParseInt(string(artifact.Size), 10, 64)
	if err != nil || int64(len(archive)) != size || artifact.Digest != "sha256:"+hex.EncodeToString(digest[:]) {
		return cistore.Locator{}, e.New(e.CodeVerifyFailed, "development carrier differs from provider integrity metadata")
	}
	bounded := &handoffZIPReaderAt{ctx: ctx, reader: bytes.NewReader(archive), metadata: true, remaining: maxQualificationZIPMetadataBytes}
	reader, err := zip.NewReader(bounded, int64(len(archive)))
	if err != nil {
		return cistore.Locator{}, e.Wrap(e.CodeVerifyFailed, err, "decoding development metadata archive")
	}
	if len(reader.File) != 1 || reader.File[0].Name != name+".ci-locator.json" || !reader.File[0].Mode().IsRegular() || reader.File[0].UncompressedSize64 > cistore.MaxMetadataRecordBytes {
		return cistore.Locator{}, e.New(e.CodeVerifyFailed, "development control archive requires one exact bounded metadata record")
	}
	bounded.metadata = false
	input, err := reader.File[0].Open()
	if err != nil {
		return cistore.Locator{}, e.Wrap(e.CodeVerifyFailed, err, "opening development metadata record")
	}
	raw, readErr := io.ReadAll(io.LimitReader(handoffContextReader{ctx: ctx, reader: input}, cistore.MaxMetadataRecordBytes+1))
	closeErr := input.Close()
	if readErr != nil || closeErr != nil || len(raw) > cistore.MaxMetadataRecordBytes || uint64(len(raw)) != reader.File[0].UncompressedSize64 {
		return cistore.Locator{}, e.New(e.CodeVerifyFailed, "development metadata record read failed or exceeded its bound")
	}
	locator, err := cistore.DecodeLocator(raw)
	if err != nil {
		return cistore.Locator{}, err
	}
	if locator.Metadata == nil {
		return cistore.Locator{}, e.New(e.CodeEngineCapability, "development recovery requires retained public metadata controls")
	}
	return locator, nil
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// MaxQualificationHandoffBytes bounds the original canonical handoff document.
// Both its producer and consumer must apply this bound before processing it.
const MaxQualificationHandoffBytes = 96 << 20

const maxQualificationArchiveBytes = 128 << 20

// A single-member archive needs only bounded footer and directory metadata.
// Limit parsing before archive/zip allocates its complete member collection.
const maxQualificationZIPMetadataBytes = 1 << 20

// QualificationArtifactName selects one exact attempt's generated handoff. The
// name is a discovery convention, not proof of the archive or its contents.
func QualificationArtifactName(locator RunLocator) (string, error) {
	if err := locator.Validate(); err != nil {
		return "", err
	}
	return "nvkr-qualification-" + locator.RunID + "-" + locator.Attempt, nil
}

type qualificationArtifactList struct {
	TotalCount json.Number             `json:"total_count"`
	Artifacts  []qualificationArtifact `json:"artifacts"`
}

type qualificationArtifact struct {
	ID          json.Number              `json:"id"`
	Name        string                   `json:"name"`
	Size        json.Number              `json:"size_in_bytes"`
	Digest      string                   `json:"digest"`
	Expired     *bool                    `json:"expired"`
	WorkflowRun qualificationArtifactRun `json:"workflow_run"`
}

type qualificationArtifactRun struct {
	ID               json.Number `json:"id"`
	RepositoryID     json.Number `json:"repository_id"`
	HeadRepositoryID json.Number `json:"head_repository_id"`
	HeadSHA          string      `json:"head_sha"`
	HeadBranch       string      `json:"head_branch"`
}

// DownloadQualificationHandoff retrieves the single pinned archive belonging to
// a live authenticated source and returns its sole handoff.json member. It grants
// no artifact or qualification authority: lifecycle must authenticate the bytes.
// The source is borrowed and must remain live through the complete operation.
func (g *GitHub) DownloadQualificationHandoff(ctx context.Context, source *SourceRun) ([]byte, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "qualification handoff download requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "qualification handoff download canceled")
	}
	origin, err := source.Description()
	if err != nil {
		return nil, err
	}
	if g == nil || g.client == nil || g.api != "https://api.github.com" {
		return nil, e.New(e.CodeEngineCapability, "GitHub qualification handoff transport is unavailable")
	}
	name, err := QualificationArtifactName(RunLocator{Repository: origin.Repository, RunID: origin.RunID, Attempt: origin.Attempt})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var listing qualificationArtifactList
	path := "/repos/" + origin.Repository + "/actions/runs/" + origin.RunID + "/artifacts?name=" + url.QueryEscape(name) + "&per_page=100"
	if err := g.get(ctx, path, &listing); err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "qualification artifact discovery canceled")
		}
		return nil, err
	}
	if err := source.Require(); err != nil {
		return nil, err
	}
	if listing.TotalCount != "1" || len(listing.Artifacts) != 1 {
		return nil, e.New(e.CodeProvenanceMissing, "qualification requires exactly one handoff artifact for the selected attempt")
	}
	artifact := listing.Artifacts[0]
	if err := artifact.validate(name, origin); err != nil {
		return nil, err
	}
	archive, err := g.downloadQualificationArchive(ctx, source, origin.Repository, artifact.ID)
	if err != nil {
		return nil, err
	}
	// This digest is exact blob integrity of a provider-pinned archive, not a
	// structured Run, policy, publication or qualification identity.
	hash := sha256.Sum256(archive)
	if "sha256:"+hex.EncodeToString(hash[:]) != artifact.Digest {
		return nil, e.New(e.CodeVerifyFailed, "qualification artifact archive differs from its provider digest")
	}
	raw, err := unpackQualificationHandoff(ctx, archive)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "qualification handoff download canceled")
	}
	if err := source.Require(); err != nil {
		return nil, err
	}
	return raw, nil
}

func (artifact qualificationArtifact) validate(name string, origin Description) error {
	run := artifact.WorkflowRun
	if !numberPattern.MatchString(string(artifact.ID)) || artifact.Name != name || artifact.Expired == nil || *artifact.Expired ||
		string(run.ID) != origin.RunID || run.HeadSHA != origin.Commit || run.HeadBranch != origin.DefaultBranch ||
		!numberPattern.MatchString(string(run.RepositoryID)) || run.RepositoryID != run.HeadRepositoryID {
		return e.New(e.CodeVerifyFailed, "qualification artifact does not belong to the exact non-expired source handoff")
	}
	if !canonical.IsDigest(artifact.Digest) {
		return e.New(e.CodeUnpinnedInput, "qualification artifact requires an exact provider archive digest")
	}
	size, err := strconv.ParseInt(string(artifact.Size), 10, 64)
	if err != nil || size <= 0 || size > maxQualificationArchiveBytes {
		return e.New(e.CodeVerifyFailed, "qualification artifact archive size is missing or exceeds its byte bound")
	}
	return nil
}

func (g *GitHub) downloadQualificationArchive(ctx context.Context, source *SourceRun, repository string, id json.Number) ([]byte, error) {
	return g.downloadArtifactArchive(ctx, source.Require, repository, id, maxQualificationArchiveBytes)
}

// downloadArtifactArchive is the shared credential-isolating artifact transport.
// The private liveness callback borrows an already authenticated owner; no
// exported transport seam can mint either source or invocation authority.
func (g *GitHub) downloadArtifactArchive(ctx context.Context, require func() error, repository string, id json.Number, maxBytes int64) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, handoffTransportError(ctx, "qualification archive download")
	}
	if err := require(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, g.api+"/repos/"+repository+"/actions/artifacts/"+string(id)+"/zip", nil)
	if err != nil {
		return nil, handoffTransportError(ctx, "creating qualification archive request")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		request.Header.Set("Authorization", "Bearer "+g.token)
	}
	response, err := g.client.Do(request)
	if err != nil {
		return nil, handoffTransportError(ctx, "requesting qualification archive location")
	}
	location := response.Header.Get("Location")
	status := response.StatusCode
	_ = response.Body.Close()
	if err := require(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, handoffTransportError(ctx, "qualification archive download")
	}
	if status != http.StatusFound {
		return nil, e.New(e.CodeFetchFailed, "qualification archive location returned HTTP %d", status)
	}
	address, err := qualificationDownloadURL(location)
	if err != nil {
		return nil, err
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return nil, handoffTransportError(ctx, "creating qualification archive download")
	}
	// A fresh request carries no Authorization, Cookie or Referer from the API.
	// Keep the owned transport and redirect refusal; also prevent a future API
	// cookie jar from attaching credentials to the signed download destination.
	client := *g.client
	client.Jar = nil
	response, err = client.Do(request)
	if err != nil {
		return nil, handoffTransportError(ctx, "downloading qualification archive")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, e.New(e.CodeFetchFailed, "qualification archive download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return nil, e.New(e.CodeVerifyFailed, "qualification archive download exceeds its byte bound")
	}
	raw, err := io.ReadAll(io.LimitReader(handoffContextReader{ctx: ctx, reader: response.Body}, maxBytes+1))
	if err != nil {
		return nil, handoffTransportError(ctx, "reading qualification archive")
	}
	if int64(len(raw)) > maxBytes {
		return nil, e.New(e.CodeVerifyFailed, "qualification archive download exceeds its byte bound")
	}
	if response.ContentLength >= 0 && int64(len(raw)) != response.ContentLength {
		return nil, e.New(e.CodeVerifyFailed, "qualification archive download is truncated")
	}
	if err := require(); err != nil {
		return nil, err
	}
	return raw, nil
}

func qualificationDownloadURL(raw string) (*url.URL, error) {
	address, err := url.Parse(raw)
	if err != nil || address.Scheme != "https" || address.Host == "" || address.User != nil || address.Port() != "" ||
		address.Opaque != "" || strings.Contains(raw, "#") {
		return nil, e.New(e.CodeVerifyFailed, "qualification archive location is not an admitted HTTPS download")
	}
	host := strings.ToLower(address.Hostname())
	if host != "results-receiver.actions.githubusercontent.com" &&
		!(len(host) > len(".blob.core.windows.net") && strings.HasSuffix(host, ".blob.core.windows.net") && !strings.Contains(host, "..")) {
		return nil, e.New(e.CodeVerifyFailed, "qualification archive location has an unapproved download host")
	}
	return address, nil
}

func unpackQualificationHandoff(ctx context.Context, archive []byte) ([]byte, error) {
	return readQualificationHandoffArchive(ctx, bytes.NewReader(archive), int64(len(archive)))
}

// ReaderAt is private, used only for the immutable in-memory archive and focused
// parser tests. It is not an injected provider or authority-minting adapter.
func readQualificationHandoffArchive(ctx context.Context, archive io.ReaderAt, size int64) ([]byte, error) {
	if size < 0 || size > maxQualificationArchiveBytes {
		return nil, e.New(e.CodeVerifyFailed, "qualification archive exceeds its byte bound")
	}
	bounded := &handoffZIPReaderAt{ctx: ctx, reader: archive, metadata: true, remaining: maxQualificationZIPMetadataBytes}
	reader, err := zip.NewReader(bounded, size)
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "qualification archive metadata decoding canceled")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "invalid qualification archive")
	}
	if len(reader.File) != 1 {
		return nil, e.New(e.CodeVerifyFailed, "qualification archive must contain exactly one handoff.json file")
	}
	member := reader.File[0]
	if member.Name != "handoff.json" || !member.Mode().IsRegular() || member.UncompressedSize64 > MaxQualificationHandoffBytes {
		return nil, e.New(e.CodeVerifyFailed, "qualification archive member has an invalid path, type or size")
	}
	// Metadata has now admitted exactly one bounded regular member. Payload
	// reads retain context checks and the separate uncompressed byte ceiling.
	bounded.metadata = false
	input, err := member.Open()
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "qualification handoff opening canceled")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "opening qualification handoff member")
	}
	defer func() { _ = input.Close() }()
	raw, err := io.ReadAll(io.LimitReader(handoffContextReader{ctx: ctx, reader: input}, MaxQualificationHandoffBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "qualification archive decoding canceled")
		}
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading qualification handoff member")
	}
	if len(raw) > MaxQualificationHandoffBytes || uint64(len(raw)) != member.UncompressedSize64 {
		return nil, e.New(e.CodeVerifyFailed, "qualification handoff member exceeds its bound or differs from its declared size")
	}
	return raw, nil
}

// Never wrap transport errors from a signed URL: url.Error and even its cause
// may contain the complete credential-bearing URL. Cancellation keeps its cause.
func handoffTransportError(ctx context.Context, operation string) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "%s canceled", operation)
	}
	return e.New(e.CodeFetchFailed, "%s failed", operation)
}

type handoffContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader handoffContextReader) Read(p []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(p)
}

// One synchronous parser/extractor owns this reader. The budget includes repeated
// footer/header reads; it cannot be bypassed by overlapping directory offsets.
type handoffZIPReaderAt struct {
	ctx       context.Context
	reader    io.ReaderAt
	metadata  bool
	remaining int64
}

func (reader *handoffZIPReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if reader.metadata {
		if int64(len(p)) > reader.remaining {
			return 0, e.New(e.CodeVerifyFailed, "qualification ZIP metadata exceeds its read bound")
		}
		reader.remaining -= int64(len(p))
	}
	n, err := reader.reader.ReadAt(p, offset)
	if cause := reader.ctx.Err(); cause != nil {
		return n, cause
	}
	return n, err
}

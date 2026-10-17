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
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	maxNativeSmokeDocumentBytes    = 16 << 20
	maxNativeSmokeSignatureBytes   = 64 << 10
	maxNativeSmokeCertificateBytes = 1 << 20
	maxNativeSmokeBundleBytes      = 16 << 20
	maxNativeSmokeArchiveBytes     = 36 << 20
	maxNativeSmokeJobs             = 1000
	nativeSmokeJobsPerPage         = 100
)

// NativeSmokeArtifact contains downloaded transport bytes for one native worker.
// Neither provider job success nor these public fields authenticate the document
// or issue smoke authority. The consumer must verify its signature, exact
// invocation, policy and complete subject/member coverage before accepting it.
type NativeSmokeArtifact struct {
	Platform    string
	Document    []byte
	Signature   []byte
	Certificate []byte
	Bundle      []byte
}

type nativeSmokeJobList struct {
	TotalCount json.Number      `json:"total_count"`
	Jobs       []nativeSmokeJob `json:"jobs"`
}

type nativeSmokeJob struct {
	ID         json.Number `json:"id"`
	RunID      json.Number `json:"run_id"`
	Attempt    json.Number `json:"run_attempt,omitempty"`
	HeadSHA    string      `json:"head_sha"`
	Name       string      `json:"name"`
	Status     string      `json:"status"`
	Conclusion string      `json:"conclusion"`
}

// DownloadNativeSmoke retrieves one fixed-name worker artifact per requested
// native platform from the authenticated current qualifier attempt. It verifies
// the exact attempt's successful generated worker jobs and provider archive pins,
// returning sorted transport descriptions only. The invocation and its source
// are borrowed and must remain live through the complete bounded operation.
//
// The generated job-name contract is "native-smoke / native-amd64" (or arm64):
// explicit caller name "native-smoke", reusable job name "native-${{ matrix.arch }}".
// This exact convention must be checked in hosted acceptance; no suffix or fuzzy
// match substitutes for it. Worker signature verification remains mandatory.
func (g *GitHub) DownloadNativeSmoke(ctx context.Context, invocation *Invocation, platforms []string) ([]NativeSmokeArtifact, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "native smoke download requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "native smoke download canceled")
	}
	selected, err := nativeSmokePlatforms(platforms)
	if err != nil {
		return nil, err
	}
	origin, err := invocation.Description()
	if err != nil {
		return nil, err
	}
	if g == nil || g.client == nil || g.api != "https://api.github.com" {
		return nil, e.New(e.CodeEngineCapability, "GitHub native smoke transport is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	require := func() error {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "native smoke download canceled")
		}
		_, err := invocation.Description()
		return err
	}
	request := SourceRequest{Branch: origin.Branch, Repository: origin.Repository, RunID: origin.RunID, Attempt: origin.Attempt,
		Commit: origin.Commit, Workflow: origin.Workflow, DefaultBranch: origin.DefaultBranch}
	repository, run, err := g.lookup(ctx, request)
	if admission := require(); admission != nil {
		return nil, admission
	}
	if err != nil {
		return nil, err
	}
	if err := matchRun(request, repository, run); err != nil {
		return nil, err
	}
	event := "workflow_run"
	if origin.Branch != "" {
		event = "workflow_dispatch"
	}
	if run.Event != event || run.Status != "in_progress" || run.Conclusion != "" {
		return nil, e.New(e.CodeVerifyFailed, "native smoke transport requires the active qualification workflow attempt")
	}
	if err := g.requireNativeSmokeJobs(ctx, require, origin, selected); err != nil {
		return nil, err
	}
	out := make([]NativeSmokeArtifact, 0, len(selected))
	for _, platform := range selected {
		name := "nvkr-smoke-" + origin.RunID + "-" + origin.Attempt + "-" + strings.TrimPrefix(platform, "linux/")
		var listing qualificationArtifactList
		path := "/repos/" + origin.Repository + "/actions/runs/" + origin.RunID + "/artifacts?name=" + url.QueryEscape(name) + "&per_page=100"
		err := g.get(ctx, path, &listing)
		if admission := require(); admission != nil {
			return nil, admission
		}
		if err != nil {
			return nil, err
		}
		if listing.TotalCount != "1" || len(listing.Artifacts) != 1 {
			return nil, e.New(e.CodeProvenanceMissing, "native smoke requires exactly one %s artifact for the current attempt", platform)
		}
		artifact := listing.Artifacts[0]
		if err := artifact.validate(name, origin); err != nil {
			return nil, err
		}
		size, err := strconv.ParseInt(string(artifact.Size), 10, 64)
		if err != nil || size > maxNativeSmokeArchiveBytes || artifact.WorkflowRun.RepositoryID != repository.ID {
			return nil, e.New(e.CodeVerifyFailed, "native smoke artifact repository or archive size does not match its admitted invocation")
		}
		archive, err := g.downloadArtifactArchive(ctx, require, origin.Repository, artifact.ID, maxNativeSmokeArchiveBytes)
		if admission := require(); admission != nil {
			return nil, admission
		}
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(archive)
		if "sha256:"+hex.EncodeToString(hash[:]) != artifact.Digest || int64(len(archive)) != size {
			return nil, e.New(e.CodeVerifyFailed, "native smoke archive differs from its provider digest or size")
		}
		members, private, err := g.resolvePrivateArtifact(ctx, require, origin, name, archive, maxNativeSmokeArchiveBytes, map[string]int64{
			"worker.json": maxNativeSmokeDocumentBytes, "worker.sig": maxNativeSmokeSignatureBytes,
			"worker.pem": maxNativeSmokeCertificateBytes, "worker.bundle.json": maxNativeSmokeBundleBytes,
		})
		if err != nil {
			return nil, err
		}
		var result NativeSmokeArtifact
		if private {
			for _, raw := range members {
				if len(raw) == 0 {
					return nil, e.New(e.CodeVerifyFailed, "private native smoke artifact contains an empty evidence member")
				}
			}
			result = NativeSmokeArtifact{Document: members["worker.json"], Signature: members["worker.sig"], Certificate: members["worker.pem"], Bundle: members["worker.bundle.json"]}
		} else {
			result, err = unpackNativeSmoke(ctx, archive)
		}
		if err != nil {
			return nil, err
		}
		result.Platform = platform
		out = append(out, result)
	}
	if err := require(); err != nil {
		return nil, err
	}
	return out, nil
}

func nativeSmokePlatforms(platforms []string) ([]string, error) {
	if len(platforms) < 1 || len(platforms) > 2 {
		return nil, e.New(e.CodeEngineCapability, "native smoke transport requires one or both supported native Linux platforms")
	}
	selected := append([]string(nil), platforms...)
	sort.Strings(selected)
	for i, platform := range selected {
		if (platform != "linux/amd64" && platform != "linux/arm64") || (i > 0 && platform == selected[i-1]) {
			return nil, e.New(e.CodeEngineCapability, "native smoke transport requires unique linux/amd64 or linux/arm64 platforms")
		}
	}
	return selected, nil
}

func (g *GitHub) requireNativeSmokeJobs(ctx context.Context, require func() error, origin Description, platforms []string) error {
	required := make(map[string]bool, len(platforms))
	for _, platform := range platforms {
		required["native-smoke / native-"+strings.TrimPrefix(platform, "linux/")] = false
	}
	seen := make(map[json.Number]bool)
	total := -1
	for page := 1; page <= maxNativeSmokeJobs/nativeSmokeJobsPerPage; page++ {
		if err := require(); err != nil {
			return err
		}
		var listing nativeSmokeJobList
		path := "/repos/" + origin.Repository + "/actions/runs/" + origin.RunID + "/attempts/" + origin.Attempt + "/jobs?per_page=100&page=" + strconv.Itoa(page)
		err := g.get(ctx, path, &listing)
		if admission := require(); admission != nil {
			return admission
		}
		if err != nil {
			return err
		}
		count, err := strconv.Atoi(string(listing.TotalCount))
		if err != nil || count < 1 || count > maxNativeSmokeJobs || (total >= 0 && total != count) {
			return e.New(e.CodeVerifyFailed, "native smoke job listing has an invalid, changing or excessive total count")
		}
		total = count
		remaining := total - (page-1)*nativeSmokeJobsPerPage
		if len(listing.Jobs) != min(remaining, nativeSmokeJobsPerPage) {
			return e.New(e.CodeVerifyFailed, "native smoke job listing is incomplete or exceeds its page bound")
		}
		for _, job := range listing.Jobs {
			if !numberPattern.MatchString(string(job.ID)) || seen[job.ID] || string(job.RunID) != origin.RunID ||
				job.HeadSHA != origin.Commit || (job.Attempt != "" && string(job.Attempt) != origin.Attempt) {
				return e.New(e.CodeVerifyFailed, "native smoke job does not belong uniquely to the exact qualifier attempt and commit")
			}
			seen[job.ID] = true
			if completed, wanted := required[job.Name]; wanted {
				if completed || job.Status != "completed" || job.Conclusion != "success" {
					return e.New(e.CodeGateUnmet, "native smoke job %q is duplicated or has not completed successfully", job.Name)
				}
				required[job.Name] = true
			}
		}
		if len(seen) == total {
			break
		}
	}
	for _, platform := range platforms {
		name := "native-smoke / native-" + strings.TrimPrefix(platform, "linux/")
		if !required[name] {
			return e.New(e.CodeGateUnmet, "native smoke job %q is missing from the exact qualifier attempt", name)
		}
	}
	return nil
}

func unpackNativeSmoke(ctx context.Context, archive []byte) (NativeSmokeArtifact, error) {
	return readNativeSmokeArchive(ctx, bytes.NewReader(archive), int64(len(archive)))
}

func readNativeSmokeArchive(ctx context.Context, archive io.ReaderAt, size int64) (NativeSmokeArtifact, error) {
	if size < 0 || size > maxNativeSmokeArchiveBytes {
		return NativeSmokeArtifact{}, e.New(e.CodeVerifyFailed, "native smoke archive exceeds its byte bound")
	}
	bounded := &handoffZIPReaderAt{ctx: ctx, reader: archive, metadata: true, remaining: maxQualificationZIPMetadataBytes}
	reader, err := zip.NewReader(bounded, size)
	if err != nil {
		if ctx.Err() != nil {
			return NativeSmokeArtifact{}, e.Wrap(e.CodeTimeout, ctx.Err(), "native smoke archive metadata decoding canceled")
		}
		return NativeSmokeArtifact{}, e.Wrap(e.CodeVerifyFailed, err, "invalid native smoke archive")
	}
	if len(reader.File) != 4 {
		return NativeSmokeArtifact{}, e.New(e.CodeVerifyFailed, "native smoke archive requires exactly four worker evidence files")
	}
	limits := map[string]uint64{"worker.json": maxNativeSmokeDocumentBytes, "worker.sig": maxNativeSmokeSignatureBytes,
		"worker.pem": maxNativeSmokeCertificateBytes, "worker.bundle.json": maxNativeSmokeBundleBytes}
	seen := make(map[string]bool, len(limits))
	for _, member := range reader.File {
		limit, exists := limits[member.Name]
		if !exists || seen[member.Name] || !member.Mode().IsRegular() || member.UncompressedSize64 == 0 || member.UncompressedSize64 > limit {
			return NativeSmokeArtifact{}, e.New(e.CodeVerifyFailed, "native smoke archive member has an invalid path, type, uniqueness or size")
		}
		seen[member.Name] = true
	}
	bounded.metadata = false
	var result NativeSmokeArtifact
	for _, member := range reader.File {
		input, err := member.Open()
		if ctx.Err() != nil {
			if input != nil {
				_ = input.Close()
			}
			return NativeSmokeArtifact{}, e.Wrap(e.CodeTimeout, ctx.Err(), "native smoke archive opening canceled")
		}
		if err != nil {
			return NativeSmokeArtifact{}, e.Wrap(e.CodeVerifyFailed, err, "opening native smoke archive member")
		}
		raw, readErr := io.ReadAll(io.LimitReader(handoffContextReader{ctx: ctx, reader: input}, int64(limits[member.Name])+1))
		closeErr := input.Close()
		if ctx.Err() != nil {
			return NativeSmokeArtifact{}, e.Wrap(e.CodeTimeout, ctx.Err(), "native smoke archive decoding canceled")
		}
		if readErr != nil || closeErr != nil || uint64(len(raw)) != member.UncompressedSize64 {
			return NativeSmokeArtifact{}, e.New(e.CodeVerifyFailed, "native smoke archive member is corrupt, truncated or oversized")
		}
		switch member.Name {
		case "worker.json":
			result.Document = raw
		case "worker.sig":
			result.Signature = raw
		case "worker.pem":
			result.Certificate = raw
		case "worker.bundle.json":
			result.Bundle = raw
		}
	}
	return result, nil
}

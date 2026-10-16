// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// MaxGitHubLocatorArtifacts bounds a generated attempt and an exact acquisition
// selection. Discovery uses server-side exact names, not a truncated run listing.
const MaxGitHubLocatorArtifacts = 2000

// GitHubLocatorSelection retains the generated download projection. Pattern is
// one exact name or a brace-delimited list of exact names; globs are unsupported.
// Missing pattern members are left to the consumer's completeness check. Name
// requires exactly one matching artifact. Exactly one selector must be present.
type GitHubLocatorSelection struct {
	Name          string
	Pattern       string
	MergeMultiple bool
}

type githubLocatorRun struct {
	ID             json.Number             `json:"id"`
	Attempt        json.Number             `json:"run_attempt"`
	HeadSHA        string                  `json:"head_sha"`
	HeadBranch     string                  `json:"head_branch"`
	Repository     githubLocatorRepository `json:"repository"`
	HeadRepository githubLocatorRepository `json:"head_repository"`
}

type githubLocatorRepository struct {
	ID       json.Number `json:"id"`
	FullName string      `json:"full_name"`
}

type githubLocatorArtifact struct {
	ID          json.Number `json:"id"`
	Name        string      `json:"name"`
	Size        json.Number `json:"size_in_bytes"`
	Digest      string      `json:"digest"`
	Expired     *bool       `json:"expired"`
	WorkflowRun struct {
		ID               json.Number `json:"id"`
		RepositoryID     json.Number `json:"repository_id"`
		HeadRepositoryID json.Number `json:"head_repository_id"`
		HeadSHA          string      `json:"head_sha"`
		HeadBranch       string      `json:"head_branch"`
	} `json:"workflow_run"`
}

type githubLocatorBytes struct {
	name string
	raw  []byte
}

// FetchGitHubLocators retrieves only provider-authenticated locator metadata.
// Request.Name must be empty: selection owns the names. The explicit repository
// token is never sent to ZIP storage, GHCR, or producer processes. This operation
// grants no payload, release, or qualification authority.
func FetchGitHubLocators(ctx context.Context, request Request, index string, selection GitHubLocatorSelection, token string) error {
	return newGitHubLocatorClient(token).fetch(ctx, request, index, selection)
}

func (g *githubLocatorClient) fetch(ctx context.Context, request Request, index string, selection GitHubLocatorSelection) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "CI locator acquisition requires context")
	}
	if ctx.Err() != nil {
		return safeIOError(ctx, e.CodeTimeout, "CI locator acquisition canceled")
	}
	if request.Name != "" || index == "" || strings.ContainsAny(index, "\x00\r\n") {
		return e.New(e.CodeUsage, "CI locator acquisition requires a separate selector and fresh index")
	}
	names, err := admitGitHubLocatorSelection(request, selection)
	if err != nil {
		return err
	}
	if !safeText(g.token, 16384) {
		return e.New(e.CodeTrustPolicyMissing, "CI locator acquisition requires an explicit repository token")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	directory, info, err := securepath.OpenDirectory(index)
	if err != nil {
		return e.New(e.CodeImmutableViolation, "holding fresh CI locator index failed")
	}
	defer func() { _ = directory.Close() }()
	if entries, readErr := directory.ReadDir(1); readErr != io.EOF || len(entries) != 0 {
		return e.New(e.CodeImmutableViolation, "CI locator index must be empty")
	}
	var run githubLocatorRun
	base := "/repos/" + request.Repository + "/actions/runs/" + request.RunID
	if err := g.getJSON(ctx, base+"/attempts/"+request.Attempt, &run); err != nil {
		return err
	}
	if err := run.validate(request); err != nil {
		return err
	}
	// Admit every selected provider record before fetching any ZIP. Exact-name
	// queries avoid pagination shifts while unrelated jobs add new artifacts.
	artifacts := make([]githubLocatorArtifact, 0, len(names))
	ids := make(map[json.Number]bool, len(names))
	for _, name := range names {
		var list struct {
			TotalCount json.Number             `json:"total_count"`
			Artifacts  []githubLocatorArtifact `json:"artifacts"`
		}
		if err := g.getJSON(ctx, base+"/artifacts?name="+url.QueryEscape(name)+"&per_page=100", &list); err != nil {
			return err
		}
		if list.TotalCount == "0" && len(list.Artifacts) == 0 && selection.Name == "" {
			continue
		}
		if list.TotalCount == "0" && len(list.Artifacts) == 0 {
			return e.New(e.CodeReleaseIncomplete, "required CI locator artifact is missing")
		}
		if list.TotalCount != "1" || len(list.Artifacts) != 1 {
			return e.New(e.CodeVerifyFailed, "CI locator discovery requires one exact provider match")
		}
		artifact := list.Artifacts[0]
		if err := artifact.validate(name, run); err != nil {
			return err
		}
		if ids[artifact.ID] {
			return e.New(e.CodeVerifyFailed, "CI locator provider artifact ID is repeated")
		}
		ids[artifact.ID] = true
		artifacts = append(artifacts, artifact)
	}
	// At most 2000 * 32 KiB is retained. No destination member is written until
	// every selected archive's provider digest and exact locator binding pass.
	locators := make([]githubLocatorBytes, 0, len(artifacts))
	for _, artifact := range artifacts {
		raw, err := g.downloadLocator(ctx, request, artifact)
		if err != nil {
			return err
		}
		locators = append(locators, githubLocatorBytes{name: artifact.Name, raw: raw})
	}
	if err := requireGitHubLocatorDirectory(index, info); err != nil {
		return e.New(e.CodeImmutableViolation, "CI locator index changed during acquisition")
	}
	return writeGitHubLocatorSet(ctx, index, directory, info, selection, locators)
}

func admitGitHubLocatorSelection(request Request, selection GitHubLocatorSelection) ([]string, error) {
	if (selection.Name == "") == (selection.Pattern == "") {
		return nil, e.New(e.CodeUsage, "CI locator acquisition requires exactly one name or fixed pattern")
	}
	value := selection.Name
	if value == "" {
		value = selection.Pattern
	}
	if len(value) > MaxGitHubLocatorArtifacts*257+2 {
		return nil, e.New(e.CodeUsage, "CI locator selection exceeds its bound")
	}
	names := []string{value}
	if selection.Pattern != "" && strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		names = strings.Split(value[1:len(value)-1], ",")
	}
	if len(names) == 0 || len(names) > MaxGitHubLocatorArtifacts {
		return nil, e.New(e.CodeUsage, "CI locator selection exceeds its bound")
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		request.Name = name
		if err := request.validate(); err != nil {
			return nil, err
		}
		if strings.ContainsAny(name, "{},[]!") || len(name+locatorSuffix) > 255 || seen[name] {
			return nil, e.New(e.CodeUsage, "CI locator selection contains an unsafe or repeated exact name")
		}
		seen[name] = true
	}
	slices.Sort(names)
	return names, nil
}

func (run githubLocatorRun) validate(request Request) error {
	sha, err := hex.DecodeString(run.HeadSHA)
	if string(run.ID) != request.RunID || string(run.Attempt) != request.Attempt ||
		run.Repository.FullName != request.Repository || !positiveGitHubNumber(run.Repository.ID) ||
		!positiveGitHubNumber(run.HeadRepository.ID) || validateSourceRepository(run.HeadRepository.FullName) != nil ||
		err != nil || len(sha) != 20 || strings.ToLower(run.HeadSHA) != run.HeadSHA || !safeText(run.HeadBranch, 1024) {
		return e.New(e.CodeVerifyFailed, "CI locator source does not match the authenticated Run attempt")
	}
	return nil
}

func (artifact githubLocatorArtifact) validate(name string, run githubLocatorRun) error {
	identity := artifact.WorkflowRun
	size, err := strconv.ParseInt(string(artifact.Size), 10, 64)
	if !positiveGitHubNumber(artifact.ID) || artifact.Name != name || artifact.Expired == nil || *artifact.Expired ||
		identity.ID != run.ID || identity.RepositoryID != run.Repository.ID || identity.HeadRepositoryID != run.HeadRepository.ID ||
		identity.HeadSHA != run.HeadSHA || identity.HeadBranch != run.HeadBranch ||
		err != nil || size <= 0 || size > maxGitHubLocatorZIPBytes || strconv.FormatInt(size, 10) != string(artifact.Size) {
		return e.New(e.CodeVerifyFailed, "CI locator artifact has invalid source, expiration or size metadata")
	}
	if !validSHA256(artifact.Digest) {
		return e.New(e.CodeUnpinnedInput, "CI locator artifact requires its provider SHA-256 ZIP digest")
	}
	return nil
}

func positiveGitHubNumber(value json.Number) bool {
	n, err := strconv.ParseUint(string(value), 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == string(value)
}

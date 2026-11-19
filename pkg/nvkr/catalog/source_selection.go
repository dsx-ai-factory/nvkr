// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"os"
	"runtime"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// SourceSelection describes a frozen main observation. It is not an authority
// receipt: replay authenticates the exact commit through the Git pin owner.
type SourceSelection struct {
	Repository      string `json:"repository"`
	Ref             string `json:"ref"`
	Commit          string `json:"commit"`
	TreeHash        string `json:"treeHash"`
	SourceDateEpoch int64  `json:"sourceDateEpoch"`
}

// FreezeSourceHead observes main once, before any fan-out. Subsequent work uses
// only the resulting exact commit, including when main advances or is rewritten.
func FreezeSourceHead(ctx context.Context, repository, ref string) (SourceSelection, error) {
	return freezeSourceHeadWithLimit(ctx, repository, ref, pinTreeObjectFileLimit)
}

func freezeSourceHeadWithLimit(ctx context.Context, repository, ref string, fileLimit int64) (SourceSelection, error) {
	if err := pinContextError(ctx); err != nil {
		return SourceSelection{}, err
	}
	if err := checkSourceRepoScheme("runtime-source", repository); err != nil {
		return SourceSelection{}, err
	}
	if ref != "refs/heads/main" {
		return SourceSelection{}, e.New(e.CodeInvalidCatalog, "source freeze requires refs/heads/main")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return SourceSelection{}, e.New(e.CodeEngineCapability, "bounded source freeze requires Linux or macOS")
	}
	ctx, cancel := context.WithTimeout(ctx, pinTreeTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "nvkr-freeze-source-")
	if err != nil {
		return SourceSelection{}, e.Wrap(e.CodeFetchFailed, err, "creating source freeze temporary directory")
	}
	defer os.RemoveAll(dir)
	if _, err := pinTreeGit(ctx, dir, 8192, "init", "--bare", "--template=", "-q"); err != nil {
		return SourceSelection{}, err
	}
	// Observe only the ref here; the exact-revision owner below authenticates
	// the commit and its metadata without this discovery step fetching a tree.
	raw, err := pinTreeGitWithLimit(ctx, dir, 128, fileLimit, "ls-remote", "--refs", "--", repository, ref)
	if err != nil {
		return SourceSelection{}, err
	}
	var commit string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		observedCommit, observedRef, ok := strings.Cut(line, "\t")
		if !ok || !fullSHA.MatchString(observedCommit) || observedRef == "" || strings.ContainsAny(observedRef, "\t\r ") {
			return SourceSelection{}, e.New(e.CodeFetchFailed, "source freeze received a malformed ref observation for %s in %s", ref, repository)
		}
		if observedRef != ref {
			continue
		}
		if commit != "" {
			return SourceSelection{}, e.New(e.CodeFetchFailed, "source freeze received duplicate observations for %s in %s", ref, repository)
		}
		commit = observedCommit
	}
	if commit == "" {
		return SourceSelection{}, e.New(e.CodeFetchFailed, "source freeze requires exactly one full commit for %s in %s", ref, repository)
	}
	revision, err := ResolveSourceRevision(ctx, repository, commit)
	if err != nil {
		return SourceSelection{}, err
	}
	return SourceSelection{Repository: repository, Ref: ref, Commit: revision.pin.Commit, TreeHash: revision.pin.TreeHash, SourceDateEpoch: revision.pin.SourceDateEpoch}, nil
}

// ReplaySourceSelection authenticates saved exact bytes, never today's ref.
// Missing source objects fail closed; a mutable ref is never a fallback.
func ReplaySourceSelection(ctx context.Context, selection SourceSelection) (*SourceRevision, error) {
	if selection.Ref != "refs/heads/main" || !fullSHA.MatchString(selection.Commit) || !fullSHA.MatchString(selection.TreeHash) || selection.SourceDateEpoch <= 0 {
		return nil, e.New(e.CodeUnpinnedSource, "frozen source selection requires exact commit, tree and timestamp")
	}
	revision, err := ResolveSourceRevision(ctx, selection.Repository, selection.Commit)
	if err != nil {
		return nil, err
	}
	if revision.pin.TreeHash != selection.TreeHash || revision.pin.SourceDateEpoch != selection.SourceDateEpoch {
		return nil, e.New(e.CodeUnverifiedRef, "frozen source metadata differs from exact Git commit")
	}
	return revision, nil
}

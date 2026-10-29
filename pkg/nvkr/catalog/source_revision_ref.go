// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const sourceRevisionAncestryDepth = 64

// ResolveSourceRevisionAtRef requires an exact main commit or a proven ancestor
// of main. The bounded Git owner authenticates membership before issuing the
// ordinary exact-tree revision; advancing main does not change the selected pin.
func ResolveSourceRevisionAtRef(ctx context.Context, repository, ref, commit string) (*SourceRevision, error) {
	if err := pinContextError(ctx); err != nil {
		return nil, err
	}
	if err := checkSourceRepoScheme("runtime-source", repository); err != nil {
		return nil, err
	}
	if ref != "refs/heads/main" || !fullSHA.MatchString(commit) {
		return nil, e.New(e.CodeInvalidCatalog, "runtime source membership requires refs/heads/main and a full commit")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, e.New(e.CodeEngineCapability, "bounded source membership acquisition requires Linux or macOS")
	}
	ctx, cancel := context.WithTimeout(ctx, pinTreeTimeout)
	defer cancel()
	// The usual case acquires only the two exact shallow trees. Retry a bounded
	// history only when main has advanced since the coordinator selected commit.
	for _, depth := range []int{1, sourceRevisionAncestryDepth} {
		admitted, err := sourceRevisionAtRefDepth(ctx, repository, ref, commit, depth)
		if err != nil {
			return nil, err
		}
		if admitted {
			return ResolveSourceRevision(ctx, repository, commit)
		}
	}
	return nil, e.New(e.CodeUnverifiedRef, "could not prove runtime source commit %s belongs to %s within %d commits", commit, ref, sourceRevisionAncestryDepth)
}

func sourceRevisionAtRefDepth(ctx context.Context, repository, ref, commit string, depth int) (bool, error) {
	dir, err := os.MkdirTemp("", "nvkr-source-ref-")
	if err != nil {
		return false, e.Wrap(e.CodeFetchFailed, err, "creating source membership repository")
	}
	defer os.RemoveAll(dir)
	for _, args := range [][]string{
		{"init", "--bare", "--template=", "-q"},
		{"fetch", "-q", "--depth=" + strconv.Itoa(depth), "--no-tags", "--recurse-submodules=no", "--no-write-fetch-head", "--", repository,
			ref + ":refs/nvkr/source-policy", commit + ":refs/nvkr/source-selected"},
	} {
		if _, err := pinTreeGit(ctx, dir, 8192, args...); err != nil {
			return false, err
		}
	}
	if err := validatePinTreeStore(ctx, dir); err != nil {
		return false, err
	}
	selected, err := pinTreeGit(ctx, dir, 128, "rev-parse", "refs/nvkr/source-selected^{commit}")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(selected)) != commit {
		return false, e.New(e.CodeUnverifiedRef, "fetched source membership commit disagrees with selected revision")
	}
	head, err := pinTreeGit(ctx, dir, 128, "rev-parse", "refs/nvkr/source-policy^{commit}")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(head)) == commit {
		return true, nil
	}
	if depth == 1 {
		return false, nil
	}
	_, err = pinTreeGit(ctx, dir, 128, "merge-base", "--is-ancestor", commit, "refs/nvkr/source-policy")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil // Off-main or outside the bounded proof, never admitted.
	}
	return err == nil, err
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"regexp"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// sourceConstraint is the dotted constraint grammar `catalog bump` already
// admits, without the empty segments constraintMatches tolerates.
var sourceConstraint = regexp.MustCompile(`^v?(0|[1-9][0-9]*|x|\*)(\.(0|[1-9][0-9]*|x|\*)){0,2}$`)

// SourceOverrideIsCommit classifies one requested source override. A full
// lowercase commit selects that exact revision; a dotted constraint selects the
// newest stable tag it admits. Every other value fails closed.
func SourceOverrideIsCommit(value string) (bool, error) {
	if fullSHA.MatchString(value) {
		return true, nil
	}
	if sourceConstraint.MatchString(value) {
		return false, nil
	}
	return false, e.New(e.CodeUsage, "source override %q must be a version constraint such as 1.2.x or a full lowercase commit", value)
}

// SourceTagAdmitted reports whether an exact upstream tag satisfies a constraint
// under the source's module tag prefix.
func SourceTagAdmitted(tagPrefix, constraint, tag string) bool {
	version, ok := parseSourceSemver(Source{TagPrefix: tagPrefix}, tag)
	return ok && sourceConstraint.MatchString(constraint) && constraintMatches(constraint, version)
}

// ResolveSourceConstraint authenticates the newest stable tag of one repository
// that an override constraint admits. Only tags carrying tagPrefix compete.
func ResolveSourceConstraint(ctx context.Context, repository, tagPrefix, constraint string) (*SourceRevision, error) {
	if commit, err := SourceOverrideIsCommit(constraint); err != nil || commit {
		return nil, e.New(e.CodeUsage, "source constraint resolution requires a version constraint")
	}
	if err := checkSourceRepoScheme("source-override", repository); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "source constraint resolution requires a context")
	}
	ctx, cancel := context.WithTimeout(ctx, pinTreeTimeout)
	defer cancel()
	tags, err := ListTags(ctx, repository)
	if err != nil {
		return nil, err
	}
	source := Source{Repo: repository, Constraint: constraint, TagPrefix: tagPrefix}
	tag, err := SelectTagForSource(tags, source)
	if err != nil {
		return nil, e.Wrap(e.CodeNoMatch, err, "source override for %s", repository)
	}
	return resolveSourceTag(ctx, source, tag, "")
}

// ResolveSourceTag re-authenticates one previously frozen tag. The tag must
// still resolve to the recorded commit; a moved tag fails closed.
func ResolveSourceTag(ctx context.Context, repository, tagPrefix, tag, commit string) (*SourceRevision, error) {
	if !fullSHA.MatchString(commit) || tag == "" || fullSHA.MatchString(tag) {
		return nil, e.New(e.CodeUnpinnedSource, "source tag replay requires an exact tag and full commit")
	}
	if err := checkSourceRepoScheme("source-override", repository); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "source tag replay requires a context")
	}
	ctx, cancel := context.WithTimeout(ctx, pinTreeTimeout)
	defer cancel()
	return resolveSourceTag(ctx, Source{Repo: repository, TagPrefix: tagPrefix}, tag, commit)
}

func resolveSourceTag(ctx context.Context, source Source, tag, commit string) (*SourceRevision, error) {
	pin, err := ResolvePin(ctx, Source{Repo: source.Repo, TagPrefix: source.TagPrefix}, tag)
	if err != nil {
		return nil, err
	}
	if pin.Tag != tag || (commit != "" && pin.Commit != commit) {
		return nil, e.New(e.CodeUnverifiedRef, "source tag %s of %s no longer resolves to its frozen commit", tag, source.Repo)
	}
	return &SourceRevision{repository: strings.TrimSuffix(source.Repo, ".git"), pin: pin, issued: true}, nil
}

// Tag returns the exact upstream tag, or the commit for a commit-mode revision.
func (revision *SourceRevision) Tag() string { return revision.pin.Tag }

// Commit returns the authenticated full commit.
func (revision *SourceRevision) Commit() string { return revision.pin.Commit }

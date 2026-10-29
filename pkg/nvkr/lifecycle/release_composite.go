// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// MemberIdentity is one composite member pin: its name and nvkr.definition digest.
type MemberIdentity = definition.MemberIdentity

// MemberReleaseView describes one composite member's outputs inside a verified
// Release. It is a description, not a member Release: it carries no signer,
// policy or capability, and nothing accepts it in place of a VerifiedRelease.
type MemberReleaseView struct {
	ReleaseSubject string     `json:"releaseSubject"`
	Member         string     `json:"member"`
	Artifacts      []Artifact `json:"artifacts"`
}

// Composition returns the authenticated composition record of a verified
// composite Release, or nil for a plain Release.
func (verified *VerifiedRelease) Composition() (*definition.CompositionRecord, error) {
	if verified == nil {
		return nil, e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	verified.mu.RLock()
	defer verified.mu.RUnlock()
	if !verified.valid || verified.publication == nil {
		return nil, e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	description, err := verified.publication.Description(verified.release.Subject)
	if err != nil {
		return nil, err
	}
	return description.Metadata.Composition.Clone(), nil
}

// RequireMembers requires a verified composite Release whose authenticated
// members are exactly members, by name and nvkr.definition digest.
func (verified *VerifiedRelease) RequireMembers(members []MemberIdentity) error {
	record, err := verified.Composition()
	if err != nil {
		return err
	}
	if record == nil {
		return e.New(e.CodeVerifyFailed, "verified Release is not a composite; it has no member set")
	}
	want := slices.Clone(members)
	slices.SortFunc(want, func(a, b MemberIdentity) int { return strings.Compare(a.Name, b.Name) })
	if !slices.Equal(record.Members, want) {
		return e.New(e.CodeVerifyFailed, "verified composite Release members differ from the required member set")
	}
	return nil
}

// MemberView filters the verified Release description to one member's outputs.
func (verified *VerifiedRelease) MemberView(member string) (MemberReleaseView, error) {
	record, err := verified.Composition()
	if err != nil {
		return MemberReleaseView{}, err
	}
	if record == nil {
		return MemberReleaseView{}, e.New(e.CodeVerifyFailed, "verified Release is not a composite; it has no members")
	}
	if !slices.ContainsFunc(record.Members, func(identity MemberIdentity) bool { return identity.Name == member }) {
		return MemberReleaseView{}, e.New(e.CodeVerifyFailed, "verified composite Release has no member %q", member)
	}
	release, err := verified.Description()
	if err != nil {
		return MemberReleaseView{}, err
	}
	view := MemberReleaseView{ReleaseSubject: release.Subject, Member: member, Artifacts: []Artifact{}}
	for _, artifact := range release.Artifacts {
		if owner, _ := record.MemberOf(artifact.Name); owner == member {
			view.Artifacts = append(view.Artifacts, artifact)
		}
	}
	return view, nil
}

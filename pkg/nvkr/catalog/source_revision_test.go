// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func sourceRevisionStackFixture(t *testing.T) *Stack {
	t.Helper()
	base, err := LoadContext(t.Context(), Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	return stack
}

func TestSourceRevisionRebindsCompleteOverlayAndStampTemplates(t *testing.T) {
	stack := sourceRevisionStackFixture(t)
	component := stack.Catalog.Components["kubectl"]
	component.VersionStamp = map[string]string{"PINNED_COMMIT": "{{.Commit}}", "PINNED_VERSION": "{{.Tag}}", "PUBLIC_LITERAL": "unchanged"}
	withOverlay, err := ApplyStack(stack.Catalog, Overlay{APIVersion: OverlayAPIVersion, Kind: OverlayKind, Name: "complete-producer", RequiresCatalogDigest: stack.Digest,
		Components: map[string]ComponentReplacement{"kubectl": {Component: &component}}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := withOverlay.Catalog.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c", 40)
	revision := &SourceRevision{repository: strings.TrimSuffix(component.Repo, ".git"), pin: Pin{Tag: commit, Commit: commit, TreeHash: strings.Repeat("d", 40), SourceDateEpoch: 2000}, issued: true}
	after, err := ApplySourceRevision(withOverlay, revision)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Applied) != 2 || after.Applied[0] != withOverlay.Applied[0] || after.Applied[1].InputDigest != before {
		t.Fatal("runtime replacement lost its complete predecessor chain")
	}
	for name, original := range withOverlay.Catalog.Components {
		got := after.Catalog.Components[name]
		if original.FromSource && strings.TrimSuffix(original.Repo, ".git") == revision.repository {
			if got.Commit != commit || got.Tag != commit || got.TreeHash != revision.pin.TreeHash || got.SourceDate != 2000 || !reflect.DeepEqual(got.BuildCommand, original.BuildCommand) {
				t.Fatalf("component %s lost frozen source or authored build command", name)
			}
		} else if !reflect.DeepEqual(got, original) {
			t.Fatalf("unrelated source or acquired dependency %s changed", name)
		}
	}
	stamps := after.Catalog.Components["kubectl"].VersionStamp
	if stamps["PINNED_COMMIT"] != commit || stamps["PINNED_VERSION"] != commit || stamps["PUBLIC_LITERAL"] != "unchanged" {
		t.Fatal("version stamps did not follow their owning templates", stamps)
	}
	unchanged, err := withOverlay.Catalog.SnapshotDigest()
	if err != nil || unchanged != before {
		t.Fatal("runtime source replacement mutated its caller", err)
	}
}

func TestSourceRevisionRejectsUnissuedOrLostStampProvenance(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*Stack, *SourceRevision)
		code e.Code
	}{
		{"unissued revision", func(_ *Stack, r *SourceRevision) { r.issued = false }, e.CodeEngineCapability},
		{"unowned stamp", func(s *Stack, _ *SourceRevision) {
			c := s.Catalog.Components["kubectl"]
			c.VersionStamp = map[string]string{"CLAIMED_COMMIT": strings.Repeat("a", 40)}
			s.Catalog.Components["kubectl"] = c
		}, e.CodeUnpinnedSource},
		{"serialized description loses template provenance", func(s *Stack, _ *SourceRevision) {
			c := s.Catalog.Components["kubectl"]
			c.VersionStamp = map[string]string{"CLAIMED_COMMIT": "{{.Commit}}"}
			resolved, err := ApplyStack(s.Catalog, Overlay{APIVersion: OverlayAPIVersion, Kind: OverlayKind, Name: "stamped", RequiresCatalogDigest: s.Digest,
				Components: map[string]ComponentReplacement{"kubectl": {Component: &c}}})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(resolved.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			var description Catalog
			if err := json.Unmarshal(raw, &description); err != nil {
				t.Fatal(err)
			}
			s.Catalog, s.Digest = &description, resolved.Digest
		}, e.CodeUnpinnedSource},
	} {
		t.Run(test.name, func(t *testing.T) {
			stack := sourceRevisionStackFixture(t)
			commit := strings.Repeat("c", 40)
			revision := &SourceRevision{repository: strings.TrimSuffix(stack.Catalog.Components["kubectl"].Repo, ".git"), pin: Pin{Tag: commit, Commit: commit, TreeHash: strings.Repeat("d", 40), SourceDateEpoch: 2000}, issued: true}
			test.edit(stack, revision)
			_, err := ApplySourceRevision(stack, revision)
			assertCatalogContractCode(t, err, test.code)
		})
	}
}

func TestResolveSourceRevisionAuthenticatesHeldGitTree(t *testing.T) {
	repository, pin, _ := pinTreeFixture(t, map[string][]byte{"readme": []byte("exact main fixture")})
	const selected = "https://github.com/example/runtime-source"
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repository+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", selected)
	revision, err := ResolveSourceRevision(t.Context(), selected, pin.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if !revision.issued || revision.repository != selected || revision.pin.Commit != pin.Commit || revision.pin.TreeHash != pin.TreeHash || revision.pin.Tag != pin.Commit {
		t.Fatal("source revision did not bind the fetched tree")
	}
}

func TestResolveSourceRevisionRejectsMissingOrCanceledContext(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{{"missing context", nil, e.CodeUsage}, {"canceled before Git", canceled, e.CodeTimeout}} {
		t.Run(test.name, func(t *testing.T) {
			revision, err := ResolveSourceRevision(test.ctx, "https://github.com/example/runtime-source", strings.Repeat("a", 40))
			assertCatalogContractCode(t, err, test.code)
			if revision != nil {
				t.Fatal("failed resolution issued a revision")
			}
		})
	}
}

func TestResolveSourceRevisionAtRefAdmitsOnlyMainMembership(t *testing.T) {
	repository, first, git := pinTreeFixture(t, map[string][]byte{"readme": []byte("main membership")})
	git("branch", "-M", "main")
	git("-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "main advance")
	main := git("rev-parse", "HEAD")
	git("checkout", "-qb", "off-main", first.Commit)
	git("-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "unmerged sibling")
	sibling := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")
	const selected = "https://github.com/example/runtime-source"
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repository+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", selected)
	for _, test := range []struct {
		name, commit string
		code         e.Code
	}{
		{"current main", main, ""},
		{"main advanced after selection", first.Commit, ""},
		{"fetchable unmerged sibling", sibling, e.CodeUnverifiedRef},
	} {
		t.Run(test.name, func(t *testing.T) {
			revision, err := ResolveSourceRevisionAtRef(t.Context(), selected, "refs/heads/main", test.commit)
			if test.code != "" {
				assertCatalogContractCode(t, err, test.code)
				if revision != nil {
					t.Fatal("failed membership issued a source revision")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !revision.issued || revision.pin.Commit != test.commit || revision.pin.TreeHash != first.TreeHash {
				t.Fatal("main membership changed the selected exact source tree")
			}
		})
	}
	if _, err := ResolveSourceRevision(t.Context(), selected, sibling); err != nil {
		t.Fatal("ordinary local exact-commit resolution acquired an unintended main policy", err)
	}
}

func TestResolveSourceRevisionAtRefRejectsUnavailableBoundedProof(t *testing.T) {
	repository, first, git := pinTreeFixture(t, map[string][]byte{"readme": []byte("bounded main history")})
	git("branch", "-M", "main")
	for range sourceRevisionAncestryDepth {
		git("-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "main advance")
	}
	const selected = "https://github.com/example/runtime-source"
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repository+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", selected)
	revision, err := ResolveSourceRevisionAtRef(t.Context(), selected, "refs/heads/main", first.Commit)
	assertCatalogContractCode(t, err, e.CodeUnverifiedRef)
	if revision != nil {
		t.Fatal("unavailable bounded ancestry proof issued a revision")
	}
}

func TestResolveSourceRevisionAtRefAdmitsPolicyBeforeGit(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name, ref, commit string
		ctx               context.Context
		code              e.Code
	}{
		{"missing context", "refs/heads/main", strings.Repeat("a", 40), nil, e.CodeUsage},
		{"canceled context", "refs/heads/main", strings.Repeat("a", 40), canceled, e.CodeTimeout},
		{"wrong branch", "refs/heads/feature", strings.Repeat("a", 40), t.Context(), e.CodeInvalidCatalog},
		{"mutable source", "refs/heads/main", "main", t.Context(), e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			revision, err := ResolveSourceRevisionAtRef(test.ctx, "https://github.com/example/runtime-source", test.ref, test.commit)
			assertCatalogContractCode(t, err, test.code)
			if revision != nil {
				t.Fatal("inadmissible policy issued a revision")
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBindSourceProducerPreservesCompleteContract(t *testing.T) {
	cat := sourceRevisionStackFixture(t).Catalog
	original := cat.Components["kubectl"]
	for _, tc := range []struct {
		name    string
		invalid bool
	}{{"explicit source", false}, {"authored source pin", true}} {
		t.Run(tc.name, func(t *testing.T) {
			declaration := original
			declaration.Repo, declaration.Tag, declaration.Commit, declaration.TreeHash, declaration.SourceDate = "", "", "", "", 0
			declaration.VersionStamp = nil
			if tc.invalid {
				declaration.Commit = strings.Repeat("a", 40)
			}
			got, err := cat.BindSourceProducer("kubectl", declaration)
			if tc.invalid {
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := original
			want.VersionStamp = nil
			if !reflect.DeepEqual(got, want) {
				t.Fatal("source binding changed fetch, toolchain or packaging contract")
			}
		})
	}
}

func TestSourceRevisionRendersRecipeWithoutInventingRelease(t *testing.T) {
	stack := sourceRevisionStackFixture(t)
	c := stack.Catalog.Components["kubectl"]
	c.VersionStamp = nil
	c.SourceRecipe = &SourceRecipe{BuildCommand: []string{"echo", "{{.Tag}}", "{{.ReleaseVersion}}"}, ArtifactPath: c.ArtifactPath, CommitVersion: "1.2.3"}
	if err := c.resolveSourceRecipe(); err != nil {
		t.Fatal(err)
	}
	stack, err := ApplyStack(stack.Catalog, Overlay{APIVersion: OverlayAPIVersion, Kind: OverlayKind, Name: "recipe", RequiresCatalogDigest: stack.Digest, Components: map[string]ComponentReplacement{"kubectl": {Component: &c}}})
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c", 40)
	after, err := ApplySourceRevision(stack, &SourceRevision{repository: strings.TrimSuffix(c.Repo, ".git"), pin: Pin{Tag: commit, Commit: commit, TreeHash: strings.Repeat("d", 40), SourceDateEpoch: 2000}, issued: true})
	if err != nil {
		t.Fatal(err)
	}
	got := after.Catalog.Components["kubectl"]
	if got.BuildCommand[1] != commit || got.BuildCommand[2] != "1.2.3" || stack.Catalog.Components["kubectl"].BuildCommand[1] == commit {
		t.Fatal("lost exact commit, explicit packaging stream or caller ownership")
	}
}

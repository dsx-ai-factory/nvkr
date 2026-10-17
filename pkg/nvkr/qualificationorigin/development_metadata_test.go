// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDevelopmentMetadataRecoveryUsesExactProviderAndPlanSelection(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*cistore.Locator, *handoffDownloadFixture)
		code e.Code
	}{
		{name: "original metadata"},
		{"wrong attempt", func(l *cistore.Locator, _ *handoffDownloadFixture) { l.Request.Attempt = "3" }, e.CodeVerifyFailed},
		{"wrong repository", func(l *cistore.Locator, _ *handoffDownloadFixture) { l.Request.Repository = "other/nvkr" }, e.CodeVerifyFailed},
		{"other root", func(l *cistore.Locator, _ *handoffDownloadFixture) {
			l.Metadata.Root = ".nvkr/other.json"
			l.Metadata.Entries[0].Path = l.Metadata.Root
		}, e.CodeVerifyFailed},
		{"executable", func(l *cistore.Locator, _ *handoffDownloadFixture) { l.Metadata.Entries[0].Mode = 0o755 }, e.CodeVerifyFailed},
		{"duplicate carrier", func(_ *cistore.Locator, f *handoffDownloadFixture) { f.list.TotalCount = "2" }, e.CodeProvenanceMissing},
		{"expired", func(_ *cistore.Locator, f *handoffDownloadFixture) { *f.list.Artifacts[0].Expired = true }, e.CodeVerifyFailed},
		{"different branch", func(_ *cistore.Locator, f *handoffDownloadFixture) {
			f.list.Artifacts[0].WorkflowRun.HeadBranch = "other"
		}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			defer func() { _ = fixture.source.Close() }()
			fixture.source.description.Branch = "feature"
			fixture.source.description.Ref = "refs/heads/feature"
			fixture.source.description.BuilderID = "https://github.com/example/nvkr/.github/workflows/nvkr-dev.yml@refs/heads/feature"
			fixture.list.Artifacts[0].WorkflowRun.HeadBranch = "feature"
			const name = "nvkr-qualification-1234-2"
			const member = ".nvkr/carriers/predicate-release.tar"
			const root = ".nvkr/factory/predicates/release.json"
			locator := cistore.Locator{SchemaVersion: "nvkr.dev/ci-metadata/v1", Request: cistore.Request{Repository: "example/nvkr", RunID: "1234", Attempt: "2", Name: name}, Paths: []string{member}, StripPrefix: ".nvkr/carriers", Metadata: &cistore.MetadataContent{Root: root, Entries: []cistore.MetadataEntry{{Path: root, Mode: 0o600, Text: `{"original":"bytes"}`}}}}
			if test.edit != nil {
				test.edit(&locator, fixture)
			}
			raw, err := canonical.Marshal(locator)
			if err != nil {
				t.Fatal(err)
			}
			fixture.archive = handoffZIP(t, []zip.FileHeader{{Name: name + ".ci-locator.json", Method: zip.Deflate}}, raw)
			fixture.list.Artifacts[0].Digest = handoffArchiveDigest(fixture.archive)
			fixture.list.Artifacts[0].Size = json.Number(strconv.Itoa(len(fixture.archive)))
			destination := t.TempDir()
			err = fixture.github(t).DownloadDevelopmentMetadata(t.Context(), fixture.source, name, member, root, destination)
			if test.code != "" {
				assertCode(t, err, test.code)
				if _, err := os.Stat(filepath.Join(destination, root)); !os.IsNotExist(err) {
					t.Fatal("refused metadata was restored")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(filepath.Join(destination, root))
			if err != nil || string(actual) != locator.Metadata.Entries[0].Text {
				t.Fatalf("wrong restored bytes: %s, %v", actual, err)
			}
		})
	}
}

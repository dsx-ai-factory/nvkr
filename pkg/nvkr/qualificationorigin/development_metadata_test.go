// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		{"earlier attempt", func(l *cistore.Locator, _ *handoffDownloadFixture) { l.Request.Attempt = "1" }, ""},
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

func TestDevelopmentMetadataRetrySelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attempts []string
		code     e.Code
	}{
		{"newest first", []string{"2", "1"}, ""},
		{"oldest first", []string{"1", "2"}, ""},
		{"tied source attempts", []string{"1", "1"}, e.CodeVerifyFailed},
		{"future candidate", []string{"2", "3"}, e.CodeVerifyFailed},
		{"invalid older candidate", []string{"2", "0"}, e.CodeVerifyFailed},
		{"repeated artifact ID", []string{"1", "2"}, e.CodeVerifyFailed},
		{"compressed aggregate bound", []string{"1", "2", "3", "4"}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			defer func() { _ = fixture.source.Close() }()
			fixture.source.description.Branch = "feature"
			fixture.source.description.Ref = "refs/heads/feature"
			fixture.list.Artifacts[0].WorkflowRun.HeadBranch = "feature"
			const name = "nvkr-qualification-1234-2"
			const member = ".nvkr/carriers/predicate-release.tar"
			const root = ".nvkr/factory/predicates/release.json"
			base := fixture.list.Artifacts[0]
			fixture.list.Artifacts = nil
			fixture.list.TotalCount = json.Number(strconv.Itoa(len(tc.attempts)))
			archives := map[string][]byte{}
			for i, attempt := range tc.attempts {
				locator := cistore.Locator{SchemaVersion: "nvkr.dev/ci-metadata/v1", Request: cistore.Request{Repository: "example/nvkr", RunID: "1234", Attempt: attempt, Name: name}, Paths: []string{member}, StripPrefix: ".nvkr/carriers", Metadata: &cistore.MetadataContent{Root: root, Entries: []cistore.MetadataEntry{{Path: root, Mode: 0o600, Text: attempt}}}}
				raw, err := canonical.Marshal(locator)
				if err != nil {
					t.Fatal(err)
				}
				archive := handoffZIP(t, []zip.FileHeader{{Name: name + ".ci-locator.json", Method: zip.Deflate}}, raw)
				id := strconv.Itoa(i + 1)
				archives[id] = archive
				artifact := base
				artifact.ID, artifact.Size, artifact.Digest = json.Number(id), json.Number(strconv.Itoa(len(archive))), handoffArchiveDigest(archive)
				fixture.list.Artifacts = append(fixture.list.Artifacts, artifact)
			}
			if tc.name == "repeated artifact ID" {
				fixture.list.Artifacts[1].ID = fixture.list.Artifacts[0].ID
			}
			if tc.name == "compressed aggregate bound" {
				for i := range fixture.list.Artifacts {
					fixture.list.Artifacts[i].Size = "41943040"
				}
			}
			g := NewGitHub("read-secret")
			g.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if _, ok := r.Context().Deadline(); !ok || r.Method != http.MethodGet {
					t.Fatal("unbounded or mutating request")
				}
				var body []byte
				status := http.StatusOK
				header := http.Header{}
				if r.URL.Host == "api.github.com" {
					if r.Header.Get("Authorization") != "Bearer read-secret" {
						t.Fatal("missing scoped token")
					}
					if strings.HasSuffix(r.URL.Path, "/artifacts") {
						var err error
						body, err = json.Marshal(fixture.list)
						if err != nil {
							t.Fatal(err)
						}
					} else {
						if tc.name == "repeated artifact ID" || tc.name == "compressed aggregate bound" {
							t.Fatal("invalid candidate list reached archive download")
						}
						parts := strings.Split(r.URL.Path, "/")
						id := parts[len(parts)-2]
						if archives[id] == nil {
							t.Fatal("unexpected archive")
						}
						status = http.StatusFound
						header.Set("Location", "https://artifacts.blob.core.windows.net/"+id)
					}
				} else {
					if r.URL.Host != "artifacts.blob.core.windows.net" || r.Header.Get("Authorization") != "" {
						t.Fatal("credential or destination escaped")
					}
					body = archives[strings.TrimPrefix(r.URL.Path, "/")]
				}
				return &http.Response{StatusCode: status, Header: header, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			destination := t.TempDir()
			err := g.DownloadDevelopmentMetadata(t.Context(), fixture.source, name, member, root, destination)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if _, err := os.Stat(filepath.Join(destination, root)); !os.IsNotExist(err) {
					t.Fatal("invalid retry wrote metadata")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(destination, root))
			if err != nil || string(raw) != "2" {
				t.Fatalf("lost newest original metadata: %s, %v", raw, err)
			}
		})
	}
}

func TestDevelopmentMetadataDecodedAggregateIncludesUnselectedCandidates(t *testing.T) {
	for _, remaining := range []uint64{0, 1} {
		t.Run(strconv.FormatUint(remaining, 10), func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			defer func() { _ = fixture.source.Close() }()
			origin, err := fixture.source.Description()
			if err != nil {
				t.Fatal(err)
			}
			const name = "nvkr-qualification-1234-2"
			fixture.archive = handoffZIP(t, []zip.FileHeader{{Name: name + ".ci-locator.json", Method: zip.Deflate}}, []byte("{}"))
			artifact := fixture.list.Artifacts[0]
			artifact.Digest = handoffArchiveDigest(fixture.archive)
			artifact.Size = json.Number(strconv.Itoa(len(fixture.archive)))
			fixture.list.Artifacts[0] = artifact
			used := uint64(128<<20) - remaining
			_, err = fixture.github(t).downloadDevelopmentLocator(t.Context(), fixture.source, origin, name, artifact, &used)
			assertCode(t, err, e.CodeVerifyFailed)
			if !strings.Contains(err.Error(), "decoded aggregate") {
				t.Fatalf("wrong refusal: %v", err)
			}
		})
	}
}

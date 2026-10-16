// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubLocatorSelectionAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selection GitHubLocatorSelection
		valid     bool
	}{
		{"exact", GitHubLocatorSelection{Name: "nvkr-helper"}, true},
		{"singleton pattern", GitHubLocatorSelection{Pattern: "nvkr-helper"}, true},
		{"brace selection", GitHubLocatorSelection{Pattern: "{nvkr-b,nvkr-a}"}, true},
		{"empty", GitHubLocatorSelection{}, false},
		{"both", GitHubLocatorSelection{Name: "a", Pattern: "a"}, false},
		{"wildcard", GitHubLocatorSelection{Pattern: "nvkr-*"}, false},
		{"duplicate", GitHubLocatorSelection{Pattern: "{a,a}"}, false},
		{"empty member", GitHubLocatorSelection{Pattern: "{a,}"}, false},
		{"traversal", GitHubLocatorSelection{Name: "../a"}, false},
		{"oversized", GitHubLocatorSelection{Pattern: strings.Repeat("a", MaxGitHubLocatorArtifacts*257+3)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := admitGitHubLocatorSelection(testRequest, tc.selection)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertCode(t, err, e.CodeUsage)
		})
	}
}

func TestGitHubLocatorAcquisitionAuthenticatesBeforeWriting(t *testing.T) {
	for _, scenario := range []string{"success flat", "success subdirectories", "singleton subdirectory", "missing pattern", "missing exact", "duplicate", "wrong head", "expired", "missing digest", "changed zip", "wrong attempt", "redirect", "secret failure", "occupied index"} {
		t.Run(scenario, func(t *testing.T) {
			index := t.TempDir()
			request := testRequest
			request.Name = ""
			selection := GitHubLocatorSelection{Pattern: "{nvkr-a,nvkr-b}", MergeMultiple: scenario != "success subdirectories" && scenario != "singleton subdirectory"}
			if scenario == "singleton subdirectory" {
				selection.Pattern = "nvkr-a"
			}
			if scenario == "missing exact" {
				selection = GitHubLocatorSelection{Name: "nvkr-a"}
			}
			if scenario == "occupied index" {
				writePayload(t, index, "existing", "retained", 0o600)
			}
			run := githubLocatorRun{ID: json.Number(request.RunID), Attempt: json.Number(request.Attempt), HeadSHA: strings.Repeat("a", 40), HeadBranch: "main",
				Repository: githubLocatorRepository{ID: "1", FullName: request.Repository}, HeadRepository: githubLocatorRepository{ID: "1", FullName: request.Repository}}
			bodies := make(map[string][]byte)
			artifacts := make(map[string]githubLocatorArtifact)
			for i, name := range []string{"nvkr-a", "nvkr-b"} {
				selected := request
				selected.Name = name
				if scenario == "wrong attempt" && i == 1 {
					selected.Attempt = "999"
				}
				locator, err := canonical.Marshal(githubAdapterLocator(selected))
				if err != nil {
					t.Fatal(err)
				}
				body := githubLocatorZIPFixture(t, name+locatorSuffix, locator, false)
				id := strconv.Itoa(i + 1)
				hash := sha256.Sum256(body)
				expired := false
				artifact := githubLocatorArtifact{ID: json.Number(id), Name: name, Size: json.Number(strconv.Itoa(len(body))), Digest: "sha256:" + hex.EncodeToString(hash[:]), Expired: &expired}
				artifact.WorkflowRun.ID = run.ID
				artifact.WorkflowRun.RepositoryID, artifact.WorkflowRun.HeadRepositoryID = "1", "1"
				artifact.WorkflowRun.HeadSHA, artifact.WorkflowRun.HeadBranch = run.HeadSHA, run.HeadBranch
				if i == 1 {
					switch scenario {
					case "wrong head":
						artifact.WorkflowRun.HeadSHA = strings.Repeat("b", 40)
					case "expired":
						expired = true
					case "missing digest":
						artifact.Digest = ""
					case "changed zip":
						body[len(body)-1] ^= 1
					}
				}
				bodies[id], artifacts[name] = body, artifact
			}
			calls, archives := 0, 0
			g := newGitHubLocatorClient("test-repository-token")
			g.wait = func(context.Context, time.Duration) error { return nil }
			g.client.Transport = chunkRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host == "account.blob.core.windows.net" {
					archives++
					if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
						t.Fatal("repository credential escaped to ZIP storage")
					}
					if entries, err := os.ReadDir(index); err != nil || len(entries) != 0 {
						t.Fatal("index was written before complete ZIP admission")
					}
					if scenario == "secret failure" {
						return nil, errors.New("SECRET raw provider credential URL")
					}
					if scenario == "redirect" {
						return githubLocatorHTTPFixture(http.StatusFound, nil, http.Header{"Location": {"https://evil.example/secret"}}), nil
					}
					return githubLocatorHTTPFixture(http.StatusOK, bodies[strings.TrimPrefix(r.URL.Path, "/")], nil), nil
				}
				if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer test-repository-token" {
					t.Fatal("unexpected API credential boundary")
				}
				if strings.Contains(r.URL.Path, "/attempts/") {
					raw, _ := json.Marshal(run)
					return githubLocatorHTTPFixture(200, raw, nil), nil
				}
				if strings.HasSuffix(r.URL.Path, "/artifacts") {
					if r.URL.Query().Get("per_page") != "100" {
						t.Fatal("unbounded exact-name query")
					}
					name := r.URL.Query().Get("name")
					if scenario == "missing exact" || (scenario == "missing pattern" && name == "nvkr-b") {
						return githubLocatorHTTPFixture(200, []byte(`{"total_count":0,"artifacts":[]}`), nil), nil
					}
					items := []githubLocatorArtifact{artifacts[name]}
					if scenario == "duplicate" {
						items = append(items, artifacts[name])
					}
					raw, _ := json.Marshal(map[string]any{"total_count": len(items), "artifacts": items})
					return githubLocatorHTTPFixture(200, raw, nil), nil
				}
				if strings.HasSuffix(r.URL.Path, "/zip") {
					parts := strings.Split(r.URL.Path, "/")
					return githubLocatorHTTPFixture(302, nil, http.Header{"Location": {"https://account.blob.core.windows.net/" + parts[len(parts)-2] + "?signed=SECRET"}}), nil
				}
				t.Fatal("unexpected request")
				return nil, nil
			})
			err := g.fetch(t.Context(), request, index, selection)
			switch scenario {
			case "success flat", "success subdirectories", "singleton subdirectory", "missing pattern":
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"nvkr-a", "nvkr-b"} {
					if (scenario == "missing pattern" || scenario == "singleton subdirectory") && name == "nvkr-b" {
						continue
					}
					path := filepath.Join(index, name+locatorSuffix)
					if scenario == "success subdirectories" || scenario == "singleton subdirectory" {
						path = filepath.Join(index, name, name+locatorSuffix)
					}
					if _, err := os.ReadFile(path); err != nil {
						t.Fatal(err)
					}
				}
				return
			case "missing exact":
				assertCode(t, err, e.CodeReleaseIncomplete)
			case "missing digest":
				assertCode(t, err, e.CodeUnpinnedInput)
			case "redirect":
				assertCode(t, err, e.CodeFetchFailed)
			case "secret failure":
				assertCode(t, err, e.CodeUnavailable)
			case "occupied index":
				assertCode(t, err, e.CodeImmutableViolation)
				if calls != 0 {
					t.Fatal("occupied index reached provider")
				}
				return
			default:
				assertCode(t, err, e.CodeVerifyFailed)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("raw provider diagnostic escaped")
			}
			if entries, err := os.ReadDir(index); err != nil || len(entries) != 0 {
				t.Fatal("failed acquisition left locator outputs")
			}
			if (scenario == "wrong head" || scenario == "duplicate" || scenario == "expired" || scenario == "missing digest") && archives != 0 {
				t.Fatal("unadmitted selection fetched ZIP bytes")
			}
		})
	}
}

func githubLocatorHTTPFixture(status int, body []byte, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}
}

func githubLocatorZIPFixture(t *testing.T, name string, raw []byte, extra bool) []byte {
	t.Helper()
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	member, err := w.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write(raw); err != nil {
		t.Fatal(err)
	}
	if extra {
		if _, err := w.Create("unexpected"); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestGitHubLocatorZIPRejectsUnexpectedMembersAndBounds(t *testing.T) {
	raw, err := canonical.Marshal(githubAdapterLocator(testRequest))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		archive []byte
	}{
		{"extra member", githubLocatorZIPFixture(t, testRequest.Name+locatorSuffix, raw, true)},
		{"wrong path", githubLocatorZIPFixture(t, "../"+testRequest.Name+locatorSuffix, raw, false)},
		{"large member", githubLocatorZIPFixture(t, testRequest.Name+locatorSuffix, bytes.Repeat([]byte("a"), MaxLocatorBytes+1), false)},
		{"large archive", make([]byte, maxGitHubLocatorZIPBytes+1)},
		{"noncanonical", githubLocatorZIPFixture(t, testRequest.Name+locatorSuffix, append(raw, '\n'), false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := unpackGitHubLocator(t.Context(), tc.archive, testRequest)
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestGitHubLocatorWriterPreservesCollisionAndRemovesOwnedFiles(t *testing.T) {
	for _, flat := range []bool{true, false} {
		t.Run(strconv.FormatBool(flat), func(t *testing.T) {
			index := t.TempDir()
			root, _, err := securepath.OpenDirectory(index)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			locator := githubLocatorBytes{name: "nvkr-a", raw: []byte("validated locator fixture")}
			if err := writeGitHubLocator(root, locator, flat); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(index, locator.name+locatorSuffix)
			var childInfo os.FileInfo
			if !flat {
				path = filepath.Join(index, locator.name, locator.name+locatorSuffix)
				childInfo, err = os.Stat(filepath.Dir(path))
				if err != nil {
					t.Fatal(err)
				}
			}
			assertCode(t, writeGitHubLocator(root, githubLocatorBytes{name: locator.name, raw: []byte("replacement")}, flat), e.CodeImmutableViolation)
			retained, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(retained, locator.raw) {
				t.Fatal("colliding locator was replaced or removed")
			}
			if err := rollbackGitHubLocatorWrite(root, locator.name, flat, childInfo, "", true); err != nil {
				t.Fatal(err)
			}
			if entries, err := os.ReadDir(index); err != nil || len(entries) != 0 {
				t.Fatal("owned locator rollback left files")
			}
		})
	}
}

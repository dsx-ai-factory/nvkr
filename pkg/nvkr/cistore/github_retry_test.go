// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubRetryRejectsChangedSourceBeforeTransfer(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Request)
	}{
		{"repository", func(r *Request) { r.Repository = "Other/Source" }},
		{"run", func(r *Request) { r.RunID = "124" }},
		{"name", func(r *Request) { r.Name = "other" }},
		{"future", func(r *Request) { r.Attempt = "3" }},
		{"zero", func(r *Request) { r.Attempt = "0" }},
		{"noncanonical", func(r *Request) { r.Attempt = "01" }},
		{"overflow", func(r *Request) { r.Attempt = "18446744073709551616" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			locator := publicMetadataFixture()
			locator.Request.Attempt = "1"
			test.mutate(&locator.Request)
			raw, err := canonical.Marshal(locator)
			if err != nil {
				t.Fatal(err)
			}
			archive := githubLocatorZIPFixture(t, testRequest.Name+locatorSuffix, raw, false)
			_, err = unpackGitHubLocator(t.Context(), archive, testRequest, true)
			assertCode(t, err, e.CodeVerifyFailed)
			// Index restoration independently rejects scope substitution, even
			// when provider acquisition is not invoked by this controlled test.
			for _, nested := range []bool{false, true} {
				index, destination := t.TempDir(), t.TempDir()
				writeNestedLocator(t, index, testRequest.Name, locator)
				backend := newBackend()
				backend.client.Transport, backend.transport = nestedForbiddenNetwork{t}, nestedForbiddenNetwork{t}
				options := Options{AllowEarlierAttempts: true}
				if nested {
					err = backend.downloadGitHubNested(t.Context(), testRequest, index, destination, map[string]NestedArchive{testRequest.Name: {Member: locator.Paths[0], Allowed: []string{locator.Metadata.Root}}}, options)
				} else {
					err = backend.downloadGitHub(t.Context(), testRequest, index, destination, options)
				}
				assertCode(t, err, e.CodeVerifyFailed)
				entries, err := os.ReadDir(destination)
				if err != nil || len(entries) != 0 {
					t.Fatal("invalid retry wrote files")
				}
			}
		})
	}
}

func TestGitHubRetryZIPPreservesOriginalBindings(t *testing.T) {
	for _, test := range []struct {
		source, current string
		allow, valid    bool
	}{
		{"1", "2", false, false}, {"1", "2", true, true},
		{"2", "2", false, true}, {"2", "2", true, true},
		{"9", "10", true, true}, {"10", "9", true, false},
	} {
		t.Run(test.source+"/"+test.current+"/"+map[bool]string{true: "retry", false: "exact"}[test.allow], func(t *testing.T) {
			locator := githubAdapterLocator(testRequest)
			locator.Request.Attempt = test.source
			request := testRequest
			request.Attempt = test.current
			raw, err := canonical.Marshal(locator)
			if err != nil {
				t.Fatal(err)
			}
			got, err := unpackGitHubLocator(t.Context(), githubLocatorZIPFixture(t, request.Name+locatorSuffix, raw, false), request, test.allow)
			if !test.valid {
				assertCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil || !bytes.Equal(raw, got) {
				t.Fatalf("original binding changed: %v", err)
			}
		})
	}
}

// Exercise both transport forms through the actual restoration owners. A partial
// rerun retains the producer's attempt, including the private image/config binding.
func TestGitHubRetryRestoresOriginalAttempt(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		for _, nested := range []bool{false, true} {
			for _, allow := range []bool{false, true} {
				name := "private"
				if metadata {
					name = "metadata"
				}
				if nested {
					name += "/nested"
				} else {
					name += "/ordinary"
				}
				if allow {
					name += "/retry"
				} else {
					name += "/exact"
				}
				t.Run(name, func(t *testing.T) {
					f := newStorageFixture(t, admittedPolicy)
					source := testRequest
					source.Attempt = "1"
					locator := publicMetadataFixture()
					locator.Request = source
					member, root, content := locator.Paths[0], locator.Metadata.Root, locator.Metadata.Entries[0].Text
					if !metadata {
						workspace := t.TempDir()
						writePayload(t, workspace, root, content, 0o644)
						if err := transportarchive.Pack(t.Context(), filepath.Join(workspace, member), workspace, []string{root}); err != nil {
							t.Fatal(err)
						}
						var err error
						locator, err = f.b.upload(t.Context(), source, workspace, []string{member}, testOptions)
						if err != nil {
							t.Fatal(err)
						}
					}
					index, destination := t.TempDir(), t.TempDir()
					writeNestedLocator(t, index, source.Name, locator)
					options := testOptions
					options.AllowEarlierAttempts = allow
					if metadata {
						options.Token = ""
					}
					var err error
					if nested {
						err = f.b.downloadGitHubNested(t.Context(), testRequest, index, destination, map[string]NestedArchive{source.Name: {Member: member, Allowed: []string{root}}}, options)
					} else {
						err = f.b.downloadGitHub(t.Context(), testRequest, index, destination, options)
					}
					if !allow {
						assertCode(t, err, e.CodeVerifyFailed)
						entries, readErr := os.ReadDir(destination)
						if readErr != nil || len(entries) != 0 {
							t.Fatal("rejected retry wrote files")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if !nested {
						if err := transportarchive.Unpack(t.Context(), filepath.Join(destination, filepath.Base(member)), destination, []string{root}); err != nil {
							t.Fatal(err)
						}
					}
					raw, err := os.ReadFile(filepath.Join(destination, root))
					if err != nil || string(raw) != content {
						t.Fatalf("restored bytes differ: %q %v", raw, err)
					}
					// Even with the GitHub opt-in, direct store consumption stays exact.
					assertCode(t, f.b.download(t.Context(), testRequest, locator, t.TempDir(), options), e.CodeVerifyFailed)
				})
			}
		}
	}
}

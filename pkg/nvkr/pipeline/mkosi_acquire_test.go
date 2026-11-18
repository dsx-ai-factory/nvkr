// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

func mkosiAcquisitionFixture() (*mkosi.Spec, map[string]string) {
	digest := func(value string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value))) }
	config := "[Output]\nFormat=oci\n[Build]\nExtraSearchPaths=" + mkosi.ArchiveToolsDirectory + "\n"
	metadata, archive := "pinned repository metadata", "pinned archive bytes"
	return &mkosi.Spec{Configuration: config, ConfigurationDigest: digest(config), Roots: []mkosi.Root{{
		Name: "runtime", Distribution: "ubuntu", Release: "noble", Inputs: material.PackageInputs{
			SchemaVersion: material.PackageInputsSchema, PackageManager: "apt", Architecture: "amd64", Snapshot: "20261109T000000Z",
			Repositories: []material.PackageRepository{{URI: "https://packages.invalid/metadata", Digest: digest(metadata), Size: int64(len(metadata))}},
			Packages: []material.PackageInput{{Name: "base-files", Version: "1.0", Architecture: "amd64", URI: "https://packages.invalid/archive",
				Digest: digest(archive), Size: int64(len(archive)), RepositoryDigest: digest(metadata)}},
		},
	}}}, map[string]string{"/metadata": metadata, "/archive": archive}
}

func TestMkosiAcquisitionRetainsExactInputs(t *testing.T) {
	spec, contents := mkosiAcquisitionFixture()
	source := t.TempDir()
	client := &http.Client{Transport: releaseExecutableRoundTripper(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		if remaining := time.Until(deadline); !ok || remaining < 19*time.Minute || remaining > 20*time.Minute {
			t.Errorf("request lost the twenty-minute acquisition bound: %v, present=%t", remaining, ok)
		}
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			t.Error("package request inherited credentials or changed method")
		}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: io.NopCloser(strings.NewReader(contents[request.URL.Path]))}, nil
	})}
	var diagnostic bytes.Buffer
	if err := acquireMkosiInputsWithClient(t.Context(), spec, source, client, &diagnostic); err != nil {
		t.Fatal(err)
	}
	wantBytes := len(contents["/metadata"]) + len(contents["/archive"])
	if !strings.Contains(diagnostic.String(), fmt.Sprintf("files=2 completed=2 body_bytes=%d", wantBytes)) ||
		strings.Count(diagnostic.String(), "mkosi acquisition slow input:") != 2 || strings.Contains(diagnostic.String(), "https:") {
		t.Fatalf("acquisition diagnostics lost byte totals or exposed transport: %s", diagnostic.String())
	}
	root := spec.Roots[0]
	for name, want := range map[string]string{
		"metadata/" + strings.TrimPrefix(root.Inputs.Repositories[0].Digest, "sha256:"):      contents["/metadata"],
		"packages/" + strings.TrimPrefix(root.Inputs.Packages[0].Digest, "sha256:") + ".deb": contents["/archive"],
	} {
		path := filepath.Join(source, mkosiInputDirectory, root.Name, name)
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("retained input %s: %q, %v", name, got, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o444 {
			t.Fatalf("retained input mode: %v, %v", info, err)
		}
	}
	want, err := root.Inputs.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(source, mkosiInputDirectory, root.Name, "inputs.json"))
	if err != nil || string(got) != string(want) {
		t.Fatalf("retained input record mismatch: %v", err)
	}
	if err := acquireMkosiInputsWithClient(t.Context(), spec, source, client, nil); err == nil {
		t.Fatal("accepted an occupied input directory")
	}
}

func TestMkosiAcquisitionRejectsIncompleteDownloads(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		size   int64
		code   e.Code
	}{
		{"status", http.StatusForbidden, "", -1, e.CodeFetchFailed},
		{"advertised overflow", http.StatusOK, "", 1000, e.CodeFetchFailed},
		{"stream overflow", http.StatusOK, strings.Repeat("x", 1000), -1, e.CodeFetchFailed},
		{"substitution", http.StatusOK, "substituted", -1, e.CodeVerifyFailed},
		{"truncated", http.StatusOK, "pinned", -1, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, _ := mkosiAcquisitionFixture()
			source := t.TempDir()
			client := &http.Client{Transport: releaseExecutableRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, ContentLength: tc.size, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			var diagnostic bytes.Buffer
			err := acquireMkosiInputsWithClient(t.Context(), spec, source, client, &diagnostic)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("got %v; want %s", err, tc.code)
			}
			if !strings.Contains(diagnostic.String(), "mkosi acquisition summary:") || !strings.Contains(diagnostic.String(), "succeeded=false") {
				t.Fatalf("failed acquisition omitted timing summary: %s", diagnostic.String())
			}
			if _, err := os.Stat(filepath.Join(source, mkosiInputDirectory, "runtime", "inputs.json")); !os.IsNotExist(err) {
				t.Fatalf("failed acquisition recorded completed inputs: %v", err)
			}
		})
	}
}

func TestMkosiAcquisitionCancellationJoinsRequests(t *testing.T) {
	spec, _ := mkosiAcquisitionFixture()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var active atomic.Int32
	started := make(chan struct{}, 2)
	client := &http.Client{Transport: releaseExecutableRoundTripper(func(request *http.Request) (*http.Response, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	result := make(chan error, 1)
	source := t.TempDir()
	go func() { result <- acquireMkosiInputsWithClient(ctx, spec, source, client, nil) }()
	<-started
	<-started
	cancel()
	err := <-result
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("got %v; want timeout", err)
	}
	if active.Load() != 0 {
		t.Fatal("acquisition returned before joining downloads")
	}
	if _, err := os.Stat(filepath.Join(source, mkosiInputDirectory, "runtime", "inputs.json")); !os.IsNotExist(err) {
		t.Fatalf("canceled acquisition recorded completed inputs: %v", err)
	}
}

func TestMkosiAcquisitionRetriesTransientResponse(t *testing.T) {
	for _, substituted := range []bool{false, true} {
		t.Run(fmt.Sprintf("substituted=%t", substituted), func(t *testing.T) {
			spec, contents := mkosiAcquisitionFixture()
			var attempts atomic.Int32
			client := &http.Client{Transport: releaseExecutableRoundTripper(func(request *http.Request) (*http.Response, error) {
				body := contents[request.URL.Path]
				status := http.StatusOK
				if request.URL.Path == "/archive" {
					if attempts.Add(1) == 1 {
						status = http.StatusServiceUnavailable
						body = "temporary failure"
					} else if substituted {
						body = "wrong bytes"
					}
				}
				return &http.Response{StatusCode: status, ContentLength: -1, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			var diagnostic bytes.Buffer
			err := acquireMkosiInputsWithClient(t.Context(), spec, t.TempDir(), client, &diagnostic)
			if substituted {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
					t.Fatalf("retried input bypassed integrity: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != 2 {
				t.Fatalf("got %d attempts, want exactly one retry", attempts.Load())
			}
			if strings.Count(diagnostic.String(), "mkosi acquisition retry:") != 1 || !strings.Contains(diagnostic.String(), "http_status=503 attempt=2") {
				t.Fatalf("transient HTTP retry was not logged: %s", diagnostic.String())
			}
		})
	}
}

func TestMkosiAcquisitionRetryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var attempts int
	client := &http.Client{Transport: releaseExecutableRoundTripper(func(*http.Request) (*http.Response, error) {
		attempts++
		cancel()
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	response, err := requestMkosiInput(ctx, client, "https://packages.invalid/archive", nil)
	if response != nil {
		defer response.Body.Close()
	}
	if !errors.Is(err, context.Canceled) || response != nil || attempts != 1 {
		t.Fatalf("retry ignored cancellation: response=%v error=%v attempts=%d", response, err, attempts)
	}
}

func TestMkosiAcquisitionAdmissionBeforeFilesystem(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		spec, _ := mkosiAcquisitionFixture()
		ctx, cancel := context.WithCancel(t.Context())
		if invalid {
			spec.Configuration += "changed"
		} else {
			cancel()
		}
		source := t.TempDir()
		err := acquireMkosiInputsWithClient(ctx, spec, source, &http.Client{}, nil)
		cancel()
		want := e.CodeTimeout
		if invalid {
			want = e.CodeUnpinnedInput
		}
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != want {
			t.Fatalf("got %v; want %s", err, want)
		}
		entries, err := os.ReadDir(source)
		if err != nil || len(entries) != 0 {
			t.Fatalf("admission failure wrote inputs: %v, %v", entries, err)
		}
	}
}

func TestMkosiFetchReceiptBindsPackageSelection(t *testing.T) {
	spec, _ := mkosiAcquisitionFixture()
	want, err := ir.MkosiSourceIdentity(spec)
	if err != nil {
		t.Fatal(err)
	}
	node := ir.Node{ID: "fetch-runtime", Mkosi: spec, VendorSpecDigest: want}
	got, err := fetchVendorSpecDigest(node)
	if err != nil || got != want {
		t.Fatalf("receipt lost recipe identity: %q, %v", got, err)
	}
	node.Mkosi.Roots[0].Inputs.Packages[0].Version = "2.0"
	_, err = fetchVendorSpecDigest(node)
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeFetchFailed {
		t.Fatalf("changed recipe accepted: %v", err)
	}
}

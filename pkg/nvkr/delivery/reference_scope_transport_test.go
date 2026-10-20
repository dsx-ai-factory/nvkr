// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestMixedAttemptMetadataAndPayloadRegistryRoundTrip(t *testing.T) {
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var mu sync.Mutex
	reads := map[string]bool{}
	var rejectedHosts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			mu.Lock()
			reads[r.URL.Path] = true
			mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	// Keep the admitted logical GHCR namespace while routing every byte to the
	// local registry. No real credentials or external registry are used.
	serverHost := strings.TrimPrefix(server.URL, "http://")
	original := remote.DefaultTransport
	remote.DefaultTransport = imageErrorTransport(func(r *http.Request) (*http.Response, error) {
		// Registry upload Location responses point at this exact test server.
		// Admit those follow-ups without allowing any other network destination.
		if r.URL.Host != "ghcr.io" && r.URL.Host != serverHost {
			mu.Lock()
			rejectedHosts = append(rejectedHosts, r.URL.Host)
			mu.Unlock()
			return nil, fmt.Errorf("unexpected registry host %s", r.URL.Host)
		}
		clone := r.Clone(r.Context())
		address := *r.URL
		address.Scheme = "http"
		address.Host = serverHost
		clone.URL = &address
		return http.DefaultTransport.RoundTrip(clone)
	})
	defer func() { remote.DefaultTransport = original }()
	const prefix = "ghcr.io/example/nvkr/development/runs/123/attempts/"
	const invocation = "https://github.com/example/nvkr/actions/runs/123/attempts/"
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	var root PublicationRoot
	var outputs []Output
	sources := map[string]string{}
	for i, item := range []string{"a", "b"} {
		attempt := []string{"1", "2"}[i]
		request := releasePublicationRequestFixture(t, "", "")
		// Distinct bundles may share payload evidence, but non-image outputs
		// must not alias a single bundle descriptor.
		source := filepath.Join(t.TempDir(), "cli")
		if err := os.WriteFile(source, []byte("exact executable"), 0o755); err != nil {
			t.Fatal(err)
		}
		bundlePath := filepath.Join(t.TempDir(), "bundle")
		bundle, err := BuildBinaryBundle(t.Context(), bundlePath, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: item, Files: []BinaryFileInput{{Name: "cli", Path: source, InstallPath: "/usr/local/bin/cli", Mode: 0o755}}})
		if err != nil {
			t.Fatal(err)
		}
		request.Objects[0].Path = bundlePath
		request.Objects[0].SemanticDigest = bundle.SemanticDigest
		request.Objects[0].BlobDigest = bundle.RootDigest
		request.Objects[0].Name = item
		request.Objects[0].Output.Repository = item
		request.Objects[1].Evidence.Outputs = []string{item}
		descriptor, err := PayloadDescriptor(t.Context(), request.Objects[0].Path)
		if err != nil {
			t.Fatal(err)
		}
		output := Output{Name: item, Kind: "executable", Digest: descriptor.Digest, BuildDigest: request.Objects[0].Output.BuildDigest, Components: []string{"cli"}}
		request.Release.ContentSubject, err = ContentDigest([]Output{output})
		if err != nil {
			t.Fatal(err)
		}
		full := filepath.Join(t.TempDir(), "full")
		if _, err = BuildPublicationRoot(t.Context(), full, request); err != nil {
			t.Fatal(err)
		}
		reference, err := Push(t.Context(), full, prefix+attempt+"/releases-"+item, options)
		if err != nil {
			t.Fatal(err)
		}
		inspection, err := Inspect(t.Context(), full)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			root = inspection.Root
			root.Objects = nil
		}
		for _, object := range inspection.Root.Objects {
			if object.Output != nil {
				object.Output.Publication = &PublicationOrigin{Ref: reference, PreparedInvocationID: invocation + attempt, SignedInvocationID: invocation + attempt}
				root.Objects = append(root.Objects, object)
				sources[object.Name] = full
			} else if i == 0 {
				object.Evidence.Outputs = []string{"a", "b"}
				root.Objects = append(root.Objects, object)
				sources[object.Name] = full
			}
		}
		outputs = append(outputs, output)
	}
	slices.SortFunc(root.Objects, func(a, b PublishedObject) int { return strings.Compare(a.Name, b.Name) })
	var err error
	root.Release.ContentSubject, err = ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	root.Release.InvocationID = invocation + "3"
	metadata := filepath.Join(t.TempDir(), "metadata")
	if _, err = BuildMetadataRoot(t.Context(), metadata, root, sources); err != nil {
		t.Fatal(err)
	}
	reference, err := PushMetadata(t.Context(), metadata, prefix+"3/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	for _, pull := range []struct {
		name string
		fn   func(context.Context, string, string, RegistryOptions) (Inspection, error)
	}{{"metadata", PullMetadata}, {"payloads", Pull}} {
		got, err := pull.fn(t.Context(), reference, filepath.Join(t.TempDir(), pull.name), options)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Root, root) {
			t.Fatal("transport changed exact output origins or evidence membership")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rejectedHosts) != 0 {
		t.Fatalf("attempted rejected registry destinations: %v", rejectedHosts)
	}
	for _, path := range []string{"/v2/example/nvkr/development/runs/123/attempts/1/a/manifests/", "/v2/example/nvkr/development/runs/123/attempts/2/b/manifests/"} {
		found := false
		for read := range reads {
			if strings.HasPrefix(read, path) {
				found = true
			}
		}
		if !found {
			t.Fatalf("never consumed retained repository %s", path)
		}
	}
	for read := range reads {
		if strings.Contains(read, "/attempts/3/a/") || strings.Contains(read, "/attempts/3/b/") {
			t.Fatalf("projected retained object into current attempt: %s", read)
		}
	}
}

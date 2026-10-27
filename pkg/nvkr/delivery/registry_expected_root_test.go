// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestPublicationExpectedRootRejectsSubstitutionBeforeRegistryIO(t *testing.T) {
	full, expected := releasePublicationFixture(t)
	metadata := filepath.Join(t.TempDir(), "metadata")
	if _, err := ExportMetadata(t.Context(), full, metadata); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, source string
		push         func(context.Context, string, string, PushOptions) (string, error)
	}{
		{"payload", full, PushWithOptions},
		{"metadata", metadata, PushMetadataWithOptions},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer server.Close()
			repository := strings.TrimPrefix(server.URL, "http://") + "/product/releases"
			other := "sha256:" + strings.Repeat("f", 64)
			if other == expected {
				t.Fatal("replacement subject accidentally matches fixture")
			}
			_, err := test.push(t.Context(), test.source, repository, PushOptions{
				Registry: RegistryOptions{Anonymous: true, PlainHTTP: true}, ExpectedRootDigest: other,
			})
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if requests.Load() != 0 || !strings.Contains(err.Error(), "changed after metadata admission") {
				t.Fatalf("replacement reached registry or wrong admission: requests=%d, %v", requests.Load(), err)
			}
		})
	}
}

func TestPublicationExpectedRootRejectsMalformedSubjectBeforeLayout(t *testing.T) {
	for _, push := range []struct {
		name string
		call func(context.Context, string, string, PushOptions) (string, error)
	}{{"payload", PushWithOptions}, {"metadata", PushMetadataWithOptions}} {
		t.Run(push.name, func(t *testing.T) {
			_, err := push.call(t.Context(), "missing-layout", "ghcr.io/example/project/releases", PushOptions{ExpectedRootDigest: "not-a-digest"})
			assertRegistryCode(t, err, e.CodeUsage)
		})
	}
}

func TestPublicationExpectedRootPublishesMatchingPayloadAndMetadata(t *testing.T) {
	full, expected := releasePublicationFixture(t)
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/product/releases"
	options := PushOptions{Registry: RegistryOptions{Anonymous: true, PlainHTTP: true}, ExpectedRootDigest: expected}
	reference, err := PushWithOptions(t.Context(), full, repository, options)
	if err != nil || reference != repository+"@"+expected {
		t.Fatalf("matching payload subject = %s, %v", reference, err)
	}
	metadata := filepath.Join(t.TempDir(), "metadata")
	result, err := ExportMetadata(t.Context(), full, metadata)
	if err != nil {
		t.Fatal(err)
	}
	options.ExpectedRootDigest = result.RootDigest
	reference, err = PushMetadataWithOptions(t.Context(), metadata, repository, options)
	if err != nil || reference != repository+"@"+result.RootDigest {
		t.Fatalf("matching metadata subject = %s, %v", reference, err)
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
)

func capturedRegistryIndexFixture(t *testing.T) (*ImageIndex, PublishedObject) {
	t.Helper()
	publication, _, description := registryImageIndexFixture(t)
	inspection, err := Inspect(t.Context(), publication)
	if err != nil {
		t.Fatal(err)
	}
	var object PublishedObject
	for _, candidate := range inspection.Root.Objects {
		if candidate.Output != nil {
			object = candidate
		}
	}
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	if _, err := Push(t.Context(), publication, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options); err != nil {
		t.Fatal(err)
	}
	reference, err := name.NewDigest(strings.TrimPrefix(server.URL, "http://")+"/product/cwe@"+description.Digest, name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	index, err := fetchRegistryImageIndex(t.Context(), reference, description.Size, imageIndexPlatforms, registryOptions(t.Context(), options))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	return index, object
}

func TestRegistryImageIndexImportUsesOnlyDestinationWorkspace(t *testing.T) {
	index, object := capturedRegistryIndexFixture(t)
	stage := t.TempDir()
	rootIndex := []byte("publication index must remain unchanged")
	if err := os.WriteFile(filepath.Join(stage, "index.json"), rootIndex, 0o600); err != nil {
		t.Fatal(err)
	}
	// The transport is already closed. Import must use the held closure directly,
	// without a second temporary layout or snapshot outside the caller's stage.
	unavailable := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(unavailable, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	previous := os.Getenv("TMPDIR")
	t.Setenv("TMPDIR", unavailable)
	err := materializeRegistryImageIndex(t.Context(), stage, object, index)
	t.Setenv("TMPDIR", previous)
	if err != nil {
		t.Fatalf("import depended on temporary storage outside the destination: %v", err)
	}
	if got := mustReadImageFile(t, filepath.Join(stage, "index.json")); !bytes.Equal(got, rootIndex) {
		t.Fatal("import replaced the publication index")
	}
	visited := map[string]bool{}
	descriptor := Descriptor{MediaType: MediaOCIIndex, Digest: object.DescriptorDigest, Size: object.Size}
	if err := verifyDescriptorForInspection(t.Context(), stage, descriptor, visited, nil); err != nil {
		t.Fatal(err)
	}
	if err := verifyPublishedObjectBinding(t.Context(), stage, object, descriptor, nil); err != nil {
		t.Fatal(err)
	}
	if err := verifyBlobInventory(stage, visited); err != nil {
		t.Fatal(err)
	}
	// Shared child layers remain one exact blob, and importing does not consume
	// the caller's capability or rewrite any native manifest, config or layer.
	if len(visited) != len(index.closure.blobs)+1 {
		t.Fatalf("import changed closure membership: %d blobs, want %d", len(visited), len(index.closure.blobs)+1)
	}
	for digest, file := range index.closure.blobs {
		want, err := file.ReadAllContext(t.Context(), maxDeliveryFileBytes)
		if err != nil {
			t.Fatal(err)
		}
		if got := mustReadImageFile(t, fixtureBlobPath(stage, digest)); !bytes.Equal(got, want) {
			t.Fatalf("import changed exact blob %s", digest)
		}
	}
	if _, err := index.Description(); err != nil {
		t.Fatalf("import consumed the caller's image index: %v", err)
	}
}

func TestRegistryImageIndexImportRejectsBeforeWriting(t *testing.T) {
	for _, mode := range []string{"root", "child", "closed", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			index, object := capturedRegistryIndexFixture(t)
			stage := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			code := e.CodeVerifyFailed
			switch mode {
			case "root":
				object.Size++
			case "child":
				object.Output.ImageIndex.Children[0].ManifestDigest = digestBytes([]byte("substituted child"))
			case "closed":
				if err := index.Close(); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
				code = e.CodeTimeout
			}
			err := materializeRegistryImageIndex(ctx, stage, object, index)
			assertRegistryCode(t, err, code)
			assertEmptyPullDirectory(t, stage)
		})
	}
}

func TestRegistryImageIndexImportCancelsWhileWaitingForOwner(t *testing.T) {
	index, object := capturedRegistryIndexFixture(t)
	stage := t.TempDir()
	index.operation <- struct{}{}
	defer func() { <-index.operation }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := make(chan struct{})
	queued := &imageWaitContext{Context: ctx, waiting: waiting}
	returned := make(chan error, 1)
	go func() {
		returned <- materializeRegistryImageIndex(queued, stage, object, index)
	}()
	<-waiting
	cancel()
	select {
	case err := <-returned:
		assertRegistryCode(t, err, e.CodeTimeout)
	case <-time.After(time.Second):
		t.Fatal("canceled import waited for an unrelated active operation")
	}
	assertEmptyPullDirectory(t, stage)
}

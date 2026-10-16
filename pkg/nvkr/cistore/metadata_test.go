// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func publicMetadataFixture() Locator {
	return Locator{SchemaVersion: metadataSchema, Request: testRequest, Paths: []string{".nvkr/carriers/metadata.tar"}, StripPrefix: ".nvkr/carriers", Metadata: &MetadataContent{
		Root: ".nvkr/records/value.json", Entries: []MetadataEntry{{Path: ".nvkr/records/value.json", Mode: 0o644, Text: "{\"value\":1}\n"}},
	}}
}

func TestPublicMetadataAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Locator)
	}{
		{"executable", func(l *Locator) { l.Metadata.Entries[0].Mode = 0o755 }},
		{"binary", func(l *Locator) { l.Metadata.Entries[0].Text = "\x7fELF\x00" }},
		{"duplicate JSON", func(l *Locator) { l.Metadata.Entries[0].Text = `{"x":1,"x":2}` }},
		{"foreign root", func(l *Locator) { l.Metadata.Entries[0].Path = "outside.json" }},
		{"missing parent", func(l *Locator) { l.Metadata.Root = ".nvkr/records" }},
		{"registry reference", func(l *Locator) { l.Reference = "ghcr.io/org/repo" }},
		{"private schema", func(l *Locator) { l.SchemaVersion = SchemaVersion }},
		{"wrong attempt", func(l *Locator) { l.Request.Attempt = "99" }},
		{"helper destination", func(l *Locator) { l.Paths = []string{"nvkr-exec"}; l.StripPrefix = "" }},
		{"duplicate member", func(l *Locator) { l.Metadata.Entries = append(l.Metadata.Entries, l.Metadata.Entries[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := publicMetadataFixture()
			tc.edit(&l)
			err := l.Validate(testRequest)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("want verify-failed, got %v", err)
			}
		})
	}
}

func TestPublicMetadataRestoresExactBytesWithoutRegistryCredentials(t *testing.T) {
	ctx := context.Background()
	l := publicMetadataFixture()
	index := t.TempDir()
	if _, err := PublishGitHubMetadata(ctx, testRequest, l.Paths[0], index, *l.Metadata); err != nil {
		t.Fatal(err)
	}
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "finalizer", true: "stage"}[nested], func(t *testing.T) {
			destination := t.TempDir()
			if nested {
				selection := map[string]NestedArchive{testRequest.Name: {Member: l.Paths[0], Allowed: []string{l.Metadata.Root}}}
				if err := DownloadGitHubNested(ctx, testRequest, index, destination, selection, Options{}); err != nil {
					t.Fatal(err)
				}
				if err := DownloadGitHubNested(ctx, testRequest, index, destination, selection, Options{}); err == nil {
					t.Fatal("replaced existing metadata")
				}
			} else {
				if err := DownloadGitHub(ctx, testRequest, index, destination, Options{}); err != nil {
					t.Fatal(err)
				}
				if err := transportarchive.Unpack(ctx, filepath.Join(destination, "metadata.tar"), destination, []string{l.Metadata.Root}); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(filepath.Join(destination, l.Metadata.Root))
			if err != nil || string(raw) != l.Metadata.Entries[0].Text {
				t.Fatalf("exact bytes changed: %q %v", raw, err)
			}
		})
	}
}

func TestPublicMetadataRejectsWholeSelectionBeforeRestore(t *testing.T) {
	l := publicMetadataFixture()
	index := t.TempDir()
	destination := t.TempDir()
	ctx := context.Background()
	if _, err := PublishGitHubMetadata(ctx, testRequest, l.Paths[0], index, *l.Metadata); err != nil {
		t.Fatal(err)
	}
	err := DownloadGitHubNested(ctx, testRequest, index, destination, map[string]NestedArchive{testRequest.Name: {Member: l.Paths[0], Allowed: []string{".nvkr/different.json"}}}, Options{})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
		t.Fatalf("wrong admission error: %v", err)
	}
	entries, readErr := os.ReadDir(destination)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed admission wrote outputs: %v %v", entries, readErr)
	}
}

func TestPublishGitHubMetadataCanceledPreservesIndex(t *testing.T) {
	l := publicMetadataFixture()
	index := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := PublishGitHubMetadata(ctx, testRequest, l.Paths[0], index, *l.Metadata)
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("want timeout, got %v", err)
	}
	if result != "" {
		t.Fatalf("canceled publication returned locator %q", result)
	}
	entries, err := os.ReadDir(index)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled publication changed empty index: %v %v", entries, err)
	}
}

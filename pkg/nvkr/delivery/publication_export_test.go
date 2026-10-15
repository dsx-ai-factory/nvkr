// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublicationLayoutExportSharedLifetime(t *testing.T) {
	for _, scenario := range []string{"nil", "unopened", "close original", "close copy", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			result, err := BuildPublicationRoot(t.Context(), filepath.Join(root, "source"), releasePublicationRequestFixture(t, "linux", "amd64"))
			if err != nil {
				t.Fatal(err)
			}
			publication, err := OpenPublication(t.Context(), filepath.Join(root, "source"), result.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			copyOf := *publication
			ctx, code := t.Context(), e.CodeGateUnmet
			handles := []*Publication{publication, &copyOf}
			switch scenario {
			case "nil":
				handles = []*Publication{nil}
			case "unopened":
				handles = []*Publication{{}}
			case "close original", "close copy":
				// A copied live handle exports the same checked closure.
				out := filepath.Join(root, "before-close")
				if err := copyOf.WriteLayout(ctx, out); err != nil {
					t.Fatal(err)
				}
				inspection, err := Inspect(ctx, out)
				if err != nil || inspection.RootDigest != result.RootDigest {
					t.Fatalf("copied handle changed held closure: %v", err)
				}
				toClose := publication
				if scenario == "close copy" {
					toClose = &copyOf
				}
				if err := toClose.Close(); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				code = e.CodeTimeout
			}
			for _, handle := range handles {
				directory := t.TempDir()
				assertRegistryCode(t, handle.WriteLayout(ctx, filepath.Join(directory, "layout")), code)
				entries, err := os.ReadDir(directory)
				if err != nil || len(entries) != 0 {
					t.Fatalf("unavailable export leaked output or stage: %v, entries=%v", err, entries)
				}
			}
		})
	}
}

func TestPublicationExportUsesHeldBytesAfterSourceReplacement(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(map[bool]string{false: "binary", true: "disk"}[disk], func(t *testing.T) {
			request := releasePublicationRequestFixture(t, "linux", "amd64")
			if disk {
				request, _ = diskPublicationRequest(t, "arm64")
			}
			root := t.TempDir()
			source := filepath.Join(root, "publication")
			result, err := BuildPublicationRoot(t.Context(), source, request)
			if err != nil {
				t.Fatal(err)
			}
			publication, err := OpenPublication(t.Context(), source, result.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			if err := os.Rename(source, filepath.Join(root, "original")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "index.json"), []byte("substituted"), 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "exported")
			if err := publication.WriteLayout(t.Context(), out); err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), out)
			if err != nil || inspection.RootDigest != result.RootDigest {
				t.Fatalf("export reopened substituted paths: %+v %v", inspection, err)
			}
		})
	}
}

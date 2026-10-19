// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublicationExtractionUsesHeldBytesAfterSourceReplacement(t *testing.T) {
	for _, binary := range []bool{true, false} {
		t.Run(map[bool]string{true: "binary", false: "chart"}[binary], func(t *testing.T) {
			var source, digest string
			var expected []byte
			if binary {
				source, digest = releasePublicationPlatformFixture(t, "linux", "amd64")
				expected = []byte("exact executable")
			} else {
				source, digest = helmPublicationFixture(t, "", "")
				_, payload, err := ReadObject(t.Context(), source, "chart")
				if err != nil {
					t.Fatal(err)
				}
				expected = payload
			}
			publication, err := OpenPublication(t.Context(), source, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			if err := os.Rename(source, source+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "index.json"), []byte("substituted"), 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "extracted")
			if binary {
				bundle, err := publication.ExtractBinaryBundle(t.Context(), "tools", out)
				if err != nil {
					t.Fatal(err)
				}
				if bundle.OS != "linux" || bundle.Arch != "amd64" {
					t.Fatal("bundle platform changed")
				}
				out = filepath.Join(out, "cli")
			} else {
				object, err := publication.ExtractObject(t.Context(), "chart", out)
				if err != nil {
					t.Fatal(err)
				}
				if object.BlobDigest != digestBytes(expected) {
					t.Fatal("chart binding changed")
				}
			}
			got, err := os.ReadFile(out)
			if err != nil || !bytes.Equal(got, expected) {
				t.Fatalf("extracted substituted bytes: %q %v", got, err)
			}
			if !binary {
				_, err = publication.ExtractObject(t.Context(), "chart", out)
				assertRegistryCode(t, err, e.CodeImmutableViolation)
			}
		})
	}
}

func TestPublicationExtractionRejectsInactiveCapabilitiesWithoutOutput(t *testing.T) {
	for _, scenario := range []string{"nil", "unopened", "closed", "canceled", "missing name"} {
		t.Run(scenario, func(t *testing.T) {
			root, digest := releasePublicationFixture(t)
			publication, err := OpenPublication(t.Context(), root, digest)
			if err != nil {
				t.Fatal(err)
			}
			defer publication.Close()
			ctx, name, code := t.Context(), "tools", e.CodeGateUnmet
			switch scenario {
			case "nil":
				publication = nil
			case "unopened":
				publication = &Publication{}
			case "closed":
				_ = publication.Close()
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				code = e.CodeTimeout
			case "missing name":
				name, code = "missing", e.CodeUnverifiedRef
			}
			out := filepath.Join(t.TempDir(), "extracted")
			_, err = publication.ExtractBinaryBundle(ctx, name, out)
			assertRegistryCode(t, err, code)
			_, err = publication.ExtractObject(ctx, name, out)
			assertRegistryCode(t, err, code)
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("failed extraction exposed output: %v", err)
			}
		})
	}
}

func TestPublicationExtractionRequiresExpectedRoot(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	_, err := OpenPublication(t.Context(), root, digestBytes([]byte("another root")))
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	out := filepath.Join(t.TempDir(), "extracted")
	if _, err := publication.ExtractBinaryBundle(t.Context(), "tools", out); err != nil {
		t.Fatal(err)
	}
	_, err = publication.ExtractBinaryBundle(t.Context(), "tools", out)
	assertRegistryCode(t, err, e.CodeImmutableViolation)
}

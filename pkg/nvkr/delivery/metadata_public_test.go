// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublicMetadataExcludesPayloadAndUnreferencedBlobs(t *testing.T) {
	for _, extra := range []string{"none", "output payload", "unreferenced JSON"} {
		t.Run(extra, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "amd64")
			full := filepath.Join(t.TempDir(), "full")
			if _, err := BuildPublicationRoot(t.Context(), full, request); err != nil {
				t.Fatal(err)
			}
			metadata := filepath.Join(t.TempDir(), "metadata")
			if _, err := ExportMetadata(t.Context(), full, metadata); err != nil {
				t.Fatal(err)
			}
			if extra != "none" {
				digest, raw := "sha256:"+strings.Repeat("f", 64), []byte(`{"extra":true}`)
				if extra == "output payload" {
					digest = request.Objects[0].Output.Disk.PayloadDigest
					var err error
					raw, err = os.ReadFile(filepath.Join(full, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")))
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(metadata, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:")), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := ValidatePublicMetadata(t.Context(), metadata)
			if extra == "none" && err != nil {
				t.Fatal(err)
			}
			if extra != "none" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
					t.Fatalf("want verify-failed for extra blob, got %v", err)
				}
			}
		})
	}
}

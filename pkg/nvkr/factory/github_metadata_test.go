// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestGitHubMetadataRejectsPayloadsAndUnknownFields(t *testing.T) {
	record := pipeline.ArtifactResult{Component: "tool", SHA256: "sha256:" + strings.Repeat("a", 64), Size: 12}
	raw, err := canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind, raw string
		mode            os.FileMode
		wantError       bool
	}{
		{"producer record", "producer-result", string(raw), 0o644, false},
		{"binary disguised as metadata", "producer-result", "\x7fELF\x00", 0o644, true},
		{"executable metadata", "producer-result", string(raw), 0o755, true},
		{"unknown JSON field", "producer-result", strings.TrimSuffix(string(raw), "}") + `,"payload":"hidden"}`, 0o644, true},
		{"wrong metadata schema", ArtifactSigningRecord, string(raw), 0o644, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			artifact := Artifact{ID: "result/tool", Kind: tc.kind, Component: "tool", Path: "result.json"}
			if err := os.WriteFile(filepath.Join(root, artifact.Path), []byte(tc.raw), tc.mode); err != nil {
				t.Fatal(err)
			}
			carrier := filepath.Join(root, "carrier.tar")
			if err := transportarchive.Pack(context.Background(), carrier, root, []string{artifact.Path}); err != nil {
				t.Fatal(err)
			}
			receipt, err := admitGitHubMetadata(context.Background(), artifact, carrier)
			if tc.wantError {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed || receipt != nil {
					t.Fatalf("want rejection without receipt, got %v %v", receipt, err)
				}
			} else if err != nil || receipt == nil {
				t.Fatalf("metadata rejected: %v", err)
			}
		})
	}
}

func TestGitHubPublicMetadataClosedKinds(t *testing.T) {
	for _, kind := range []string{"pipeline-tool", "source-closure", "raw", "tarball", PackageOCI, PackageDiskImage, "unknown"} {
		t.Run(kind, func(t *testing.T) {
			if githubMetadataKind(kind) {
				t.Fatal("payload kind admitted as public metadata")
			}
		})
	}
}

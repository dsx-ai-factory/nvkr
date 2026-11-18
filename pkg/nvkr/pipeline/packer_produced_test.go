// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestProducedPackerArtifactCollection(t *testing.T) {
	for _, scenario := range []string{"exact", "wrong format", "file link", "directory link", "escaping path", "zero capacity", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			payload := append([]byte{'Q', 'F', 'I', 0xfb}, bytes.Repeat([]byte{7}, 64)...)
			if scenario == "wrong format" {
				payload[0] = 'X'
			}
			if err := os.WriteFile(filepath.Join(root, "disk.qcow2"), payload, 0o600); err != nil {
				t.Fatal(err)
			}
			name, storage := "disk.qcow2", uint64(1)
			switch scenario {
			case "file link":
				if err := os.Symlink("disk.qcow2", filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
				name = "link"
			case "directory link":
				if err := os.Symlink(root, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
				name = "link/disk.qcow2"
			case "escaping path":
				name = "../disk.qcow2"
			case "zero capacity":
				storage = 0
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			artifact, err := collectProducedPackerArtifact(ctx, root, name, storage)
			if scenario != "exact" {
				if code(err) != e.CodeVerifyFailed || artifact != nil {
					t.Fatalf("artifact=%v error=%v", artifact, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer artifact.Close()
			got, err := io.ReadAll(artifact.file)
			if err != nil || !bytes.Equal(got, payload) || artifact.Size() != int64(len(payload)) {
				t.Fatalf("collected bytes=%x error=%v", got, err)
			}
			if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
				t.Fatalf("collected disk remains named: %v", err)
			}
		})
	}
}

func TestProducedPackerLogsRejectUnsafeOrUnboundedOutput(t *testing.T) {
	for _, scenario := range []string{"exact", "symlink", "oversize", "entries", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "console.log"), []byte("booted\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "symlink":
				if err := os.Symlink("console.log", filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.Truncate(filepath.Join(root, "console.log"), maxPackerDiagnosticFileBytes+1); err != nil {
					t.Fatal(err)
				}
			case "entries":
				for i := 0; i < maxPackerDiagnosticEntries; i++ {
					if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("dir-%03d", i)), 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			directory, _, err := securepath.OpenDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			var raw bytes.Buffer
			writer := tar.NewWriter(&raw)
			err = (&producedPackerLogBudget{}).pack(ctx, writer, directory, "")
			if closeErr := writer.Close(); err == nil {
				err = closeErr
			}
			if scenario != "exact" {
				if scenario == "canceled" {
					if err != context.Canceled {
						t.Fatalf("error=%v", err)
					}
				} else if code(err) != e.CodeVerifyFailed {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			reader := tar.NewReader(&raw)
			header, err := reader.Next()
			if err != nil || header.Name != "console.log" {
				t.Fatalf("header=%v error=%v", header, err)
			}
			got, err := io.ReadAll(reader)
			if err != nil || string(got) != "booted\n" {
				t.Fatalf("payload=%q error=%v", got, err)
			}
		})
	}
}

func TestArtifactRuntimeUseDescriptionRequiresExactSelection(t *testing.T) {
	for _, scenario := range []string{"exact", "missing", "foreign tool", "foreign build", "foreign subject", "missing SBOM", "unselected runtime"} {
		t.Run(scenario, func(t *testing.T) {
			_, _, binding, node := consumerRootFixture(t)
			result := ArtifactResult{RuntimeUse: &binding}
			switch scenario {
			case "missing":
				result.RuntimeUse = nil
			case "foreign tool":
				binding.Runtime.Tool = "another"
			case "foreign build":
				binding.Production.BuildDigest = "sha256:" + strings.Repeat("a", 64)
			case "foreign subject":
				binding.Subject += "-other"
			case "missing SBOM":
				binding.SBOM = ""
			case "unselected runtime":
				node.Runtime = nil
			}
			err := ValidateArtifactRuntimeUse(node, result)
			if scenario == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else if code(err) != e.CodeVerifyFailed {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

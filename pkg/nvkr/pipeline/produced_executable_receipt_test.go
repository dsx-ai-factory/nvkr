// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestProducedExecutableReceiptRetainsSingleProducerBinding(t *testing.T) {
	for _, change := range []string{"exact", "replaced inode", "changed bytes", "canceled"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			opts := RunOptions{SourceRoot: filepath.Join(root, "src")}
			node := producedExecutableGraph("linux/arm64", FileDigest([]byte("tool"))).Nodes[0]
			path := filepath.Join(prebuiltDir(opts, node), "helper")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("tool"), 0700); err != nil {
				t.Fatal(err)
			}
			result := ArtifactResult{Component: node.Component, OutputType: node.OutputType, SHA256: node.Digest, Size: 4, AbsPath: path, Path: path, Qualification: ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire}
			receipt, err := captureProducedExecutableReceipt(t.Context(), "build", node, result, opts)
			if err != nil {
				t.Fatal(err)
			}
			// The old single-producer terminal receipt encoded these exact fields. Keep
			// its canonical bytes stable while retiring the whole-Run receipt API.
			legacy := map[string]any{
				"buildDigest": "build",
				"nodes":       []any{map[string]any{"id": node.ID, "kind": node.Kind, "status": "ok", "duration": 0}},
				"artifacts":   []any{map[string]any{"nodeId": node.ID, "result": result, "liveRoot": prebuiltDir(opts, node), "livePath": path}},
			}
			want, err := canonical.Digest(canonical.KindTerminalVerification, legacy)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.binding != want {
				t.Fatalf("binding changed: %s != %s", receipt.binding, want)
			}
			ctx := t.Context()
			switch change {
			case "replaced inode":
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("tool"), 0700); err != nil {
					t.Fatal(err)
				}
			case "changed bytes":
				if err := os.WriteFile(path, []byte("evil"), 0700); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err = receipt.require(ctx)
			wantCode := e.CodeVerifyFailed
			if change == "canceled" {
				wantCode = e.CodeTimeout
			}
			if change == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else if code(err) != wantCode {
				t.Fatalf("error=%v, want %s", err, wantCode)
			}
		})
	}
}

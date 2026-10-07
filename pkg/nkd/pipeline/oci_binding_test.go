// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestBoundImageRejectsCustomTransportBeforeOutput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-exist")
	node := ir.Node{ID: "pull-tool", Kind: ir.KindPull, Component: "tool", Artifact: "registry.example/tool@sha256:" + strings.Repeat("a", 64),
		Digest: "sha256:" + strings.Repeat("b", 64), ImageParentDigest: "sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64",
		Network: ir.NetworkDefault, OutputType: "oci-image", Version: "1.0.0"}
	_, err := (Local{}).pull(node, RunOptions{Ctx: t.Context(), SourceRoot: filepath.Join(root, "src"), Stdout: io.Discard})
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("custom/missing transport admission: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("unsupported transport created output: %v", err)
	}
}

func TestBoundImageCrossRepositoryFailsBeforeOutput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-exist")
	_, _, err := (registryOCITransport{}).acquireNative(t.Context(), "registry.example/parent@sha256:"+strings.Repeat("a", 64),
		"registry.example/other@sha256:"+strings.Repeat("b", 64), "linux/amd64", root)
	if code(err) != e.CodeVerifyFailed {
		t.Fatalf("repository substitution: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("repository substitution created output: %v", err)
	}
}

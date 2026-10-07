// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// verify must be authoritative about the current run: an artifact that exists but
// was not produced this run (no collected entry) fails closed, and a recorded
// artifact whose bytes changed fails closed. This guards the stale-artifact /
// --keep-going gap and the "presence != verification" gap.
func TestVerifyRunAuthoritative(t *testing.T) {
	root := t.TempDir()
	opts := withDefaults(RunOptions{SourceRoot: filepath.Join(root, "work", "src")})
	producer := ir.Node{ID: "build-kubectl", Kind: ir.KindBuild, Component: "kubectl",
		OutputType: "host-package", WriteDir: "out", ArtifactPath: "out/kubectl"}
	full := artifactHostPath(opts, producer)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("compiled-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _, err := sha256File(full)
	if err != nil {
		t.Fatal(err)
	}

	builds := map[string]ir.Node{
		producer.ID: producer,
	}
	included := map[string]bool{"build-kubectl": true, "verify": true}
	l := Local{}

	// happy path: collected records the true digest + the file it hashed.
	good := []ArtifactResult{{Component: "kubectl", SHA256: sum, AbsPath: full}}
	if err := l.verify(ir.Node{Kind: ir.KindVerify}, builds, included, good, opts); err != nil {
		t.Fatalf("valid artifact should verify: %v", err)
	}

	// stale artifact: file exists but this run produced no collected entry -> fail.
	if err := l.verify(ir.Node{Kind: ir.KindVerify}, builds, included, nil, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("missing-from-run artifact must fail closed, got %v", err)
	}

	// tampered/wrong content: recorded digest no longer matches the file -> fail.
	wrong := []ArtifactResult{{Component: "kubectl", SHA256: "sha256:deadbeef", AbsPath: full}}
	if err := l.verify(ir.Node{Kind: ir.KindVerify}, builds, included, wrong, opts); code(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("digest mismatch must fail closed, got %v", err)
	}
}

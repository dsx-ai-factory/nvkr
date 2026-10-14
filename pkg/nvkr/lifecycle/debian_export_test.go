// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

type fixtureDebianExporter struct {
	fakeDiskExporter
	mode string
}

func (exporter *fixtureDebianExporter) ExportDebianLifecyclePayload(ctx context.Context, _ *factory.Plan, output factory.LifecycleOutputPlan, destination io.Writer) (factory.DebianOutputObservation, []byte, []byte, error) {
	if _, err := destination.Write(exporter.payload); err != nil {
		return factory.DebianOutputObservation{}, nil, nil, err
	}
	observed := factory.DebianOutputObservation{Digest: pipeline.FileDigest(exporter.payload), Size: int64(len(exporter.payload)), BuildDigest: output.BuildDigest, Subject: output.ArtifactID, Component: output.Components[0]}
	document, predicate := []byte("controlled document"), []byte("controlled unsigned predicate")
	if exporter.mode == "missing evidence" {
		document = nil
	}
	if exporter.mode == "foreign output" {
		observed.Subject = "artifact/sibling/agent"
	}
	return observed, document, predicate, nil
}
func TestDebianLifecycleStagesExactBytesAndEvidenceWithSharedTransaction(t *testing.T) {
	for _, mode := range []string{"success", "missing evidence", "foreign output", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			slot := requiredOutput{Name: "agent", Kind: OutputDebianPackage, MemberArtifactID: "artifact/agent/agent", BuildDigest: "sha256:" + strings.Repeat("a", 64), Components: []string{"agent"}}
			exporter := &fixtureDebianExporter{fakeDiskExporter: fakeDiskExporter{payload: []byte("controlled retained archive")}, mode: mode}
			transaction := newDiskExportTransaction(workspace)
			artifact, err := transaction.observe(t.Context(), slot, nil, exporter)
			if mode == "missing evidence" || mode == "foreign output" {
				if err == nil || artifact.Digest != "" {
					t.Fatal("failed join returned output", err)
				}
				if err := cleanupDiskExports(transaction.stages, false); err != nil {
					t.Fatal(err)
				}
				entries, _ := os.ReadDir(filepath.Join(workspace, "outputs"))
				if len(entries) != 0 {
					t.Fatal("failed stage left bytes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(transaction.stages) != 3 || artifact.Digest != pipeline.FileDigest(exporter.payload) {
				t.Fatal("lost payload/evidence bindings")
			}
			if _, err := os.Stat(artifact.Reference); !os.IsNotExist(err) {
				t.Fatal("staging published prematurely")
			}
			if err := commitDiskExports(transaction.stages); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(artifact.Reference)
			if err != nil || pipeline.FileDigest(raw) != artifact.Digest {
				t.Fatal("committed bytes differ", err)
			}
			if err := cleanupDiskExports(transaction.stages, mode != "rollback"); err != nil {
				t.Fatal(err)
			}
			if mode == "rollback" {
				if _, err := os.Stat(artifact.Reference); !os.IsNotExist(err) {
					t.Fatal("rollback retained package")
				}
			}
		})
	}
}

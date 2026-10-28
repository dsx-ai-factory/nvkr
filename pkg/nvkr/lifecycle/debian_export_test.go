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

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

type fixtureDebianExporter struct {
	fakeDiskExporter
	mode string
}

func (exporter *fixtureDebianExporter) ExportDebianLifecyclePayload(ctx context.Context, _ *factory.Plan, output factory.LifecycleOutputPlan, destination io.Writer) (factory.DebianOutputObservation, []byte, []byte, error) {
	if _, err := destination.Write(exporter.payload); err != nil {
		return factory.DebianOutputObservation{}, nil, nil, err
	}
	observed := factory.DebianOutputObservation{Digest: pipeline.FileDigest(exporter.payload), Size: int64(len(exporter.payload)), BuildDigest: output.BuildDigest, Subject: output.ArtifactID, Component: output.Components[0], PlanDigest: "sha256:" + strings.Repeat("b", 64), InputDigest: "sha256:" + strings.Repeat("c", 64), Target: target.Debian{Distribution: "ubuntu-24.04", Platform: "linux/amd64"}}
	// Controlled schema-shaped bytes test staging integrity, not inspection.
	bom := sbom.BOM{}
	document, err := canonical.Marshal(bom)
	if err != nil {
		return observed, nil, nil, err
	}
	observed.SBOMDigest, err = canonical.Digest(canonical.KindSBOM, bom)
	if err != nil {
		return observed, nil, nil, err
	}
	predicate := []byte("controlled unsigned predicate")
	if exporter.mode == "changed document" {
		observed.SBOMDigest = "sha256:" + strings.Repeat("f", 64)
	}
	if exporter.mode == "missing evidence" {
		document = nil
	}
	if exporter.mode == "foreign output" {
		observed.Subject = "artifact/sibling/agent"
	}
	return observed, document, predicate, nil
}
func TestDebianLifecycleStagesExactBytesAndEvidenceWithSharedTransaction(t *testing.T) {
	for _, mode := range []string{"success", "missing evidence", "changed document", "foreign output", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			slot := requiredOutput{Name: "agent", Kind: OutputDebianPackage, MemberArtifactID: "artifact/agent/agent", BuildDigest: "sha256:" + strings.Repeat("a", 64), Components: []string{"agent"}}
			exporter := &fixtureDebianExporter{fakeDiskExporter: fakeDiskExporter{payload: []byte("controlled retained archive")}, mode: mode}
			transaction := newDiskExportTransaction(workspace)
			artifact, err := transaction.observe(t.Context(), slot, nil, exporter)
			if mode == "missing evidence" || mode == "changed document" || mode == "foreign output" {
				want := e.CodeVerifyFailed
				if mode == "missing evidence" {
					want = e.CodeProvenanceMissing
				}
				assertCode(t, err, want)
				if artifact.Digest != "" {
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
			if len(transaction.stages) != 3 || artifact.Digest != pipeline.FileDigest(exporter.payload) || artifact.Debian == nil || artifact.Debian.PlanDigest == "" || artifact.Debian.SBOMDigest == "" || artifact.Debian.PredicateDigest != pipeline.FileDigest([]byte("controlled unsigned predicate")) {
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

func TestDebianStagingAdmissionCodes(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name     string
		ctx      context.Context
		nameSlot string
		exporter diskPayloadExporter
		want     e.Code
	}{
		{"nil context", nil, "agent", &fixtureDebianExporter{}, e.CodeUsage},
		{"canceled", canceled, "agent", &fixtureDebianExporter{}, e.CodeTimeout},
		{"non exporter", t.Context(), "agent", &fakeDiskExporter{}, e.CodeEngineCapability},
		{"unsafe name", t.Context(), "../outside", &fixtureDebianExporter{}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			transaction := newDiskExportTransaction(workspace)
			artifact, err := transaction.observeDebian(tc.ctx, requiredOutput{Name: tc.nameSlot, Kind: OutputDebianPackage}, nil, tc.exporter)
			assertCode(t, err, tc.want)
			if artifact.Digest != "" || len(transaction.stages) != 0 {
				t.Fatal("admission wrote output")
			}
		})
	}
	artifact, err := observeOutput(t.Context(), requiredOutput{Name: "agent", Kind: OutputDebianPackage}, nil, nil, nil, t.TempDir())
	assertCode(t, err, e.CodeEngineCapability)
	if artifact.Digest != "" {
		t.Fatal("ordinary observation admitted native package")
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Synthetic one-producer description, not proof of a real consumed graph.
// Factory's live-trace tests cover transitive material bindings separately.
func writeLocalMaterialFixture(t *testing.T, workspace string, inputs *localQualificationInputs) localDiskMaterials {
	t.Helper()
	envelope := localDiskMaterials{SchemaVersion: "nvkr.dev/local-run-materials/v1", Attempt: inputs.run.Attempt, PlanDigest: inputs.run.PlanDigest, Disks: []factory.DiskMaterialRecord{}}
	for _, output := range inputs.run.Observed {
		if output.Kind != OutputDiskImage {
			continue
		}
		disk := output.Disk
		record := factory.DiskMaterialRecord{SchemaVersion: "nvkr.dev/local-disk-materials/v1", Lane: factory.TierLocalExecution.String(), Output: output.Name,
			PlanDigest: disk.PlanDigest, BuildDigest: output.BuildDigest, PayloadDigest: disk.PayloadDigest, Size: disk.Size,
			Format: disk.Format, Encryption: disk.Encryption, Components: output.Components, Coverage: "staged-producer-outputs",
			Members: []factory.DiskMaterialRecordMember{}, Edges: []factory.DiskMaterialRecordEdge{}, UntracedNetworkBuilds: []string{}}
		for _, build := range inputs.plan.Builds {
			if build.Name != output.Name {
				continue
			}
			for _, node := range build.Graph.Nodes {
				if node.Kind != ir.KindBuild && node.Kind != ir.KindPull {
					continue
				}
				if len(node.Materials) != 0 || node.ID != disk.Producer {
					t.Fatal("material fixture requires a single producer")
				}
				production := "built"
				if node.Kind == ir.KindPull {
					production = "acquired"
				}
				record.Members = append(record.Members, factory.DiskMaterialRecordMember{Producer: node.ID, Component: node.Component, Kind: node.OutputType, Production: production, Digest: disk.PayloadDigest, Size: disk.Size})
				if node.Kind == ir.KindBuild && node.Network != ir.NetworkNone {
					record.UntracedNetworkBuilds = append(record.UntracedNetworkBuilds, node.ID)
				}
			}
		}
		envelope.Disks = append(envelope.Disks, record)
	}
	writeCanonicalTestFile(t, filepath.Join(workspace, localDiskMaterialsFile), envelope)
	return envelope
}

func TestLocalQualificationMaterialsRejectIncompleteOrSubstitutedRecords(t *testing.T) {
	for _, mode := range []string{"exact", "schema", "attempt", "plan", "missing disk", "duplicate disk", "payload", "missing member", "unknown field", "duplicate field", "trailing", "noncanonical", "symlink", "missing file", "oversized", "commit marker", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			prepared, exporter := diskLifecycleFixtureWithProducer(t, "materials", true)
			pending := prepared.Description()
			pending.Lane = factory.TierLocalExecution.String()
			run, err := bindObserved(&pending, []Artifact{artifactFromDiskObservation(pending.Expected[0], exporter.observation)})
			if err != nil {
				t.Fatal(err)
			}
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			inputs := &localQualificationInputs{plan: prepared.plan, run: *run}
			envelope := writeLocalMaterialFixture(t, root, inputs)
			path := filepath.Join(root, localDiskMaterialsFile)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "schema":
				envelope.SchemaVersion = "other"
			case "attempt":
				envelope.Attempt = "other"
			case "plan":
				envelope.PlanDigest = "sha256:" + repeat("9", 64)
			case "missing disk":
				envelope.Disks = nil
			case "duplicate disk":
				envelope.Disks = append(envelope.Disks, envelope.Disks[0])
			case "payload":
				envelope.Disks[0].PayloadDigest = "sha256:" + repeat("9", 64)
			case "missing member":
				envelope.Disks[0].Members = nil
			case "canceled":
				cancel()
			}
			raw, err := canonical.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unknown field":
				raw = append([]byte(`{"unknown":true,`), raw[1:]...)
			case "duplicate field":
				raw = append([]byte(`{"attempt":"materials",`), raw[1:]...)
			case "trailing":
				raw = append(raw, []byte(`{}`)...)
			case "noncanonical":
				raw = append([]byte(" "), raw...)
			case "oversized":
				raw = []byte(strings.Repeat("x", (4<<20)+1))
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if mode == "missing file" || mode == "symlink" {
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if mode == "symlink" {
					if err := os.Symlink(path+".saved", path); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "commit marker" {
				if err := os.WriteFile(filepath.Join(root, diskAttemptMarker), []byte("pending"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := loadLocalQualificationMaterials(ctx, root, inputs)
			if mode == "exact" {
				if err != nil || got != string(raw) {
					t.Fatalf("exact material retention: %v", err)
				}
				return
			}
			code := e.CodeVerifyFailed
			if mode == "canceled" {
				code = e.CodeTimeout
			}
			if mode == "commit marker" {
				code = e.CodeImmutableViolation
			}
			assertCode(t, err, code)
			if got != "" {
				t.Fatal("failed material load returned partial bytes")
			}
		})
	}
}

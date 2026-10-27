// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDiskMaterialRecordRetainsLiveGraphWithoutRestoringAuthority(t *testing.T) {
	plan, owner := diskMaterialTraceFixtureWithNetwork(t, false, true)
	first, err := owner.ObserveDiskMaterialRecord(t.Context(), plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Members) != 3 || len(first.Edges) != 3 || len(first.UntracedNetworkBuilds) != 1 || first.Coverage != "staged-producer-outputs" {
		t.Fatalf("lost transitive handoffs or network limits: %+v", first)
	}
	want, err := canonical.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	// Changing descriptive results after consumption cannot rewrite the live
	// handoff exported here. Changing the exported slices cannot rewrite it either.
	_, artifacts := planIndex(plan)
	if err := os.WriteFile(artifacts["result/node-image/agent"].Path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	first.Members[0].Digest, first.Edges[0].Binding, first.Components[0], first.UntracedNetworkBuilds[0] = "changed", "changed", "changed", "changed"
	again, err := owner.ObserveDiskMaterialRecord(t.Context(), plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	got, err := canonical.Marshal(again)
	if err != nil || !bytes.Equal(want, got) {
		t.Fatalf("live trace changed through descriptive state: %v", err)
	}
	serialized, err := canonical.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	var decoded LocalRunResult
	if err := canonical.UnmarshalExact(serialized, &decoded); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = decoded.Close() }()
	if _, err := decoded.ObserveDiskMaterialRecord(t.Context(), plan, plan.LifecycleOutputs[0]); err == nil {
		t.Fatal("serialized local Run reconstructed live material consumption")
	} else {
		assertFactoryErrorCode(t, err, e.CodeReleaseIncomplete)
	}
}

func TestDiskMaterialRecordRefusesIncompleteAndClosedHandoffs(t *testing.T) {
	for _, mode := range []string{"missing consumption", "closed", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			plan, owner := diskMaterialTraceFixture(t)
			ctx := t.Context()
			code := e.CodeVerifyFailed
			switch mode {
			case "missing consumption":
				delete(owner.materialConsumptions, "node-image/pull-runtime")
			case "closed":
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
				code = e.CodeGateUnmet
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				code = e.CodeTimeout
			}
			got, err := owner.ObserveDiskMaterialRecord(ctx, plan, plan.LifecycleOutputs[0])
			assertFactoryErrorCode(t, err, code)
			if !reflect.DeepEqual(got, DiskMaterialRecord{}) {
				t.Fatal("refused observation returned partial evidence")
			}
		})
	}
}

func TestDiskMaterialRecordValidatesExactGraphDescription(t *testing.T) {
	plan, owner := diskMaterialTraceFixtureWithNetwork(t, false, true)
	expected := plan.LifecycleOutputs[0]
	trace, err := owner.observeDiskMaterials(t.Context(), plan, expected)
	if err != nil {
		t.Fatal(err)
	}
	subject := trace.Description().Subject
	record, err := owner.ObserveDiskMaterialRecord(t.Context(), plan, expected)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*DiskMaterialRecord)
	}{
		{"exact", func(*DiskMaterialRecord) {}},
		{"schema", func(r *DiskMaterialRecord) { r.SchemaVersion = "other" }},
		{"lane", func(r *DiskMaterialRecord) { r.Lane = "qualified" }},
		{"output", func(r *DiskMaterialRecord) { r.Output = "sibling" }},
		{"plan", func(r *DiskMaterialRecord) { r.PlanDigest = r.BuildDigest }},
		{"payload", func(r *DiskMaterialRecord) { r.PayloadDigest = r.BuildDigest }},
		{"size", func(r *DiskMaterialRecord) { r.Size++ }},
		{"encryption", func(r *DiskMaterialRecord) { r.Encryption = "luks2" }},
		{"coverage", func(r *DiskMaterialRecord) { r.Coverage = "complete" }},
		{"missing member", func(r *DiskMaterialRecord) { r.Members = r.Members[1:] }},
		{"duplicate member", func(r *DiskMaterialRecord) { r.Members = append(r.Members, r.Members[0]) }},
		{"member component", func(r *DiskMaterialRecord) { r.Members[0].Component = "other" }},
		{"member digest", func(r *DiskMaterialRecord) { r.Members[0].Digest = "invalid" }},
		{"member size", func(r *DiskMaterialRecord) { r.Members[0].Size = -1 }},
		{"production", func(r *DiskMaterialRecord) { r.Members[0].Production = "unknown" }},
		{"missing edge", func(r *DiskMaterialRecord) { r.Edges = r.Edges[1:] }},
		{"duplicate edge", func(r *DiskMaterialRecord) { r.Edges = append(r.Edges, r.Edges[0]) }},
		{"binding", func(r *DiskMaterialRecord) { r.Edges[0].Binding = "other" }},
		{"edge producer", func(r *DiskMaterialRecord) { r.Edges[0].Producer = "other" }},
		{"manifest", func(r *DiskMaterialRecord) { r.Edges[0].ManifestDigest = "invalid" }},
		{"network gap", func(r *DiskMaterialRecord) { r.UntracedNetworkBuilds = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed DiskMaterialRecord
			if err := canonical.UnmarshalExact(raw, &changed); err != nil {
				t.Fatal(err)
			}
			tc.change(&changed)
			err := changed.ValidateDescription(t.Context(), plan, expected, subject)
			if tc.name == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertFactoryErrorCode(t, record.ValidateDescription(ctx, plan, expected, subject), e.CodeTimeout)
}

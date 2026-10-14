// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianProducerContentRejectsUnissuedHandles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		archive *DebianProducerArchive
		want    e.Code
	}{
		{"nil context", nil, nil, e.CodeUsage},
		{"nil archive", t.Context(), nil, e.CodeProvenanceMissing},
		{"zero archive", t.Context(), &DebianProducerArchive{}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var container *DiskInspectionContainer
			if got, err := container.InspectDebianProducerContent(tc.ctx, tc.archive); got != nil || code(err) != tc.want {
				t.Fatal("unissued handle reached inspection", err)
			}
		})
	}
	var restored DebianProducerArchive
	if err := canonical.UnmarshalExact([]byte("{}"), &restored); err != nil {
		t.Fatal(err)
	}
	var container *DiskInspectionContainer
	if got, err := container.InspectDebianProducerContent(t.Context(), &restored); got != nil || code(err) != e.CodeEngineCapability {
		t.Fatal("decoded description restored handle", err)
	}
}

func TestDebianProducerContentProfileAdmissionPreservesArchive(t *testing.T) {
	_, archive, _, _, _ := capturedArchiveFixture(t)
	container := &DiskInspectionContainer{selection: testDiskInspectionSelection(t)}
	if got, err := container.InspectDebianProducerContent(t.Context(), archive); got != nil || code(err) != e.CodeEngineCapability {
		t.Fatal("incomplete profile entered inspection", err)
	}
	if _, err := archive.WriteToContext(t.Context(), io.Discard); err != nil {
		t.Fatal("failed inspection consumed Run bytes", err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := container.InspectDebianProducerContent(t.Context(), archive); got != nil || code(err) != e.CodeEngineCapability {
		t.Fatal("closed archive inspected", err)
	}
}

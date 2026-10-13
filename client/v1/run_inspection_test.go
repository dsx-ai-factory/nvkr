// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSDKRunInspectionRemainsReadOnlyWithoutRecord(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := New()
	view, err := client.InspectRun(t.Context(), filepath.Join(root, "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if view.RecordAvailable || !view.Interrupted || view.Status != "unavailable" {
		t.Fatalf("missing record restored authority: %+v", view)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.InspectRun(ctx, filepath.Join(root, "lifecycle.json"))
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatalf("cancellation was not coded: %v", err)
	}
}

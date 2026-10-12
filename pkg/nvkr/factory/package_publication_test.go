// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestPublicationObjectsPropagateDigestFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		missing  bool
		canceled bool
		wantErr  error
	}{
		{name: "regular payload"},
		{name: "missing payload", missing: true, wantErr: os.ErrNotExist},
		{name: "canceled directory digest", canceled: true, wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "payload")
			payload := []byte("publication payload")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				path = dir
				cancel()
			} else if !tc.missing {
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			output := Artifact{Path: filepath.Join(dir, "publication")}
			objects, cleanup, err := collectPublicationObjects(ctx, &Plan{}, output,
				[]Artifact{{Name: "payload", Kind: "record", Path: path}}, nil)
			if cleanup != nil {
				defer cleanup()
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("digest error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				if objects != nil || cleanup != nil {
					t.Fatal("failed digest transferred publication objects")
				}
			} else if len(objects) != 1 || objects[0].SemanticDigest != pipeline.FileDigest(payload) {
				t.Fatalf("publication objects = %+v", objects)
			}
			if _, err := os.Stat(output.Path); !os.IsNotExist(err) {
				t.Fatalf("collection created output: %v", err)
			}
		})
	}
}

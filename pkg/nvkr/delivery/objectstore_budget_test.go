// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
)

func TestObjectStoreUsesOperationReadBudget(t *testing.T) {
	for _, mode := range []string{"", "always-fail"} {
		t.Run(mode, func(t *testing.T) {
			original, _ := newObjectFixture(t, mode)
			options := original.(*s3ObjectStore).options
			if err := original.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, budget, err := readbudget.WithLimit(t.Context(), 64)
			if err != nil {
				t.Fatal(err)
			}
			store, err := NewObjectStore(ctx, ObjectStoreOptions{Provider: "s3", S3: options})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			}()
			raw := bytes.Repeat([]byte("x"), 128)
			stored, err := putVerifiedMirrorObject(ctx, store, objectDescription(raw), bytes.NewReader(raw))
			if err == nil || stored != (StoredObject{}) || budget.Check() == nil || budget.Used() != 64 {
				t.Fatal("S3 readback or failed SDK response escaped operation allowance", err, budget.Check(), budget.Used())
			}
		})
	}
}

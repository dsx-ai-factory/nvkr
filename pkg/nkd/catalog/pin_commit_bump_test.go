// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestCommitModeBumpRejectedBeforeUpstream(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected", true: "all"}[all], func(t *testing.T) {
			dir := sourceUpdateFixture(t)
			_, lock, err := pinUpdateInputs(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			pin := lock.Pins["source"]
			pin.Tag = pin.Commit
			lock.Pins["source"] = pin
			if err := WriteLockContext(t.Context(), dir, lock); err != nil {
				t.Fatal(err)
			}
			before := readPinFixtureLock(t, dir)
			request := SourcePinRequest{Directory: dir, Bump: true, All: all}
			if !all {
				request.Sources = []string{"source"}
			}
			called := false
			update, err := updateSourcePins(t.Context(), request,
				func(context.Context, string) ([]string, error) { called = true; return []string{"v1.2.3"}, nil },
				func(context.Context, Source, string) (Pin, error) { called = true; return pin, nil })
			assertCatalogContractCode(t, err, e.CodeUnsupported)
			if called || len(update.Pins) != 0 || !bytes.Equal(before, readPinFixtureLock(t, dir)) {
				t.Fatal("commit-mode bump performed upstream work or changed the lock")
			}
		})
	}
}

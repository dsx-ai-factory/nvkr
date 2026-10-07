// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Exercise the byte ceiling with the real writer and check-mode resolver. The
// opaque tag deliberately fills the limit without adding unrelated schema fields.
func TestSourcePinCheckAndWriteShareExactSerializedBound(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int64
	}{
		{"last valid byte", MaxCatalogFileBytes},
		{"first invalid byte", MaxCatalogFileBytes + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := sourceUpdateFixture(t)
			before := readPinFixtureLock(t, dir)
			candidate, err := LoadLockContext(t.Context(), dir)
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := serializePinLock(candidate)
			if err != nil {
				t.Fatal(err)
			}
			pin := candidate.Pins["source"]
			pin.Tag += strings.Repeat("a", int(tc.size)-len(baseline))
			candidate.Pins["source"] = pin
			resolve := func(context.Context, Source, string) (Pin, error) { return pin, nil }
			request := SourcePinRequest{Directory: dir, Sources: []string{"source"}, Tag: "v1.1.0", Check: true}
			checked, checkErr := updateSourcePins(t.Context(), request, nil, resolve)
			if !bytes.Equal(before, readPinFixtureLock(t, dir)) {
				t.Fatal("check changed original lock bytes")
			}
			request.Check = false
			written, writeErr := updateSourcePins(t.Context(), request, nil, resolve)
			if !reflect.DeepEqual(checked, written) {
				t.Fatal("check and write resolved different candidates")
			}
			if tc.size > MaxCatalogFileBytes {
				assertCatalogContractCode(t, checkErr, e.CodeInvalidCatalog)
				assertCatalogContractCode(t, writeErr, e.CodeInvalidCatalog)
				if !bytes.Equal(before, readPinFixtureLock(t, dir)) {
					t.Fatal("oversized candidate changed original lock bytes")
				}
				return
			}
			assertCatalogContractCode(t, checkErr, e.CodeDiff)
			if writeErr != nil {
				t.Fatal(writeErr)
			}
			want, err := serializePinLock(candidate)
			if err != nil {
				t.Fatal(err)
			}
			got := readPinFixtureLock(t, dir)
			if int64(len(got)) != tc.size || !bytes.Equal(want, got) {
				t.Fatalf("writer did not emit admitted bytes: got %d bytes, want %d", len(got), tc.size)
			}
		})
	}
}

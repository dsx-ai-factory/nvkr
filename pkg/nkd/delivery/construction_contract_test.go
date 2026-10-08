// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPublicationConstructionFailureCleansStaging(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PublicationRequest)
		code e.Code
	}{
		{"empty objects", func(r *PublicationRequest) { r.Objects = nil }, e.CodeUsage},
		{"partial release binding", func(r *PublicationRequest) { r.InventoryDigest = digestBytes([]byte("inventory")) }, e.CodeUnverifiedRef},
		{"duplicate object", func(r *PublicationRequest) { r.Objects = append(r.Objects, r.Objects[0]) }, e.CodeInvalidCatalog},
		{"missing semantic pin", func(r *PublicationRequest) { r.Objects[0].SemanticDigest = "" }, e.CodeUnverifiedRef},
		{"wrong byte pin", func(r *PublicationRequest) { r.Objects[0].BlobDigest = digestBytes([]byte("different")) }, e.CodeVerifyFailed},
		{"missing file", func(r *PublicationRequest) { r.Objects[0].Bytes = nil; r.Objects[0].Path = "missing-file" }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			request := PublicationRequest{Objects: []ObjectInput{{Name: "record", Kind: "record", Bytes: []byte("payload"), SemanticDigest: digestBytes([]byte("payload"))}}}
			tc.edit(&request)
			result, err := BuildPublicationRoot(t.Context(), filepath.Join(parent, "publication"), request)
			assertRegistryCode(t, err, tc.code)
			if !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("failure returned publication identity: %+v", result)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed construction leaked state: %v, %v", entries, err)
			}
		})
	}
}

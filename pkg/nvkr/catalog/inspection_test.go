// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDescribeStackMatchesLocalLoader(t *testing.T) {
	for _, root := range []string{"../../../testdata/examples/layered-catalog", "../../../testdata/catalogs/kubernetes-layered"} {
		t.Run(root, func(t *testing.T) {
			loaded, err := LoadContext(t.Context(), Dir(root))
			if err != nil {
				t.Fatal(err)
			}
			digest, err := loaded.SnapshotDigest()
			if err != nil {
				t.Fatal(err)
			}
			got, err := DescribeStackContext(t.Context(), root, ".", nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Digest != digest || !reflect.DeepEqual(got.Components, slices.Sorted(maps.Keys(loaded.Components))) {
				t.Fatalf("description differs from execution loader: %+v", got)
			}
		})
	}
}

func TestDescribeStackPinSetsStayOffline(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got, err := DescribeStackContext(t.Context(), "../../../inputs", "catalogs/nvke", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Components) == 0 || len(got.Imports) == 0 {
		t.Fatal("missing maintained descriptions")
	}
}

func TestDescribeStackConfinesLoaderImports(t *testing.T) {
	dir, _, _, _ := fragmentFixture(t)
	_, err := DescribeStackContext(t.Context(), dir, ".", nil)
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
		t.Fatalf("outside import admitted: %v", err)
	}
	if _, err := DescribeStackContext(t.Context(), filepath.Dir(dir), filepath.Base(dir), nil); err != nil {
		t.Fatal(err)
	}
}

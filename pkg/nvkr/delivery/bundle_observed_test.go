// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestImportObservedBundleLayoutBindsIndex(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact index", true: "substituted index"}[mismatch], func(t *testing.T) {
			source := aicrLayoutFixture(t)
			raw, err := os.ReadFile(filepath.Join(source, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			expected := digestBytes(raw)
			if mismatch {
				expected = digestBytes([]byte("other"))
			}
			out := filepath.Join(t.TempDir(), "imported")
			_, err = ImportObservedBundleLayout(t.Context(), out, source, expected)
			if mismatch {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				if _, err := os.Stat(out); !os.IsNotExist(err) {
					t.Fatal("refusal created output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			descriptor, err := PayloadDescriptor(t.Context(), out)
			if err != nil || descriptor.Digest != aicrFixtureManifest {
				t.Fatalf("manifest changed: %+v, %v", descriptor, err)
			}
		})
	}
}

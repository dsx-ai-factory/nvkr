// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package assemble

import (
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestDiskManifestWireRejectsRemovedDeploymentRole(t *testing.T) {
	for _, test := range []struct {
		name  string
		check func() error
		want  e.Code
	}{
		{"removed role", func() error {
			var manifest DiskManifest
			return canonical.UnmarshalExact([]byte(`{"role":"worker","schemaVersion":"nkd.dev/disk-manifest/v2"}`), &manifest)
		}, e.CodeVerifyFailed},
		{"earlier schema", func() error { return (&DiskManifest{SchemaVersion: "nkd.dev/disk-manifest/v1"}).Validate() }, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.check(); code(err) != test.want {
				t.Fatalf("removed disk topology accepted: %v", err)
			}
		})
	}
}

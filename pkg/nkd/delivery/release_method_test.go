// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func TestNonDiskReleaseRejectsDiskInspectionMethods(t *testing.T) {
	for _, method := range []string{"disk-content/v1", "disk-reconciled/v1"} {
		t.Run(method, func(t *testing.T) {
			request := releasePublicationRequestFixture(t, "linux", "amd64")
			object := &request.Objects[1]
			var bom sbom.BOM
			if err := canonical.UnmarshalExact(object.Bytes, &bom); err != nil {
				t.Fatal(err)
			}
			if method == "disk-reconciled/v1" {
				// Admit the disk method's document shape, then require the
				// non-disk release output to reject that otherwise-valid method.
				bom.Metadata.Component.Type = "operating-system"
			}
			withInventory, err := sbom.WithDependencies(&bom, []sbom.Component{{Type: "library", Name: "fixture", Version: "1"}}, method, request.Objects[0].Output.BuildDigest)
			if err != nil {
				t.Fatal(err)
			}
			object.Bytes, err = withInventory.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			object.SemanticDigest, err = withInventory.Digest()
			if err != nil {
				t.Fatal(err)
			}
			object.Name = "sbom-" + strings.TrimPrefix(object.SemanticDigest, "sha256:")
			result, err := BuildPublicationRoot(t.Context(), filepath.Join(t.TempDir(), "publication"), request)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if !strings.Contains(err.Error(), "disk inspection method is not valid for a non-disk output") || result.RootDigest != "" {
				t.Fatalf("did not reach method-confusion gate: %+v, %v", result, err)
			}
		})
	}
}

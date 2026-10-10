// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPrebuiltImageBindingDescriptions(t *testing.T) {
	parent := "sha256:" + strings.Repeat("b", 64)
	for _, tc := range []struct {
		name, ref, parent string
		native            string
		code              e.Code
	}{
		{"tag legacy", "registry.example/tool:v1", "", goodArtifactDigest, ""},
		{"tag retained parent", "oci://registry.example/tool:v1", parent, goodArtifactDigest, ""},
		{"native legacy", "registry.example/tool@" + goodArtifactDigest, "", goodArtifactDigest, ""},
		{"native retained", "registry.example/tool@" + goodArtifactDigest, goodArtifactDigest, goodArtifactDigest, ""},
		{"index", "registry.example/tool@" + parent, parent, goodArtifactDigest, ""},
		{"tag and digest", "registry.example/tool:v1@" + parent, parent, goodArtifactDigest, ""},
		{"missing parent", "registry.example/tool@" + parent, "", goodArtifactDigest, e.CodeUnverifiedRef},
		{"substituted parent", "registry.example/tool@" + parent, goodArtifactDigest, goodArtifactDigest, e.CodeUnverifiedRef},
		{"malformed parent", "registry.example/tool:v1", "not-a-digest", goodArtifactDigest, e.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateImageBinding("tool", tc.ref, tc.parent, map[string]string{"amd64": tc.native})
			if tc.code != "" {
				assertCatalogContractCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPrebuiltImageParentIsLockOwned(t *testing.T) {
	raw := rawComponent{Platforms: []string{"linux/amd64"}, Source: "s", OutputType: "oci-image",
		Prebuilt: &Prebuilt{Image: "registry.example/tool:v1", ImageParentDigest: goodArtifactDigest, Evidence: &ExternalEvidence{None: true}}}
	reg := &registryDoc{Sources: map[string]Source{"s": {Repo: "https://example.invalid/source"}}, Components: map[string]rawComponent{"tool": raw}}
	assertCatalogContractCode(t, validateRegistryDeclarations(reg), e.CodeInvalidCatalog)
}

func TestPrebuiltImageTagAndTemplateReferenceAdmission(t *testing.T) {
	for _, ref := range []string{"registry.example/tool:v1", "oci://registry.example/tool:v1", "registry.example/tool:{{.Version}}", "registry.example/tool@" + goodArtifactDigest} {
		if err := validateAcquiredImageReference("tool", ref); err != nil {
			t.Fatal(err)
		}
	}
}

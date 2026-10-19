// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// These are role-selection descriptions, not fabricated material receipts.
// Production invokes the helper only after the complete live trace is verified.
func TestDiskMaterialRoleUsesExactRootConsumerBinding(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, test := range []struct {
			name, kind, want string
			edges            []diskMaterialEdge
			code             e.Code
		}{
			{"direct declared base", "raw", "installer-iso", []diskMaterialEdge{{Consumer: "root", Binding: "installer-iso", Producer: "material"}}, ""},
			{"transitive original ISO", "raw", "installer-content", []diskMaterialEdge{{Consumer: "root", Binding: "installer-iso", Producer: "patched"}, {Consumer: "patched", Binding: "installer-source", Producer: "material"}}, ""},
			{"same binding different consumer", "raw", "installer-content", []diskMaterialEdge{{Consumer: "sibling", Binding: "installer-iso", Producer: "material"}}, ""},
			{"same consumer different purpose", "raw", "installer-content", []diskMaterialEdge{{Consumer: "root", Binding: "ordinary-material", Producer: "material"}}, ""},
			{"same purpose different producer", "raw", "installer-content", []diskMaterialEdge{{Consumer: "root", Binding: "installer-iso", Producer: "other"}}, ""},
			{"raw type is not base", "raw", "installer-content", nil, ""},
			{"package", "host-package", "host-package", nil, ""},
			{"archive", "tarball", "tarball", nil, ""},
			{"container", "oci-image", "oci-image", nil, ""},
			{"unsupported", "executable", "", nil, e.CodeEngineCapability},
		} {
			t.Run(architecture+"/"+test.name, func(t *testing.T) {
				got, err := diskMaterialContentKind("root", test.edges, diskMaterialMember{Producer: "material", Kind: test.kind})
				if got != test.want || factoryErrorCode(err) != test.code {
					t.Fatalf("kind=%q want=%q error=%v", got, test.want, err)
				}
			})
		}
	}
}

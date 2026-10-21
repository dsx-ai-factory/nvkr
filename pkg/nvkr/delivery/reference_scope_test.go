// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

func TestPublicationReferencesRetainDevelopmentAttempts(t *testing.T) {
	const prefix = "ghcr.io/example/nvkr/development/runs/123/attempts/"
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct {
		name, root, origin, want string
		invalid                  bool
	}{
		{"no origin", prefix + "3/releases", "", prefix + "3/node", false},
		{"same attempt", prefix + "3/releases", prefix + "3/releases-node", prefix + "3/node", false},
		{"earlier attempt", prefix + "3/releases", prefix + "1/releases-node", prefix + "1/node", false},
		{"malformed current root", prefix + "3/releases/extra", prefix + "1/releases-node", "", true},
		{"tagged origin", prefix + "3/releases", prefix + "1/releases-node:tag", "", true},
		{"multiple digest separators", prefix + "3/releases", prefix + "1/releases-node@" + digest, "", true},
		{"future attempt", prefix + "3/releases", prefix + "4/releases-node", "", true},
		{"other run", prefix + "3/releases", strings.Replace(prefix, "/123/", "/124/", 1) + "1/releases-node", "", true},
		{"other repository", prefix + "3/releases", strings.Replace(prefix, "/nvkr/", "/other/", 1) + "1/releases-node", "", true},
		{"other host", prefix + "3/releases", strings.Replace(prefix, "ghcr.io", "registry.example.test", 1) + "1/releases-node", "", true},
		{"noncanonical attempt", prefix + "3/releases", prefix + "01/releases-node", "", true},
		{"production sibling", "ghcr.io/example/nvkr/releases", "ghcr.io/example/nvkr/releases-node", "ghcr.io/example/nvkr/node", false},
		{"production provenance cannot redirect namespace", "ghcr.io/example/nvkr/releases", "ghcr.io/example/other/releases-node", "ghcr.io/example/nvkr/node", false},
		{"production provenance cannot redirect host", "ghcr.io/example/nvkr/releases", "registry.example.test/other/releases-node", "ghcr.io/example/nvkr/node", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			binding := &OutputBinding{Repository: "node"}
			if test.origin != "" {
				binding.Publication = &PublicationOrigin{Ref: test.origin + "@" + digest}
			}
			root, err := name.NewDigest(test.root+"@"+digest, name.StrictValidation)
			if err != nil {
				t.Fatal(err)
			}
			publication := PublicationRoot{Objects: []PublishedObject{
				{Name: "node", DescriptorDigest: digest, Output: binding},
				{Name: "node-sbom", DescriptorDigest: digest, Evidence: &EvidenceBinding{Outputs: []string{"node"}}},
			}}
			refs, err := publicationReferences(root, publication, false)
			if test.invalid {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, member := range []string{"node", "node-sbom"} {
				if got := refs[member].Name(); got != test.want+"@"+digest {
					t.Fatalf("%s reference = %s, want original %s", member, got, test.want)
				}
			}
		})
	}
}

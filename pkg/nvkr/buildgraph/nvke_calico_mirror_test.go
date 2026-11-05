// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// The mirror must reach the source-authored vendor command: the isolated
// vendoring container does not inherit the outer job's environment.
func TestNVKECalicoVendorMirror(t *testing.T) {
	for _, data := range []string{
		"../../../inputs/catalogs/node-images/release",
		"../../../inputs/catalogs/node-images/diagnostic",
		"../../../inputs/experiments/nvsentinel/catalog",
	} {
		t.Run(data, func(t *testing.T) {
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(data))
			if err != nil {
				t.Fatal(err)
			}
			component, ok := cat.Components["nvke-cni-calico"]
			if !ok {
				t.Fatal("Calico producer is missing")
			}
			for _, arch := range []string{"amd64", "arm64"} {
				t.Run(arch, func(t *testing.T) {
					graph := mustCompile(t, &Resolved{
						Platform:   "linux/" + arch,
						Components: []catalog.NamedComponent{{Name: "nvke-cni-calico", Component: component}},
					})
					want := []string{"bash", "-ec", "CNI_DOCKERHUB_MIRROR=mirror.gcr.io make -C os-image/packages/cni-calico bundle nfpm \\\n  ARCH=" + arch + " BIN_DIR=/usr/local/bin NFPM_BIN=/usr/local/bin/nfpm\n"}
					found := false
					for _, node := range graph.Nodes {
						if node.Kind != ir.KindFetch {
							continue
						}
						found = true
						if node.Network != ir.NetworkDefault || !slices.Equal(node.VendorCmd, want) {
							t.Fatalf("Calico fetch must set the mirror inside its native vendor command: network=%s argv=%q", node.Network, node.VendorCmd)
						}
						unmirrored := slices.Clone(want)
						unmirrored[2] = strings.TrimPrefix(unmirrored[2], "CNI_DOCKERHUB_MIRROR=mirror.gcr.io ")
						oldIdentity, err := ir.VendorSpecIdentity(unmirrored, node.VendorImage)
						if err != nil {
							t.Fatal(err)
						}
						if node.VendorSpecDigest == oldIdentity {
							t.Fatal("mirror selection must change the frozen vendor identity")
						}
					}
					if !found {
						t.Fatal("Calico fetch is missing")
					}
				})
			}
		})
	}
}

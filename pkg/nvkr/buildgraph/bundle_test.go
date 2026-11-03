// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBundleSelectionRequiresOneBuiltArchive(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*catalog.Catalog, *Selection)
		want   e.Code
	}{
		{name: "source archive"},
		{name: "raw producer", change: func(c *catalog.Catalog, _ *Selection) {
			v := c.Components["containerd"]
			v.OutputType = "raw"
			c.Components["containerd"] = v
		}, want: e.CodeInvalidCatalog},
		{name: "multiple producers", change: func(c *catalog.Catalog, s *Selection) {
			c.Components["copy"] = c.Components["containerd"]
			s.Components = append(s.Components, "copy")
		}, want: e.CodeInvalidCatalog},
		{name: "acquired archive", change: func(c *catalog.Catalog, _ *Selection) {
			c.Components["containerd"] = catalog.Component{Platforms: []string{"linux/amd64"}, OutputType: "tarball", Tag: "1.0.0",
				Prebuilt: &catalog.Prebuilt{URL: "https://example.test/bundle.tar.gz", Digests: map[string]string{"amd64": digAmd}, Evidence: &catalog.ExternalEvidence{None: true}}}
		}, want: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			cat := kubernetesFixture(t)
			c := cat.Components["containerd"]
			c.OutputType, c.ArtifactPath = "tarball", "bin/bundle.tar.gz"
			cat.Components["containerd"] = c
			selection := Selection{Components: []string{"containerd"}, Platform: "linux/amd64", OutputFormat: definition.OutputBundle}
			if test.change != nil {
				test.change(cat, &selection)
			}
			resolved, err := Resolve(cat, selection)
			if test.want != "" {
				if code(err) != test.want || resolved != nil {
					t.Fatalf("got %v, %v; want %s", resolved, err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Compile(resolved); err != nil {
				t.Fatal(err)
			}
		})
	}
}

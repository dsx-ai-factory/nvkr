// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"errors"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestHelmProducerAdmissionAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Component)
	}{
		{name: "source chart"},
		{name: "acquired", change: func(c *Component) { c.Prebuilt = &Prebuilt{} }},
		{name: "excluded", change: func(c *Component) { c.FromSource = false }},
		{name: "wrong payload", change: func(c *Component) { c.OutputType = "raw" }},
		{name: "overlap", change: func(c *Component) { c.Helm.Images["other"] = c.Helm.Images["controller"] }},
		{name: "missing path", change: func(c *Component) { c.Helm.Images["controller"] = HelmImage{} }},
		{name: "numeric selector", change: func(c *Component) {
			b := c.Helm.Images["controller"]
			b.Values.Digest = "images[0].digest"
			c.Helm.Images["controller"] = b
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := goodComponent()
			c.OutputType = "tarball"
			c.Helm = &HelmPackaging{Chart: ".", Images: map[string]HelmImage{"controller": {Values: HelmImageValues{Digest: "image.digest"}}}}
			original, err := base(c).SnapshotDigest()
			if err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(&c)
			}
			err = validateComponent("chart", c)
			if tc.change != nil {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
					t.Fatalf("expected invalid catalog, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// A producer's chart mapping is part of the frozen catalog identity.
			b := c.Helm.Images["controller"]
			b.Values.Digest = "other.digest"
			c.Helm.Images["controller"] = b
			changed, err := base(c).SnapshotDigest()
			if err != nil {
				t.Fatal(err)
			}
			if original == changed {
				t.Fatal("chart mapping omitted from catalog identity")
			}
		})
	}
}

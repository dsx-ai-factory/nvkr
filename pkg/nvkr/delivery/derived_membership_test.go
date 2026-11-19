// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDerivedInventoryPublicationMembership(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]PublishedObject)
		code e.Code
	}{
		{"aggregate", nil, ""},
		{"output local", func(o map[string]PublishedObject) { delete(o, "chart") }, ""},
		{"changed sibling", func(o map[string]PublishedObject) {
			v := o["chart"]
			v.DescriptorDigest = "sha256:" + strings.Repeat("b", 64)
			o["chart"] = v
		}, e.CodeVerifyFailed},
		{"missing sibling", func(o map[string]PublishedObject) { o["other"] = o["chart"]; delete(o, "chart") }, e.CodeReleaseIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest := "sha256:" + strings.Repeat("a", 64)
			binding := &derivedoutput.Binding{Encoding: derivedoutput.InventoryV1, RepositoryPrefix: "registry.example.com/nvke", Inputs: []derivedoutput.Input{{Name: "chart", Kind: "helm-oci", BuildDigest: digest}}}
			objects := map[string]PublishedObject{
				"chart":     {Name: "chart", DescriptorDigest: digest, Output: &OutputBinding{Format: "helm-oci", BuildDigest: digest, Repository: "chart"}},
				"inventory": {Name: "inventory", Output: &OutputBinding{Derived: binding, DerivedMaterials: []derivedoutput.Material{{Name: "chart", Digest: digest}}}},
			}
			if tc.edit != nil {
				tc.edit(objects)
			}
			err := validateDerivedMembership(objects)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("got %v want %s", err, tc.code)
			}
		})
	}
}

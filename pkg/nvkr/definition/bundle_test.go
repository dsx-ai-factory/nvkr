// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBundleOutputAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*DefinitionOutput)
		code   e.Code
	}{
		{name: "explicit source selection"},
		{name: "missing component", change: func(o *DefinitionOutput) { o.Components = nil }, code: e.CodeInvalidCatalog},
		{name: "multiple components", change: func(o *DefinitionOutput) { o.Components = []string{"a", "b"} }, code: e.CodeInvalidCatalog},
		{name: "helm binding", change: func(o *DefinitionOutput) { o.Helm = &HelmOutput{Images: map[string]string{"image": "other"}} }, code: e.CodeInvalidCatalog},
		{name: "APT settings", change: func(o *DefinitionOutput) { o.APT = &APTOutput{Distribution: "stable", Component: "main"} }, code: e.CodeInvalidCatalog},
		{name: "platform aggregate", change: func(o *DefinitionOutput) { o.Platform = ""; o.Platforms = []string{"linux/amd64", "linux/arm64"} }, code: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind, Metadata: Metadata{Name: "bundles"}, Catalog: Catalog{Base: "catalog"},
				Outputs: []DefinitionOutput{{Name: "rendered", Format: OutputBundle, Platform: "linux/arm64", Components: []string{"render"}}}}
			if test.change != nil {
				test.change(&d.Outputs[0])
			}
			err := d.Validate()
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("got %v, want %s", err, test.code)
			}
		})
	}
}

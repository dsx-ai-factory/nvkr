// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"bytes"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCompositeRuntimeMerge(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]AdmittedMember)
		code   e.Code
	}{
		{name: "default and explicit catalogs"},
		{name: "identical duplicate requirement", code: e.CodeInvalidCatalog, mutate: func(m []AdmittedMember) {
			m[1].Definition.InternalTools = m[0].Definition.Clone().InternalTools
		}},
		{name: "executable use remains gated", code: e.CodeEngineCapability, mutate: func(m []AdmittedMember) {
			m[0].Definition.InternalTools[0].Format = OutputExecutable
		}},
		{name: "escaping catalog", code: e.CodeInvalidCatalog, mutate: func(m []AdmittedMember) {
			m[1].Definition.InternalTools[0].Catalog.Base = "../../outside"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			members := mergeMembers()
			members[0].Definition.InternalTools = []InternalTool{{Name: "z-runtime", Platform: "linux/amd64", Format: OutputOCI, Components: []string{"runtime"}, Result: "runtime"}}
			members[1].Definition.InternalTools = []InternalTool{{Name: "a-runtime", Platform: "linux/amd64", Format: OutputOCI, Components: []string{"runtime"}, Result: "runtime", Catalog: &Catalog{Base: "../runtime", Overlays: []string{"runtime.yaml"}}}}
			if tc.mutate != nil {
				tc.mutate(members)
			}
			got, attribution, err := mergeComposite("product/composite.yaml", Metadata{Name: "product"}, members)
			if tc.code != "" {
				requireCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got.InternalTools) != 2 || got.InternalTools[0].Name != "a-runtime" || got.InternalTools[1].Name != "z-runtime" ||
				got.InternalTools[0].Catalog.Base != "../runtime" || got.InternalTools[0].Catalog.Overlays[0] != "../defs/runtime.yaml" ||
				got.InternalTools[1].Catalog.Base != "../catalog" || len(got.Outputs) != 5 || len(attribution) != 5 {
				t.Fatal("runtime merge lost rebasing, sort order or delivered membership")
			}
			again, _, err := mergeComposite("product/composite.yaml", Metadata{Name: "product"}, []AdmittedMember{members[2], members[1], members[0]})
			if err != nil {
				t.Fatal(err)
			}
			left, _ := canonical.Marshal(got)
			right, _ := canonical.Marshal(again)
			if !bytes.Equal(left, right) {
				t.Fatal("member order changed runtime merge")
			}
			got.InternalTools[0].Components[0] = "changed"
			got.InternalTools[0].Catalog.Overlays[0] = "changed"
			if members[0].Definition.InternalTools[0].Catalog != nil || members[1].Definition.InternalTools[0].Components[0] != "runtime" || members[1].Definition.InternalTools[0].Catalog.Overlays[0] != "runtime.yaml" {
				t.Fatal("merge aliases or mutates admitted member tools")
			}
		})
	}
}

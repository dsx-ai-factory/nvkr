// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublicationWireRejectsRemovedClusterFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields map[string]any
	}{
		{"cluster definition", map[string]any{"clusterDefinitionDigest": digestBytes([]byte("old cluster"))}},
		{"cluster realizations", map[string]any{"release": map[string]any{"clusters": []any{}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.fields["schemaVersion"] = PublicationRootSchema
			raw, err := canonical.Marshal(test.fields)
			if err != nil {
				t.Fatal(err)
			}
			var root PublicationRoot
			assertRegistryCode(t, canonical.UnmarshalExact(raw, &root), e.CodeVerifyFailed)
		})
	}
	assertRegistryCode(t, (PublicationRoot{SchemaVersion: "nvkr.dev/publication-root/v2"}).Validate(), e.CodeVerifyFailed)
}

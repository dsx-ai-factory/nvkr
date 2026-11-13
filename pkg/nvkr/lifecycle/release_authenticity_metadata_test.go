// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestAuthenticatedMetadataIsIndependentAndRevoked(t *testing.T) {
	layout, digest := authenticityMetadataFixture(t)
	consumer := &Consumer{verifier: &authenticityVerifier{}, inspection: InspectionOptions{LayoutPath: layout}}
	accepted, err := consumer.Authenticate(t.Context(), "registry.example.test/releases@"+digest, authenticityPolicy())
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := accepted.MetadataDescription()
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.Outputs) == 0 || metadata.RootDigest != digest {
		t.Fatal("missing authenticated metadata")
	}
	name := metadata.Outputs[0].Name
	metadata.Outputs[0].Name = "changed"
	again, err := accepted.MetadataDescription()
	if err != nil || again.Outputs[0].Name != name {
		t.Fatal("metadata aliases authority", err)
	}
	if err := accepted.Close(); err != nil {
		t.Fatal(err)
	}
	for _, handle := range []*AuthenticatedRelease{accepted, nil, {}} {
		_, err := handle.MetadataDescription()
		assertCode(t, err, e.CodeGateUnmet)
	}
}

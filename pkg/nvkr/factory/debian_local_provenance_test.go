// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianLocalProvenanceRequiresOriginalReceipt(t *testing.T) {
	for _, receipt := range []*DebianOutputReceipt{nil, {}} {
		raw, observation, err := receipt.localProducerProvenance(t.Context())
		if raw != nil || observation != (DebianOutputObservation{}) || factoryErrorCode(err) != e.CodeProvenanceMissing {
			t.Fatal("description restored local producer provenance", err)
		}
	}
}

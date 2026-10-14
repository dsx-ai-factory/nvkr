// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianOutputReceiptDescriptionsCannotRestoreAuthority(t *testing.T) {
	for _, receipt := range []*DebianOutputReceipt{nil, {}, func() *DebianOutputReceipt {
		raw, err := canonical.Marshal(&DebianOutputReceipt{})
		if err != nil {
			t.Fatal(err)
		}
		var value DebianOutputReceipt
		if err := canonical.UnmarshalExact(raw, &value); err != nil {
			t.Fatal(err)
		}
		return &value
	}()} {
		observation, err := receipt.DescribeContext(t.Context())
		if observation != (DebianOutputObservation{}) || factoryErrorCode(err) != e.CodeProvenanceMissing {
			t.Fatal("description restored receipt", err)
		}
	}
	plan, result, _ := debianRunOwnerFixture(t)
	receipt, err := result.debian.observeBoundOutput(t.Context(), result, plan, plan.LifecycleOutputs[0])
	if receipt != nil || factoryErrorCode(err) != e.CodeProvenanceMissing {
		t.Fatal("custody restored inspected bytes", err)
	}
	if _, err := (&DebianOutputReceipt{}).WriteToContext(t.Context(), nil); factoryErrorCode(err) != e.CodeUsage {
		t.Fatal("missing staging writer", err)
	}
	if _, err := (&DebianOutputReceipt{}).WriteToContext(t.Context(), io.Discard); factoryErrorCode(err) != e.CodeProvenanceMissing {
		t.Fatal(err)
	}
}

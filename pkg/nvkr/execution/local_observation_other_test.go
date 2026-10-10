// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package execution

import (
	"context"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestLocalHostObservationFailsClosedOutsideLinux(t *testing.T) {
	facts, err := ObserveLocalHost(context.Background(), openLocalWorkspace(t))
	if !isZeroObservedHostFacts(facts) || errorCode(err) != nvkrerr.CodeEngineCapability {
		t.Fatalf("facts = %#v, error = %v; want %s", facts, err, nvkrerr.CodeEngineCapability)
	}
}

func TestLocalVMAdmissionFailsClosedOutsideLinux(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityPreferred, false)
	admission, err := AdmitLocalVM(context.Background(), request, openLocalWorkspace(t))
	if admission != nil || errorCode(err) != nvkrerr.CodeEngineCapability {
		t.Fatalf("admission = %#v, error = %v; want %s", admission, err, nvkrerr.CodeEngineCapability)
	}
}

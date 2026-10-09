// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestVMCapacityFailureDescribesObservedAndRequired(t *testing.T) {
	request := testLocalVMRequest(t, CapabilityRequired, true)
	for _, tc := range []struct {
		name       string
		change     func(*ObservedHostFacts)
		diagnostic string
	}{
		{"CPU", func(f *ObservedHostFacts) { f.Resources.CPUMillis-- }, "CPU 7999/8000 milliCPU"},
		{"memory", func(f *ObservedHostFacts) { f.Resources.MemoryMiB-- }, "memory 16383/16384 MiB"},
		{"workspace", func(f *ObservedHostFacts) { f.Resources.EphemeralStorageMiB-- }, "workspace 65535/65536 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := testVMFacts(true)
			tc.change(&facts)
			err := observedHostSatisfiesVM(request, facts)
			if errorCode(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), tc.diagnostic) || !strings.Contains(err.Error(), "not reserved") {
				t.Fatalf("capacity diagnostic: %v", err)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestVMDriverPublicBootstrapAdmission(t *testing.T) {
	for _, test := range []struct {
		name      string
		variables map[string]string
		allow     bool
	}{
		{name: "public bootstrap", variables: map[string]string{"luks_passphrase": "changeit"}, allow: true},
		{name: "implicit default"},
		{name: "private value", variables: map[string]string{"luks_passphrase": "private-value-must-not-appear"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan, node := compiledPackerVMNodeWithEncryption(t, "luks2", test.variables)
			admission := new(fakeVMAdmission)
			executor := new(fakeVMDriverExecutor)
			artifact, result, err := dispatchAdmittedVMProducer(t.Context(), admission, plan, node, executor, nil)
			if test.allow {
				if err != nil || artifact == nil || result == nil || admission.calls != 1 || executor.packerCalls != 1 {
					t.Fatalf("public bootstrap dispatch artifact=%v result=%v error=%v admission=%d executor=%d", artifact, result, err, admission.calls, executor.packerCalls)
				}
				if err := artifact.Close(); err != nil {
					t.Fatal(err)
				}
				if err := validateVMDriverPlan(plan); err != nil {
					t.Fatal(err)
				}
				return
			}
			if artifact != nil || result != nil || factoryErrorCode(err) != e.CodeEngineCapability || admission.calls != 0 || executor.packerCalls != 0 {
				t.Fatalf("rejected bootstrap reached VM work: artifact=%v result=%v error=%v admission=%d executor=%d", artifact, result, err, admission.calls, executor.packerCalls)
			}
			if strings.Contains(err.Error(), "private-value-must-not-appear") {
				t.Fatal("rejected bootstrap exposed its value")
			}
			workspace := ".nvkr/public-bootstrap-must-not-exist"
			resolver := new(fakeVMDriverResolver)
			options := withLocalDefaults(LocalRunOptions{Workspace: workspace})
			options.vmResolver = resolver
			run, err := runLocal(t.Context(), plan, options, productionLocalRunDeps())
			if run != nil || factoryErrorCode(err) != e.CodeEngineCapability || resolver.calls != 0 {
				t.Fatalf("rejected bootstrap reached resolver: run=%v error=%v calls=%d", run, err, resolver.calls)
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Fatalf("rejected bootstrap mutated workspace: %v", err)
			}
		})
	}
}

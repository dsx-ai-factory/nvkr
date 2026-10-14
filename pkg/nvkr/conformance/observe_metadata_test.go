// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

func TestObservedNodeFactsKeepOSReleaseAndMissingEvidence(t *testing.T) {
	options := buildFixture(t, "success")
	admitted, err := admit(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer admitted.close()
	facts, err := admitted.observe(t.Context(), filepath.Dir(options.Hydrophone.Path), "before")
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[[2]string]testresults.EnvironmentFact{}
	for _, fact := range facts {
		byKey[[2]string{fact.Scope, fact.Name}] = fact
		if fact.Phase != "before" {
			t.Fatal("observation phase lost")
		}
	}
	for _, tc := range []struct{ name, value, status string }{
		{"name", "private-node", "observed"},
		{"osImage", "Ubuntu 24.04.3", "observed"},
		{"architecture", "amd64", "observed"},
		{"kernelVersion", "6.8.0", "observed"},
		{"containerRuntimeVersion", "containerd://2.1.0", "observed"},
		{"kubeletVersion", "", "unavailable"},
		{"machineID", "", "unavailable"},
		{"bootID", "", "unavailable"},
		{"systemUUID", "", "unavailable"},
	} {
		got, exists := byKey[[2]string{"node:privateuid", tc.name}]
		if !exists || got.Metadata.Value != tc.value || got.Metadata.Status != tc.status {
			t.Errorf("node fact %s: got %+v exists %v", tc.name, got, exists)
		}
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"reflect"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestHeldRuntimeStorageSelectionIsExplicitAndDoesNotMutateCommand(t *testing.T) {
	t.Setenv("STORAGE_DRIVER", "attacker")
	for _, driver := range []definition.PodmanStorageDriver{"", definition.PodmanStorageVFS, definition.PodmanStorageOverlay} {
		t.Run(string(driver), func(t *testing.T) {
			runtime := heldRuntimeExecutable{storageDriver: driver}
			args := []string{"image", "inspect", "exact-image"}
			want := append([]string(nil), args...)
			if driver != "" {
				want = append([]string{"--storage-driver=" + string(driver)}, want...)
			}
			if got := runtime.commandArgs(args); !reflect.DeepEqual(got, want) {
				t.Fatalf("args: %v want %v", got, want)
			}
			if !reflect.DeepEqual(args, []string{"image", "inspect", "exact-image"}) {
				t.Fatal("mutated caller command")
			}
		})
	}
}

func TestPodmanStorageObservationMatchesFrozenRoute(t *testing.T) {
	base := `{"host":{"arch":"amd64","os":"linux","hostname":"native","security":{"rootless":true}},"store":{"graphDriverName":"vfs","graphRoot":"/owned/store","runRoot":"/owned/run"},"version":{"Version":"4.9.3","OsArch":"linux/amd64"}}`
	for _, tc := range []struct {
		name   string
		driver definition.PodmanStorageDriver
		valid  bool
	}{
		{"existing default", "", true}, {"explicit match", definition.PodmanStorageVFS, true},
		{"different observed store", definition.PodmanStorageOverlay, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity, err := podmanServerIdentityWithDriver([]byte(base), tc.driver)
			if tc.valid && (err != nil || identity == "") {
				t.Fatalf("missing observation: %q %v", identity, err)
			}
			if !tc.valid && (identity != "" || code(err) != e.CodeEngineCapability) {
				t.Fatalf("substituted route admitted: %q %v", identity, err)
			}
		})
	}
}

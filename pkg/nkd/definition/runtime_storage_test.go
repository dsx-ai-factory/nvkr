// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPodmanStorageDriverSelection(t *testing.T) {
	for _, tc := range []struct {
		name, runtime string
		driver        PodmanStorageDriver
		valid         bool
	}{
		{"default", "docker", "", true},
		{"vfs", "podman", PodmanStorageVFS, true},
		{"overlay", "", PodmanStorageOverlay, true},
		{"unknown", "podman", "custom", false},
		{"wrong runtime", "docker", PodmanStorageVFS, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.driver.Validate(tc.runtime)
			if tc.valid && err != nil {
				t.Fatal(err)
			}
			var coded *e.Error
			if !tc.valid && (!errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog) {
				t.Fatalf("unexpected code: %v", err)
			}
		})
	}
}

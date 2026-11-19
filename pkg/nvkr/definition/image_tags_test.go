// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestEnvironmentImageTagAdmissionAndClone(t *testing.T) {
	for _, tc := range []struct {
		name, output, tag string
		valid             bool
	}{
		{"explicit", "nvke-controller", "candidate", true},
		{"version", "nvke-controller", "v0.1.1-rc.9", true},
		{"empty", "nvke-controller", "", false},
		{"repository", "nvke-controller", "registry.example/nvke-controller:v1", false},
		{"reserved", "nvke-controller", "sha256-deadbeef", false},
		{"expansion", "nvke-controller", "${VERSION}", false},
		{"newline", "nvke-controller", "v1\n", false},
		{"long", "nvke-controller", strings.Repeat("a", 129), false},
		{"invalid output", "nvke-controller/image", "v1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "fixture"}, UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64", Publication: &Publication{RepositoryPrefix: "registry.example/team", Anonymous: true, ImageTags: map[string]string{tc.output: tc.tag}}}
			err := env.Validate()
			if !tc.valid {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
					t.Fatalf("admission error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			clone := env.Clone()
			clone.Publication.ImageTags[tc.output] = "changed"
			if env.Publication.ImageTags[tc.output] != tc.tag {
				t.Fatal("clone shares discovery tags")
			}
		})
	}
}

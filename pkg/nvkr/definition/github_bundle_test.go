// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubBundleNameAdmission(t *testing.T) {
	for _, test := range []struct {
		name  string
		valid bool
	}{
		{"", true}, {"a", true}, {"0", true}, {"nvke-nightly", true},
		{strings.Repeat("a", 48), true}, {strings.Repeat("a", 49), false},
		{"../nightly", false}, {"nightly/other", false}, {"nightly" + string('\\') + "other", false},
		{"nightly.yml", false}, {"Nightly", false}, {"-nightly", false}, {"nightly-", false},
		{"nightly\n", false}, {"nightly\x00", false}, {"nightly snow", false}, {"nightly_1", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind,
				Metadata: Metadata{Name: "test"}, UtilityPlatform: "linux/amd64",
				UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), GitHubBundle: test.name}
			err := input.Validate()
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("unsafe name returned %v", err)
			}
		})
	}
}

func TestGitHubBundleBindsEnvironmentIdentity(t *testing.T) {
	input := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind,
		Metadata: Metadata{Name: "test"}, UtilityPlatform: "linux/amd64",
		UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64)}
	before, err := input.Digest()
	if err != nil {
		t.Fatal(err)
	}
	copied := input.Clone()
	copied.GitHubBundle = "nightly"
	after, err := copied.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before == after || input.GitHubBundle != "" {
		t.Fatal("namespace selection failed Environment identity or clone isolation")
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestGitHubRunnerAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runners map[string]string
		runsOn  string
		code    e.Code
	}{
		{name: "legacy omitted"},
		{name: "both native", runners: map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"}},
		{name: "empty", runners: map[string]string{}, code: e.CodeInvalidCatalog},
		{name: "mixed selection", runners: map[string]string{"linux/amd64": "ubuntu-24.04"}, runsOn: "ubuntu-latest", code: e.CodeInvalidCatalog},
		{name: "mismatch", runners: map[string]string{"linux/arm64": "ubuntu-24.04"}, code: e.CodeEngineCapability},
		{name: "converse mismatch", runners: map[string]string{"linux/amd64": "ubuntu-24.04-arm"}, code: e.CodeEngineCapability},
		{name: "too many routes", runners: map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm", "linux/riscv64": "ubuntu-24.04"}, code: e.CodeInvalidCatalog},
		{name: "unknown platform", runners: map[string]string{"darwin/arm64": "ubuntu-24.04-arm"}, code: e.CodeEngineCapability},
		{name: "self hosted", runners: map[string]string{"linux/amd64": "self-hosted"}, code: e.CodeEngineCapability},
		{name: "unrecognized route", runners: map[string]string{"linux/amd64": "company-linux"}, code: e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "test"}, UtilityPlatform: "linux/amd64", UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), GitHubRunners: tc.runners, RunsOn: tc.runsOn}
			err := input.Validate()
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}

func TestGitHubRunnerCloneAndIdentity(t *testing.T) {
	input := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "test"}, UtilityPlatform: "linux/amd64", UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), GitHubRunners: map[string]string{"linux/amd64": "ubuntu-24.04"}}
	before, err := input.Digest()
	if err != nil {
		t.Fatal(err)
	}
	copied := input.Clone()
	copied.GitHubRunners["linux/amd64"] = "ubuntu-22.04"
	if input.GitHubRunners["linux/amd64"] != "ubuntu-24.04" {
		t.Fatal("clone shares route map")
	}
	after, err := copied.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("route changes must change Environment identity")
	}
}

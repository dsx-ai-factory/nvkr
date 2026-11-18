// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func gitlabTestEnvironment() Environment {
	return Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind,
		Metadata: Metadata{Name: "test"}, UtilityPlatform: "linux/amd64",
		UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64),
		GitLab: &GitLab{APIURL: "https://gitlab.example.com/api/v4", ProjectID: 123, APIReadCredentialRef: "gitlab:NVKR_API_READ", APIReadCredentialKind: "private-token", Runners: map[string]GitLabRunner{"linux/amd64": {
			Tags: []string{"native-amd64"}, BootstrapImage: "registry.example.com/tools/go@sha256:" + strings.Repeat("b", 64),
		}}}}
}

func TestGitLabEnvironmentAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Environment)
		code   e.Code
	}{
		{name: "explicit route", change: func(*Environment) {}},
		{name: "omitted unchanged", change: func(e *Environment) { e.GitLab = nil }},
		{name: "empty map", change: func(e *Environment) { e.GitLab.Runners = nil }, code: e.CodeInvalidCatalog},
		{name: "missing project", change: func(e *Environment) { e.GitLab.ProjectID = 0 }, code: e.CodeInvalidCatalog},
		{name: "insecure api", change: func(e *Environment) { e.GitLab.APIURL = "http://gitlab.example.com/api/v4" }, code: e.CodeInvalidCatalog},
		{name: "hostname missing", change: func(e *Environment) { e.GitLab.APIURL = "https://:443/api/v4" }, code: e.CodeInvalidCatalog},
		{name: "explicit port", change: func(e *Environment) { e.GitLab.APIURL = "https://gitlab.example.com:8443/api/v4" }},
		{name: "IPv6 hostname", change: func(e *Environment) { e.GitLab.APIURL = "https://[::1]:8443/api/v4" }},
		{name: "foreign api path", change: func(e *Environment) { e.GitLab.APIURL = "https://gitlab.example.com/other" }, code: e.CodeInvalidCatalog},
		{name: "embedded api credential", change: func(e *Environment) { e.GitLab.APIURL = "https://user:password@gitlab.example.com/api/v4" }, code: e.CodeInvalidCatalog},
		{name: "query api", change: func(e *Environment) { e.GitLab.APIURL = "https://gitlab.example.com/api/v4?token=value" }, code: e.CodeInvalidCatalog},
		{name: "empty query api", change: func(e *Environment) { e.GitLab.APIURL += "?" }, code: e.CodeInvalidCatalog},
		{name: "empty fragment api", change: func(e *Environment) { e.GitLab.APIURL += "#" }, code: e.CodeInvalidCatalog},
		{name: "fragment api", change: func(e *Environment) { e.GitLab.APIURL += "#fragment" }, code: e.CodeInvalidCatalog},
		{name: "opaque api", change: func(e *Environment) { e.GitLab.APIURL = "https:gitlab.example.com/api/v4" }, code: e.CodeInvalidCatalog},
		{name: "escaped api path", change: func(e *Environment) { e.GitLab.APIURL = "https://gitlab.example.com/api/%764" }, code: e.CodeInvalidCatalog},
		{name: "wrong credential provider", change: func(e *Environment) { e.GitLab.APIReadCredentialRef = "github:NVKR_API_READ" }, code: e.CodeInvalidCatalog},
		{name: "native token as private reader", change: func(e *Environment) { e.GitLab.APIReadCredentialRef = "gitlab:CI_JOB_TOKEN" }, code: e.CodeInvalidCatalog},
		{name: "native registry password as private reader", change: func(e *Environment) { e.GitLab.APIReadCredentialRef = "gitlab:CI_REGISTRY_PASSWORD" }, code: e.CodeInvalidCatalog},
		{name: "native token as job reader", change: func(e *Environment) {
			e.GitLab.APIReadCredentialRef = "gitlab:CI_JOB_TOKEN"
			e.GitLab.APIReadCredentialKind = "job-token"
		}, code: e.CodeInvalidCatalog},
		{name: "native registry password as job reader", change: func(e *Environment) {
			e.GitLab.APIReadCredentialRef = "gitlab:CI_REGISTRY_PASSWORD"
			e.GitLab.APIReadCredentialKind = "job-token"
		}, code: e.CodeInvalidCatalog},
		{name: "separate job reader", change: func(e *Environment) { e.GitLab.APIReadCredentialKind = "job-token" }},
		{name: "unknown credential kind", change: func(e *Environment) { e.GitLab.APIReadCredentialKind = "auto" }, code: e.CodeEngineCapability},
		{name: "unsupported platform", change: func(e *Environment) { e.GitLab.Runners["darwin/arm64"] = e.GitLab.Runners["linux/amd64"] }, code: e.CodeEngineCapability},
		{name: "two native routes", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = []string{"native-arm64"}
			e.GitLab.Runners["linux/arm64"] = r
		}},
		{name: "too many routes", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			e.GitLab.Runners["linux/arm64"], e.GitLab.Runners["linux/ppc64le"] = r, r
		}, code: e.CodeInvalidCatalog},
		{name: "mixed provider", change: func(e *Environment) { e.GitHubBundle = "other" }, code: e.CodeEngineCapability},
		{name: "legacy runner", change: func(e *Environment) { e.RunsOn = "ubuntu-latest" }, code: e.CodeEngineCapability},
		{name: "empty tags", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = nil
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeInvalidCatalog},
		{name: "unsorted tags", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = []string{"z", "a"}
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeInvalidCatalog},
		{name: "variable selector", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = []string{"$RUNNER_TAG"}
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeInvalidCatalog},
		{name: "maximum tag count", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = strings.Fields("a b c d e f g h i j k l m n o p")
			e.GitLab.Runners["linux/amd64"] = r
		}},
		{name: "too many tags", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = strings.Fields("a b c d e f g h i j k l m n o p q")
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeInvalidCatalog},
		{name: "maximum tag length", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = []string{strings.Repeat("a", 128)}
			e.GitLab.Runners["linux/amd64"] = r
		}},
		{name: "tag too long", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = []string{strings.Repeat("a", 129)}
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeInvalidCatalog},
		{name: "control in tag", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = []string{"native\nrunner"}
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeInvalidCatalog},
		{name: "backslash in tag", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = []string{"native\\runner"}
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeInvalidCatalog},
		{name: "backtick in tag", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.Tags = []string{"native`runner"}
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeInvalidCatalog},
		{name: "floating image", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.BootstrapImage = "registry.example.com/tools/go:latest"
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeUnpinnedSource},
		{name: "variable image authority", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.BootstrapImage = "$REGISTRY.example.com/tools/go@sha256:" + strings.Repeat("b", 64)
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeUnpinnedSource},
		{name: "invalid repository", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.BootstrapImage = "invalid repository@sha256:" + strings.Repeat("b", 64)
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeUnpinnedSource},
		{name: "control in image", change: func(e *Environment) {
			r := e.GitLab.Runners["linux/amd64"]
			r.BootstrapImage += "\n"
			e.GitLab.Runners["linux/amd64"] = r
		}, code: e.CodeUnpinnedSource},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := gitlabTestEnvironment()
			test.change(&input)
			err := input.Validate()
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("want %s, got %v", test.code, err)
			}
			if strings.Contains(err.Error(), "user:password") || strings.Contains(err.Error(), "token=value") {
				t.Fatal("API URL refusal echoed credential-bearing input")
			}
		})
	}
}

func TestGitLabEnvironmentCloneAndIdentity(t *testing.T) {
	input := gitlabTestEnvironment()
	before, err := input.Digest()
	if err != nil {
		t.Fatal(err)
	}
	copied := input.Clone()
	runner := copied.GitLab.Runners["linux/amd64"]
	runner.Tags[0] = "other-amd64"
	copied.GitLab.Runners["linux/amd64"] = runner
	if input.GitLab.Runners["linux/amd64"].Tags[0] != "native-amd64" {
		t.Fatal("clone shares mutable tags")
	}
	after, err := copied.Digest()
	if err != nil || before == after {
		t.Fatalf("runner policy must bind Environment identity: %v", err)
	}
	delete(copied.GitLab.Runners, "linux/amd64")
	if len(input.GitLab.Runners) != 1 {
		t.Fatal("clone shares mutable routes")
	}
}

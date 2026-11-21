// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublicationVisibilityStrictLoadingAndIdentity(t *testing.T) {
	env := publicationTestEnvironment()
	env.Publication = &Publication{RepositoryPrefix: "ghcr.io/example/source", CredentialRef: "github:GITHUB_TOKEN"}
	raw, err := canonical.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := env.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, field string
		want        bool
		code        e.Code
	}{
		{name: "omitted"},
		{name: "false", field: `"allowPublicPackages":false,`},
		{name: "true", field: `"allowPublicPackages":true,`, want: true},
		{name: "string refused", field: `"allowPublicPackages":"true",`, code: e.CodeInvalidCatalog},
		{name: "number refused", field: `"allowPublicPackages":1,`, code: e.CodeInvalidCatalog},
		{name: "unknown field refused", field: `"publicPackages":true,`, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := strings.Replace(string(raw), `"publication":{`, `"publication":{`+tc.field, 1)
			loaded, err := DecodeEnvironment([]byte(input))
			if tc.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code {
					t.Fatalf("want %s, got %v", tc.code, err)
				}
				return
			}
			if err != nil || loaded.Publication.AllowPublicPackages != tc.want {
				t.Fatalf("visibility opt-in: %+v, %v", loaded.Publication, err)
			}
			digest, err := loaded.Clone().Digest()
			if err != nil || (digest != baseDigest) != tc.want {
				t.Fatalf("opt-in not bound to Environment identity: %s, %v", digest, err)
			}
		})
	}
}

func TestPublicationVisibilityOptInRequiresPlatformTokenGHCR(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Environment)
	}{
		{"independent writer", func(env *Environment) { env.Publication.CredentialRef = ProductionRegistryWriteCredential }},
		{"other registry", func(env *Environment) { env.Publication.RepositoryPrefix = "registry.example/source" }},
		{"plain HTTP", func(env *Environment) { env.Publication.PlainHTTP = true }},
		{"anonymous", func(env *Environment) { env.Publication.CredentialRef, env.Publication.Anonymous = "", true }},
		{"GitLab", func(env *Environment) { env.GitLab = gitlabTestEnvironment().GitLab }},
		{"Tekton", func(env *Environment) {
			*env = tektonEnvironmentFixture(t)
			env.Publication.AllowPublicPackages = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := publicationTestEnvironment()
			env.Publication = &Publication{RepositoryPrefix: "ghcr.io/example/source", CredentialRef: "github:GITHUB_TOKEN", AllowPublicPackages: true}
			tc.edit(&env)
			withoutOptIn := env.Clone()
			withoutOptIn.Publication.AllowPublicPackages = false
			if err := withoutOptIn.Validate(); err != nil {
				t.Fatalf("fixture fails admission before the visibility opt-in: %v", err)
			}
			var coded *e.Error
			if err := env.Validate(); !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("unsupported opt-in accepted: %v", err)
			}
		})
	}
}

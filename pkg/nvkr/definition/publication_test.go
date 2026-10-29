// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPublicationTransportAdmissionAndStrictLoading(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transports []string
		code       e.Code
	}{
		{name: "omitted OCI"},
		{name: "explicit OCI", transports: []string{PublicationTransportOCI}},
		{name: "APT only", transports: []string{PublicationTransportAPT}, code: e.CodeEngineCapability},
		{name: "both", transports: []string{PublicationTransportAPT, PublicationTransportOCI}, code: e.CodeEngineCapability},
		{name: "empty", transports: []string{}, code: e.CodeInvalidCatalog},
		{name: "empty member", transports: []string{""}, code: e.CodeInvalidCatalog},
		{name: "unknown", transports: []string{"s3"}, code: e.CodeInvalidCatalog},
		{name: "unknown after APT", transports: []string{PublicationTransportAPT, "unknown"}, code: e.CodeInvalidCatalog},
		{name: "duplicate OCI", transports: []string{PublicationTransportOCI, PublicationTransportOCI}, code: e.CodeInvalidCatalog},
		{name: "duplicate APT", transports: []string{PublicationTransportAPT, PublicationTransportAPT}, code: e.CodeInvalidCatalog},
		{name: "unsorted", transports: []string{PublicationTransportOCI, PublicationTransportAPT}, code: e.CodeInvalidCatalog},
		{name: "case", transports: []string{"OCI"}, code: e.CodeInvalidCatalog},
		{name: "whitespace", transports: []string{"oci "}, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := publicationTestEnvironment()
			env.Publication.Transports = tc.transports
			assertCode := func(err error) {
				t.Helper()
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
			}
			assertCode(env.Validate())
			_, err := env.Digest()
			assertCode(err)

			// Restore an explicit empty array after canonical marshaling so the
			// strict loader tests rejection instead of the omitted-field default.
			raw, err := canonical.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			if tc.transports != nil && len(tc.transports) == 0 {
				text := strings.Replace(string(raw), `"publication":{`, `"publication":{"transports":[],`, 1)
				raw = []byte(text)
			}
			path := filepath.Join(t.TempDir(), "environment.yaml")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadEnvironment(t.Context(), path)
			assertCode(err)
			if tc.code != "" && loaded.Publication != nil {
				t.Fatal("rejected selection returned a partial Environment")
			}
		})
	}
}

func TestPublicationTransportOmissionIdentityAndClone(t *testing.T) {
	env := publicationTestEnvironment()
	raw, err := canonical.Marshal(env.Publication)
	if err != nil || string(raw) != `{"anonymous":true,"repositoryPrefix":"registry.example/team"}` {
		t.Fatalf("omitted selection changed legacy canonical bytes: %s (%v)", raw, err)
	}
	env.Publication.Transports = []string{PublicationTransportOCI}
	cloned := env.Clone()
	cloned.Publication.Transports[0] = PublicationTransportAPT
	if env.Publication.Transports[0] != PublicationTransportOCI {
		t.Fatal("cloned selection aliases caller memory")
	}
}

func TestAPTOnlyAdmissionDoesNotRequireOCISettings(t *testing.T) {
	env := publicationTestEnvironment()
	env.Publication = &Publication{Transports: []string{PublicationTransportAPT}}
	var coded *e.Error
	if err := env.Validate(); !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability || !strings.Contains(err.Error(), "apt") {
		t.Fatalf("APT-only selection did not reach its capability boundary: %v", err)
	}
}

func publicationTestEnvironment() Environment {
	return Environment{
		APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "fixture"},
		UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64",
		Publication: &Publication{RepositoryPrefix: "registry.example/team", Anonymous: true},
	}
}

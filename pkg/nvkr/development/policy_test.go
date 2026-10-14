// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package development

import (
	"errors"
	"regexp"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNamespaceAdmissionAndSigningIdentity(t *testing.T) {
	for _, test := range []struct {
		prefix string
		valid  bool
	}{
		{"ghcr.io/example/nvkr/development", true},
		{"ghcr.io/dsx-ai-factory/nvkr/development", true},
		{"ghcr.io/example/nvkr/production", false},
		{"ghcr.io/example/nvkr/development/sibling", false},
		{"ghcr.io/example/nvkr/development:tag", false},
		{"ghcr.io/example/nvkr/development@sha256:abc", false},
		{"ghcr.io/Example/nvkr/development", false},
		{"ghcr.io/example/../development", false},
		{"registry.example.com/example/nvkr/development", false},
		{"ghcr.io/example/nvkr/development\n", false},
	} {
		t.Run(test.prefix, func(t *testing.T) {
			namespace, err := ParseNamespace(test.prefix)
			if !test.valid {
				requireCapability(t, err)
				return
			}
			if err != nil || namespace.Prefix() != test.prefix {
				t.Fatalf("namespace = %+v (%v)", namespace, err)
			}
			identity := regexp.MustCompile(namespace.Identity())
			base := "https://github.com/" + namespace.Repository() + "/.github/workflows/"
			for _, candidate := range []struct {
				identity string
				valid    bool
			}{
				{base + "nvkr-dev.yml@refs/heads/feature/pipeline", true},
				{base + "nvkr-dev.yml@refs/heads/main", true},
				{base + "nvkr-run.yml@refs/heads/main", false},
				{base + "nvkr-dev.yml@refs/tags/v1", false},
				{base + "nvkr-dev.yml@refs/pull/1/head", false},
				{base + "nvkr-dev.yml@refs/heads/", false},
				{base + "nvkr-dev.yml@refs/heads/main\n", false},
				{"https://github.com/other/nvkr/.github/workflows/nvkr-dev.yml@refs/heads/main", false},
			} {
				if identity.MatchString(candidate.identity) != candidate.valid {
					t.Fatalf("wrong identity admission: %s", candidate.identity)
				}
			}
		})
	}
}

func TestAttemptReferenceAdmission(t *testing.T) {
	namespace, err := ParseNamespace("ghcr.io/example/nvkr/development")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		suffix string
		valid  bool
	}{
		{"/runs/123/attempts/1/releases", true},
		{"/runs/123/attempts/2/releases-node-image", true},
		{"/runs/123/attempts/2/node-image", true},
		{"/runs/123/attempts/1/releases-nvkectl--debug", false},
		{"/runs/123/attempts/1/nvkectl__debug", false},
		{"/runs/123/attempts/1/nvkectl..debug", false},
		{"/runs/0/attempts/1/releases", false},
		{"/runs/01/attempts/1/releases", false},
		{"/runs/+1/attempts/1/releases", false},
		{"/runs/18446744073709551616/attempts/1/releases", false},
		{"/runs/123/attempts/0/releases", false},
		{"/runs/123/attempts/01/releases", false},
		{"/runs/123/attempts/1/../releases", false},
		{"/runs/123/attempts/1/releases:tag", false},
		{"/runs/123/attempts/1/releases@sha256:abc", false},
		{"/runs/123/attempts/1/", false},
		{"/runs/123/attempts/1/release\n", false},
		{"-adjacent/runs/123/attempts/1/releases", false},
	} {
		t.Run(test.suffix, func(t *testing.T) {
			ref, err := namespace.ParseReference(namespace.Prefix() + test.suffix)
			if !test.valid {
				requireCapability(t, err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			again, err := namespace.Reference(ref.RunID(), ref.Attempt(), ref.Leaf())
			if err != nil || again != ref.String() || again != namespace.Prefix()+test.suffix {
				t.Fatalf("reference did not round trip: %s (%v)", again, err)
			}
		})
	}
	var zero Namespace
	if zero.Identity() != "" || zero.Repository() != "" || zero.Prefix() != "" {
		t.Fatal("zero namespace acquired scope")
	}
	_, err = zero.Reference("123", "1", "releases")
	requireCapability(t, err)
}

func requireCapability(t *testing.T, err error) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("error = %v, want engine capability", err)
	}
}

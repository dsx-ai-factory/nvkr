// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestImageLocationBinding(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, source, want string
		locations          map[string]string
		code               e.Code
	}{
		{"explicit key", "mirror.invalid/compiler@" + digest, "ghcr.io/example/compiler@" + digest, map[string]string{"mirror.invalid/compiler": "ghcr.io/example/compiler"}, ""},
		{"ordinary source", "docker.io/library/golang@" + digest, "ghcr.io/example/compiler@" + digest, map[string]string{"docker.io/library/golang": "ghcr.io/example/compiler"}, ""},
		{"tagged source", "source.example/compiler:1.0@" + digest, "ghcr.io/example/compiler@" + digest, map[string]string{"source.example/compiler": "ghcr.io/example/compiler"}, ""},
		{"tagged port source", "source.example:5000/compiler:1.0@" + digest, "ghcr.io/example/compiler@" + digest, map[string]string{"source.example:5000/compiler": "ghcr.io/example/compiler"}, ""},
		{"port source", "source.example:5000/compiler@" + digest, "ghcr.io/example/compiler@" + digest, map[string]string{"source.example:5000/compiler": "ghcr.io/example/compiler"}, ""},
		{"tagged short source", "golang:1.27@" + digest, "ghcr.io/example/compiler@" + digest, map[string]string{"golang": "ghcr.io/example/compiler"}, ""},
		{"unbound tagged source", "golang:1.27@" + digest, "golang:1.27@" + digest, nil, ""},
		{"unresolved key", "mirror.invalid/compiler@" + digest, "", nil, e.CodeEngineCapability},
		{"legacy unresolved", "nvkr-toolchain-renamed@" + digest, "", nil, e.CodeEngineCapability},
		{"legacy explicit", "nvkr-toolchain-renamed@" + digest, "ghcr.io/example/compiler@" + digest, map[string]string{"nvkr-toolchain-renamed": "ghcr.io/example/compiler"}, ""},
		{"measured local", digest, digest, nil, ""},
		{"public", "golang@" + digest, "golang@" + digest, nil, ""},
		{"unpinned", "golang:latest", "", nil, e.CodeUnpinnedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := Runtime{ImageLocations: tc.locations}
			if err := runtime.validate(); err != nil {
				t.Fatal(err)
			}
			got, err := runtime.ResolveImageLocation(tc.source)
			if tc.code != "" {
				if !toolchainErrorCode(err, tc.code) {
					t.Fatalf("want %s: %v", tc.code, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("%q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestImageLocationDeclarationRefusals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		locations map[string]string
	}{
		{"ambiguous aliases", map[string]string{"golang": "ghcr.io/example/go", "docker.io/library/golang": "ghcr.io/example/other"}},
		{"tagged source", map[string]string{"golang:latest": "ghcr.io/example/go"}},
		{"unresolved destination", map[string]string{"golang": "mirror.invalid/go"}},
		{"unqualified destination", map[string]string{"golang": "example/go"}},
		{"traversal", map[string]string{"golang": "ghcr.io/example/../go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := (Runtime{ImageLocations: tc.locations}).validate(); !toolchainErrorCode(err, e.CodeInvalidCatalog) {
				t.Fatalf("wrong refusal: %v", err)
			}
		})
	}
	if err := (Runtime{ToolchainRegistry: "ghcr.io/example"}).validate(); !toolchainErrorCode(err, e.CodeEngineCapability) {
		t.Fatalf("legacy registry admitted: %v", err)
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestReleaseInventoryAdmissionBeforeManifestIO(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		code  e.Code
	}{
		{"tag", []string{"--reference", "registry.example.test/releases:latest"}, e.CodeUnverifiedRef},
		{"platform", []string{"--platform", "windows/amd64"}, e.CodeUsage},
		{"qualifier policy", []string{"--qualification-policy-digest", "untrusted"}, e.CodeTrustPolicyMissing},
		{"timeout", []string{"--timeout", "0s"}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest := "sha256:" + strings.Repeat("a", 64)
			args := []string{"nvkr", "release", "export-inventory", "--reference", "registry.example.test/releases@" + digest, "--manifest", "/must-not-open",
				"--identity", "publisher", "--issuer", "issuer", "--qualifier-identity", "qualifier", "--qualification-policy-digest", digest, "--cosign", "/must-not-execute", "--cosign-digest", digest}
			args = append(args, tc.flags...)
			app := newApp()
			var out bytes.Buffer
			app.Writer = &out
			app.ErrWriter = &out
			err := app.RunContext(t.Context(), args)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code || out.Len() != 0 {
				t.Fatalf("admission: err=%v output=%q", err, out.String())
			}
		})
	}
}

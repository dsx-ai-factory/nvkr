// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func exactInputDescriptionFixture() ExactInputDescription {
	digest := "sha256:" + strings.Repeat("a", 64)
	return ExactInputDescription{ReleaseSubject: "registry.example.test/nvke/release@" + digest, PolicyDigest: digest, DefinitionDigest: digest, EnvironmentDigest: digest, PlanDigest: digest, ContentSubject: digest}
}
func TestExactInputDescriptionValidateCompleteShape(t *testing.T) {
	// A nonexistent repository is valid descriptive shape; this does not perform
	// registry work or produce a receipt or verified-publication capability.
	if err := exactInputDescriptionFixture().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct {
		name string
		set  func(*ExactInputDescription, string)
	}{
		{"policy", func(d *ExactInputDescription, v string) { d.PolicyDigest = v }},
		{"definition", func(d *ExactInputDescription, v string) { d.DefinitionDigest = v }},
		{"environment", func(d *ExactInputDescription, v string) { d.EnvironmentDigest = v }},
		{"plan", func(d *ExactInputDescription, v string) { d.PlanDigest = v }},
		{"content subject", func(d *ExactInputDescription, v string) { d.ContentSubject = v }},
	} {
		for _, tc := range []struct{ name, value string }{{"blank", ""}, {"invalid", "sha256:bad"}, {"uppercase", "sha256:" + strings.Repeat("A", 64)}, {"wrong family", "sha512:" + strings.Repeat("a", 128)}} {
			t.Run(field.name+"/"+tc.name, func(t *testing.T) {
				d := exactInputDescriptionFixture()
				field.set(&d, tc.value)
				assertCode(t, d.Validate(), e.CodeVerifyFailed)
			})
		}
	}
}
func TestExactInputDescriptionRejectsNoncanonicalReleaseReference(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, reference string
		code            e.Code
	}{
		{"blank", "", e.CodeUnverifiedRef},
		{"tag", "registry.example.test/nvke/release:latest", e.CodeUnverifiedRef},
		{"scheme", "https://registry.example.test/nvke/release@" + digest, e.CodeVerifyFailed},
		{"implicit registry", "nvke/release@" + digest, e.CodeUnverifiedRef},
		{"trailing whitespace", "registry.example.test/nvke/release@" + digest + " ", e.CodeUnverifiedRef},
		{"invalid digest", "registry.example.test/nvke/release@sha256:bad", e.CodeUnverifiedRef},
		{"terminal control", "registry.example.test/nvke/\x1brelease@" + digest, e.CodeUnverifiedRef},
		{"newline", "registry.example.test/nvke/release@" + digest + "\n", e.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := exactInputDescriptionFixture()
			d.ReleaseSubject = tc.reference
			assertCode(t, d.Validate(), tc.code)
		})
	}
}

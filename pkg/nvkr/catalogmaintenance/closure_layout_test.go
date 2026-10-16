// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"path"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
)

// The unnamed-bundle refusal finds committed bundles with its own glob. Every
// manifest layout an owner issues (default, development, named and qualification)
// must match it, or a future owner layout would escape the closure unnoticed.
// The layouts are rendered offline from the local fixture.
func TestGeneratedBundleGlobMatchesEveryOwnerLayout(t *testing.T) {
	cfg, _ := maintenanceFixture(t)
	for _, tc := range []struct{ name, extra string }{
		{"default", ""},
		{"development", "githubDevelopment: true\n"},
		{"named", "githubBundle: named\n"},
		{"named runtime", "githubBundle: runtime\ngithubRuntimePlan: true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			environment := "environment-" + path.Base(tc.name) + ".yaml"
			writeFixture(t, environment, append(readFixture(t, cfg.Environment), tc.extra...))
			plan, err := lifecycle.CompileFiles(t.Context(), cfg.Definition, environment, "test (deadbeef)")
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := factory.Render(t.Context(), plan, factory.EngineGitHub)
			if err != nil {
				t.Fatal(err)
			}
			if matched, err := path.Match(generatedBundleGlob, bundle.ManifestPath); err != nil || !matched {
				t.Fatalf("%s manifest %s escapes %s", tc.name, bundle.ManifestPath, generatedBundleGlob)
			}
			if matched, err := path.Match(committedPlanGlob, bundle.PlanPath); err != nil || !matched {
				t.Fatalf("%s Plan %s escapes %s", tc.name, bundle.PlanPath, committedPlanGlob)
			}
		})
	}
	if matched, err := path.Match(generatedBundleGlob, qualification.QualificationManifestPath); err != nil || !matched {
		t.Fatalf("qualification manifest %s escapes %s", qualification.QualificationManifestPath, generatedBundleGlob)
	}
}

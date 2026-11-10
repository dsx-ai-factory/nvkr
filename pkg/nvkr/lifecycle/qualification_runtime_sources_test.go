// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestQualificationReplaysFrozenCompositeSources(t *testing.T) {
	root, handoff, original := qualificationHandoffFixture(t)
	composite := definition.CompositeDefinition{APIVersion: definition.CompositeAPIVersion, Kind: definition.CompositeKind, Metadata: definition.Metadata{Name: "frozen-product"}}
	for _, name := range []string{"alpha", "beta"} {
		member := original.Definition.Clone()
		member.Metadata.Name, member.Outputs[0].Name = name, name
		digest, err := member.Digest()
		if err != nil {
			t.Fatal(err)
		}
		writeQualificationHandoffFile(t, filepath.Join(root, name+".json"), qualificationHandoffJSON(t, member))
		composite.Members = append(composite.Members, definition.CompositeMember{Name: name, Definition: name + ".json", Digest: digest})
	}
	writeQualificationHandoffFile(t, filepath.Join(root, "composite.json"), qualificationHandoffJSON(t, composite))
	environment := original.Environment.Clone()
	environment.GitHubBundle, environment.GitHubRuntimePlan = "frozen-product", true
	const repository = "https://github.com/kubernetes/kubernetes"
	environment.GitHubRuntimeSources = []definition.GitHubRuntimeSource{{Repository: repository, Ref: "refs/heads/main"}}
	writeQualificationHandoffFile(t, filepath.Join(root, "environment.json"), qualificationHandoffJSON(t, environment))
	baseline, inputs, err := factory.CaptureCompileFiles(t.Context(), root, "composite.json", "environment.json", original.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	local := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+local+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", repository)
	git := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", local}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "--template=", "-q", "-b", "main")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "qualification source")
	source, err := catalog.FreezeSourceHead(t.Context(), repository, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	inputs.Sources = []catalog.SourceSelection{source}
	frozen, err := factory.CompileSnapshot(t.Context(), inputs, original.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	handoff.Plan, handoff.Inputs = qualificationHandoffJSON(t, frozen), *frozen.RuntimeInputs
	git("branch", "-m", "retained-source")
	for _, tc := range []struct {
		name string
		edit func(*QualificationHandoff)
		code e.Code
	}{
		{"complete frozen composite", func(*QualificationHandoff) {}, ""},
		{"unfrozen baseline", func(h *QualificationHandoff) { h.Plan = qualificationHandoffJSON(t, baseline); h.Inputs.Sources = nil }, e.CodeUnpinnedSource},
		{"missing selection", func(h *QualificationHandoff) { h.Inputs.Sources = nil }, e.CodePipelineDrift},
		{"substituted tree", func(h *QualificationHandoff) { h.Inputs.Sources[0].TreeHash = strings.Repeat("a", 40) }, e.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := handoff
			copy.Inputs.Sources = append([]catalog.SourceSelection(nil), handoff.Inputs.Sources...)
			tc.edit(&copy)
			_, plan, err := prepareQualificationHandoff(t.Context(), qualificationHandoffJSON(t, copy), copy.Source)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if plan != nil {
					t.Fatal("failed replay returned Plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Composition.Equal(frozen.Composition) || len(plan.LifecycleOutputs) != 2 {
				t.Fatal("replay lost complete composition")
			}
		})
	}
}

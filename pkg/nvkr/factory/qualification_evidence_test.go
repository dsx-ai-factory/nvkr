// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func qualificationEvidenceFixture(t *testing.T, perOutput bool) (*Plan, string, map[string]Artifact, map[string][]byte) {
	t.Helper()
	plan := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
	if perOutput {
		plan = referenceTestPlan(t)
	}
	workspace := filepath.Join(t.TempDir(), "restored")
	_, artifacts := planIndex(plan)
	for id, artifact := range artifacts {
		artifact.Path = filepath.Join(workspace, strings.TrimPrefix(artifact.Path, ".nvkr/factory/"))
		artifacts[id] = artifact
	}
	for key, value := range map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com",
		"GITHUB_REPOSITORY": "example/nvkr", "GITHUB_WORKFLOW_REF": "example/nvkr/.github/workflows/nvkr-factory.yml@refs/heads/main",
		"GITHUB_RUN_ID": "12345", "GITHUB_RUN_ATTEMPT": "1"} {
		t.Setenv(key, value)
	}
	routes := []string{"release"}
	if perOutput {
		for _, output := range plan.LifecycleOutputs {
			routes = append(routes, outputPublicationRoute(output.Name))
		}
	}
	saved := map[string][]byte{}
	for index, route := range routes {
		// Model valid M2 reuse: transport must retain every original invocation,
		// leaving exact-attempt qualification refusal to the consuming gate.
		t.Setenv("GITHUB_RUN_ATTEMPT", fmt.Sprint(index+1))
		digest := "sha256:" + strings.Repeat(fmt.Sprintf("%x", index+1), 64)
		writeReferenceSigningFixture(t, plan, artifacts, route, digest)
		raw, err := os.ReadFile(artifacts["predicate/"+route].Path)
		if err != nil {
			t.Fatal(err)
		}
		saved[plan.Targets[route].Ref+"@"+digest] = raw
	}
	return plan, workspace, artifacts, saved
}

func TestCollectQualificationEvidencePreservesEveryOriginalPredicate(t *testing.T) {
	for _, perOutput := range []bool{false, true} {
		t.Run(fmt.Sprint(perOutput), func(t *testing.T) {
			plan, workspace, _, saved := qualificationEvidenceFixture(t, perOutput)
			reference, predicates, err := CollectQualificationEvidence(t.Context(), workspace, plan)
			if err != nil {
				t.Fatal(err)
			}
			if reference != plan.Targets["release"].Ref+"@sha256:"+strings.Repeat("1", 64) || len(predicates) != len(saved) {
				t.Fatalf("incomplete handoff: %s, %d predicates", reference, len(predicates))
			}
			previous := ""
			for _, predicate := range predicates {
				if predicate.Reference <= previous || !bytes.Equal(predicate.Predicate, saved[predicate.Reference]) {
					t.Fatalf("unsorted, substituted or relabeled evidence for %s", predicate.Reference)
				}
				previous = predicate.Reference
			}
		})
	}
}

func TestCollectQualificationEvidenceRejectsIncompleteOrSubstitutedRecords(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, map[string]Artifact, string)
		code e.Code
	}{
		{"missing output predicate", func(t *testing.T, artifacts map[string]Artifact, route string) {
			name := artifacts["predicate/"+route].Path
			if err := os.Rename(name, name+".missing"); err != nil {
				t.Fatal(err)
			}
		}, e.CodePipelineDrift},
		{"changed exact bytes", func(t *testing.T, artifacts map[string]Artifact, route string) {
			name := artifacts["predicate/"+route].Path
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, append(raw, ' '), 0o600); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
		{"substituted publication", func(t *testing.T, artifacts map[string]Artifact, route string) {
			if err := os.WriteFile(artifacts["publication/"+route].Path, []byte("ghcr.io/other/release@sha256:"+strings.Repeat("1", 64)), 0o600); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
		{"missing signing record", func(t *testing.T, artifacts map[string]Artifact, route string) {
			name := artifacts["signature/"+route].Path
			if err := os.Rename(name, name+".missing"); err != nil {
				t.Fatal(err)
			}
		}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, workspace, artifacts, _ := qualificationEvidenceFixture(t, true)
			tc.edit(t, artifacts, outputPublicationRoute(plan.LifecycleOutputs[0].Name))
			reference, predicates, err := CollectQualificationEvidence(t.Context(), workspace, plan)
			assertFactoryErrorCode(t, err, tc.code)
			if reference != "" || predicates != nil {
				t.Fatal("failed export returned partial evidence")
			}
		})
	}
}

func TestCollectQualificationEvidenceAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"nil", nil, e.CodeUsage}, {"canceled", canceled, e.CodeTimeout}, {"missing plan", t.Context(), e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := CollectQualificationEvidence(tc.ctx, "/must-not-open", nil)
			assertFactoryErrorCode(t, err, tc.code)
		})
	}
}

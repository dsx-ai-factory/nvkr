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

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func qualificationEvidenceFixture(t *testing.T, perOutput bool) (*Plan, string, map[string]Artifact, map[string][]byte) {
	t.Helper()
	if perOutput {
		return qualificationOutputEvidenceFixture(t)
	}
	plan := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
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

// Reuse the existing bounded-metadata producer fixture. This tests transport
// mapping, not a full qualification gate or real signatures. Different producer
// attempts prove that collection preserves original predicates without relabeling.
func qualificationOutputEvidenceFixture(t *testing.T) (*Plan, string, map[string]Artifact, map[string][]byte) {
	t.Helper()
	plan, artifacts, signer := referenceMetadataFixture(t, "1", "2")
	t.Setenv("GITHUB_RUN_ATTEMPT", "3")
	if err := executeReferenceRelease(t.Context(), plan, planNode(plan, "package/release"), artifacts, referenceSignerFactory(signer)); err != nil {
		t.Fatal(err)
	}
	metadata := artifacts["metadata/release"].Path
	if err := os.MkdirAll(metadata, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(metadata, os.DirFS(artifacts["package/release"].Path)); err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.InspectMetadata(t.Context(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	writeReferenceSigningFixture(t, plan, artifacts, "release", inspection.RootDigest)
	reference := plan.Targets["release"].Ref + "@" + inspection.RootDigest
	references, err := delivery.OutputReferences(reference, inspection)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(artifacts["predicate/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	saved := map[string][]byte{reference: raw}
	for _, output := range plan.LifecycleOutputs {
		raw, err := os.ReadFile(artifacts["predicate/"+outputPublicationRoute(output.Name)].Path)
		if err != nil {
			t.Fatal(err)
		}
		saved[references[output.Name]] = raw
	}
	// The restored finalizer has control metadata only. No package payload is
	// accessible at its planned path while the collector derives the subjects.
	packages := filepath.Dir(artifacts["package/release"].Path)
	if err := os.Rename(packages, packages+".not-restored"); err != nil {
		t.Fatal(err)
	}
	return plan, ".nvkr/factory", artifacts, saved
}

func TestCollectQualificationEvidencePreservesEveryOriginalPredicate(t *testing.T) {
	for _, perOutput := range []bool{false, true} {
		t.Run(fmt.Sprint(perOutput), func(t *testing.T) {
			plan, workspace, artifacts, saved := qualificationEvidenceFixture(t, perOutput)
			if !perOutput {
				// A legacy publication may complete successfully but cannot supply
				// the complete original root/output qualification evidence. Refuse
				// before opening even an absent workspace, without partial records.
				reference, predicates, err := CollectQualificationEvidence(t.Context(), filepath.Join(t.TempDir(), "missing"), plan)
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if reference != "" || predicates != nil {
					t.Fatal("unsupported handoff returned partial evidence")
				}
				return
			}
			reference, predicates, err := CollectQualificationEvidence(t.Context(), workspace, plan)
			if err != nil {
				t.Fatal(err)
			}
			root, err := readPublicationSubject(t.Context(), artifacts["publication/release"].Path)
			if err != nil {
				t.Fatal(err)
			}
			if reference != root.Ref || len(predicates) != len(saved) {
				t.Fatalf("incomplete handoff: %s, %d predicates", reference, len(predicates))
			}
			for _, output := range plan.LifecycleOutputs {
				metadata, err := readPublicationSubject(t.Context(), artifacts["publication/"+outputPublicationRoute(output.Name)].Path)
				if err != nil {
					t.Fatal(err)
				}
				for _, predicate := range predicates {
					if predicate.Reference == metadata.Ref {
						t.Fatalf("handoff used metadata root instead of %s payload subject", output.Name)
					}
				}
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
		edit func(*testing.T, *Plan, map[string]Artifact, string)
		code e.Code
	}{
		{"missing release metadata", func(t *testing.T, _ *Plan, artifacts map[string]Artifact, _ string) {
			name := artifacts["metadata/release"].Path
			if err := os.Rename(name, name+".missing"); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
		{"root metadata differs from record", func(t *testing.T, plan *Plan, artifacts map[string]Artifact, _ string) {
			writeReferenceSigningFixture(t, plan, artifacts, "release", "sha256:"+strings.Repeat("f", 64))
		}, e.CodeVerifyFailed},
		{"output route differs from root metadata", func(t *testing.T, plan *Plan, artifacts map[string]Artifact, route string) {
			writeReferenceSigningFixture(t, plan, artifacts, route, "sha256:"+strings.Repeat("e", 64))
		}, e.CodeVerifyFailed},
		{"output original signing attempt differs", func(t *testing.T, plan *Plan, artifacts map[string]Artifact, route string) {
			subject, err := readPublicationSubject(t.Context(), artifacts["publication/"+route].Path)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("GITHUB_RUN_ATTEMPT", "4")
			writeReferenceSigningFixture(t, plan, artifacts, route, subject.Digest)
		}, e.CodeVerifyFailed},
		{"missing output predicate", func(t *testing.T, _ *Plan, artifacts map[string]Artifact, route string) {
			name := artifacts["predicate/"+route].Path
			if err := os.Rename(name, name+".missing"); err != nil {
				t.Fatal(err)
			}
		}, e.CodePipelineDrift},
		{"changed exact bytes", func(t *testing.T, _ *Plan, artifacts map[string]Artifact, route string) {
			name := artifacts["predicate/"+route].Path
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, append(raw, ' '), 0o600); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
		{"substituted publication", func(t *testing.T, _ *Plan, artifacts map[string]Artifact, route string) {
			if err := os.WriteFile(artifacts["publication/"+route].Path, []byte("ghcr.io/other/release@sha256:"+strings.Repeat("1", 64)), 0o600); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
		{"missing signing record", func(t *testing.T, _ *Plan, artifacts map[string]Artifact, route string) {
			name := artifacts["signature/"+route].Path
			if err := os.Rename(name, name+".missing"); err != nil {
				t.Fatal(err)
			}
		}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, workspace, artifacts, _ := qualificationEvidenceFixture(t, true)
			tc.edit(t, plan, artifacts, outputPublicationRoute(plan.LifecycleOutputs[0].Name))
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

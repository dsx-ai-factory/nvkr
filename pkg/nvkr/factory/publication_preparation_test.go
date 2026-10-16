// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func TestPublicationRejectsPrivacyBeforeReadingOriginalPreparation(t *testing.T) {
	plan, original, _ := referenceMetadataFixture(t)
	route := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
	node := planNode(plan, NodePublish+"/"+route)
	t.Setenv("NVKR_REGISTRY_USER", "fixture-user")
	t.Setenv("NVKR_CREDENTIAL", "fixture-token")
	for _, tc := range []struct {
		name, repositoryToken string
		wantCalls             int
	}{
		{"missing repository credential", "", 0},
		{"private destination refused", "fixture-repository-token", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(githubRepositoryTokenEnv, tc.repositoryToken)
			artifacts := maps.Clone(original)
			payload := artifacts["package/"+outputPublicationPackage(plan.LifecycleOutputs[0].Name)]
			payload.Path = filepath.Join(t.TempDir(), "must-not-be-opened")
			artifacts[payload.ID] = payload
			for _, id := range node.Outputs {
				artifact := artifacts[id]
				artifact.Path = filepath.Join(t.TempDir(), "must-not-be-written")
				artifacts[id] = artifact
			}
			calls := 0
			var log bytes.Buffer
			err := executePublishWithAdmission(t.Context(), plan, node, artifacts, &log,
				func(context.Context, string, []string, cistore.Options) error {
					calls++
					return e.New(e.CodeTrustPolicyMissing, "fixture private admission denied")
				})
			assertFactoryErrorCode(t, err, e.CodeTrustPolicyMissing)
			if calls != tc.wantCalls || log.Len() != 0 {
				t.Fatalf("privacy calls = %d, log = %q", calls, log.String())
			}
			for _, id := range node.Outputs {
				if _, err := os.Lstat(artifacts[id].Path); !os.IsNotExist(err) {
					t.Fatalf("privacy refusal emitted output: %v", err)
				}
			}
		})
	}
}

func TestPublicationCompletionRejectsChangedPrivacyRootBeforePayloadOrVerifier(t *testing.T) {
	plan, artifacts, _ := referenceMetadataFixture(t)
	route := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
	node := planNode(plan, NodePublish+"/"+route)
	payload := artifacts["package/"+outputPublicationPackage(plan.LifecycleOutputs[0].Name)]
	payload.Path = filepath.Join(t.TempDir(), "must-not-be-opened")
	artifacts[payload.ID] = payload
	called := false
	_, err := completePublicationInput(t.Context(), plan, node, artifacts,
		func(context.Context, *Plan, Node, map[string]Artifact) (sign.OCISigner, error) {
			called = true
			return nil, nil
		}, "sha256:"+strings.Repeat("f", 64))
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
	if called || !strings.Contains(err.Error(), "changed after publication privacy admission") {
		t.Fatalf("changed controls reached payload or verifier: called=%t, %v", called, err)
	}
}

func TestPublicationRejectsControlsSubstitutedDuringPrivacyAdmission(t *testing.T) {
	plan, artifacts, _ := referenceMetadataFixture(t)
	first := outputPublicationRoute(plan.LifecycleOutputs[0].Name)
	second := outputPublicationRoute(plan.LifecycleOutputs[1].Name)
	node := planNode(plan, NodePublish+"/"+first)
	t.Setenv("NVKR_REGISTRY_USER", "fixture-user")
	t.Setenv("NVKR_CREDENTIAL", "fixture-token")
	t.Setenv(githubRepositoryTokenEnv, "fixture-repository-token")
	for _, id := range node.Outputs {
		artifact := artifacts[id]
		artifact.Path = filepath.Join(t.TempDir(), "must-not-be-written")
		artifacts[id] = artifact
	}
	calls := 0
	var log bytes.Buffer
	err := executePublishWithAdmission(t.Context(), plan, node, artifacts, &log,
		func(context.Context, string, []string, cistore.Options) error {
			calls++
			if calls == 1 {
				original := artifacts[attestedEvidenceMetadataID(first)].Path
				if err := os.Rename(original, filepath.Join(t.TempDir(), "original-controls")); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(artifacts[attestedEvidenceMetadataID(second)].Path, original); err != nil {
					t.Fatal(err)
				}
			}
			return nil
		})
	assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
	if !strings.Contains(err.Error(), "changed between scope and publication privacy admission") || log.Len() != 0 {
		t.Fatalf("metadata substitution escaped its admission boundary: %v, %q", err, log.String())
	}
	for _, id := range node.Outputs {
		if _, err := os.Lstat(artifacts[id].Path); !os.IsNotExist(err) {
			t.Fatalf("substituted controls emitted output: %v", err)
		}
	}
}

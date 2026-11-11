// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"os"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestInternalToolEvidenceRejectsUnownedWorkBeforeWriting(t *testing.T) {
	_, plan := internalToolPlanFixture(t)
	digest, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	nodes, artifacts := planIndex(plan)
	for _, tc := range []struct {
		name  string
		owner *LocalRunResult
		code  e.Code
	}{
		{"absent", nil, e.CodeEngineCapability},
		{"foreign", &LocalRunResult{PlanDigest: "foreign"}, e.CodeVerifyFailed},
		{"closed", &LocalRunResult{PlanDigest: digest, diskClosed: true}, e.CodeGateUnmet},
		{"unproduced", &LocalRunResult{PlanDigest: digest}, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newOperationSession(plan, operationPlacement{owner: tc.owner}, io.Discard, io.Discard)
			for _, id := range []string{plan.InternalTools[0].ObservationArtifactID, plan.InternalTools[0].SBOMArtifactID} {
				artifact := artifacts[id]
				assertFactoryErrorCode(t, session.dispatch(t.Context(), plan, nodes[artifact.Producer]), tc.code)
				if _, err := os.Stat(artifact.Path); !os.IsNotExist(err) {
					t.Fatalf("unowned evidence wrote %s: %v", artifact.Path, err)
				}
			}
		})
	}
}

func TestInternalToolProductionRequiresOriginalOwnerBeforeWork(t *testing.T) {
	_, plan := internalToolPlanFixture(t)
	digest, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	var producer Node
	for _, node := range plan.Graph.Nodes {
		if node.BuildTarget == plan.InternalTools[0].BuildTarget && node.Kind == NodeBuild {
			producer = node
		}
	}
	if producer.ID == "" {
		t.Fatal("fixture lacks helper producer")
	}
	for _, tc := range []struct {
		name  string
		owner *LocalRunResult
		code  e.Code
	}{
		{"absent", nil, e.CodeEngineCapability},
		{"foreign Plan", &LocalRunResult{PlanDigest: "foreign"}, e.CodeVerifyFailed},
		{"closed", &LocalRunResult{PlanDigest: digest, diskClosed: true}, e.CodeGateUnmet},
		{"closing", &LocalRunResult{PlanDigest: digest, diskClosing: true}, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launch := func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
				t.Fatal("unowned tool reached execution")
				return nil, nil
			}
			err := executeProducer(t.Context(), plan, producer, "/must-not-run", 1, io.Discard, io.Discard, launch, tc.owner)
			assertFactoryErrorCode(t, err, tc.code)
		})
	}
}

func TestInternalToolOwnerRejectsSerializedDescriptions(t *testing.T) {
	_, plan := internalToolPlanFixture(t)
	digest, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	owner := &LocalRunResult{PlanDigest: digest}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	assertFactoryErrorCode(t, owner.retainInternalExecutable(t.Context(), plan, plan.InternalTools[0], &pipeline.ProducedExecutable{}), e.CodeVerifyFailed)
	if len(owner.internalExecutables) != 0 {
		t.Fatal("description populated original tool ownership")
	}
	owner.Nodes = []LocalNodeResult{{ID: "internal/inspector/sbom", Kind: NodeSBOM, Status: "ok"}}
	_, err = owner.borrowInternalExecutable(t.Context(), plan, "inspector")
	assertFactoryErrorCode(t, err, e.CodeGateUnmet)
}

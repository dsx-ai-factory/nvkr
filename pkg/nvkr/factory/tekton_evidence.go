// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

// TektonEvidenceTask is issued only inside an authenticated generated task.
// It borrows the runner's lifetime and cannot be constructed from provider JSON.
// The lifecycle owner applies the shared artifact and smoke gates; this owner
// authenticates scheduling, complete build outcomes and exact transported bytes.
type TektonEvidenceTask struct {
	runner *tektonRuntime
	closed atomic.Bool
}

func (task *TektonEvidenceTask) Require(ctx context.Context) error {
	if task == nil || task.runner == nil || task.closed.Load() {
		return e.New(e.CodeGateUnmet, "Tekton evidence task is not live")
	}
	receipt, err := task.runner.authenticate(ctx, task.runner.options.Stage)
	if err != nil {
		return err
	}
	if receipt.State() != "running" {
		return e.New(e.CodeGateUnmet, "Tekton evidence task is no longer active")
	}
	return nil
}

func (task *TektonEvidenceTask) Operation(ctx context.Context) (string, error) {
	if err := task.Require(ctx); err != nil {
		return "", err
	}
	return task.runner.options.Operation, nil
}

// Plan returns independently owned descriptive inputs, not the runner receipt.
func (task *TektonEvidenceTask) Plan(ctx context.Context) (*Plan, error) {
	if err := task.Require(ctx); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(task.runner.plan)
	if err != nil {
		return nil, err
	}
	var plan Plan
	if err := canonical.UnmarshalExact(raw, &plan); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "freezing Tekton evidence Plan")
	}
	return &plan, nil
}

func (task *TektonEvidenceTask) RunContext(ctx context.Context) (provenance.RunContext, error) {
	if err := task.Require(ctx); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return task.runner.plan.tektonExecution.Context(now, now)
}

func (task *TektonEvidenceTask) Identity(ctx context.Context) (stage, uid, account string, err error) {
	if err := task.Require(ctx); err != nil {
		return "", "", "", err
	}
	return task.runner.options.Stage, task.runner.options.TaskUID, task.runner.tasks[task.runner.options.Stage].account, nil
}

func (task *TektonEvidenceTask) CompletedTaskUID(ctx context.Context, stage string) (string, error) {
	if err := task.Require(ctx); err != nil {
		return "", err
	}
	if stage != tektonSmoke && stage != tektonQualify {
		return "", e.New(e.CodeEngineCapability, "unsupported Tekton evidence predecessor")
	}
	receipt, err := task.runner.authenticate(ctx, stage)
	if err != nil {
		return "", err
	}
	if receipt.State() != "success" {
		return "", e.New(e.CodeGateUnmet, "Tekton evidence predecessor did not succeed")
	}
	return receipt.TaskUID(), nil
}

// SigningKey reads only the admitted role's projected Secret. The caller owns
// and must clear the returned buffer; it never enters a Plan, carrier or result.
func (task *TektonEvidenceTask) SigningKey(ctx context.Context) ([]byte, error) {
	if err := task.Require(ctx); err != nil {
		return nil, err
	}
	if task.runner.options.Operation != "smoke" && task.runner.options.Operation != "qualify" {
		return nil, e.New(e.CodeEngineCapability, "Tekton finalization has no signing authority")
	}
	raw, err := readTektonControl(ctx, "/nvkr/keys/private.pem", 64<<10)
	if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "reading exact Tekton role key")
	}
	return raw, nil
}

func (task *TektonEvidenceTask) Cosign(ctx context.Context) (string, string, error) {
	if err := task.Require(ctx); err != nil {
		return "", "", err
	}
	_, artifacts := planIndex(task.runner.plan)
	tool, exists := artifacts["tool/"+task.runner.plan.Definition.CosignTool]
	if !exists {
		return "", "", e.New(e.CodeUnpinnedInput, "Tekton evidence omitted its exact cosign tool")
	}
	return filepath.Join("/work", tool.Path), toolExecutableDigest(task.runner.plan, task.runner.plan.Definition.CosignTool), nil
}

// EvidenceDirectory fixes both restored input and new output paths. It cannot
// redirect a read to another stage or a write into a shared carrier directory.
func (task *TektonEvidenceTask) EvidenceDirectory(ctx context.Context, role string) (string, error) {
	if err := task.Require(ctx); err != nil {
		return "", err
	}
	op := task.runner.options.Operation
	allowed := role == "smoke" || role == "qualification" && op != "smoke" || role == "result" && op == "finalize"
	if !allowed {
		return "", e.New(e.CodeEngineCapability, "Tekton evidence role is outside the admitted task")
	}
	return filepath.Join("/work/.nvkr/tekton", role), nil
}

func (task *TektonEvidenceTask) prepare(ctx context.Context) error {
	runner := task.runner
	_, artifacts := planIndex(runner.plan)
	for _, stage := range runner.projection.stages {
		receipt, err := runner.authenticate(ctx, stage.ID)
		if err != nil {
			return err
		}
		if receipt.State() != "success" {
			return e.New(e.CodeReleaseIncomplete, "Tekton build stage %q did not succeed", stage.ID)
		}
		var ids []string
		for _, id := range sortedKeys(runner.outputs(stage.ID)) {
			if finalizerArtifact(runner.plan, artifacts[id]) {
				ids = append(ids, id)
			}
		}
		if len(ids) != 0 {
			if err := runner.restore(ctx, stage.ID, ids); err != nil {
				return err
			}
		}
	}
	if runner.options.Operation != "smoke" {
		if err := runner.restore(ctx, tektonSmoke, []string{"smoke"}); err != nil {
			return err
		}
	}
	if runner.options.Operation == "finalize" {
		if err := runner.restore(ctx, tektonQualify, []string{"qualification"}); err != nil {
			return err
		}
	}
	return task.Require(ctx)
}

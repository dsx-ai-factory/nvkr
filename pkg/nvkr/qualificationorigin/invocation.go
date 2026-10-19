// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// Invocation is the current qualification workflow, authenticated independently
// of its successful source build. Production uses workflow_run; explicit
// development policy admits its separate manual-dispatch workflow.
type Invocation struct {
	description Description
	source      *SourceRun
	closed      *atomic.Bool
}

func (i *Invocation) Description() (Description, error) {
	if i == nil || i.closed == nil || i.closed.Load() {
		return Description{}, e.New(e.CodeProvenanceMissing, "qualification invocation is not authenticated")
	}
	if err := i.source.Require(); err != nil {
		return Description{}, err
	}
	return i.description, nil
}

func (i *Invocation) RequireSource(source Description) error {
	if _, err := i.Description(); err != nil {
		return err
	}
	actual, err := i.source.Description()
	if err != nil {
		return err
	}
	if actual != source {
		return e.New(e.CodeVerifyFailed, "qualification invocation belongs to a different producing Run")
	}
	return nil
}

func (i *Invocation) Close() error {
	if i != nil && i.closed != nil {
		i.closed.Store(true)
	}
	return nil
}

// AuthenticateInvocation requires the trusted GitHub runner environment and its
// exact workflow_run event file, then independently reads the current attempt
// from GitHub. A caller-provided success flag cannot admit signing. The committed
// qualification workflow is trusted code; the delivered artifacts never receive
// its environment, API credential, registry credential or OIDC permission.
func (g *GitHub) AuthenticateInvocation(ctx context.Context, source *SourceRun, workflow string) (*Invocation, error) {
	if err := admitContext(ctx); err != nil {
		return nil, err
	}
	description, err := source.Description()
	if err != nil {
		return nil, err
	}
	if description.Branch != "" {
		return g.authenticateDevelopmentInvocation(ctx, source, workflow)
	}
	request := SourceRequest{Repository: os.Getenv("GITHUB_REPOSITORY"), RunID: os.Getenv("GITHUB_RUN_ID"), Attempt: os.Getenv("GITHUB_RUN_ATTEMPT"), Commit: os.Getenv("GITHUB_SHA"), Workflow: workflow, DefaultBranch: description.DefaultBranch}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_SERVER_URL") != "https://github.com" ||
		os.Getenv("GITHUB_EVENT_NAME") != "workflow_run" || os.Getenv("GITHUB_REF") != "refs/heads/"+description.DefaultBranch ||
		request.Repository != description.Repository || request.RunID == description.RunID || workflow == description.Workflow ||
		os.Getenv("GITHUB_WORKFLOW_REF") != request.Repository+"/"+workflow+"@refs/heads/"+description.DefaultBranch {
		return nil, e.New(e.CodeTrustPolicyMissing, "qualification requires its independent default-branch workflow_run invocation")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	held, err := heldfile.OpenContext(ctx, os.Getenv("GITHUB_EVENT_PATH"), heldfile.Options{Label: "qualification workflow event", MaxBytes: maxResponseBytes})
	if err != nil {
		return nil, e.Wrap(e.CodeProvenanceMissing, err, "opening qualification source event")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxResponseBytes)
	if err != nil {
		return nil, e.Wrap(e.CodeProvenanceMissing, err, "reading qualification source event")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "invalid qualification source event")
	}
	var event struct {
		Action string      `json:"action"`
		Run    runResponse `json:"workflow_run"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding qualification source event")
	}
	if event.Action != "completed" || string(event.Run.ID) != description.RunID || string(event.Run.Attempt) != description.Attempt ||
		event.Run.HeadSHA != description.Commit || event.Run.HeadBranch != description.DefaultBranch || event.Run.Path != description.Workflow ||
		event.Run.Repository.FullName != description.Repository || event.Run.HeadRepository.FullName != description.Repository ||
		event.Run.Status != "completed" || event.Run.Conclusion != "success" || (event.Run.Event != "push" && event.Run.Event != "workflow_dispatch") {
		return nil, e.New(e.CodeVerifyFailed, "qualification event does not identify its exact authenticated successful source Run")
	}
	repo, run, err := g.lookup(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := matchRun(request, repo, run); err != nil {
		return nil, err
	}
	if run.Event != "workflow_run" || run.Status != "in_progress" || run.Conclusion != "" {
		return nil, e.New(e.CodeVerifyFailed, "qualification invocation is not an active workflow_run execution")
	}
	if err := admitContext(ctx); err != nil {
		return nil, err
	}
	if err := source.Require(); err != nil {
		return nil, err
	}
	return &Invocation{description: describe(request), source: source, closed: new(atomic.Bool)}, nil
}

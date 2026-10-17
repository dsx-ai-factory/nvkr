// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// AuthenticateTriggeredSource selects the source attempt only from the trusted
// runner's completed workflow_run event, then authenticates that exact attempt
// through GitHub under an independently selected policy. GITHUB_SHA identifies
// the qualifier, so it is never used as the source commit. This operation grants
// no invocation or signing authority; AuthenticateInvocation remains required.
func (g *GitHub) AuthenticateTriggeredSource(ctx context.Context, policy SourcePolicy) (*SourceRun, error) {
	if err := admitContext(ctx); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if policy.Development {
		return g.authenticateDispatchedDevelopmentSource(ctx, policy)
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("GITHUB_SERVER_URL") != "https://github.com" ||
		os.Getenv("GITHUB_EVENT_NAME") != "workflow_run" || os.Getenv("GITHUB_REPOSITORY") != policy.Repository ||
		os.Getenv("GITHUB_REF") != "refs/heads/"+policy.DefaultBranch {
		return nil, e.New(e.CodeTrustPolicyMissing, "qualification source discovery requires the approved default-branch workflow_run environment")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	held, err := heldfile.OpenContext(ctx, os.Getenv("GITHUB_EVENT_PATH"), heldfile.Options{Label: "qualification source event", MaxBytes: maxResponseBytes})
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
	request := SourceRequest{Repository: policy.Repository, RunID: string(event.Run.ID), Attempt: string(event.Run.Attempt),
		Commit: event.Run.HeadSHA, Workflow: policy.Workflow, DefaultBranch: policy.DefaultBranch}
	if err := request.Validate(); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "qualification source event has no exact Run attempt and commit")
	}
	// The event's nested repository objects need not contain default_branch.
	// Their numeric identities must agree. AuthenticateSource separately checks
	// the provider's repository identity and actual default branch.
	repository := repositoryResponse{FullName: policy.Repository, DefaultBranch: policy.DefaultBranch, ID: event.Run.Repository.ID}
	if err := matchRun(request, repository, event.Run); err != nil {
		return nil, err
	}
	if event.Action != "completed" || event.Run.Status != "completed" || event.Run.Conclusion != "success" ||
		(event.Run.Event != "push" && event.Run.Event != "workflow_dispatch") {
		return nil, e.New(e.CodeVerifyFailed, "qualification source event is not a successful approved build")
	}
	source, err := g.AuthenticateSource(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := admitContext(ctx); err != nil {
		_ = source.Close()
		return nil, err
	}
	return source, nil
}

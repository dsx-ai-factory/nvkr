// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"net/url"
	"regexp"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/tekton"
)

// Validate checks the description's strict identity syntax; it authenticates no
// provider fact and cannot issue a SourceRun or Invocation receipt.
func (d Description) Validate() error {
	if d.Provider == "tekton" {
		return d.validateTekton()
	}
	if d.Provider != "" || d.Task != "" || d.TaskUID != "" {
		return e.New(e.CodeVerifyFailed, "unsupported execution provider description")
	}
	r := SourceRequest{Branch: d.Branch, Repository: d.Repository, RunID: d.RunID, Attempt: d.Attempt, Commit: d.Commit, Workflow: d.Workflow, DefaultBranch: d.DefaultBranch}
	if err := r.Validate(); err != nil {
		return err
	}
	if describe(r) != d {
		return e.New(e.CodeVerifyFailed, "workflow description has inconsistent identity projections")
	}
	return nil
}

func (d Description) validateTekton() error {
	if d.Branch != "" || d.Repository != "" || d.RunID != "" || d.Attempt != "" || d.Commit != "" || d.Workflow != "" || d.DefaultBranch != "" || d.Ref != "" {
		return e.New(e.CodeVerifyFailed, "Tekton identity cannot contain hosted workflow facts")
	}
	if err := tekton.ValidateInvocation(d.InvocationID); err != nil {
		return err
	}
	u, _ := url.Parse(d.InvocationID)
	namespace := strings.Split(u.Path, "/")[5]
	account := "nvkr-publisher"
	if d.Task != "" {
		if d.Task != "nvkr-smoke" && d.Task != "nvkr-qualify" && d.Task != "nvkr-finalize" {
			return e.New(e.CodeVerifyFailed, "unknown Tekton evidence task")
		}
		if !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(d.TaskUID) {
			return e.New(e.CodeVerifyFailed, "Tekton evidence task omitted its exact UID")
		}
		switch d.Task {
		case "nvkr-smoke":
			account = "nvkr-smoke"
		case "nvkr-qualify":
			account = "nvkr-qualifier"
		case "nvkr-finalize":
			account = "nvkr-finalizer"
		}
	} else if d.TaskUID != "" {
		return e.New(e.CodeVerifyFailed, "Tekton source cannot invent a task UID")
	}
	if d.BuilderID != u.Scheme+"://"+u.Host+"/api/v1/namespaces/"+namespace+"/serviceaccounts/"+account {
		return e.New(e.CodeVerifyFailed, "Tekton builder differs from its exact cluster role")
	}
	return nil
}

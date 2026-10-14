// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// Validate checks the description's strict identity syntax; it authenticates no
// provider fact and cannot issue a SourceRun or Invocation receipt.
func (d Description) Validate() error {
	r := SourceRequest{Repository: d.Repository, RunID: d.RunID, Attempt: d.Attempt, Commit: d.Commit, Workflow: d.Workflow, DefaultBranch: d.DefaultBranch}
	if err := r.Validate(); err != nil {
		return err
	}
	if describe(r) != d {
		return e.New(e.CodeVerifyFailed, "workflow description has inconsistent identity projections")
	}
	return nil
}

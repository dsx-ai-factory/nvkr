// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// localRunResult is a detached, safe presentation record, never an executable
// Run or a substitute for the immutable lifecycle record.
type localRunResult struct {
	SchemaVersion     string            `json:"schemaVersion"`
	Authority         string            `json:"authority"`
	Attempt           string            `json:"attempt,omitempty"`
	PlanDigest        string            `json:"planDigest,omitempty"`
	Lane              string            `json:"lane,omitempty"`
	Status            string            `json:"status"`
	FailureCode       string            `json:"failureCode,omitempty"`
	ExitCode          int               `json:"exitCode"`
	RequiredOutputs   []runResultOutput `json:"requiredOutputs"`
	ObservedOutputs   []runResultOutput `json:"observedOutputs"`
	RecordAvailable   bool              `json:"recordAvailable"`
	CleanupIncomplete bool              `json:"cleanupIncomplete"`
}

type runResultOutput struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Digest string `json:"digest,omitempty"`
}

func writeLocalRunJSON(out io.Writer, prepared *lifecycle.PreparedRun, run *lifecycle.Run, err error, dryRun, cleanupIncomplete bool) error {
	result := localRunResult{SchemaVersion: "nvkr.dev/local-run-result/v1", Authority: "description-only", Status: "not-started",
		ExitCode: factory.GitHubStepExitCode(err), FailureCode: string(runErrorCode(err)),
		RequiredOutputs: []runResultOutput{}, ObservedOutputs: []runResultOutput{}, CleanupIncomplete: cleanupIncomplete}
	allowed := make(map[string]runResultOutput)
	if prepared != nil {
		result.Status = "unavailable"
		frozen := prepared.Description()
		result.Attempt, result.PlanDigest = frozen.Attempt, frozen.PlanDigest
		for _, output := range frozen.Expected {
			value := runResultOutput{Name: output.Name, Kind: output.Kind}
			result.RequiredOutputs = append(result.RequiredOutputs, value)
			allowed[output.Name] = value
		}
	}
	if run != nil {
		// Projection takes membership only from frozen names and kinds. Private
		// paths, raw failure messages and provider diagnostics are never copied.
		switch run.Status {
		case lifecycle.RunPending, lifecycle.RunFailed, lifecycle.RunComplete:
			result.Status = run.Status
		}
		switch run.Lane {
		case lifecycle.LanePipeline, lifecycle.LaneUnqualified, factory.TierLocalExecution.String():
			result.Lane = run.Lane
		default:
			return e.New(e.CodeInternal, "local Run result has an unsupported description lane")
		}
		result.RecordAvailable = !dryRun
		for _, observed := range run.Observed {
			value, ok := allowed[observed.Name]
			if !ok || value.Kind != observed.Kind || !canonical.IsDigest(observed.Digest) {
				return e.New(e.CodeInternal, "local Run result has an inconsistent output description")
			}
			value.Digest = observed.Digest
			result.ObservedOutputs = append(result.ObservedOutputs, value)
		}
	}
	sort.Slice(result.RequiredOutputs, func(i, j int) bool { return result.RequiredOutputs[i].Name < result.RequiredOutputs[j].Name })
	sort.Slice(result.ObservedOutputs, func(i, j int) bool { return result.ObservedOutputs[i].Name < result.ObservedOutputs[j].Name })
	raw, marshalErr := canonical.Marshal(result)
	if marshalErr != nil {
		return e.Wrap(e.CodeInternal, marshalErr, "serializing descriptive Run result")
	}
	return checkedRunWrite(out, append(raw, '\n'))
}

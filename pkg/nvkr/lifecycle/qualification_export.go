// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

// FinalizeGeneratedWithHandoff is the generated producer's finalization path.
// The saved handoff is descriptive transport; only a later successful exact
// provider attempt plus independent artifact verification can grant authority.
func FinalizeGeneratedWithHandoff(ctx context.Context, definitionPath, environmentPath, planPath, generatorVersion, attempt, workspace, outputPath, handoffPath string) (*Run, error) {
	if handoffPath == "" {
		return nil, e.New(e.CodeUsage, "hosted qualification handoff requires an output path")
	}
	return finalizeGenerated(ctx, definitionPath, environmentPath, planPath, generatorVersion, attempt, workspace, outputPath, handoffPath)
}

// producingSourceDescription checks the runner's descriptive context. It does
// not mint a SourceRun: the build is still in progress, so the consumer must
// independently authenticate its later completed state and actual default branch.
func producingSourceDescription(attempt string, lookup func(string) string) (qualificationorigin.Description, error) {
	if lookup("GITHUB_ACTIONS") != "true" || lookup("GITHUB_SERVER_URL") != "https://github.com" ||
		(lookup("GITHUB_EVENT_NAME") != "push" && lookup("GITHUB_EVENT_NAME") != "workflow_dispatch") {
		return qualificationorigin.Description{}, e.New(e.CodeEngineCapability, "qualification handoff requires a GitHub push or manual producer")
	}
	repository, ref := lookup("GITHUB_REPOSITORY"), lookup("GITHUB_REF")
	branch, ok := strings.CutPrefix(ref, "refs/heads/")
	if !ok {
		return qualificationorigin.Description{}, e.New(e.CodeProvenanceMissing, "qualification handoff requires a producing branch")
	}
	workflow, ok := strings.CutPrefix(lookup("GITHUB_WORKFLOW_REF"), repository+"/")
	if !ok {
		return qualificationorigin.Description{}, e.New(e.CodeProvenanceMissing, "qualification handoff workflow belongs to another repository")
	}
	workflow, ok = strings.CutSuffix(workflow, "@"+ref)
	if !ok {
		return qualificationorigin.Description{}, e.New(e.CodeProvenanceMissing, "qualification handoff workflow differs from the producing branch")
	}
	r := qualificationorigin.SourceRequest{Repository: repository, RunID: lookup("GITHUB_RUN_ID"), Attempt: lookup("GITHUB_RUN_ATTEMPT"),
		Commit: lookup("GITHUB_SHA"), Workflow: workflow, DefaultBranch: branch}
	if err := r.Validate(); err != nil {
		return qualificationorigin.Description{}, err
	}
	if attempt != r.RunID+"-"+r.Attempt {
		return qualificationorigin.Description{}, e.New(e.CodeVerifyFailed, "qualification handoff attempt differs from the producing runner")
	}
	return qualificationorigin.Description{Repository: r.Repository, RunID: r.RunID, Attempt: r.Attempt, Commit: r.Commit,
		Workflow: r.Workflow, DefaultBranch: r.DefaultBranch, Ref: ref,
		InvocationID: "https://github.com/" + r.Repository + "/actions/runs/" + r.RunID + "/attempts/" + r.Attempt,
		BuilderID:    "https://github.com/" + r.Repository + "/" + r.Workflow + "@" + ref}, nil
}

func writeQualificationHandoff(ctx context.Context, path, workspace string, plan *factory.Plan, inputs factory.InputSnapshot, source qualificationorigin.Description) error {
	if err := admitQualificationProducer(plan, source); err != nil {
		return err
	}
	reference, saved, err := factory.CollectQualificationEvidence(ctx, workspace, plan)
	if err != nil {
		return err
	}
	candidate := QualificationCandidate{SchemaVersion: QualificationCandidateSchema, Reference: reference}
	for _, predicate := range saved {
		candidate.Predicates = append(candidate.Predicates, QualificationPredicate{Reference: predicate.Reference, Predicate: predicate.Predicate})
	}
	planRaw, err := canonical.Marshal(plan)
	if err != nil {
		return err
	}
	raw, err := canonical.Marshal(QualificationHandoff{SchemaVersion: QualificationHandoffSchema, Source: source,
		Plan: planRaw, Inputs: inputs, Candidate: candidate})
	if err != nil {
		return err
	}
	if len(raw) > qualificationorigin.MaxQualificationHandoffBytes {
		return e.New(e.CodeProvenanceMissing, "qualification handoff exceeds its byte limit")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "qualification handoff canceled")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating qualification handoff directory")
	}
	_, err = writeLifecycleRecordState(ctx, path, raw, func(directory *os.File) error { return directory.Sync() })
	return err
}

func admitQualificationProducer(plan *factory.Plan, source qualificationorigin.Description) error {
	if err := factory.AdmitQualificationHandoff(plan); err != nil {
		return err
	}
	if plan.Environment.Signing == nil || plan.Environment.Signing.Mode != factory.SigningModeCosign ||
		plan.Environment.Signing.Issuer != "https://token.actions.githubusercontent.com" {
		return e.New(e.CodeEngineCapability, "qualification handoff requires live GitHub build signing")
	}
	identity, err := regexp.Compile(plan.Environment.Signing.Identity)
	if err != nil || !identity.MatchString(source.BuilderID) {
		return e.New(e.CodeUntrustedSigner, "qualification handoff source differs from the producing signing policy")
	}
	return nil
}

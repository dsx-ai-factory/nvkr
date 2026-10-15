// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// RestoreGitHubInputs restores the exact external carriers of one generated
// stage. The complete Plan owns selection; transport conveys no authority.
func RestoreGitHubInputs(ctx context.Context, plan *Plan, stageID string, options GitHubStepOptions) error {
	stages, nodeJobs, artifacts, err := admitGitHubCarriers(ctx, plan)
	if err != nil {
		return err
	}
	var selected *githubStage
	for i := range stages {
		if stages[i].ID == stageID {
			selected = &stages[i]
			break
		}
	}
	if selected == nil {
		return e.New(e.CodeInvalidCatalog, "GitHub plan has no stage %q", stageID)
	}
	inputs, err := selected.carrierInputs(plan, artifacts, nodeJobs)
	if err != nil {
		return err
	}
	details := make([]string, 0, len(inputs))
	for _, id := range inputs {
		details = append(details, githubArtifactDetail(plan, "Input", id))
	}
	return githubProgress(ctx, options, "Restore stage inputs", details, func(progress *githubStepProgress) error {
		for _, id := range inputs {
			progress.phase("restore " + id)
			progress.log("Restore: " + id)
			if err := transportarchive.Unpack(ctx, carrierPath(id), ".", []string{artifacts[id].Path}); err != nil {
				return err
			}
			if err := removeFactoryCarrier(carrierPath(id)); err != nil {
				return err
			}
		}
		return nil
	})
}

// PackGitHubOutputs prepares each exact output carrier for provider upload.
// A missing or invalid output stops preparation at that artifact.
func PackGitHubOutputs(ctx context.Context, plan *Plan, nodeID string, options GitHubStepOptions) error {
	stages, _, artifacts, err := admitGitHubCarriers(ctx, plan)
	if err != nil {
		return err
	}
	var selected *Node
	for _, stage := range stages {
		for i := range stage.Nodes {
			if stage.Nodes[i].ID == nodeID {
				selected = &stage.Nodes[i]
			}
		}
	}
	if selected == nil {
		return e.New(e.CodeInvalidCatalog, "GitHub plan has no node %q", nodeID)
	}
	outputs := uniqueSorted(selected.Outputs)
	details := make([]string, 0, len(outputs))
	for _, id := range outputs {
		details = append(details, githubArtifactDetail(plan, "Output", id))
	}
	return githubProgress(ctx, options, "Prepare output carriers", details, func(progress *githubStepProgress) error {
		for _, id := range outputs {
			progress.phase("archive " + id)
			progress.log("Archive: " + id)
			if err := transportarchive.Pack(ctx, carrierPath(id), ".", []string{artifacts[id].Path}); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecoverGitHubArtifacts records the transport outcome of every planned carrier.
// providerResults is the bounded JSON description from GitHub's needs context;
// neither these provider descriptions nor the resulting statuses confer authority.
// Ordinary carrier failures are recorded and recovery continues. Cancellation or
// failure to persist an outcome stops recovery and returns a coded error.
func RecoverGitHubArtifacts(ctx context.Context, plan *Plan, providerResults string, options GitHubStepOptions) error {
	return recoverGitHubArtifacts(ctx, plan, providerResults, options, recoverGitHubCarrier, writeGitHubRestoreOutcome)
}

// The public operation always supplies the concrete held-carrier restorer and
// outcome writer. These private seams permit resource-failure tests without
// exhausting the test host.
func recoverGitHubArtifacts(ctx context.Context, plan *Plan, providerResults string, options GitHubStepOptions,
	restore func(context.Context, Artifact) (string, error),
	writeOutcome func(context.Context, string, string) error,
) error {
	if err := admitGitHubRecoveryPlatform(); err != nil {
		return err
	}
	_, nodeJobs, _, err := admitGitHubCarriers(ctx, plan)
	if err != nil {
		return err
	}
	jobs, err := githubFinalizerJobs(plan, nodeJobs)
	if err != nil {
		return err
	}
	projection, err := githubNeedsForPlan(ctx, plan)
	if err != nil {
		return err
	}
	results, err := parseGitHubCollectedProviderResults(ctx, providerResults, jobs, projection.Collectors)
	if err != nil {
		return err
	}
	if PerOutputPublication(plan) {
		status := "restored:success"
		for _, job := range jobs {
			if results[job] != "success" {
				status = "job-failed"
				break
			}
		}
		if err := writeGitHubRestoreOutcome(ctx, "all-provider-jobs", status); err != nil {
			return err
		}
	}
	carriers := append([]Artifact(nil), plan.Graph.Artifacts...)
	sort.Slice(carriers, func(i, j int) bool { return carriers[i].ID < carriers[j].ID })
	return githubProgress(ctx, options, "Recover artifacts for finalization", []string{
		"Transport outcomes are diagnostic; the finalizer authenticates completeness.",
	}, func(progress *githubStepProgress) error {
		counts := map[string]int{}
		visited, restoreNoSpace, outcomeWriteFailed, outcomeNoSpace := 0, 0, 0, 0
		defer func() {
			progress.detail(fmt.Sprintf("Recovery totals: visited=%d restored=%d restore-failed=%d (no-space=%d) missing=%d job-failed=%d; persistence: outcome-write-failed=%d (no-space=%d)",
				visited, counts["restored:success"], counts["restore-failed"], restoreNoSpace, counts["missing"], counts["job-failed"], outcomeWriteFailed, outcomeNoSpace))
			if restoreNoSpace+outcomeNoSpace > 0 {
				// A fixed annotation cannot include archive-controlled paths or text.
				_, _ = fmt.Fprintln(progress.options.Stderr, "::error::Artifact recovery exhausted storage; restoration or outcome persistence is incomplete.")
			}
		}()
		for _, artifact := range carriers {
			if !finalizerArtifact(plan, artifact) {
				continue
			}
			if err := githubCarrierContext(ctx); err != nil {
				return err
			}
			progress.phase("recover " + artifact.ID)
			progress.log("Recover: " + artifact.ID + " (" + githubArtifactLabel(plan, artifact) + ")")
			status := "job-failed"
			if results[nodeJobs[artifact.Producer]] == "success" {
				var restoreErr error
				status, restoreErr = restore(ctx, artifact)
				if restoreErr != nil {
					progress.detail("Recovery failure: " + artifact.ID + " — " + githubRecoveryErrorDetail(restoreErr))
					if errors.Is(restoreErr, syscall.ENOSPC) || errors.Is(restoreErr, syscall.EDQUOT) {
						restoreNoSpace++
					}
				}
			}
			// Restoration describes every visited carrier, independently of whether
			// its diagnostic status can subsequently be persisted.
			visited++
			counts[status]++
			// Cancellation must not become an ordinary restore failure or allow
			// a partially visited carrier set to appear complete.
			if err := githubCarrierContext(ctx); err != nil {
				return err
			}
			if err := writeOutcome(ctx, artifact.ID, status); err != nil {
				outcomeWriteFailed++
				progress.detail("Recovery outcome failure: " + artifact.ID + " — " + githubRecoveryErrorDetail(err))
				if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
					outcomeNoSpace++
				}
				return err
			}
			progress.detail("Recovery: " + artifact.ID + " — " + status)
		}
		return nil
	})
}

func admitGitHubCarriers(ctx context.Context, plan *Plan) ([]githubStage, map[string]string, map[string]Artifact, error) {
	if err := admitGitHubPlan(ctx, plan); err != nil {
		return nil, nil, nil, err
	}
	stages, nodeJobs, err := githubStages(plan)
	if err != nil {
		return nil, nil, nil, err
	}
	_, artifacts := planIndex(plan)
	return stages, nodeJobs, artifacts, githubCarrierContext(ctx)
}

func githubCarrierContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "GitHub carrier operation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "GitHub carrier operation canceled")
	}
	return nil
}

// githubFinalizerJobs is shared with emission so recovery accepts exactly the
// finalizer's stage and runner-bootstrap dependencies, including grouped stages.
func githubFinalizerJobs(plan *Plan, nodeJobs map[string]string) ([]string, error) {
	jobs := make([]string, 0, len(nodeJobs)+len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		job := nodeJobs[node.ID]
		if job == "" {
			return nil, e.New(e.CodeInvalidCatalog, "GitHub node %q has no producer job", node.ID)
		}
		resolved, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job, "build-runner-"+shortStableName(resolved.RunsOn))
	}
	return uniqueSorted(jobs), nil
}

func recoverGitHubCarrier(ctx context.Context, artifact Artifact) (string, error) {
	if ctx.Err() != nil {
		return "restore-failed", e.Wrap(e.CodeTimeout, ctx.Err(), "carrier recovery canceled")
	}
	archive, _, err := securepath.OpenRegular(carrierPath(artifact.ID))
	if err != nil {
		if os.IsNotExist(err) {
			return "missing", nil
		}
		return "restore-failed", e.Wrap(e.CodeVerifyFailed, err, "opening recovery carrier")
	}
	defer archive.Close()
	if err := transportarchive.UnpackOpen(ctx, archive, ".", []string{artifact.Path}); err != nil {
		return "restore-failed", err
	}
	return "restored:success", nil
}

// Report the owning code and known resource failures without logging untrusted
// archive entry names or arbitrary error text. Recovery status remains diagnostic;
// the finalizer still rejects every missing or unsuccessfully restored carrier.
func githubRecoveryErrorDetail(err error) string {
	detail := string(e.CodeInternal)
	var coded *e.Error
	if errors.As(err, &coded) {
		detail = string(coded.Code)
	}
	switch {
	case errors.Is(err, syscall.ENOSPC):
		return detail + ": no space left on device"
	case errors.Is(err, syscall.EDQUOT):
		return detail + ": disk quota exceeded"
	case errors.Is(err, os.ErrPermission):
		return detail + ": permission denied"
	default:
		return detail
	}
}

const (
	maxGitHubProviderBytes  = 1 << 20
	maxGitHubProviderTokens = 32768
)

// Read the tiny provider schema token-by-token: generic unmarshalling would
// silently accept duplicate names, while recursive ignored values would admit
// unnecessary depth and complexity. Outputs are accepted only as string maps and
// discarded; provider-controlled values never enter diagnostic messages.
func parseGitHubProviderResults(ctx context.Context, raw string, expected []string) (map[string]string, error) {
	jobs, err := parseGitHubProviderJobs(ctx, raw, expected, &githubProviderBudget{})
	if err != nil {
		return nil, err
	}
	results := make(map[string]string, len(jobs))
	for id, job := range jobs {
		results[id] = job.result
	}
	return results, nil
}

// Collectors only forward provider descriptions. Their exact leaf partition
// comes from the admitted Plan, never from the reports. A failed collector is
// rejected before restoration in every lane, even if it supplied complete data.
func parseGitHubCollectedProviderResults(ctx context.Context, raw string, expected []string, collectors map[string][]string) (map[string]string, error) {
	if len(collectors) == 0 {
		return parseGitHubProviderResults(ctx, raw, expected)
	}
	if err := githubCarrierContext(ctx); err != nil {
		return nil, err
	}
	if err := validateGitHubCollectorMembers(expected, collectors); err != nil {
		return nil, err
	}
	budget := &githubProviderBudget{}
	reports, err := parseGitHubProviderJobs(ctx, raw, sortedKeys(collectors), budget)
	if err != nil {
		return nil, err
	}
	results := make(map[string]string, len(expected))
	for _, id := range sortedKeys(collectors) {
		report := reports[id]
		if report.result != "success" {
			return nil, e.New(e.CodeInvalidCatalog, "GitHub result collector did not complete successfully")
		}
		if len(report.outputs) != 2 || report.outputs["results"] == "" || report.outputs["statuses"] == "" {
			return nil, e.New(e.CodeInvalidCatalog, "GitHub result collector requires exactly its results and statuses outputs")
		}
		leaves, err := parseGitHubProviderJobs(ctx, report.outputs["results"], collectors[id], budget)
		if err != nil {
			return nil, err
		}
		if err := validateGitHubCollectorStatuses(report.outputs["statuses"], leaves); err != nil {
			return nil, err
		}
		for leaf, job := range leaves {
			results[leaf] = job.result
		}
	}
	return results, githubCarrierContext(ctx)
}

func validateGitHubCollectorMembers(expected []string, collectors map[string][]string) error {
	unclaimed := make(map[string]bool, len(expected))
	for _, leaf := range expected {
		if leaf == "" || unclaimed[leaf] {
			return e.New(e.CodeInvalidCatalog, "GitHub result membership contains an empty or duplicate leaf")
		}
		unclaimed[leaf] = true
	}
	claimed := 0
	for id, members := range collectors {
		if _, collision := unclaimed[id]; id == "" || collision || len(members) == 0 {
			return e.New(e.CodeInvalidCatalog, "GitHub result collectors require distinct names and nonempty membership")
		}
		for _, member := range members {
			if !unclaimed[member] {
				return e.New(e.CodeInvalidCatalog, "GitHub result collectors contain an unknown or repeated member")
			}
			unclaimed[member] = false
			claimed++
		}
	}
	if claimed != len(expected) {
		return e.New(e.CodeInvalidCatalog, "GitHub result collectors omit a required stage or runner bootstrap")
	}
	return nil
}

// Object-filter order is not guaranteed by GitHub. The diagnostic status list
// must match the exact result multiset; only the results map binds job names.
func validateGitHubCollectorStatuses(raw string, leaves map[string]githubProviderJob) error {
	counts := make(map[string]int)
	for _, job := range leaves {
		counts[job.result]++
	}
	statuses := strings.SplitN(raw, " ", len(leaves)+1)
	if len(statuses) != len(leaves) {
		return e.New(e.CodeInvalidCatalog, "GitHub collector status summary differs from its member results")
	}
	for _, status := range statuses {
		if counts[status] == 0 {
			return e.New(e.CodeInvalidCatalog, "GitHub collector status summary differs from its member results")
		}
		counts[status]--
	}
	return nil
}

type githubProviderJob struct {
	result  string
	outputs map[string]string
}

type githubProviderBudget struct {
	bytes, tokens int
}

// One budget covers the outer description and all decoded collector reports.
// Leaf output strings stay inert: this parser does not recursively follow them.
func parseGitHubProviderJobs(ctx context.Context, raw string, expected []string, budget *githubProviderBudget) (map[string]githubProviderJob, error) {
	if len(raw) > maxGitHubProviderBytes-budget.bytes || !utf8.ValidString(raw) {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub job results exceed their size bound or contain invalid UTF-8")
	}
	budget.bytes += len(raw)
	parser := githubProviderParser{ctx: ctx, decoder: json.NewDecoder(strings.NewReader(raw)), budget: budget}
	if err := parser.delimiter('{'); err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(expected))
	for _, job := range expected {
		allowed[job] = true
	}
	results := make(map[string]githubProviderJob, len(expected))
	for parser.decoder.More() {
		job, err := parser.stringValue()
		if err != nil {
			return nil, err
		}
		if !allowed[job] || results[job].result != "" {
			return nil, e.New(e.CodeInvalidCatalog, "GitHub job results contain an unexpected or duplicate job")
		}
		result, err := parser.job()
		if err != nil {
			return nil, err
		}
		results[job] = result
	}
	if err := parser.delimiter('}'); err != nil {
		return nil, err
	}
	if _, err := parser.decoder.Token(); err != io.EOF {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub job results contain trailing data")
	}
	if len(results) != len(allowed) {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub job results omit a required stage or runner bootstrap")
	}
	return results, githubCarrierContext(ctx)
}

type githubProviderParser struct {
	ctx     context.Context
	decoder *json.Decoder
	budget  *githubProviderBudget
}

func (parser *githubProviderParser) token() (json.Token, error) {
	if err := githubCarrierContext(parser.ctx); err != nil {
		return nil, err
	}
	parser.budget.tokens++
	if parser.budget.tokens > maxGitHubProviderTokens {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub job results exceed their complexity bound")
	}
	token, err := parser.decoder.Token()
	if err != nil {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub job results contain invalid JSON")
	}
	return token, nil
}

func (parser *githubProviderParser) delimiter(want json.Delim) error {
	token, err := parser.token()
	if err != nil {
		return err
	}
	if token != want {
		return e.New(e.CodeInvalidCatalog, "GitHub job results contain an invalid object")
	}
	return nil
}

func (parser *githubProviderParser) stringValue() (string, error) {
	token, err := parser.token()
	if err != nil {
		return "", err
	}
	value, ok := token.(string)
	if !ok {
		return "", e.New(e.CodeInvalidCatalog, "GitHub job results require string fields")
	}
	return value, nil
}

func (parser *githubProviderParser) job() (githubProviderJob, error) {
	if err := parser.delimiter('{'); err != nil {
		return githubProviderJob{}, err
	}
	seen := map[string]bool{}
	var job githubProviderJob
	for parser.decoder.More() {
		field, err := parser.stringValue()
		if err != nil {
			return githubProviderJob{}, err
		}
		if seen[field] {
			return githubProviderJob{}, e.New(e.CodeInvalidCatalog, "GitHub job results contain a duplicate field")
		}
		seen[field] = true
		switch field {
		case "result":
			job.result, err = parser.stringValue()
			if err != nil {
				return githubProviderJob{}, err
			}
			switch job.result {
			case "success", "failure", "cancelled", "skipped": //nolint:misspell // GitHub result enum spelling.
			default:
				return githubProviderJob{}, e.New(e.CodeInvalidCatalog, "GitHub job results contain an unsupported result")
			}
		case "outputs":
			job.outputs, err = parser.outputs()
			if err != nil {
				return githubProviderJob{}, err
			}
		default:
			return githubProviderJob{}, e.New(e.CodeInvalidCatalog, "GitHub job results contain an unexpected field")
		}
	}
	if err := parser.delimiter('}'); err != nil {
		return githubProviderJob{}, err
	}
	if job.result == "" {
		return githubProviderJob{}, e.New(e.CodeInvalidCatalog, "GitHub job results omit a result")
	}
	return job, nil
}

func (parser *githubProviderParser) outputs() (map[string]string, error) {
	if err := parser.delimiter('{'); err != nil {
		return nil, err
	}
	outputs := map[string]string{}
	for parser.decoder.More() {
		field, err := parser.stringValue()
		if err != nil {
			return nil, err
		}
		if _, seen := outputs[field]; seen {
			return nil, e.New(e.CodeInvalidCatalog, "GitHub job outputs contain a duplicate field")
		}
		value, err := parser.stringValue()
		if err != nil {
			return nil, err
		}
		outputs[field] = value
	}
	return outputs, parser.delimiter('}')
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

const refreshLabel = "native-toolchain-refresh"
const refreshTitle = "Native toolchain refresh"

var refreshJobs = []string{"measure", "admit", "publish", "verify-candidate", "propose"}
var manifestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var legacyRefreshTitle = regexp.MustCompile(`^Native toolchain refresh(?: [0-9]+)?$`)

// Compare authenticates the Plan using its existing packed/tree loader, then
// compares described image manifests. Its boolean grants no publication authority.
func Compare(ctx context.Context, planPath, proposalPath, arch string) (bool, error) {
	if arch != "amd64" && arch != "arm64" {
		return false, e.New(e.CodeUsage, "unsupported native architecture")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	plan, err := factory.LoadPlanContext(ctx, planPath)
	if err != nil {
		return false, err
	}
	raw, err := read(ctx, proposalPath)
	if err != nil {
		return false, err
	}
	var images []Image
	if err := decode(raw, &images); err != nil {
		return false, err
	}
	return compareManifests(plan, images, arch)
}
func compareManifests(plan *factory.Plan, images []Image, arch string) (bool, error) {
	if plan == nil || (arch != "amd64" && arch != "arm64") {
		return false, e.New(e.CodeUsage, "unsupported native architecture or missing Plan")
	}
	platform := "linux/" + arch
	packers := map[string]bool{}
	for _, build := range plan.Builds {
		if build.Platform == platform && build.Graph != nil {
			for _, node := range build.Graph.Nodes {
				if strings.HasPrefix(node.Toolchain, Registry+"/packer-native@") {
					packers[node.Toolchain] = true
				}
			}
		}
	}
	if len(packers) != 1 {
		return false, e.New(e.CodeGateUnmet, "expected one selected native Packer pin")
	}
	selected := map[string]string{"inspector": plan.Environment.DiskInspectionProfiles[platform].Image}
	for pin := range packers {
		selected["packer"] = pin
	}
	for kind, ref := range selected {
		prefix := Registry + "/" + kind + "-native@"
		if !strings.HasPrefix(ref, prefix) || !manifestPattern.MatchString(strings.TrimPrefix(ref, prefix)) {
			return false, e.New(e.CodeGateUnmet, "invalid selected %s manifest pin", kind)
		}
	}
	measured := map[string]string{}
	for _, image := range images {
		if image.Arch != arch || selected[image.Kind] == "" || measured[image.Kind] != "" {
			return false, e.New(e.CodeGateUnmet, "expected one measured subject per native image")
		}
		if !manifestPattern.MatchString(image.ManifestDigest) {
			return false, e.New(e.CodeGateUnmet, "invalid measured manifest digest")
		}
		measured[image.Kind] = image.Reference()
	}
	if len(measured) != len(selected) {
		return false, e.New(e.CodeGateUnmet, "incomplete native measurements")
	}
	return !maps.Equal(measured, selected), nil
}

type jobResult struct {
	Result  string            `json:"result"`
	Outputs map[string]string `json:"outputs"`
}

func refreshReason(results map[string]jobResult, approvalRun string) (string, error) {
	var failed []string
	for _, job := range refreshJobs {
		switch results[job].Result {
		case "success", "skipped":
		case "failure", "cancelled": //nolint:misspell // GitHub's job-result value.
			failed = append(failed, job)
		default:
			return "", e.New(e.CodeGateUnmet, "unknown job result: %s", job)
		}
	}
	if len(failed) > 0 {
		return "Refresh failed or was canceled: " + strings.Join(failed, ", "), nil
	}
	if approvalRun == "" && results["measure"].Result == "success" {
		var changed []string
		for _, arch := range []string{"amd64", "arm64"} {
			value := results["measure"].Outputs["changed_"+arch]
			if value != "true" && value != "false" {
				return "", e.New(e.CodeGateUnmet, "missing or invalid %s manifest comparison", arch)
			}
			if value == "true" {
				changed = append(changed, arch)
			}
		}
		if len(changed) > 0 {
			return "Measured manifests differ from committed pins; review required: " + strings.Join(changed, ", "), nil
		}
	}
	return "", nil
}

type issueLabel struct {
	Name string `json:"name"`
}
type refreshIssue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Labels      []issueLabel    `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request,omitempty"`
}

func hasRefreshLabel(labels []issueLabel) bool {
	for _, label := range labels {
		if label.Name == refreshLabel {
			return true
		}
	}
	return false
}
func (c reviewClient) postReport(ctx context.Context, body string) error {
	var labeled []refreshIssue
	if err := c.pages(ctx, "issues?state=open&labels="+refreshLabel+"&per_page=100", &labeled); err != nil {
		return err
	}
	candidates := []refreshIssue{}
	for _, issue := range labeled {
		if issue.PullRequest == nil {
			candidates = append(candidates, issue)
		}
	}
	if len(candidates) == 0 {
		var issues []refreshIssue
		if err := c.pages(ctx, "issues?state=open&per_page=100", &issues); err != nil {
			return err
		}
		for _, issue := range issues {
			if issue.PullRequest == nil && legacyRefreshTitle.MatchString(issue.Title) {
				candidates = append(candidates, issue)
			}
		}
	}
	var chosen *refreshIssue
	for i := range candidates {
		if candidates[i].Number < 1 {
			return e.New(e.CodeGateUnmet, "invalid refresh issue number")
		}
		if chosen == nil || candidates[i].Number < chosen.Number {
			chosen = &candidates[i]
		}
	}
	var labels []issueLabel
	if err := c.pages(ctx, "labels?per_page=100", &labels); err != nil {
		return err
	}
	var response json.RawMessage
	if !hasRefreshLabel(labels) {
		if err := c.repoAPI(ctx, "labels", map[string]any{"name": refreshLabel, "color": "0e8a16", "description": "Native toolchain refresh tracking"}, &response); err != nil {
			return err
		}
	}
	if chosen != nil {
		endpoint := fmt.Sprintf("issues/%d", chosen.Number)
		if !hasRefreshLabel(chosen.Labels) {
			if err := c.repoAPI(ctx, endpoint+"/labels", map[string]any{"labels": []string{refreshLabel}}, &response); err != nil {
				return err
			}
		}
		return c.repoAPI(ctx, endpoint+"/comments", map[string]string{"body": body}, &response)
	}
	return c.repoAPI(ctx, "issues", map[string]any{"title": refreshTitle, "labels": []string{refreshLabel}, "assignees": []string{Owner}, "body": body}, &response)
}

// Report posts only actionable outcomes on the admitted repository's main
// branch. The report is diagnostic and cannot authorize pin or image changes.
func Report(ctx context.Context, root string) (string, error) {
	return report(ctx, newReviewClient(root), os.Getenv)
}
func report(ctx context.Context, c reviewClient, getenv func(string) string) (string, error) {
	if getenv("GITHUB_REPOSITORY") != Repository || getenv("GITHUB_REF") != "refs/heads/main" {
		return "", e.New(e.CodeGateUnmet, "refresh reporting requires the upstream main branch")
	}
	var results map[string]jobResult
	if err := json.Unmarshal([]byte(getenv("RESULTS")), &results); err != nil {
		return "", e.Wrap(e.CodeGateUnmet, err, "decode refresh results")
	}
	approval := getenv("APPROVAL_RUN")
	actionable, err := refreshReason(results, approval)
	if err != nil {
		return "", err
	}
	if actionable == "" {
		return "No actionable native toolchain refresh; no issue update.", nil
	}
	if approval == "" {
		approval = "not yet approved"
	}
	body := fmt.Sprintf("@%s: %s\n\nRun: %s\n\nPreparation approval run: %s\n\nJob results:\n", Owner, actionable, getenv("RUN_URL"), approval)
	for i, job := range refreshJobs {
		if i > 0 {
			body += "\n"
		}
		body += "- " + job + ": " + results[job].Result
	}
	body += "\n\nOld pins remain selected until the complete pin change is reviewed and merged.\nReview retained proposals, license/security findings and source-lock freshness. Changed measurements require reviewed per-image exceptions before publication; a refresh report is not approval. Follow docs/toolchain-maintenance.md.\n"
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "write refresh summary")
	}
	summary, err := os.OpenFile(getenv("GITHUB_STEP_SUMMARY"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", e.Wrap(e.CodeGateUnmet, err, "open refresh summary")
	}
	_, writeErr := summary.WriteString(body)
	closeErr := summary.Close()
	if writeErr != nil {
		return "", e.Wrap(e.CodeGateUnmet, writeErr, "write refresh summary")
	}
	if closeErr != nil {
		return "", e.Wrap(e.CodeGateUnmet, closeErr, "close refresh summary")
	}
	return "", c.postReport(ctx, body)
}

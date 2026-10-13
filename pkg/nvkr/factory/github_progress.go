// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// githubRunnerCommand binds a provider step to the complete generated Plan. The
// helper owns execution and transport mechanics; no paths or argv are authored here.
func githubRunnerCommand(operation, selectorFlag, selector string, plan *Plan) string {
	layout := githubBundleLayout(plan.Environment.GitHubBundle)
	command := "./nvkr-exec factory github " + operation + " --plan " + shellQuote(layout.planPath)
	if selectorFlag != "" {
		command += " --" + selectorFlag + " " + shellQuote(selector)
	}
	return command
}

func githubNodeCommands(node Node, plan *Plan) []string {
	return []string{githubRunnerCommand("run-node", "node", node.ID, plan)}
}

// This allowlist intentionally excludes arbitrary recipe arguments: they may
// contain literal authentication values. Exact instructions remain inspectable in
// the canonical Plan; the logs identify which instruction executed.
func githubNodeDetails(plan *Plan, node Node) []string {
	details := []string{"Node: " + node.ID, "Operation: " + node.Kind}
	if node.BuildTarget != "" {
		details = append(details, "Selected output: "+node.BuildTarget)
	}
	if node.Platform != "" {
		details = append(details, "Platform: "+node.Platform)
	}
	if node.Network != "" {
		details = append(details, "Network: "+node.Network)
	}
	for _, build := range plan.Builds {
		if build.Name != node.BuildTarget || build.Graph == nil {
			continue
		}
		for _, instruction := range build.Graph.Nodes {
			if instruction.ID != node.IRNode {
				continue
			}
			details = append(details, "Producer instruction: "+instruction.ID)
			if instruction.Component != "" {
				details = append(details, "Component: "+instruction.Component)
			}
			if instruction.BuilderType != "" {
				details = append(details, "Builder: "+instruction.BuilderType)
			}
			if instruction.Commit != "" {
				details = append(details, "Source commit: "+instruction.Commit)
			}
			if instruction.SourceCommit != "" {
				details = append(details, "Source commit: "+instruction.SourceCommit)
			}
		}
	}
	if node.PackageKind != "" {
		details = append(details, "Package format: "+node.PackageKind)
	}
	if node.PackageComponent != "" {
		details = append(details, "Component: "+node.PackageComponent)
	}
	if node.Target != "" {
		details = append(details, "Publication route: "+node.Target)
	}
	for _, input := range uniqueSorted(node.Inputs) {
		details = append(details, githubArtifactDetail(plan, "Input", input))
	}
	for _, output := range uniqueSorted(node.Outputs) {
		details = append(details, githubArtifactDetail(plan, "Expected output", output))
	}
	return details
}

func githubArtifactDetail(plan *Plan, prefix, id string) string {
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.ID == id {
			return fmt.Sprintf("%s: %s (%s; producer %s)", prefix, id, githubArtifactLabel(plan, artifact), artifact.Producer)
		}
	}
	return prefix + ": " + id
}

func githubDisplayText(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

func githubSummaryText(value string) string {
	return strings.NewReplacer("|", "&#124;", "`", "&#96;").Replace(html.EscapeString(githubDisplayText(value)))
}

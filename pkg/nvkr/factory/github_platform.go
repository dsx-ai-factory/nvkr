// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"strings"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// GitHubExecutionFor projects the effective hosted runner and instruction without
// changing producer IR or logical Build identity. It is a descriptive transport
// view, not admission or authority; callers validate the complete Plan first.
func (plan *Plan) GitHubExecutionFor(node Node) (Node, error) {
	if plan == nil {
		return Node{}, e.New(e.CodeUsage, "GitHub execution projection requires a Plan")
	}
	node, err := plan.ExecutionFor(node)
	if err != nil {
		return Node{}, err
	}
	if len(plan.Environment.GitHubRunners) == 0 || githubDiskOutput(plan, node) != "" {
		return node, nil
	}
	platform := node.Platform
	if platform == "" {
		platform = plan.Environment.UtilityPlatform
	}
	runner, ok := plan.Environment.GitHubRunners[platform]
	if !ok {
		return Node{}, e.New(e.CodeEngineCapability, "GitHub operation %q has no native runner route for %s", node.ID, platform)
	}
	if node.ExecutionBinding != nil || node.Requirements != nil {
		return Node{}, e.New(e.CodeEngineCapability, "GitHub native routes do not admit execution requirements for %q", node.ID)
	}
	node.RunsOn = runner
	return node, nil
}

func admitGitHubPayloads(plan *Plan) error {
	// Payload admission precedes execution projection: a Linux cross-toolchain
	// does not authorize a Darwin payload on any hosted runner selector.
	for _, output := range plan.Definition.Outputs {
		if output.Platform != "" {
			if err := target.ValidatePlatform(output.Platform); err != nil {
				return e.Wrap(e.CodeEngineCapability, err, "GitHub execution requires Linux payload targets")
			}
		}
	}
	return nil
}

func admitGitHubPlatforms(plan *Plan) error {
	if err := admitGitHubPayloads(plan); err != nil {
		return err
	}
	// Existing ARC plans retain their independently admitted execution projection.
	if plan.Environment.GitHubARC != "" && len(plan.Environment.GitHubRunners) == 0 {
		return nil
	}
	for _, node := range plan.Graph.Nodes {
		execution, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return err
		}
		platform, err := authored.GitHubRunnerPlatform(githubBootstrapRunner(execution.RunsOn))
		if err != nil {
			return err
		}
		want := execution.Platform
		if want == "" {
			want = plan.Environment.UtilityPlatform
		}
		if platform != want {
			return e.New(e.CodeEngineCapability, "GitHub operation %q requests %s on %s; configure an exact githubRunners route", node.ID, want, platform)
		}
	}
	return nil
}

func admitGitHubNodePlatform(plan *Plan, node Node, observed string) error {
	if err := admitGitHubPayloads(plan); err != nil {
		return err
	}
	execution, err := plan.GitHubExecutionFor(node)
	if err != nil {
		return err
	}
	want, err := authored.GitHubRunnerPlatform(githubBootstrapRunner(execution.RunsOn))
	if err != nil {
		return err
	}
	if observed != want {
		return e.New(e.CodeEngineCapability, "GitHub operation %q requires native helper platform %s; observed %s", node.ID, want, observed)
	}
	return nil
}

const githubPlatformCheckName = "Check native runner platform"

func githubPlatformCheck(plan *Plan, runner string) (string, error) {
	if len(plan.Environment.GitHubRunners) == 0 {
		return "", nil
	}
	platform, err := authored.GitHubRunnerPlatform(githubBootstrapRunner(runner))
	if err != nil {
		return "", err
	}
	machine := "x86_64"
	if platform == "linux/arm64" {
		machine = "aarch64"
	}
	return fmt.Sprintf("set -eu\nif [ \"$(uname -s)/$(uname -m)\" != %s ]; then printf 'NVKR-E-ENGINE-CAPABILITY: expected native %s runner\\n' >&2; exit %d; fi", shellQuote("Linux/"+machine), platform, e.ExitCode(e.New(e.CodeEngineCapability, "foreign runner"))), nil
}

func writeGitHubPlatformCheck(out *bytes.Buffer, plan *Plan, runner string) error {
	command, err := githubPlatformCheck(plan, runner)
	if err != nil {
		return err
	}
	if command != "" {
		fmt.Fprintf(out, "      - name: %s\n        run: |\n", githubPlatformCheckName)
		for _, line := range strings.Split(command, "\n") {
			fmt.Fprintf(out, "          %s\n", line)
		}
	}
	return nil
}

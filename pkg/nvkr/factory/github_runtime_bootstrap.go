// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// These are descriptive provider fields used by existing development admission
// and diagnostics. No token, home, executable override or authored variable is
// forwarded into the privileged coordinator.
var runtimeDiskProviderEnvironment = []string{
	"GITHUB_ACTIONS", "GITHUB_EVENT_NAME", "GITHUB_REF", "GITHUB_REPOSITORY",
	"GITHUB_RUN_ATTEMPT", "GITHUB_RUN_ID", "GITHUB_SERVER_URL", "GITHUB_WORKFLOW_REF",
	"GITHUB_OUTPUT",
}

func githubDiskCommand(plan *Plan, stage githubStage) string {
	run := githubRunnerCommand(plan, "run-disk", "output", stage.DiskOutput)
	if plan.Environment.Runtime.ProducedRuntimes[stage.DiskOutput] == "" {
		return run
	}
	var environment []string
	for _, name := range runtimeDiskProviderEnvironment {
		environment = append(environment, name+"=\"${"+name+"-}\"")
	}
	// The Plan-derived worker budget remains the inner execution deadline. This
	// outer root-owned watchdog also bounds a coordinator that cannot shut down.
	seconds := int64((stage.diskExecutionBudget+time.Second-1)/time.Second) + 30
	return githubRunnerCommand(plan, "admit-disk", "output", stage.DiskOutput) +
		" && /usr/bin/sudo -n -- /usr/bin/env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/dev/null DOCKER_CONFIG=/proc/self/fd LANG=C LC_ALL=C TZ=UTC " +
		strings.Join(environment, " ") + fmt.Sprintf(" /usr/bin/timeout --signal=TERM --kill-after=30s %ds ", seconds) + run
}

// CheckGitHubDisk performs the unprivileged half of generated native admission.
// It starts no producer and creates no scratch. The privileged coordinator must
// repeat the full check, then probe its namespace capabilities before work.
func CheckGitHubDisk(ctx context.Context, plan *Plan, output string) (returnErr error) {
	_, root, err := selectGitHubDisk(ctx, plan, output)
	if err != nil {
		return err
	}
	workspace, _, err := securepath.OpenDirectory(".")
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "holding disk preflight workspace")
	}
	defer func() {
		returnErr = joinFactoryCleanupError(returnErr, workspace.Close(), "closing disk preflight workspace")
	}()
	admission, err := AdmitLocalVMBuild(ctx, plan, root.ID, workspace)
	if err != nil {
		return err
	}
	return admission.Close()
}

func selectGitHubDisk(ctx context.Context, plan *Plan, output string) (githubStage, Node, error) {
	if err := admitGitHubPlan(ctx, plan); err != nil {
		return githubStage{}, Node{}, err
	}
	for _, key := range []string{sourceTokenEnv, githubImageToken, "NVKR_CREDENTIAL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"} {
		if os.Getenv(key) != "" {
			return githubStage{}, Node{}, e.New(e.CodeEngineCapability, "ARC disk job contains an unrelated credential")
		}
	}
	if runtime.GOOS != "linux" {
		return githubStage{}, Node{}, e.New(e.CodeEngineCapability, "ARC disks require native Linux")
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		return githubStage{}, Node{}, err
	}
	for _, stage := range stages {
		if output == "" || stage.DiskOutput != output {
			continue
		}
		if len(stage.Nodes) == 0 || stage.Nodes[0].Platform != runtime.GOOS+"/"+runtime.GOARCH {
			return githubStage{}, Node{}, e.New(e.CodeEngineCapability, "ARC disk host architecture differs from the Plan")
		}
		var root Node
		for _, node := range stage.Nodes {
			if node.Kind != NodeBuild {
				continue
			}
			_, instruction, err := plan.producerInstruction(node)
			if err != nil {
				return githubStage{}, Node{}, err
			}
			if instruction.BuilderType == ir.BuilderVMImage {
				if root.ID != "" {
					return githubStage{}, Node{}, e.New(e.CodeEngineCapability, "native disk group has ambiguous VM producers")
				}
				root = node
			}
		}
		if root.ID == "" {
			return githubStage{}, Node{}, e.New(e.CodeEngineCapability, "native disk group lacks its exact VM producer")
		}
		return stage, root, nil
	}
	return githubStage{}, Node{}, e.New(e.CodeEngineCapability, "no admitted ARC disk output %q", output)
}

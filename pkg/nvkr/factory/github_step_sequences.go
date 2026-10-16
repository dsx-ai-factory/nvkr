// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"strings"

	yamlv3 "go.yaml.in/yaml/v3"
)

// anchorGitHubStepSequences shares only complete, credential-free node sequences.
// It runs before individual-step anchoring, on resolved independent nodes, so
// every retained anchor definition precedes its uses and remains in the document.
// Job policy and provider-result identities are outside this transformation.
func anchorGitHubStepSequences(plan *Plan, jobs *yamlv3.Node, actions githubActionRefs) error {
	type candidate struct {
		steps *yamlv3.Node
		bytes []byte
	}
	first := map[string]candidate{}
	for index := 1; index < len(jobs.Content); index += 2 {
		job := jobs.Content[index]
		name := githubStepSequenceName(plan, job, actions)
		if name == "" {
			continue
		}
		steps := githubYAMLField(job, "steps")
		raw, err := encodeGitHubYAML(steps)
		if err != nil {
			return err
		}
		if prior, exists := first[name]; exists {
			// A descriptive name is never proof of equivalence. Compare the
			// complete resolved sequence, including every binding and label.
			if bytes.Equal(prior.bytes, raw) {
				prior.steps.Anchor = name
				githubYAMLSet(job, "steps", &yamlv3.Node{Kind: yamlv3.AliasNode, Value: name, Alias: prior.steps})
			}
		} else {
			first[name] = candidate{steps: steps, bytes: raw}
		}
	}
	return nil
}

// Keep the admitted set deliberately small: native preflight, credential-free
// checkout, host helper preparation and one or two plain node calls. Source
// calls, additional inputs, authored setup, transfers and provider controls stay
// explicit even when two jobs happen to contain identical steps.
func githubStepSequenceName(plan *Plan, job *yamlv3.Node, actions githubActionRefs) string {
	steps := githubYAMLField(job, "steps")
	if steps == nil || steps.Kind != yamlv3.SequenceNode || len(steps.Content) < 4 || len(steps.Content) > 5 ||
		githubYAMLField(job, "container") != nil {
		return ""
	}
	runner := githubYAMLValue(job, "runs-on")
	preflight, err := githubPlatformCheck(plan, runner)
	check := steps.Content[0]
	if err != nil || preflight == "" || githubYAMLLength(check) != 4 ||
		githubYAMLValue(check, "name") != githubPlatformCheckName || strings.TrimSpace(githubYAMLValue(check, "run")) != preflight {
		return ""
	}
	checkout := steps.Content[1]
	checkoutWith := githubYAMLField(checkout, "with")
	if githubYAMLLength(checkout) != 6 || githubYAMLValue(checkout, "name") != "Check out workflow source" ||
		githubYAMLValue(checkout, "uses") != actions.Checkout || githubYAMLLength(checkoutWith) != 2 ||
		githubYAMLValue(checkoutWith, "persist-credentials") != "false" {
		return ""
	}
	prepare := steps.Content[2]
	prepareWith := githubYAMLField(prepare, "with")
	shared := "./" + githubActionsDirectoryFor(plan) + "shared/"
	if githubYAMLLength(prepare) != 8 || githubYAMLValue(prepare, "name") != "Prepare NVKR runner and job parameters" ||
		githubYAMLValue(prepare, "id") != githubContextStepID || githubYAMLValue(prepare, "uses") != shared+"prepare-stage" ||
		githubCISequenceArgumentLength(prepareWith) != 2 ||
		githubYAMLValue(prepareWith, "runner-artifact") != "nvkr-exec-"+shortStableName(runner) {
		return ""
	}
	for index, call := range steps.Content[3:] {
		with := githubYAMLField(call, "with")
		part := fmt.Sprintf("part-%d", index+1)
		if githubYAMLLength(call) != 6 || githubYAMLValue(call, "name") != "execute node "+part ||
			githubYAMLValue(call, "uses") != shared+"execute-node" || githubCISequenceArgumentLength(with) != 2 ||
			githubYAMLValue(with, "parameters") != githubParametersExpression(part) {
			return ""
		}
	}
	operation := "node"
	if len(steps.Content) == 5 {
		operation = "two-nodes"
	}
	return "steps-" + shortStableName(runner) + "-" + operation
}

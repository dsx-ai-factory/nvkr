// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Batch only the exact provider names of required carriers. The provider's batch
// action can succeed on partial/no matches; every subsequent exact unpack remains
// mandatory and fails closed before executing a consumer. No wildcard selects
// unrelated artifacts or merges their untrusted payloads directly into a workspace.
func writeGitHubStageInputSteps(out *bytes.Buffer, plan *Plan, stage githubStage, artifacts map[string]Artifact, nodeJobs map[string]string, actions githubActionRefs) error {
	inputs, err := stage.carrierInputs(plan, artifacts, nodeJobs)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return nil
	}
	selected := make([]Artifact, len(inputs))
	for i, input := range inputs {
		selected[i] = artifacts[input]
	}
	pattern, err := githubCarrierPattern(selected)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "      - name: Download %d required input carriers\n        uses: %s\n        with:\n          pattern: %s\n          path: .nvkr/carriers\n          merge-multiple: 'true'\n", len(inputs), actions.Download, yamlSingle(pattern))
	writeGitHubShellStep(out, fmt.Sprintf("Restore %d required inputs", len(inputs)), []string{githubRunnerCommand("restore-inputs", "stage", stage.ID, plan)})
	return nil
}

func writeGitHubOutputSteps(out *bytes.Buffer, plan *Plan, node Node, artifacts map[string]Artifact, actions githubActionRefs) {
	if len(node.Outputs) == 0 {
		return
	}
	writeGitHubShellStep(out, "Prepare output carriers: "+githubNodeLabel(plan, node), []string{githubRunnerCommand("pack-outputs", "node", node.ID, plan)})
	for _, output := range node.Outputs {
		actions.writeUploadStep(out, githubArtifactLabel(plan, artifacts[output]), githubArtifactName(output), carrierPath(output))
	}
}

func writeGitHubShellStep(out *bytes.Buffer, label string, commands []string) {
	fmt.Fprintf(out, "      - name: %s\n        run: |\n", yamlSingle(label))
	for _, command := range commands {
		for _, line := range strings.Split(command, "\n") {
			fmt.Fprintf(out, "          %s\n", line)
		}
	}
}

// Carrier names are derived from admitted IDs, so this exact brace set has no
// minimatch metacharacters. Fail before returning bytes on lossy name collisions.
func githubCarrierPattern(artifacts []Artifact) (string, error) {
	if len(artifacts) == 0 {
		return "", e.New(e.CodeInvalidCatalog, "GitHub carrier selection requires at least one artifact")
	}
	seenNames, seenPaths := map[string]string{}, map[string]string{}
	var names []string
	for _, artifact := range artifacts {
		name, path := githubArtifactName(artifact.ID), carrierPath(artifact.ID)
		if previous := seenNames[name]; previous != "" {
			return "", e.New(e.CodeInvalidCatalog, "GitHub artifacts %q and %q collide at carrier name %q", previous, artifact.ID, name)
		}
		if previous := seenPaths[path]; previous != "" {
			return "", e.New(e.CodeInvalidCatalog, "GitHub artifacts %q and %q collide at carrier path %q", previous, artifact.ID, path)
		}
		seenNames[name], seenPaths[path] = artifact.ID, artifact.ID
		names = append(names, name)
	}
	names = uniqueSorted(names)
	if len(names) == 1 {
		return names[0], nil
	}
	return "{" + strings.Join(names, ",") + "}", nil
}

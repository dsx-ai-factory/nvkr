// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

const githubCIRestoreStageInput = "restore-stage"

// RestoreGitHubPrivateInputs derives the complete input selection from the
// admitted Plan. Private storage authenticates and extracts each nested carrier
// directly into its final roots, without materializing the carrier TAR locally.
// These transport checks do not replace each consumer's artifact authentication.
func RestoreGitHubPrivateInputs(ctx context.Context, plan *Plan, stageID string, request cistore.Request,
	index string, storage cistore.Options, options GitHubStepOptions,
) error {
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
	if len(inputs) == 0 {
		return e.New(e.CodeInvalidCatalog, "GitHub stage %q has no external carriers", stageID)
	}
	selections := make(map[string]cistore.NestedArchive, len(inputs))
	details := make([]string, 0, len(inputs))
	for _, id := range inputs {
		name := githubArtifactName(id)
		if _, duplicate := selections[name]; duplicate {
			return e.New(e.CodeInvalidCatalog, "GitHub stage carriers collide at provider name %q", name)
		}
		selections[name] = cistore.NestedArchive{Member: carrierPath(id), Allowed: []string{artifacts[id].Path}}
		details = append(details, githubArtifactDetail(plan, "Input", id))
	}
	return githubProgress(ctx, options, "Restore stage metadata and private payloads", details, func(progress *githubStepProgress) error {
		progress.phase("authenticate and stream required carriers")
		return cistore.DownloadGitHubNested(ctx, request, index, ".", selections, storage)
	})
}

// The closed semantic families already retain the exact download and restore
// selectors in their invocation data. Fuse only their known pair after logical
// sharing; inversion recreates that exact pair for the existing graph validator.
// Bootstrap helpers and finalizer recovery keep their ordinary transfer path.
func githubCIStageRestoreStep(plan *Plan, name string) *yamlv3.Node {
	prefix := githubActionsDirectoryFor(plan) + "shared/"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, "/action.yml") {
		return nil
	}
	family := strings.TrimSuffix(strings.TrimPrefix(name, prefix), "/action.yml")
	shell := "bash -e {0}"
	if strings.HasSuffix(family, "-container") {
		family = strings.TrimSuffix(family, "-container")
		shell = "sh -e {0}"
	}
	switch family {
	case "execute-node", "execute-source", "prepare-inputs":
	default:
		return nil
	}
	step := githubYAMLMap("name", githubYAMLString(githubParameterExpression("restore_name")), "id", githubYAMLString("operation_restore"))
	if family != "prepare-inputs" {
		githubYAMLSet(step, "if", githubYAMLString("${{ fromJSON(inputs.parameters).restore_present == 'true' }}"))
	}
	githubYAMLSet(step, "run", githubYAMLString(githubRunnerCommand(plan, "restore-inputs", "", "")+` --stage "$`+githubSelectorEnvironment+`"`))
	githubYAMLSet(step, "shell", githubYAMLString(shell))
	githubYAMLSet(step, "env", githubYAMLMap(githubSelectorEnvironment, githubYAMLString(githubParameterExpression("restore_selector"))))
	return step
}

func projectGitHubPrivateRestore(plan *Plan, name string, document *yamlv3.Node, inverse bool) error {
	expected := githubCIStageRestoreStep(plan, name)
	if expected == nil {
		return nil
	}
	steps := githubYAMLField(githubYAMLField(document.Content[0], "runs"), "steps")
	if steps == nil || len(steps.Content) == 0 || githubYAMLValue(steps.Content[0], "id") != "operation_download" {
		return e.New(e.CodeEngineCapability, "private stage restoration requires its exact semantic download")
	}
	download := steps.Content[0]
	with := githubYAMLField(download, "with")
	if with == nil || githubYAMLValue(download, "uses") != "./"+githubCITransferPath(plan, "download") {
		return e.New(e.CodeEngineCapability, "private stage restoration requires its private transfer adapter")
	}
	selector := githubParameterExpression("restore_selector")
	if inverse {
		if githubYAMLValue(with, githubCIRestoreStageInput) != selector {
			return e.New(e.CodeEngineCapability, "private stage restoration changes its Plan-derived selector")
		}
		githubYAMLRemove(with, githubCIRestoreStageInput)
		steps.Content = append(steps.Content[:1], append([]*yamlv3.Node{expected}, steps.Content[1:]...)...)
		return nil
	}
	if len(steps.Content) < 2 {
		return e.New(e.CodeEngineCapability, "private stage restoration omits its exact restore operation")
	}
	actualRaw, err := encodeGitHubYAML(steps.Content[1])
	if err != nil {
		return err
	}
	expectedRaw, err := encodeGitHubYAML(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(actualRaw, expectedRaw) || githubYAMLField(with, githubCIRestoreStageInput) != nil {
		return e.New(e.CodeEngineCapability, "private stage restoration changes its exact restore operation")
	}
	githubYAMLSet(with, githubCIRestoreStageInput, githubYAMLString(selector))
	steps.Content = append(steps.Content[:1], steps.Content[2:]...)
	return nil
}

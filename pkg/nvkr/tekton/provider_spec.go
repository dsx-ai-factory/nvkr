// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tekton

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// authenticateTaskSpec admits the complete TaskRun spec, including the fields
// outside TaskSpec which could override its commands, volumes or identity.
func (provider *Provider) authenticateTaskSpec(ctx context.Context, run pipelineRun, task taskRun, selected pipelineTask, expected TaskExpectation) error {
	var parent struct {
		TaskRunTemplate struct {
			ServiceAccountName string
			PodTemplate        json.RawMessage
		}
		TaskRunSpecs []struct{ PipelineTaskName, ServiceAccountName string }
		Workspaces   []struct {
			Name                string
			VolumeClaimTemplate struct{ Spec json.RawMessage }
		}
	}
	if err := json.Unmarshal(run.exactSpec, &parent); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decoding exact Tekton Run settings")
	}
	account := parent.TaskRunTemplate.ServiceAccountName
	for _, override := range parent.TaskRunSpecs {
		if override.PipelineTaskName == expected.Name {
			account = override.ServiceAccountName
		}
	}
	if account != expected.ServiceAccount {
		return e.New(e.CodePipelineDrift, "Tekton task account differs from its admitted Run")
	}
	want := map[string]any{"taskSpec": expected.Spec, "serviceAccountName": account,
		"params": []nameValue{{"run-name", provider.policy.RunName}, {"run-uid", provider.policy.RunUID}}}
	if selected.Timeout != "" {
		want["timeout"] = selected.Timeout
	}
	if len(parent.TaskRunTemplate.PodTemplate) != 0 {
		want["podTemplate"] = parent.TaskRunTemplate.PodTemplate
	}
	if len(selected.Workspaces) > 0 {
		if len(parent.Workspaces) != 1 || parent.Workspaces[0].Name != "carriers" {
			return e.New(e.CodePipelineDrift, "Tekton task requires its exact generated carrier workspace")
		}
		var actual []struct {
			Name, SubPath         string
			PersistentVolumeClaim struct{ ClaimName string }
		}
		if err := json.Unmarshal(task.Spec.Workspaces, &actual); err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "decoding Tekton carrier bindings")
		}
		if len(actual) != len(selected.Workspaces) {
			return e.New(e.CodePipelineDrift, "Tekton task changed workspace membership")
		}
		claim := ""
		bindings := make([]map[string]any, 0, len(actual))
		for index, binding := range selected.Workspaces {
			if binding.Workspace != "carriers" || actual[index].Name != binding.Name || !resourceName.MatchString(actual[index].PersistentVolumeClaim.ClaimName) {
				return e.New(e.CodePipelineDrift, "Tekton task substituted its carrier workspace")
			}
			if claim == "" {
				claim = actual[index].PersistentVolumeClaim.ClaimName
			}
			if claim != actual[index].PersistentVolumeClaim.ClaimName {
				return e.New(e.CodePipelineDrift, "Tekton task combined foreign carrier volumes")
			}
			entry := map[string]any{"name": binding.Name, "persistentVolumeClaim": map[string]string{"claimName": claim}}
			if binding.SubPath != "" {
				entry["subPath"] = binding.SubPath
			}
			bindings = append(bindings, entry)
		}
		if err := provider.authenticateClaim(ctx, claim, parent.Workspaces[0].VolumeClaimTemplate.Spec); err != nil {
			return err
		}
		want["workspaces"] = bindings
	}
	encoded, err := canonical.Marshal(want)
	if err != nil {
		return err
	}
	actual, err := exactJSON(task.exactSpec)
	if err != nil || !bytes.Equal(encoded, actual) {
		return e.New(e.CodePipelineDrift, "Tekton TaskRun execution fields differ from their admitted projection")
	}
	return nil
}

func (provider *Provider) authenticateClaim(ctx context.Context, name string, expected json.RawMessage) error {
	var claim struct {
		APIVersion, Kind string
		Metadata         objectMetadata
		Spec             map[string]json.RawMessage
	}
	if err := provider.get(ctx, "persistentvolumeclaims", name, &claim); err != nil {
		return err
	}
	owners := claim.Metadata.OwnerReferences
	if claim.APIVersion != "v1" || claim.Kind != "PersistentVolumeClaim" || claim.Metadata.Name != name || claim.Metadata.Namespace != provider.policy.Namespace || claim.Metadata.DeletionTimestamp != "" || !resourceUID.MatchString(claim.Metadata.UID) || len(owners) != 1 || owners[0].APIVersion != "tekton.dev/v1" || owners[0].Kind != "PipelineRun" || owners[0].Name != provider.policy.RunName || owners[0].UID != provider.policy.RunUID || !owners[0].Controller {
		return e.New(e.CodeVerifyFailed, "Tekton carrier volume is not owned by the exact Run")
	}
	var volume string
	if err := json.Unmarshal(claim.Spec["volumeName"], &volume); err != nil || !resourceName.MatchString(volume) {
		return e.New(e.CodeVerifyFailed, "Tekton carrier volume is not bound")
	}
	delete(claim.Spec, "volumeName")
	actual, err := canonical.Marshal(claim.Spec)
	if err != nil {
		return err
	}
	want, err := exactJSON(expected)
	if err != nil || !bytes.Equal(actual, want) {
		return e.New(e.CodePipelineDrift, "Tekton carrier volume differs from its selected storage contract")
	}
	return nil
}

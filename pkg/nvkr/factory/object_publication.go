// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const ArtifactObjectPublication = "object-publication"

func objectPublicationDestination(plan *Plan, target string) (definition.ObjectStoreDestination, error) {
	if plan != nil && plan.Environment.Publication != nil {
		for _, destination := range plan.Environment.Publication.ObjectStores {
			if target == "object-"+destination.Name {
				return destination, nil
			}
		}
	}
	return definition.ObjectStoreDestination{}, e.New(e.CodePipelineDrift, "object target has no configured destination")
}

func validateObjectPublicationJob(plan *Plan, node Node, stage githubStage, job githubJob) error {
	wantRole := ""
	if IsObjectPublicationNode(plan, node.ID) {
		destination, err := objectPublicationDestination(plan, node.Target)
		if err != nil {
			return err
		}
		wantRole = "${{ vars['" + strings.TrimPrefix(destination.S3.RoleRef, "github:") + "'] }}"
		permissions, err := githubStagePermissions(plan, stage)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(job.Permissions, permissions) {
			return e.New(e.CodeEngineCapability, "object publisher requires its exact workload and source-read permissions")
		}
		if err := validateGitHubRegistryEnvironment(plan, node.Kind, node.Target, stage.ID, job); err != nil {
			return err
		}
	}
	for key, value := range job.Env {
		if strings.HasPrefix(key, "NVKR_S3_") && (key != "NVKR_S3_ROLE_ARN" || wantRole == "" || value != wantRole) {
			return e.New(e.CodeEngineCapability, "workload role must stay on its exact object publication job")
		}
	}
	if job.Env["NVKR_S3_ROLE_ARN"] != wantRole {
		return e.New(e.CodeEngineCapability, "object workload role differs from its configured reference")
	}
	for _, step := range job.Steps {
		for key := range step.Env {
			if strings.HasPrefix(key, "NVKR_S3_") {
				return e.New(e.CodeEngineCapability, "object workload role cannot be rebound by a step")
			}
		}
	}
	return nil
}

func withObjectPublicationTargets(environment definition.Environment, packages []packageSpec, targets map[string]Target, routes []Route, err error) ([]packageSpec, map[string]Target, []Route, error) {
	if err != nil || environment.Publication == nil {
		return packages, targets, routes, err
	}
	for _, destination := range environment.Publication.ObjectStores {
		name := "object-" + destination.Name
		if _, exists := targets[name]; exists {
			return nil, nil, nil, e.New(e.CodeInvalidCatalog, "object publication target collides with %q", name)
		}
		target := targets["release"]
		target.Kind, target.ImageTags = TargetObject, nil
		targets[name] = target
	}
	return packages, targets, routes, nil
}

func addObjectPublicationDataflow(plan *Plan, runsOn string) error {
	if plan.Environment.Publication == nil {
		return nil
	}
	nodes, artifacts := planIndex(plan)
	for _, destination := range plan.Environment.Publication.ObjectStores {
		name := "object-" + destination.Name
		inputs := []string{"publication/release", "signature/release", "predicate/release", "verification/release", "tool/" + plan.Definition.CosignTool}
		for _, input := range inputs {
			if artifacts[input].ID == "" {
				return e.New(e.CodePipelineDrift, "object publication omits original verified publication input %q", input)
			}
		}
		id, output := "publish-object/"+destination.Name, "object-publication/"+destination.Name
		nodes[id] = Node{ID: id, Kind: NodePublish, Target: name, Inputs: uniqueSorted(inputs), Outputs: []string{output}, RunsOn: runsOn, Network: ir.NetworkDefault}
		artifacts[output] = Artifact{ID: output, Name: destination.Name, Kind: ArtifactObjectPublication, Producer: id, Target: name, Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", "object-publications", destination.Name+".json"))}
	}
	plan.Graph.Nodes, plan.Graph.Artifacts = mapNodes(nodes), mapArtifacts(artifacts)
	return nil
}

// IsObjectPublicationNode is a descriptive dispatch predicate; the execution
// owner must still admit the complete Plan before any effects.
func IsObjectPublicationNode(plan *Plan, nodeID string) bool {
	if plan == nil || plan.Environment.Publication == nil || len(plan.Environment.Publication.ObjectStores) == 0 {
		return false
	}
	for _, node := range plan.Graph.Nodes {
		if node.ID == nodeID {
			return node.Kind == NodePublish && plan.Targets[node.Target].Kind == TargetObject
		}
	}
	return false
}

// ObjectPublicationForNode rederives the complete Plan before projecting one
// exact configured destination and its required ordinary publish operation.
func ObjectPublicationForNode(plan *Plan, nodeID string) (definition.ObjectStoreDestination, Node, error) {
	if plan == nil {
		return definition.ObjectStoreDestination{}, Node{}, e.New(e.CodeUsage, "object publication requires a Plan")
	}
	if err := plan.Validate(); err != nil {
		return definition.ObjectStoreDestination{}, Node{}, err
	}
	for _, node := range plan.Graph.Nodes {
		if node.ID != nodeID || !IsObjectPublicationNode(plan, nodeID) {
			continue
		}
		for _, destination := range plan.Environment.Publication.ObjectStores {
			if node.Target == "object-"+destination.Name {
				return destination, node, nil
			}
		}
	}
	return definition.ObjectStoreDestination{}, Node{}, e.New(e.CodePipelineDrift, "object publication does not bind a configured destination")
}

// RunGitHubObjectPublicationNode uses the normal hosted admission, operation
// accounting and reporting around a lifecycle-owned publication callback.
func RunGitHubObjectPublicationNode(ctx context.Context, plan *Plan, nodeID string, options GitHubStepOptions, execute func(context.Context, *Plan, Node, GitHubStepOptions) error) error {
	if execute == nil {
		return e.New(e.CodeUsage, "object publication requires its lifecycle executor")
	}
	if _, _, err := ObjectPublicationForNode(plan, nodeID); err != nil {
		return err
	}
	return runGitHubNode(ctx, plan, nodeID, options, execute)
}

// ObjectPublicationSourceOptions projects the same pinned verifier and scoped
// source credentials as ordinary OCI verification. These are descriptions;
// lifecycle still authenticates the source's complete exact-input closure.
func ObjectPublicationSourceOptions(ctx context.Context, workspace string, plan *Plan, nodeID string) (sign.CosignOCIOptions, delivery.RegistryOptions, error) {
	if err := snapshotContext(ctx); err != nil {
		return sign.CosignOCIOptions{}, delivery.RegistryOptions{}, err
	}
	if _, _, err := ObjectPublicationForNode(plan, nodeID); err != nil {
		return sign.CosignOCIOptions{}, delivery.RegistryOptions{}, err
	}
	local, err := relocateFactoryPlan(plan, workspace)
	if err != nil {
		return sign.CosignOCIOptions{}, delivery.RegistryOptions{}, err
	}
	_, artifacts := planIndex(local)
	tool := artifacts["tool/"+plan.Definition.CosignTool]
	target, err := runtimeTarget(plan, "release")
	if err != nil {
		return sign.CosignOCIOptions{}, delivery.RegistryOptions{}, err
	}
	credential, err := registryCredential(plan, Node{Kind: NodeVerifyPublication, Target: "release"})
	if err != nil {
		return sign.CosignOCIOptions{}, delivery.RegistryOptions{}, err
	}
	return sign.CosignOCIOptions{Binary: tool.Path, BinaryDigest: toolExecutableDigest(plan, plan.Definition.CosignTool), Identity: plan.Environment.Signing.Identity, Issuer: plan.Environment.Signing.Issuer, Credential: credential}, delivery.RegistryOptions{PlainHTTP: target.PlainHTTP, Anonymous: target.Anonymous, Username: credential.Username, Password: credential.Password}, nil
}

// ObjectPublicationSourceReference reads the existing source carrier; its value
// is only an expectation until the lifecycle source verifier authenticates it.
func ObjectPublicationSourceReference(ctx context.Context, workspace string, plan *Plan, nodeID string) (string, error) {
	if err := snapshotContext(ctx); err != nil {
		return "", err
	}
	if _, _, err := ObjectPublicationForNode(plan, nodeID); err != nil {
		return "", err
	}
	local, err := relocateFactoryPlan(plan, workspace)
	if err != nil {
		return "", err
	}
	_, artifacts := planIndex(local)
	subject, err := readPublicationSubject(ctx, artifacts["publication/release"].Path)
	if err != nil {
		return "", err
	}
	target, err := runtimeTarget(plan, "release")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(subject.Ref, target.Ref+"@") {
		return "", e.New(e.CodeVerifyFailed, "object publication source differs from its planned repository")
	}
	return subject.Ref, nil
}

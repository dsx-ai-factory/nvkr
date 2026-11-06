// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/imagepreflight"
	"sigs.k8s.io/yaml"
)

// This projection deliberately consumes the existing admitted image set. The
// complete image-use inventory owns future additions; preflight does not infer
// public access from the absence of a reader or from publication configuration.
func githubPreflightSubjects(plan *Plan) (map[string][]imagepreflight.Subject, error) {
	result := map[string][]imagepreflight.Subject{}
	if plan.Environment.ToolchainAccess == nil {
		return result, nil
	}
	classifier, err := plan.Environment.ToolchainAccess.ImageClassifier()
	if err != nil {
		return nil, err
	}
	add := func(image, platform, runner string, startup bool) error {
		if image == "" {
			return nil
		}
		private, err := classifier.ClassifyImage(image)
		if err != nil {
			return err
		}
		route := imagepreflight.Anonymous
		if private {
			reader := plan.Environment.ToolchainAccess.Reader
			if startup || reader == nil {
				return e.New(e.CodeEngineCapability, "image preflight requires an admitted reader; private startup images have no provider startup route")
			}
			route = reader.CredentialRef + "|" + reader.UsernameRef
		}
		result[runner] = append(result[runner], imagepreflight.Subject{Image: image, Platform: platform, Route: route, Placement: runner})
		return nil
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		return nil, err
	}
	for _, stage := range stages {
		for _, node := range stage.Nodes {
			if err := add(node.Container, node.Platform, node.RunsOn, false); err != nil {
				return nil, err
			}
		}
		node := stage.Nodes[0]
		if stage.DiskOutput != "" && githubDiskInspectionRequired(plan) {
			if err := add(plan.Environment.DiskInspectionProfiles[node.Platform].Image, node.Platform, node.RunsOn, false); err != nil {
				return nil, err
			}
		}
		for _, image := range append([]string{plan.Environment.Runtime.RuntimeImage}, plan.Environment.Runtime.Services...) {
			if err := add(image, node.Platform, node.RunsOn, true); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func githubImagePreflightID(runner string) string {
	return "preflight-images-" + shortStableName(runner)
}

func githubImagePreflightJobs(plan *Plan) ([]string, error) {
	placements, err := githubPreflightSubjects(plan)
	if err != nil {
		return nil, err
	}
	var jobs []string
	for _, runner := range sortedKeys(placements) {
		jobs = append(jobs, githubImagePreflightID(runner))
	}
	return uniqueSorted(jobs), nil
}

// PreflightGitHubImages admits the final loaded Plan before resolving its
// dedicated reader. Reports are availability diagnostics only; no receipt is
// issued and per-job materialization and runtime verification remain required.
func PreflightGitHubImages(ctx context.Context, plan *Plan, runner string) (*imagepreflight.Report, error) {
	return preflightGitHubImages(ctx, plan, runner, imagepreflight.Observe)
}

func preflightGitHubImages(ctx context.Context, plan *Plan, runner string, observe func(context.Context, string, []imagepreflight.Subject, imagepreflight.CredentialResolver) (*imagepreflight.Report, error)) (*imagepreflight.Report, error) {
	if err := admitGitHubPlan(ctx, plan); err != nil {
		return nil, err
	}
	placements, err := githubPreflightSubjects(plan)
	if err != nil {
		return nil, err
	}
	subjects, ok := placements[runner]
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "image preflight requires an admitted placement")
	}
	digest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	return observe(ctx, digest, subjects, func(_ context.Context, route string) (imagepreflight.Credential, error) {
		reader := plan.Environment.ToolchainAccess.Reader
		if reader == nil || route != reader.CredentialRef+"|"+reader.UsernameRef {
			return imagepreflight.Credential{}, e.New(e.CodeEngineCapability, "image preflight reader route differs")
		}
		return imagepreflight.Credential{Username: os.Getenv(githubImageUser), Token: os.Getenv(githubImageToken)}, nil
	})
}

func writeGitHubImagePreflights(out *bytes.Buffer, plan *Plan, bootstrap map[string]string, actions githubActionRefs) error {
	placements, err := githubPreflightSubjects(plan)
	if err != nil {
		return err
	}
	var needs []string
	for _, job := range bootstrap {
		needs = append(needs, job)
	}
	needs = uniqueSorted(needs)
	for _, runner := range sortedKeys(placements) {
		fmt.Fprintf(out, "  %s:\n    name: %s\n    runs-on: %s\n    timeout-minutes: %d\n    needs: [%s]\n    if: ${{ %s }}\n", githubImagePreflightID(runner), yamlSingle("Observe image reader availability ("+runner+")"), yamlSingle(runner), githubSupportJobTimeoutMinutes, strings.Join(needs, ", "), githubDispatchCondition(plan))
		if runner == "arc-amd64-kvm" || runner == "arc-arm64-kvm" {
			fmt.Fprintf(out, "    environment: %s\n", yamlSingle(githubARCEnvironmentFor(plan)))
		}
		fmt.Fprintln(out, "    steps:")
		if err := writeGitHubPlatformCheck(out, plan, runner); err != nil {
			return err
		}
		actions.writeCheckoutStep(out)
		actions.writeDownloadStep(out, "NVKR runner", "nvkr-exec-"+shortStableName(runner), githubHelperDownloadPath(plan))
		writeGitHubPrepareHelperStep(out, plan)
		fmt.Fprintln(out, "      - name: Observe exact image manifests\n        timeout-minutes: 3")
		private := false
		for _, subject := range placements[runner] {
			private = private || subject.Route != imagepreflight.Anonymous
		}
		if private {
			fmt.Fprintln(out, "        env:")
			for _, key := range sortedKeys(githubImageEnvironment(plan)) {
				fmt.Fprintf(out, "          %s: %s\n", key, yamlSingle(githubImageEnvironment(plan)[key]))
			}
		}
		fmt.Fprintf(out, "        run: %s\n", githubRunnerCommand(plan, "preflight-images", "runner", runner))
	}
	return nil
}

// Validate the complete support jobs before removing them from the legacy
// stage validator's view. Dependency admission has already restored full needs.
func validateGitHubImagePreflights(plan *Plan, document *githubWorkflowDocument, actions githubActionRefs) ([]string, error) {
	bootstrap := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		execution, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return nil, err
		}
		bootstrap[execution.RunsOn] = "build-runner-" + shortStableName(execution.RunsOn)
	}
	var out bytes.Buffer
	fmt.Fprintln(&out, "jobs:")
	if err := writeGitHubImagePreflights(&out, plan, bootstrap, actions); err != nil {
		return nil, err
	}
	var expected githubWorkflowDocument
	if err := yaml.UnmarshalStrict(out.Bytes(), &expected); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "decode image preflight jobs")
	}
	for id, job := range expected.Jobs {
		if !reflect.DeepEqual(document.Jobs[id], job) {
			return nil, e.New(e.CodeEngineCapability, "image preflight job %q differs from its admitted placement, reader, Plan or dependencies", id)
		}
		delete(document.Jobs, id)
	}
	return sortedKeys(expected.Jobs), nil
}

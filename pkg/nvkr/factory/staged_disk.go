// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func stagedDiskRoute(plan *Plan, route string) bool {
	if plan == nil || !plan.Environment.GitHubDevelopment || plan.Environment.GitHubARC != "native-kvm-v1" || !PerOutputPublication(plan) {
		return false
	}
	for _, output := range plan.LifecycleOutputs {
		if output.Kind == PackageDiskImage && outputPublicationRoute(output.Name) == route {
			return true
		}
	}
	return false
}

func stagedDiskReferenceID(route string) string { return "staged/" + route }

func stagedDiskEnvironment() map[string]string {
	return map[string]string{
		"NVKR_CREDENTIAL":        "${{ secrets." + development.StageWriteSecret + " }}",
		"NVKR_REGISTRY_USER":     "${{ vars." + development.StageWriteUserVariable + " }}",
		githubRepositoryTokenEnv: "${{ github.token }}",
	}
}

// StageGitHubDisk admits only the development ARC route and pushes its exact
// preparation in a separate fixed credential step, after producer execution.
func StageGitHubDisk(ctx context.Context, plan *Plan, output string, options GitHubStepOptions) error {
	return stageGitHubDisk(ctx, plan, output, options, runtime.GOOS+"/"+runtime.GOARCH, cistore.AdmitPublication)
}

func stageGitHubDisk(ctx context.Context, plan *Plan, output string, options GitHubStepOptions, platform string,
	admit func(context.Context, string, []string, cistore.PublicationOptions) error,
) error {
	if err := admitGitHubPlan(ctx, plan); err != nil {
		return err
	}
	routeName := outputPublicationRoute(output)
	if !stagedDiskRoute(plan, routeName) {
		return e.New(e.CodeEngineCapability, "destination staging requires a development ARC disk")
	}
	for _, key := range []string{sourceTokenEnv, githubImageToken, "ACTIONS_ID_TOKEN_REQUEST_TOKEN"} {
		if os.Getenv(key) != "" {
			return e.New(e.CodeEngineCapability, "disk staging contains unrelated authority")
		}
	}
	if os.Getenv("NVKR_CREDENTIAL") == "" || os.Getenv("NVKR_REGISTRY_USER") == "" {
		return e.New(e.CodeTrustPolicyMissing, "disk staging requires its stage-only writer")
	}
	if !strings.HasPrefix(platform, "linux/") {
		return e.New(e.CodeEngineCapability, "destination staging requires native Linux")
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		return err
	}
	matched := false
	for _, stage := range stages {
		if stage.DiskOutput == output {
			matched = stage.Nodes[0].Platform == platform
		}
	}
	if !matched {
		return e.New(e.CodeEngineCapability, "disk staging requires its native ARC platform")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	var route Route
	for _, candidate := range plan.Routes {
		if candidate.Name == routeName {
			route = candidate
		}
	}
	target, err := runtimeTarget(plan, route.Target)
	if err != nil {
		return err
	}
	_, artifacts := planIndex(plan)
	source := artifacts[route.Input].Path
	root, err := admitGitHubPublication(ctx, target, false, source, admit)
	if err != nil {
		return err
	}
	inspection, err := delivery.InspectMetadata(ctx, artifacts[preparedEvidenceMetadataID(routeName)].Path)
	if err != nil {
		return err
	}
	if root != inspection.RootDigest {
		return e.New(e.CodeVerifyFailed, "staging preparation changed after destination admission")
	}
	if err := admitStagedDiskMetadata(plan, route, inspection, sign.Subject{Ref: target.Ref + "@" + root, Digest: root}); err != nil {
		return err
	}
	ref, err := delivery.StagePreparedDisk(ctx, source, target.Ref, root, stagedRegistryOptions(target))
	if err != nil {
		return err
	}
	if err := writeFactoryFile(ctx, artifacts[stagedDiskReferenceID(routeName)].Path, []byte(ref+"\n"), 0o644); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(options.Stdout, "Staged immutable disk preparation: %s\n", ref); err != nil {
		return e.Wrap(e.CodeInternal, err, "report staged disk reference")
	}
	return nil
}

func stagedRegistryOptions(target Target) delivery.RegistryOptions {
	return delivery.RegistryOptions{PlainHTTP: target.PlainHTTP, Anonymous: target.Anonymous,
		Username: os.Getenv("NVKR_REGISTRY_USER"), Password: os.Getenv("NVKR_CREDENTIAL")}
}

func stagedDiskSubject(ctx context.Context, plan *Plan, route Route, artifacts map[string]Artifact) (sign.Subject, Target, error) {
	target, err := runtimeTarget(plan, route.Target)
	if err != nil {
		return sign.Subject{}, Target{}, err
	}
	subject, err := readPublicationSubject(ctx, artifacts[stagedDiskReferenceID(route.Name)].Path)
	if err != nil {
		return sign.Subject{}, Target{}, err
	}
	if err := admitDevelopmentSubject(plan, route.Target, subject); err != nil {
		return sign.Subject{}, Target{}, err
	}
	// A failed-job retry may retain a successful producer from an earlier
	// attempt of this Run. Complete in that original immutable namespace.
	target.Ref = strings.TrimSuffix(subject.Ref, "@"+subject.Digest)
	return subject, target, nil
}

func admitStagedDiskMetadata(plan *Plan, route Route, inspection delivery.Inspection, subject sign.Subject) error {
	if err := admitEvidenceMetadata(plan, route, inspection); err != nil {
		return err
	}
	namespace, err := development.ParseNamespace(plan.Environment.Publication.RepositoryPrefix)
	if err != nil {
		return err
	}
	prepared, err := namespace.ParseReference(strings.TrimSuffix(subject.Ref, "@"+subject.Digest))
	if err != nil {
		return err
	}
	invocation := "https://github.com/" + namespace.Repository() + "/actions/runs/" + prepared.RunID() + "/attempts/" + prepared.Attempt()
	if inspection.Root.Release.InvocationID != invocation {
		return e.New(e.CodeVerifyFailed, "staged disk destination differs from its preparation invocation")
	}
	return nil
}

func executeStagedDiskPublish(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, stdout io.Writer) error {
	return executeStagedDiskPublishWithAdmission(ctx, plan, node, artifacts, stdout, cistore.AdmitPublication, productionOCISigner)
}

func executeStagedDiskPublishWithAdmission(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, stdout io.Writer,
	admit func(context.Context, string, []string, cistore.PublicationOptions) error, newSigner signerFor,
) error {
	route, ok := preparedEvidenceRoute(plan, node)
	if !ok || !stagedDiskRoute(plan, route.Name) {
		return e.New(e.CodeEngineCapability, "staged publication requires its disk route")
	}
	if _, err := registryCredential(plan, node); err != nil {
		return err
	}
	subject, target, err := stagedDiskSubject(ctx, plan, route, artifacts)
	if err != nil {
		return err
	}
	controls := artifacts[attestedEvidenceMetadataID(route.Name)]
	inspection, err := delivery.InspectMetadata(ctx, controls.Path)
	if err != nil {
		return err
	}
	if err := admitStagedDiskMetadata(plan, route, inspection, subject); err != nil {
		return err
	}
	root, err := admitGitHubPublication(ctx, target, false, controls.Path, admit)
	if err != nil {
		return err
	}
	if root != inspection.RootDigest {
		return e.New(e.CodeVerifyFailed, "staged completion controls changed after destination admission")
	}
	signer, err := newSigner(ctx, plan, node, artifacts)
	if err != nil {
		return err
	}
	verifier, ok := signer.(sign.SBOMAttester)
	if !ok {
		return e.New(e.CodeEngineCapability, "staged completion requires SBOM verifier")
	}
	workspace := filepath.Dir(controls.Path)
	ref, err := delivery.CompleteStagedDisk(ctx, subject.Ref, filepath.Join(workspace, route.Name+"-readback"), controls.Path, root, stagedRegistryOptions(target), verifier.VerifySBOM)
	if err != nil {
		return err
	}
	metadata, err := signNodeOutput(node, artifacts, ArtifactSigningMetadata)
	if err != nil {
		return err
	}
	if _, err := delivery.CopyMetadataWithExpectedRoot(ctx, controls.Path, metadata.Path, root); err != nil {
		return err
	}
	output, err := signNodeOutput(node, artifacts, "published-reference")
	if err != nil {
		return err
	}
	if err := writeFactoryFile(ctx, output.Path, []byte(ref+"\n"), 0o644); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "Published staged disk controls: %s\n", ref); err != nil {
		return e.Wrap(e.CodeInternal, err, "report completed disk reference")
	}
	return nil
}

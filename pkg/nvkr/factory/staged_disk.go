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
	if runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "destination staging requires native Linux")
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		return err
	}
	matched := false
	for _, stage := range stages {
		if stage.DiskOutput == output {
			matched = stage.Nodes[0].Platform == runtime.GOOS+"/"+runtime.GOARCH
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
	root, err := admitGitHubPublication(ctx, target, false, source, cistore.AdmitPublication)
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
	if err := admitEvidenceMetadata(plan, route, inspection); err != nil {
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
	if subject.Ref != target.Ref+"@"+subject.Digest {
		return sign.Subject{}, Target{}, e.New(e.CodeVerifyFailed, "staged disk differs from its exact destination")
	}
	return subject, target, nil
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
	if err := admitEvidenceMetadata(plan, route, inspection); err != nil {
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

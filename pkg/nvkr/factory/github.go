// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	GitHubWorkflowPath            = ".github/workflows/nvkr-run.yml"
	FactoryPlanPath               = ".github/nvkr-run/plan.json"
	GitHubDevelopmentWorkflowPath = ".github/workflows/nvkr-dev.yml"
	GitHubDevelopmentPlanPath     = ".github/nvkr-dev/plan.json"
	GitHubDevelopmentManifestPath = ".github/nvkr-dev/manifest.json"
	// Protected provider objects must be provisioned under these exact names.
	githubReleaseEnvironment            = "nvkr-release"
	githubARCEnvironment                = "nvkr-arc"
	githubDevelopmentReleaseEnvironment = "nvkr-dev-release"
	githubDevelopmentARCEnvironment     = "nvkr-dev-arc"
	githubExecutionCondition            = "github.event_name == 'workflow_dispatch' && github.ref == format('refs/heads/{0}', github.event.repository.default_branch)"
	githubSupportJobTimeoutMinutes      = 15
	githubDefaultStageTimeoutMinutes    = 60
	githubMaximumJobTimeoutMinutes      = 360
)

var githubDevelopmentLayout = generationLayout{workflowPath: GitHubDevelopmentWorkflowPath,
	manifestPath: GitHubDevelopmentManifestPath, planPath: GitHubDevelopmentPlanPath}

// Paths are selected from the admitted Environment policy, never supplied
// as an independent workflow override. Development and production own disjoint
// directories so either can regenerate without retiring the other's members.
func githubLayoutFor(plan *Plan) generationLayout {
	if plan.Environment.GitHubDevelopment {
		return githubDevelopmentLayout
	}
	return githubBundleLayout(plan.Environment.GitHubBundle)
}

func githubWorkflowName(plan *Plan) string {
	if plan.Environment.GitHubDevelopment {
		return "nvkr-dev-" + plan.Name
	}
	if plan.Environment.GitHubBundle != "" {
		return "nvkr-project-" + plan.Environment.GitHubBundle
	}
	return "nvkr-run-" + plan.Name
}

func githubBundleLayout(name string) generationLayout {
	if name == "" {
		return generationLayout{workflowPath: GitHubWorkflowPath,
			manifestPath: GeneratedManifestPath, planPath: FactoryPlanPath}
	}
	root := ".github/nvkr-run-" + name
	return generationLayout{workflowPath: ".github/workflows/nvkr-run-" + name + ".yml",
		manifestPath: root + "/manifest.json", planPath: root + "/plan.json"}
}

// RenderGitHub emits a locally inspectable workflow and its complete canonical Plan,
// then runs the semantic static validator before returning any bytes.
func RenderGitHub(ctx context.Context, plan *Plan) (*GeneratedBundle, error) {
	return renderGitHub(ctx, plan, true)
}

// validate=false is private to the validator's exact regeneration comparison.
func renderGitHub(ctx context.Context, plan *Plan, validate bool) (*GeneratedBundle, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "GitHub factory rendering requires a context")
	}
	if plan == nil {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub factory rendering requires a plan")
	}
	if len(plan.Environment.Runtime.NativeToolchains) != 0 {
		return nil, e.New(e.CodeEngineCapability, "native toolchain bundle execution is local-only")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if err := rejectUnsupportedRuntimeStorage(plan, "GitHub generation"); err != nil {
		return nil, err
	}
	if err := admitGitHubRequirements(plan, "GitHub generation"); err != nil {
		return nil, err
	}
	if GitHubDiskBuildOnly(plan) {
		if _, err := githubDiskBuildFinalizerRunner(plan); err != nil {
			return nil, err
		}
	}
	if err := admitGitHubPlatforms(plan); err != nil {
		return nil, err
	}
	if plan.DefinitionPath == "" || plan.EnvironmentPath == "" {
		return nil, e.New(e.CodeEngineCapability, "GitHub generation requires repository-relative definition and delivery source paths")
	}
	actions, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		return nil, err
	}
	linkerFlags, err := generatorLinkerFlags(plan.GeneratorVersion)
	if err != nil {
		return nil, err
	}
	layout := githubLayoutFor(plan)
	needsProjection, err := githubNeedsForPlan(ctx, plan)
	if err != nil {
		return nil, err
	}
	files, planDigest, err := renderStaticInputs(ctx, plan, layout)
	if err != nil {
		return nil, err
	}

	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	stages, nodeJobs, err := githubStages(plan)
	if err != nil {
		return nil, err
	}
	bootstrapJobs := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		node, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return nil, err
		}
		bootstrapJobs[node.RunsOn] = "build-runner-" + shortStableName(node.RunsOn)
	}

	var body bytes.Buffer
	fmt.Fprintf(&body, "# GENERATED BY nvkr run generate — DO NOT EDIT\n")
	fmt.Fprintf(&body, "# plan: %s\n# manifest: %s\n", layout.planPath, layout.manifestPath)
	fmt.Fprintf(&body, "# qualification: %s (runtime receipts are not fabricated)\n", plan.Qualification)
	fmt.Fprintf(&body, "name: %s\n", githubWorkflowName(plan))
	fmt.Fprintf(&body, "'on':\n  workflow_dispatch: {}\n")
	fmt.Fprintf(&body, "permissions:\n  contents: read\n\n")
	fmt.Fprintf(&body, "concurrency:\n  group: nvkr-run-${{ github.workflow }}-${{ github.ref }}\n  cancel-in-progress: %t\n", githubCancelInProgress(plan))
	fmt.Fprintf(&body, "jobs:\n")
	for _, runner := range sortedKeys(bootstrapJobs) {
		jobID := bootstrapJobs[runner]
		fmt.Fprintf(&body, "  %s:\n", jobID)
		fmt.Fprintf(&body, "    name: %s\n", yamlSingle("Prepare NVKR runner ("+runner+")"))
		fmt.Fprintf(&body, "    runs-on: %s\n", yamlSingle(githubBootstrapRunner(runner)))
		fmt.Fprintf(&body, "    timeout-minutes: %d\n", githubSupportJobTimeoutMinutes)
		if plan.Environment.GitHubRuntimePlan && runner != sortedKeys(bootstrapJobs)[0] {
			fmt.Fprintf(&body, "    needs: [%s]\n", bootstrapJobs[sortedKeys(bootstrapJobs)[0]])
		}
		fmt.Fprintf(&body, "    if: ${{ %s }}\n", githubDispatchCondition(plan))
		fmt.Fprintf(&body, "    steps:\n")
		if err := writeGitHubPlatformCheck(&body, plan, runner); err != nil {
			return nil, err
		}
		if runner == sortedKeys(bootstrapJobs)[0] {
			writeGitHubRunSummary(&body, githubRunStartSummary(plan))
		}
		actions.writeCheckoutStep(&body)
		actions.writeSetupGoStep(&body)
		helperEnvironment := "CGO_ENABLED=0"
		if len(plan.Environment.GitHubRunners) > 0 {
			platform, err := authored.GitHubRunnerPlatform(githubBootstrapRunner(runner))
			if err != nil {
				return nil, err
			}
			helperEnvironment += " GOOS=linux GOARCH=" + strings.TrimPrefix(platform, "linux/")
		}
		fmt.Fprintf(&body, "      - name: Build runner from this checkout\n        run: %s go build -trimpath -ldflags %s -o nvkr-exec ./cmd/nvkr\n", helperEnvironment, shellQuote(linkerFlags))
		command := generationCommand(plan, EngineGitHub)
		if plan.Environment.GitHubRuntimePlan {
			if runner == sortedKeys(bootstrapJobs)[0] {
				command = githubRuntimeGenerationCommand(plan)
			} else {
				actions.writeDownloadStep(&body, "NVKR runtime inputs", githubRuntimeInputsArtifact, githubRuntimeInputDirectory)
				writeGitHubShellStep(&body, "Install NVKR runtime inputs", []string{githubRuntimeInstallCommand(plan)})
			}
		}
		writeCatalogValidationStep(&body, plan, "Verify committed Run generation is current", "validate-catalog", command)
		if plan.Environment.GitHubRuntimePlan && runner == sortedKeys(bootstrapJobs)[0] {
			writeGitHubRuntimeInputsUpload(&body, plan, actions)
		}
		actions.writeUploadStep(&body, plan, "NVKR runner", "nvkr-exec-"+shortStableName(runner), "nvkr-exec")
	}
	for _, stage := range stages {
		node := stage.Nodes[0]
		timeoutMinutes, err := githubStageTimeoutMinutes(stage)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "GitHub factory rendering canceled")
		}
		jobID := stage.ID
		needs := stage.needs(artifacts, nodeJobs, bootstrapJobs[node.RunsOn])
		fmt.Fprintf(&body, "  %s:\n", jobID)
		fmt.Fprintf(&body, "    name: %s\n", yamlSingle(stage.Name))
		fmt.Fprintf(&body, "    runs-on: %s\n", yamlSingle(node.RunsOn))
		fmt.Fprintf(&body, "    timeout-minutes: %d\n", timeoutMinutes)
		// Engine-agnostic environment config (ADR-18 §8), rendered in GitHub syntax: a docker-capable
		// job container, dind service containers, and job env — for a runner that lacks them
		// natively. GitHub provisions Go via setup-go.
		if img := strings.TrimSpace(plan.Environment.Runtime.RuntimeImage); img != "" {
			fmt.Fprintf(&body, "    container: %s\n", yamlSingle(img))
		}
		if svcs := plan.Environment.Runtime.Services; len(svcs) > 0 {
			fmt.Fprintln(&body, "    services:")
			for i, svc := range svcs {
				fmt.Fprintf(&body, "      svc-%02d:\n        image: %s\n", i, yamlSingle(svc))
			}
		}
		fmt.Fprintf(&body, "    needs: [%s]\n", strings.Join(needs, ", "))
		fmt.Fprintf(&body, "    if: %s\n", stage.condition(plan))
		if stage.DiskOutput != "" {
			fmt.Fprintf(&body, "    environment: %s\n", yamlSingle(githubARCEnvironmentFor(plan)))
		}
		if githubProtectedPublication(plan, node) {
			fmt.Fprintf(&body, "    environment: %s\n", yamlSingle(githubReleaseEnvironmentFor(plan)))
		}
		env := map[string]string{}
		privateImages := len(githubStagePrivateImages(plan, stage)) > 0
		permissions, err := githubStagePermissions(plan, stage)
		if err != nil {
			return nil, err
		}
		writeGitHubPermissions(&body, permissions)
		for k, v := range plan.Environment.Runtime.Variables {
			env[k] = v
		}
		if node.Kind == NodePublish || signFamilyNode(node.Kind) {
			registryEnv, err := githubRegistryEnvironment(plan, node.Kind, node.Target)
			if err != nil {
				return nil, err
			}
			for key, value := range registryEnv {
				env[key] = value
			}
		}
		if token := githubPublicationRepositoryToken(plan, node); token != "" {
			env[githubRepositoryTokenEnv] = token
		}
		if len(env) > 0 {
			fmt.Fprintln(&body, "    env:")
			for _, k := range sortedKeys(env) {
				fmt.Fprintf(&body, "      %s: %s\n", k, yamlSingle(env[k]))
			}
		}
		fmt.Fprintf(&body, "    steps:\n")
		if err := writeGitHubPlatformCheck(&body, plan, node.RunsOn); err != nil {
			return nil, err
		}
		if stage.DiskOutput != "" {
			check := githubKVMAccessCheck()
			fmt.Fprintf(&body, "      - name: %s\n        timeout-minutes: %d\n        run: |-\n", check.Name, check.TimeoutMinutes)
			for _, line := range strings.Split(check.Run, "\n") {
				fmt.Fprintf(&body, "          %s\n", line)
			}
		}
		actions.writeCheckoutStep(&body)
		actions.writeDownloadStep(&body, "NVKR runner", "nvkr-exec-"+shortStableName(node.RunsOn), githubHelperDownloadPath(plan))
		// upload-artifact/download-artifact do not preserve the execute bit, so the helper
		// arrives non-executable. Restore it in a dedicated step IMMEDIATELY after the helper
		// download, before ANY ./nvkr-exec invocation: the input Restore steps below are
		// themselves helper invocations, so a chmod folded into the later Execute step leaves
		// every input-bearing job failing at its first Restore.
		writeGitHubPrepareHelperStep(&body, plan)
		if err := writeGitHubStageInputSteps(&body, plan, stage, artifacts, nodeJobs, actions); err != nil {
			return nil, err
		}
		// Operator setup before the helper runs (e.g. `docker login` to pull a private,
		// digest-pinned toolchain), configured by the admitted Environment.
		if setup := plan.Environment.Runtime.Setup; len(setup) > 0 {
			fmt.Fprintf(&body, "      - name: Configure build environment\n        run: |\n")
			for _, line := range setup {
				fmt.Fprintf(&body, "          %s\n", line)
			}
		}
		if privateImages {
			fmt.Fprintln(&body, "      - name: Prepare private native toolchains\n        env:")
			for _, key := range sortedKeys(githubImageEnvironment()) {
				fmt.Fprintf(&body, "          %s: %s\n", key, yamlSingle(githubImageEnvironment()[key]))
			}
			fmt.Fprintf(&body, "        run: %s\n", githubRunnerCommand(plan, "prepare-images", "stage", stage.ID))
		}
		if stage.DiskOutput != "" {
			fmt.Fprintf(&body, "      - name: %s\n        id: run-disk\n", yamlSingle(stage.Name))
			if minutes := githubDiskStepTimeoutMinutes(stage); minutes != 0 {
				fmt.Fprintf(&body, "        timeout-minutes: %d\n", minutes)
			}
			fmt.Fprintf(&body, "        run: %s\n", githubRunnerCommand(plan, "run-disk", "output", stage.DiskOutput))
			writeGitHubPackerDiagnosticStep(&body, stage, actions)
			if githubDiskInspectionRequired(plan) {
				// Diagnostics are short-lived descriptions, never planned evidence
				// carriers. Preserve them even when the live inspection fails.
				diagnostic := githubInspectionDiagnosticStep(actions, stage.DiskOutput)
				fmt.Fprintf(&body, "      - name: %s\n        uses: %s\n        if: %s\n        continue-on-error: %t\n        timeout-minutes: %d\n        with:\n          name: %s\n          path: %s\n          include-hidden-files: %s\n          if-no-files-found: %s\n          retention-days: %s\n",
					diagnostic.Name, diagnostic.Uses, diagnostic.If, diagnostic.ContinueOnError, diagnostic.TimeoutMinutes,
					yamlSingle(diagnostic.With["name"]), yamlSingle(diagnostic.With["path"]), diagnostic.With["include-hidden-files"], diagnostic.With["if-no-files-found"], diagnostic.With["retention-days"])
			}
			for _, member := range stage.Nodes {
				writeGitHubOutputSteps(&body, plan, member, artifacts, nodeJobs, actions)
			}
			continue
		}
		for _, node := range stage.Nodes {
			fmt.Fprintf(&body, "      - name: %s\n", yamlSingle(githubNodeLabel(plan, node)))
			fmt.Fprintf(&body, "        id: %s\n", githubJobID(node.ID))
			sourceEnv := githubSourceEnvironment(plan, node)
			if len(sourceEnv) > 0 {
				fmt.Fprintln(&body, "        env:")
				for _, key := range sortedKeys(sourceEnv) {
					fmt.Fprintf(&body, "          %s: %s\n", key, yamlSingle(sourceEnv[key]))
				}
			}
			fmt.Fprintln(&body, "        run: |")
			for _, line := range githubNodeCommands(node, plan) {
				fmt.Fprintf(&body, "          %s\n", line)
			}
			writeGitHubOutputSteps(&body, plan, node, artifacts, nodeJobs, actions)
		}
	}
	if plan.DefinitionDigest != "" && plan.EnvironmentDigest != "" {
		allNeeds, err := githubFinalizerJobs(plan, nodeJobs)
		if err != nil {
			return nil, err
		}
		runner := sortedKeys(bootstrapJobs)[0]
		if len(plan.Environment.GitHubRunners) > 0 {
			var ok bool
			runner, ok = plan.Environment.GitHubRunners[plan.Environment.UtilityPlatform]
			if !ok || bootstrapJobs[runner] == "" {
				return nil, e.New(e.CodeEngineCapability, "finalizer has no admitted utility runner/helper")
			}
		}
		runsOn := githubBootstrapRunner(runner)
		if GitHubDiskBuildOnly(plan) {
			runner, err = githubDiskBuildFinalizerRunner(plan)
			if err != nil {
				return nil, err
			}
			if bootstrapJobs[runner] == "" {
				return nil, e.New(e.CodeEngineCapability, "build-only finalizer has no matching native helper")
			}
			runsOn = runner
		}
		fmt.Fprintln(&body, "  nvkr-lifecycle-run:")
		fmt.Fprintln(&body, "    name: Finalize Run and check completeness")
		fmt.Fprintf(&body, "    runs-on: %s\n", yamlSingle(runsOn))
		fmt.Fprintf(&body, "    timeout-minutes: %d\n", githubFinalizerTimeoutMinutes(plan))
		fmt.Fprintf(&body, "    needs: [%s]\n", strings.Join(allNeeds, ", "))
		fmt.Fprintf(&body, "    if: ${{ always() && %s }}\n", githubDispatchCondition(plan))
		if GitHubDiskBuildOnly(plan) {
			fmt.Fprintf(&body, "    environment: %s\n", yamlSingle(githubARCEnvironment))
		}
		if PerOutputPublication(plan) {
			fmt.Fprintf(&body, "    environment: %s\n", yamlSingle(githubReleaseEnvironmentFor(plan)))
			registryEnv, err := githubRegistryEnvironment(plan, NodeVerifyPublication, "release")
			if err != nil {
				return nil, err
			}
			if len(registryEnv) > 0 {
				fmt.Fprintf(&body, "    env:\n      NVKR_REGISTRY_USER: %s\n      NVKR_CREDENTIAL: %s\n", yamlSingle(registryEnv["NVKR_REGISTRY_USER"]), yamlSingle(registryEnv["NVKR_CREDENTIAL"]))
			}
		}
		writeGitHubPermissions(&body, githubFinalizerPermissions(plan))
		fmt.Fprintln(&body, "    steps:")
		if err := writeGitHubPlatformCheck(&body, plan, runner); err != nil {
			return nil, err
		}
		actions.writeCheckoutStep(&body)
		actions.writeDownloadStep(&body, "NVKR runner", "nvkr-exec-"+shortStableName(runner), githubHelperDownloadPath(plan))
		writeGitHubPrepareHelperStep(&body, plan)
		if err := writeGitHubFinalizerRestoreSteps(&body, plan, nodeJobs); err != nil {
			return nil, err
		}
		finalize := fmt.Sprintf("./nvkr-exec run finalize --definition %s --environment %s --plan %s --attempt \"${{ github.run_id }}-${{ github.run_attempt }}\" --workspace .nvkr/factory --out .nvkr/run.json",
			shellQuote(plan.DefinitionPath), shellQuote(plan.EnvironmentPath), shellQuote(layout.planPath))
		if githubQualificationHandoffEnabled(plan) {
			finalize += " --handoff " + githubQualificationHandoffPath
		}
		writeCatalogValidationStep(&body, plan, "Finalize Run and check completeness", "finalize-catalog", finalize)
		// Keep the small terminal record beyond the transient transport window.
		fmt.Fprintf(&body, "      - name: Upload final Run record\n        id: %s\n        uses: %s\n        if: ${{ always() }}\n        with:\n          name: nvkr-lifecycle-run\n          path: .nvkr/run.json\n          if-no-files-found: error\n          retention-days: 7\n", githubRunUploadID, actions.Upload)
		if githubQualificationHandoffEnabled(plan) {
			writeGitHubQualificationHandoffStep(&body, actions)
		}
		summary := githubRunEndSummary(len(allNeeds))
		if len(needsProjection.Collectors) != 0 {
			summary = githubCollectedRunEndSummary(len(allNeeds))
		}
		writeGitHubRunSummary(&body, summary)
	}
	workflow, actionFiles, err := lowerGitHubActions(plan, body.Bytes(), actions)
	if err != nil {
		return nil, err
	}
	workflow, err = shareGitHubActions(plan, workflow, actionFiles, actions)
	if err != nil {
		return nil, err
	}
	workflow, err = lowerGitHubNeeds(ctx, plan, workflow)
	if err != nil {
		return nil, err
	}
	workflow, actionFiles, err = privatizeGitHubTransfers(plan, workflow, actionFiles, actions)
	if err != nil {
		return nil, err
	}
	for name, raw := range actionFiles {
		files[name] = raw
	}
	if err := validateGitHubWorkflowSize(workflow); err != nil {
		return nil, err
	}
	files[layout.workflowPath] = append([]byte(nil), workflow...)
	bundle := newGeneratedBundle(EngineGitHub, layout, files, planDigest)
	if validate {
		if err := ValidateGitHubBundle(ctx, plan, bundle); err != nil {
			return nil, err
		}
	}
	return bundle, nil
}

func writeGitHubPackerDiagnosticStep(out *bytes.Buffer, stage githubStage, actions githubActionRefs) {
	fmt.Fprintf(out, "      - name: Upload failed Packer diagnostics\n        if: %s\n        timeout-minutes: %d\n        uses: %s\n        with:\n          name: %s\n          path: %s\n          if-no-files-found: error\n          include-hidden-files: 'true'\n          retention-days: '1'\n", yamlSingle("${{ failure() && steps.run-disk.outputs.packer_diagnostics != '' }}"), githubDiskDiagnosticUploadMinutes, actions.Upload, yamlSingle("nvkr-packer-diagnostics-"+stage.DiskOutput), yamlSingle("${{ steps.run-disk.outputs.packer_diagnostics }}"))
}

// signFamilyNode reports whether a node kind belongs to the signing family: the
// jobs that use a signing profile. Artifact signing and SBOM attestation request an OIDC identity token;
// publication verification uses registry read access without minting credentials.
func signFamilyNode(kind string) bool {
	return kind == NodeSign || kind == NodeVerifyPublication || kind == NodeAttestEvidence || kind == NodeAssembleRelease
}

// githubSigningPermissions is the exact token scope set of one signing-family job.
// dry-run signing executes nothing and contacts no registry, so it asks for no
// write scope and no identity token at all; cosign mode needs an OIDC token to
// obtain a short-lived certificate, plus registry write to attach the signature
// (sign) or registry read to fetch it back (verify).
func githubSigningPermissions(kind, mode string) map[string]string {
	permissions := map[string]string{"contents": "read"}
	if kind == NodeVerifyPublication || kind == NodeAssembleRelease {
		permissions["packages"] = "read"
	}
	if mode != SigningModeCosign {
		return permissions
	}
	if kind == NodeAttestEvidence {
		permissions["id-token"] = "write"
	}
	if kind == NodeSign {
		permissions["id-token"] = "write"
		permissions["packages"] = "write"
	}
	return permissions
}

func githubSigningPermissionsFor(plan *Plan, kind string) map[string]string {
	permissions := githubSigningPermissions(kind, plan.Environment.Signing.Mode)
	if githubScopedPublication(plan) {
		// Scoped publication uses separately scoped principals. The workflow
		// token cannot acquire production-package write access as a side effect.
		delete(permissions, "packages")
	}
	return permissions
}

// ProductionRegistryWriteCredential selects independently scoped production accounts.
const ProductionRegistryWriteCredential = "github:NVKR_REGISTRY_WRITE"

func githubScopedPublication(plan *Plan) bool {
	return plan.Environment.GitHubDevelopment ||
		(plan.Environment.Publication != nil && plan.Environment.Publication.CredentialRef == ProductionRegistryWriteCredential)
}

// githubRegistryEnvironment selects the fixed credential for one graph operation.
// Development readers never receive the writer token; SBOM attestation has no
// registry credential. Environment admission binds the development writer name.
func githubRegistryEnvironment(plan *Plan, kind, targetName string) (map[string]string, error) {
	target := plan.Targets[targetName]
	if target.CredentialRef == "" || kind == NodeAttestEvidence {
		return nil, nil
	}
	secret, ok := strings.CutPrefix(target.CredentialRef, "github:")
	if !ok || !regexpSecret(secret) {
		return nil, e.New(e.CodeInvalidCatalog, "GitHub target %q credentialRef must be github:SECRET_NAME", targetName)
	}
	user := "${{ github.actor }}"
	if plan.Environment.GitHubDevelopment {
		if target.CredentialRef != development.WriteCredentialRef {
			return nil, e.New(e.CodeEngineCapability, "development publication requires its fixed scoped writer credential")
		}
		switch kind {
		case NodePublish, NodeSign:
			secret, user = development.WriteSecret, "${{ vars."+development.WriteUserVariable+" }}"
		case NodeVerifyPublication, NodeAssembleRelease:
			secret, user = development.ReadSecret, "${{ vars."+development.ReadUserVariable+" }}"
		default:
			return nil, e.New(e.CodeEngineCapability, "development operation %q has no registry credential scope", kind)
		}
	}
	if !plan.Environment.GitHubDevelopment && target.CredentialRef == ProductionRegistryWriteCredential {
		switch kind {
		case NodePublish, NodeSign:
			secret, user = "NVKR_REGISTRY_WRITE", "${{ vars.NVKR_REGISTRY_WRITE_USER }}"
		case NodeVerifyPublication, NodeAssembleRelease:
			secret, user = "NVKR_REGISTRY_READ", "${{ vars.NVKR_REGISTRY_READ_USER }}"
		default:
			return nil, e.New(e.CodeEngineCapability, "production operation %q has no registry credential scope", kind)
		}
	}
	return map[string]string{"NVKR_CREDENTIAL": "${{ secrets." + secret + " }}", "NVKR_REGISTRY_USER": user}, nil
}

// GHCR privacy admission authenticates the source repository with the automatic
// workflow token. Package inspection and writes keep the publisher's credential.
// Only publication performs this admission; producers and other release stages
// receive no repository metadata credential through their job environment.
func githubPublicationRepositoryToken(plan *Plan, node Node) string {
	if node.Kind == NodePublish && strings.HasPrefix(plan.Targets[node.Target].Ref, "ghcr.io/") {
		return "${{ github.token }}"
	}
	return ""
}

func githubCancelInProgress(plan *Plan) bool {
	return plan.Environment.GitHubDevelopment && plan.Environment.Publication == nil
}

func githubReleaseEnvironmentFor(plan *Plan) string {
	if plan.Environment.GitHubDevelopment {
		return githubDevelopmentReleaseEnvironment
	}
	return githubReleaseEnvironment
}

func githubARCEnvironmentFor(plan *Plan) string {
	if plan.Environment.GitHubDevelopment {
		return githubDevelopmentARCEnvironment
	}
	return githubARCEnvironment
}

// irProjectedNode identifies a reference to an exact producer instruction.
func irProjectedNode(node Node) bool { return node.BuildTarget != "" && node.IRNode != "" }

func (actions githubActionRefs) writeUploadStep(out *bytes.Buffer, plan *Plan, label, name, path string) {
	// Unsigned development handoffs expire quickly. Signed Runs keep room for
	// 24-hour ARC stages and subsequent publication/finalization within seven days.
	days := 7
	if githubCancelInProgress(plan) {
		days = 1
	}
	if plan.Environment.GitHubRuntimePlan && label == "NVKR runner" {
		fmt.Fprintf(out, "      - name: %s\n        uses: %s\n        with:\n          name: %s\n          path: |\n            nvkr-exec\n", yamlSingle("Upload "+label), actions.Upload, yamlSingle(name))
		for _, member := range githubRuntimeInputPaths(plan) {
			fmt.Fprintf(out, "            %s\n", member)
		}
		fmt.Fprintf(out, "          include-hidden-files: 'true'\n          if-no-files-found: error\n          retention-days: %d\n", days)
		return
	}
	fmt.Fprintf(out, "      - name: %s\n        uses: %s\n        with:\n          name: %s\n          path: %s\n          if-no-files-found: error\n          retention-days: %d\n", yamlSingle("Upload "+label), actions.Upload, yamlSingle(name), yamlSingle(path), days)
}

const githubRuntimeInputsArtifact = "nvkr-runtime-inputs"

func githubRuntimeInstallCommand(plan *Plan) string {
	return "./nvkr-exec factory github install-runtime-inputs --plan " + shellQuote(githubLayoutFor(plan).planPath)
}

func githubRuntimeInputPaths(plan *Plan) []string {
	layout := githubLayoutFor(plan)
	return []string{layout.planPath, filepath.ToSlash(filepath.Join(filepath.Dir(layout.planPath), "action-data.json")), layout.manifestPath}
}

func githubRuntimeGenerationCommand(plan *Plan) string {
	return strings.TrimSuffix(generationCommand(plan, EngineGitHub), " --check") + " --runtime"
}

func writeGitHubRuntimeInputsUpload(out *bytes.Buffer, plan *Plan, actions githubActionRefs) {
	fmt.Fprintf(out, "      - name: Upload NVKR runtime inputs\n        uses: %s\n        with:\n          name: %s\n          path: |\n", actions.Upload, githubRuntimeInputsArtifact)
	for _, member := range githubRuntimeInputPaths(plan) {
		fmt.Fprintf(out, "            %s\n", member)
	}
	fmt.Fprint(out, "          include-hidden-files: 'true'\n          if-no-files-found: error\n          retention-days: 7\n")
}

func (actions githubActionRefs) writeCheckoutStep(out *bytes.Buffer) {
	fmt.Fprintf(out, "      - name: Check out workflow source\n        uses: %s\n        with:\n          persist-credentials: 'false'\n", actions.Checkout)
}

// writeSetupGoStep provisions the Go toolchain on the runner before the bootstrap `go build`,
// so the workflow does not silently depend on a preinstalled Go (true on GitHub-hosted
// ubuntu-latest, but not on a minimal or self-hosted runner). The version is read from the
// checked-out repo's .go-version, so setup-go and the actual build toolchain cannot drift.
func (actions githubActionRefs) writeSetupGoStep(out *bytes.Buffer) {
	// Candidate diagnostic jobs can write the main branch's Actions cache. Do
	// not restore executable build inputs from that cache into a trusted helper.
	fmt.Fprintf(out, "      - name: Set up Go\n        uses: %s\n        with:\n          go-version-file: '.go-version'\n          cache: 'false'\n", actions.SetupGo)
}

// prepareHelperStepName and prepareHelperRun are the ONE definition of the helper
// preparation step, shared by the emitter and the validator so the rendered and accepted
// step orders cannot diverge.
const (
	prepareHelperStepName = "Prepare helper"
	prepareHelperRun      = "chmod +x ./nvkr-exec"
)

func writePrepareHelperStep(out *bytes.Buffer) {
	fmt.Fprintf(out, "      - name: %s\n        run: %s\n", prepareHelperStepName, prepareHelperRun)
}

func githubHelperDownloadPath(plan *Plan) string {
	if plan.Environment.GitHubRuntimePlan {
		return ".nvkr/runtime-runner"
	}
	return "."
}

func prepareHelperRunFor(plan *Plan) string {
	if plan == nil || !plan.Environment.GitHubRuntimePlan {
		return prepareHelperRun
	}
	var command strings.Builder
	fmt.Fprintln(&command, "set -eu")
	members := append([]string{"nvkr-exec"}, githubRuntimeInputPaths(plan)...)
	for _, member := range members {
		source := shellQuote(githubHelperDownloadPath(plan) + "/" + member)
		fmt.Fprintf(&command, "[ -f %s ] && [ ! -L %s ] || { printf 'nvkr: [NVKR-E-PIPELINE-DRIFT] incomplete runtime input carrier\\n' >&2; exit %d; }\n", source, source, e.ExitCode(e.New(e.CodePipelineDrift, "")))
	}
	for _, member := range members {
		fmt.Fprintf(&command, "mv -- %s %s\n", shellQuote(githubHelperDownloadPath(plan)+"/"+member), shellQuote(member))
	}
	fmt.Fprintln(&command, prepareHelperRun)
	fmt.Fprintf(&command, "./nvkr-exec factory github check-runtime-inputs --plan %s\n", shellQuote(githubLayoutFor(plan).planPath))
	return strings.TrimSuffix(command.String(), "\n")
}

func writeGitHubPrepareHelperStep(out *bytes.Buffer, plan *Plan) {
	if !plan.Environment.GitHubRuntimePlan {
		writePrepareHelperStep(out)
		return
	}
	fmt.Fprintf(out, "      - name: %s\n        run: |\n", prepareHelperStepName)
	for _, line := range strings.Split(prepareHelperRunFor(plan), "\n") {
		fmt.Fprintf(out, "          %s\n", line)
	}
}

func (actions githubActionRefs) writeDownloadStep(out *bytes.Buffer, label, name, path string) {
	fmt.Fprintf(out, "      - name: %s\n        uses: %s\n        with:\n          name: %s\n          path: %s\n", yamlSingle("Download "+label), actions.Download, yamlSingle(name), yamlSingle(path))
}

func githubJobID(id string) string { return "nvkr-" + shortStableName(id) }

func shortStableName(value string) string {
	clean := strings.ToLower(value)
	var out strings.Builder
	for _, r := range clean {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			out.WriteRune(r)
		} else {
			out.WriteByte('-')
		}
	}
	result := strings.Trim(out.String(), "-")
	if len(result) <= 48 && result != "" {
		return result
	}
	sum := sha256.Sum256([]byte(value))
	if len(result) > 36 {
		result = result[:36]
	}
	return strings.Trim(result, "-") + "-" + hex.EncodeToString(sum[:6])
}

func githubArtifactName(id string) string { return "nvkr-" + shortStableName(id) }

func carrierPath(id string) string {
	return filepath.ToSlash(filepath.Join(".nvkr", "carriers", shortStableName(id)+".tar"))
}

func yamlSingle(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func shellQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n'\"\\$`;&|<>(){}[]*?!#~") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func regexpSecret(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}

func generatorLinkerFlags(value string) (string, error) {
	separator := strings.LastIndex(value, " (")
	if separator <= 0 || !strings.HasSuffix(value, ")") {
		return "", e.New(e.CodeInvalidCatalog, "factory generatorVersion %q must be 'version (commit)'", value)
	}
	version, commit := value[:separator], value[separator+2:len(value)-1]
	if !safeGeneratorToken(version) || !safeGeneratorToken(commit) {
		return "", e.New(e.CodeInvalidCatalog, "factory generatorVersion %q is unsafe", value)
	}
	return "-X main.version=" + version + " -X main.commit=" + commit, nil
}

func safeGeneratorToken(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._+/-", character) {
			continue
		}
		return false
	}
	return true
}

func sortedFileNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func githubProtectedPublication(plan *Plan, node Node) bool {
	return plan.Environment.Signing != nil && plan.Environment.Signing.Mode == SigningModeCosign &&
		(node.Kind == NodePublish || signFamilyNode(node.Kind))
}

// Production admits the default branch plus exact Environment-selected refs.
// Development admits manually selected branches under its separate credential,
// namespace and protected-environment policy. Tags and PR events stay closed.
func githubDispatchCondition(plan *Plan) string {
	if plan.Environment.GitHubDevelopment {
		return "github.event_name == 'workflow_dispatch' && startsWith(github.ref, 'refs/heads/')"
	}
	if len(plan.Environment.GitHubDispatchBranches) == 0 {
		return githubExecutionCondition
	}
	refs := []string{"github.ref == format('refs/heads/{0}', github.event.repository.default_branch)"}
	for _, branch := range plan.Environment.GitHubDispatchBranches {
		refs = append(refs, "github.ref == 'refs/heads/"+branch+"'")
	}
	return "github.event_name == 'workflow_dispatch' && (" + strings.Join(refs, " || ") + ")"
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	yaml "go.yaml.in/yaml/v3"
)

const gitlabMaximumOperations = 256

// ValidateGitLabPlan admits the first ordinary, unsigned execution profile. No
// caller-supplied runner label or provider environment grants a stronger lane.
func ValidateGitLabPlan(ctx context.Context, plan *Plan) error {
	if err := executionContext(ctx); err != nil {
		return err
	}
	if plan == nil {
		return e.New(e.CodeUsage, "GitLab execution requires a Plan")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := rejectUnjoinedDebianPlan(plan, "GitLab generation"); err != nil {
		return err
	}
	if err := admitBootPublicationExecution(plan); err != nil {
		return err
	}
	if plan.Environment.GitLab == nil {
		return e.New(e.CodeEngineCapability, "GitLab execution requires an explicit Environment.gitlab policy")
	}
	if plan.Environment.Signing != nil || plan.Qualification != TierStaticUnqualified {
		return e.New(e.CodeEngineCapability, "GitLab signing and qualified evidence are not admitted by the ordinary unsigned profile")
	}
	if publication := plan.Environment.Publication; publication != nil {
		repo, err := name.NewRepository(publication.RepositoryPrefix, name.StrictValidation)
		credential := (!publication.Anonymous && publication.CredentialRef == "gitlab:CI_JOB_TOKEN") || (publication.Anonymous && publication.CredentialRef == "")
		if err != nil || repo.Name() != publication.RepositoryPrefix || repo.Scheme() != "https" || !credential || publication.PlainHTTP || publication.Mode != "" || len(publication.ImageTags) != 0 || len(publication.ObjectStores) != 0 {
			return e.New(e.CodeEngineCapability, "GitLab publication requires an admitted HTTPS OCI release without user-selected image aliases")
		}
	}
	if len(plan.Environment.SourceAccess) != 0 || len(plan.Environment.Runtime.NativeToolchains) != 0 || plan.RuntimeInputs != nil || plan.RuntimeSourceCommit != "" {
		return e.New(e.CodeEngineCapability, "GitLab private-source, native-toolchain and runtime-source profiles are unavailable")
	}
	// Producer requirements are refused through the shared ordinary-adapter owner
	// before any generated output; resolving them below for placement and budgets
	// grants no new execution-requirement capability.
	if err := rejectUnsupportedPlanRequirements(plan, "GitLab generation"); err != nil {
		return err
	}
	if err := rejectUnsupportedRuntimeStorage(plan, "GitLab generation"); err != nil {
		return err
	}
	if plan.Environment.Runtime.RuntimeImage != "" || plan.Environment.Runtime.HelperImage != "" {
		return e.New(e.CodeEngineCapability, "GitLab startup and source-built helper use explicit runner policy, not legacy runtime image overrides")
	}
	uses, err := plan.imageUses("local")
	if err != nil {
		return err
	}
	for _, use := range uses {
		if use.Route != ImageAnonymous {
			return e.New(e.CodeEngineCapability, "GitLab ordinary profile requires declared anonymous immutable execution images")
		}
	}
	if len(plan.Environment.Runtime.Setup) != 0 {
		return e.New(e.CodeEngineCapability, "GitLab ordinary execution does not admit arbitrary setup hooks")
	}
	if len(plan.Environment.Runtime.Variables) != 0 || len(plan.Environment.Runtime.Services) != 0 {
		return e.New(e.CodeEngineCapability, "GitLab ordinary profile does not admit arbitrary job variables or services")
	}
	if plan.DefinitionPath == "" || plan.EnvironmentPath == "" || len(plan.Graph.Nodes) > gitlabMaximumOperations {
		return e.New(e.CodeEngineCapability, "GitLab generation requires bounded operations and authored input paths")
	}
	for _, output := range plan.LifecycleOutputs {
		if output.Kind == PackageDiskImage {
			return e.New(e.CodeEngineCapability, "GitLab disk execution requires an admitted grouped native owner")
		}
	}
	for _, node := range plan.Graph.Nodes {
		if _, err := gitlabOperationBudget(plan, node); err != nil {
			return err
		}
		switch node.Kind {
		case NodeFetch, NodeBuild, NodeAcquire, NodeVerify, NodeSBOM, NodePackage:
		case NodePublish:
			if plan.Environment.Publication == nil || node.ID != "publish/release" || node.Target != "release" || len(node.Inputs) != 1 || len(node.Outputs) != 1 {
				return e.New(e.CodeEngineCapability, "GitLab publication requires the compiled release route")
			}
		default:
			return e.New(e.CodeEngineCapability, "GitLab ordinary profile does not support operation %q", node.Kind)
		}
		platform, err := gitlabNodePlatform(plan, node)
		if err != nil {
			return err
		}
		if _, ok := plan.Environment.GitLab.Runners[platform]; !ok {
			return e.New(e.CodeEngineCapability, "GitLab operation %q has no exact native runner route", node.ID)
		}
	}
	return nil
}

// gitlabNodePlatform resolves a scheduled operation's native runner platform
// through the Plan's execution view. Producer references carry no copied
// execution fields, so an ARM64 producer never inherits the utility-platform
// fallback; utility operations keep their own compiled placement.
func gitlabNodePlatform(plan *Plan, node Node) (string, error) {
	resolved, err := plan.ExecutionFor(node)
	if err != nil {
		return "", err
	}
	return gitlabResolvedPlatform(plan, resolved), nil
}

// gitlabResolvedPlatform applies the platform fallback chain to an already
// resolved execution view: the binding's host platform, then the resolved
// operation platform, and only then the utility platform.
func gitlabResolvedPlatform(plan *Plan, resolved Node) string {
	if resolved.ExecutionBinding != nil {
		return string(resolved.ExecutionBinding.Class.HostPlatform)
	}
	if resolved.Platform != "" {
		return resolved.Platform
	}
	return plan.Environment.UtilityPlatform
}

func gitlabUsedPlatforms(plan *Plan) ([]string, error) {
	platforms := []string{plan.Environment.UtilityPlatform}
	for _, node := range plan.Graph.Nodes {
		platform, err := gitlabNodePlatform(plan, node)
		if err != nil {
			return nil, err
		}
		platforms = append(platforms, platform)
	}
	return uniqueSorted(platforms), nil
}

// RenderGitLab is pure: canonical Plan bytes and transport YAML are returned
// together. The existing bundle owner separately validates/writes the manifest.
func RenderGitLab(ctx context.Context, plan *Plan) (*GeneratedBundle, error) {
	return renderGitLab(ctx, plan, true)
}

func renderGitLab(ctx context.Context, plan *Plan, validate bool) (*GeneratedBundle, error) {
	if err := ValidateGitLabPlan(ctx, plan); err != nil {
		return nil, err
	}
	projection, err := projectGitLabGraph(ctx, plan)
	if err != nil {
		return nil, err
	}
	files, digest, err := renderStaticInputs(ctx, plan, gitlabLayout)
	if err != nil {
		return nil, err
	}
	document := map[string]any{"stages": []string{"nvkr-bootstrap", "nvkr-execute", "nvkr-finalize"}}
	// Runner consumes this native switch before checkout for every generated
	// job. Its default Docker helper otherwise creates non-executable Git files
	// with umask 0000, incompatible with the exact generated-file mode contract.
	// Fresh source checkout avoids retained permission differences in reused
	// CI workspaces; Git tracks only the executable bit of regular-file modes.
	// Native Runner owns that workspace lifecycle; NVKR never chmods its inputs.
	document["variables"] = map[string]string{
		"FF_DISABLE_UMASK_FOR_DOCKER_EXECUTOR": "true",
		"GIT_STRATEGY":                         "clone",
	}
	// Native rules run before Runner startup. Shell set+x is defense in depth,
	// not permission to expose scoped API credentials through debug traces.
	document["workflow"] = map[string]any{"rules": []map[string]any{
		{"if": `($CI_DEBUG_TRACE != null && $CI_DEBUG_TRACE != "false") || ($CI_DEBUG_SERVICES != null && $CI_DEBUG_SERVICES != "false")`, "when": "never"},
		{"when": "always"},
	}}

	release, err := githubRunnerReleaseForGenerator()
	if err != nil {
		return nil, err
	}
	flags, err := generatorLinkerFlags(plan.GeneratorVersion)
	if err != nil {
		return nil, err
	}
	platforms, err := gitlabUsedPlatforms(plan)
	if err != nil {
		return nil, err
	}
	for _, platform := range platforms {
		route := plan.Environment.GitLab.Runners[platform]
		dir := ".nvkr/gitlab/bootstrap/" + strings.ReplaceAll(platform, "/", "-")
		install := githubRunnerInstallCommand("CGO_ENABLED=0 GOOS=linux GOARCH="+strings.TrimPrefix(platform, "linux/"), flags, release)
		version := "$(cat .go-version)"
		if release.GoVersion != "" {
			version = release.GoVersion
		}
		script := "set -eu\nset +x\nexport GOTOOLCHAIN=local\ntest \"$(go env GOVERSION)\" = \"go" + version + "\"\ntest \"$(go env GOHOSTOS)/$(go env GOHOSTARCH)\" = " + shellQuote(platform) + "\nRUNNER_TEMP=$(mktemp -d)\nexport RUNNER_TEMP\ntrap 'rm -rf -- \"$RUNNER_TEMP\"' EXIT\n" + "mkdir -p \"$RUNNER_TEMP/home\"\n" + "env -i PATH=\"$PATH\" HOME=\"$RUNNER_TEMP/home\" GOTOOLCHAIN=local RUNNER_TEMP=\"$RUNNER_TEMP\" /bin/sh -c " + shellQuote(install) + "\nmkdir -p " + shellQuote(dir) + "\nmv nvkr-exec " + shellQuote(dir+"/nvkr-exec") + "\n(cd " + shellQuote(dir) + " && sha256sum nvkr-exec > SHA256SUMS)\n"
		document[gitlabBootstrapJob(platform)] = map[string]any{"stage": "nvkr-bootstrap", "image": route.BootstrapImage, "tags": route.Tags, "timeout": "15 minutes", "script": []string{script}, "artifacts": map[string]any{"paths": []string{dir}, "access": "developer", "expire_in": "1 day"}, "allow_failure": false}
	}
	leaves := map[string]bool{}
	for _, op := range projection.Operations {
		leaves[op.Job] = true
	}
	for _, op := range projection.Operations {
		platform, err := gitlabNodePlatform(plan, op.Node)
		if err != nil {
			return nil, err
		}
		route := plan.Environment.GitLab.Runners[platform]
		needs := []map[string]any{{"job": gitlabBootstrapJob(platform), "artifacts": true}}
		for _, parent := range op.Needs {
			needs = append(needs, map[string]any{"job": parent, "artifacts": false})
			delete(leaves, parent)
		}
		if len(needs) > 50 {
			return nil, e.New(e.CodeEngineCapability, "GitLab operation exceeds the supported native needs bound")
		}
		runner := ".nvkr/gitlab/bootstrap/" + strings.ReplaceAll(platform, "/", "-") + "/nvkr-exec"
		command := gitlabRunnerCheck(runner) + "\n" + shellQuote(runner) + " factory gitlab run-node --plan " + shellQuote(GitLabPlanPath) + " --node " + shellQuote(op.Node.ID) + " --root .\n"
		when := "on_success"
		if op.Condition == executionAfterCompletion {
			when = "always"
		}
		budget, err := gitlabOperationBudget(plan, op.Node)
		if err != nil {
			return nil, err
		}
		job := map[string]any{"stage": "nvkr-execute", "image": route.BootstrapImage, "tags": route.Tags, "needs": needs, "when": when, "allow_failure": false, "timeout": fmt.Sprintf("%d seconds", int64(budget/time.Second)+900), "script": []string{command}, "artifacts": map[string]any{"when": "always", "access": "developer", "expire_in": "1 day", "paths": []string{gitlabOutcomePath(op.Node.ID), gitlabNodeCarrierDirectory(op.Node.ID)}}}
		document[op.Job] = job
	}
	if len(leaves)+1 > 50 {
		return nil, e.New(e.CodeEngineCapability, "GitLab finalizer exceeds the supported native needs bound")
	}
	platform := plan.Environment.UtilityPlatform
	route, ok := plan.Environment.GitLab.Runners[platform]
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "GitLab finalizer has no exact utility-platform route")
	}
	needs := []map[string]any{{"job": gitlabBootstrapJob(platform), "artifacts": true}}
	for _, leaf := range sortedKeys(leaves) {
		needs = append(needs, map[string]any{"job": leaf, "artifacts": false})
	}
	runner := ".nvkr/gitlab/bootstrap/" + strings.ReplaceAll(platform, "/", "-") + "/nvkr-exec"
	document[gitlabFinalizerJob] = map[string]any{"stage": "nvkr-finalize", "image": route.BootstrapImage, "tags": route.Tags, "needs": needs, "when": "always", "allow_failure": false, "timeout": "2 hours", "script": []string{gitlabRunnerCheck(runner) + "\n" + shellQuote(runner) + " run finalize-gitlab --definition " + shellQuote(plan.DefinitionPath) + " --environment " + shellQuote(plan.EnvironmentPath) + " --plan " + shellQuote(GitLabPlanPath) + " --workspace .nvkr/factory --out .nvkr/gitlab/run.json"}, "artifacts": map[string]any{"when": "always", "access": "developer", "expire_in": "1 day", "paths": []string{".nvkr/gitlab/run.json"}}}
	raw, err := yaml.Marshal(document)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "encoding GitLab transport YAML")
	}
	files[GitLabWorkflowPath] = raw
	bundle := newGeneratedBundle(EngineGitLab, gitlabLayout, files, digest)
	if validate {
		if err := ValidateGitLabBundle(ctx, plan, bundle); err != nil {
			return nil, err
		}
	}
	return bundle, nil
}

func gitlabRunnerCheck(runner string) string {
	dir := strings.TrimSuffix(runner, "/nvkr-exec")
	return "set -eu\nset +x\n(cd " + shellQuote(dir) + " && sha256sum --check SHA256SUMS)"
}

// ValidateGitLabBundle admits only the exact in-binary projection; parsed YAML
// supplied by a caller never creates execution or provider authority.
func ValidateGitLabBundle(ctx context.Context, plan *Plan, bundle *GeneratedBundle) error {
	if bundle == nil {
		return e.New(e.CodeUsage, "GitLab bundle is required")
	}
	expected, err := renderGitLab(ctx, plan, false)
	if err != nil {
		return err
	}
	if bundle.Engine != EngineGitLab || bundle.WorkflowPath != GitLabWorkflowPath || bundle.PlanPath != GitLabPlanPath || bundle.ManifestPath != GitLabManifestPath || bundle.PlanDigest != expected.PlanDigest || bundle.WorkflowDigest != expected.WorkflowDigest || !reflect.DeepEqual(bundle.Files, expected.Files) {
		return e.New(e.CodePipelineDrift, "GitLab generated bundle differs from the exact native projection")
	}
	return nil
}

func gitlabRootProfile(raw []byte) error {
	var document struct {
		Include []struct {
			Local string `yaml:"local"`
		} `yaml:"include"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return e.New(e.CodeEngineCapability, "GitLab root requires only the exact generated local include")
	}
	if len(document.Include) != 1 || document.Include[0].Local != GitLabWorkflowPath {
		return e.New(e.CodeEngineCapability, "GitLab root does not select the exact generated pipeline")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return e.New(e.CodeEngineCapability, "GitLab root contains multiple YAML documents")
	}
	return nil
}

// Native producer deadlines remain source-owned: the budget reads the resolved
// execution view, never the raw scheduler reference's empty requirements. Utility
// operations use the same finite one-hour default as ordinary hosted utility
// stages; transport gets a separate bounded provider allowance rather than
// reducing producer execution. Generation admission refuses unsupported
// requirements, so this resolution grants no new execution-requirement capability.
func gitlabOperationBudget(plan *Plan, node Node) (time.Duration, error) {
	resolved, err := plan.ExecutionFor(node)
	if err != nil {
		return 0, err
	}
	seconds := uint64(3600)
	if resolved.Requirements != nil && resolved.Requirements.TimeoutSeconds != 0 {
		seconds = resolved.Requirements.TimeoutSeconds
	}
	if seconds > 86400 {
		return 0, e.New(e.CodeEngineCapability, "GitLab operation exceeds the supported one-day native execution bound")
	}
	return time.Duration(seconds) * time.Second, nil
}

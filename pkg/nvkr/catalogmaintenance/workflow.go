// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

//go:embed workflow.yaml.tmpl
var workflowTemplate string

// DefaultSchedule is the nightly 02:00 UTC cron used when none is requested.
const DefaultSchedule = "0 2 * * *"

// PolicyDirectory and PolicySuffix locate maintenance policies; the rendered
// workflow finds them with PolicyGlob at run time and names each by its prefix.
const (
	PolicyDirectory = "inputs/policies"
	PolicySuffix    = "-catalog-maintenance.yaml"
	PolicyGlob      = PolicyDirectory + "/*" + PolicySuffix
)

var policyName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const (
	// sourceCheckoutPath is where the workflow fetches the private source, relative to the workspace.
	sourceCheckoutPath = ".nvkr-inputs/source"
	// workspaceExpression is the Actions expression for the checked-out workspace.
	workspaceExpression = "${{ github.workspace }}"
)

// sourceTransport is the Git URL rewrite later steps use to read the private
// source from its credential-free local checkout. The renderer and the tests
// both take it from transportFor, so admission reasons about what is emitted.
type sourceTransport struct{ Key, Value string }

func transportFor(repository string) sourceTransport {
	return sourceTransport{
		Key:   "url." + workspaceExpression + "/" + sourceCheckoutPath + "/.insteadOf",
		Value: "https://github.com/" + repository,
	}
}

// DefaultAppClientSecret and DefaultAppKeySecret name the repository secrets
// that hold the GitHub App client ID and private key when none are given.
const (
	DefaultAppClientSecret = "RELEASE_BOT_CLIENT"
	DefaultAppKeySecret    = "RELEASE_BOT_SECRET"
)

// AppPermission is one repository permission the publishing GitHub App must hold with write access.
type AppPermission struct {
	// Name is the label GitHub's App settings show; Input is the create-github-app-token input suffix.
	Name, Input string
}

// AppPermissions are the repository permissions, all write, that the rendered workflow requests for the App
// token. The App's registration and installation must grant each one, or GitHub refuses the token.
func AppPermissions() []AppPermission {
	return []AppPermission{{"Contents", "contents"}, {"Pull requests", "pull-requests"}, {"Workflows", "workflows"}}
}

// The App token action is pinned here because it is a maintenance-workflow dependency, not a Run action the
// catalog supplies.
const (
	appTokenAction  = "actions/create-github-app-token@64ab729a6294000f5aa4a4bc0b73d1f32bc45510"
	appTokenVersion = "v3.3.0"
)

// WorkflowOptions are the only settings a policy file cannot supply, because
// they belong to the whole workflow rather than to one Definition.
type WorkflowOptions struct {
	// Schedule is a reviewed cron; empty means manual dispatch only.
	Schedule string
	// SourceRepository and SourceSecret name a private repository fetched with a
	// secret in one credential-free-afterwards step. Both or neither.
	SourceRepository, SourceSecret string
	// AppClientSecret and AppKeySecret name the secrets holding the GitHub App
	// client ID and private key; only the pull-request job reads them, to mint
	// its short-lived installation token. Empty selects the defaults.
	AppClientSecret, AppKeySecret string
}

// withDefaults fills the App secret names left empty.
func (options WorkflowOptions) withDefaults() WorkflowOptions {
	if options.AppClientSecret == "" && options.AppKeySecret == "" {
		options.AppClientSecret, options.AppKeySecret = DefaultAppClientSecret, DefaultAppKeySecret
	}
	return options
}

// Workflow renders one GitHub workflow that refreshes the one Definition with a
// maintenance policy and opens, updates or closes its pull request through a
// separate job that alone writes, with a GitHub App installation token it mints
// for this repository; the workflow token stays read-only. The policy is
// discovered by the workflow itself at run time; it is read here only to
// refuse output the generation owner could not produce. It never changes the
// product Run. Run it from the repository root, then review and install the output.
func Workflow(ctx context.Context, options WorkflowOptions, generatorVersion string) ([]byte, error) {
	options = options.withDefaults()
	if err := options.validate(); err != nil {
		return nil, err
	}
	linkerFlags, err := factory.GeneratorLinkerFlags(generatorVersion)
	if err != nil {
		return nil, err
	}
	policies, err := filepath.Glob(PolicyGlob)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "listing maintenance policies")
	}
	if len(policies) == 0 {
		return nil, e.New(e.CodeInvalidCatalog, "no maintenance policies match %s", PolicyGlob)
	}
	// Every refresh checks and rewrites the one repository-root generated bundle,
	// so two Definitions cannot both match its baseline. Fail closed until each
	// Definition owns a bundle root.
	if len(policies) > 1 {
		return nil, e.New(e.CodeEngineCapability, "%d maintenance policies match %s; the generated bundle root is shared, so enroll one policy", len(policies), PolicyGlob)
	}
	policy := policies[0]
	if name := strings.TrimSuffix(filepath.Base(policy), PolicySuffix); !policyName.MatchString(name) {
		return nil, e.New(e.CodeUsage, "unsupported maintenance policy name %q", name)
	}
	if err := admitPolicyLocation(policy); err != nil {
		return nil, err
	}
	config, err := Load(ctx, policy)
	if err != nil {
		return nil, err
	}
	// Refuse a mismatched generator before resolving any catalog or private source.
	if err := admitCommittedPlanStamps(ctx, generatorVersion); err != nil {
		return nil, err
	}
	plan, err := prepare(ctx, config, generatorVersion, options.SourceRepository)
	if err != nil {
		return nil, err
	}
	// The candidate must also be producible by the actual generation owner; a
	// maintenance wrapper cannot enable an unsupported product Run.
	bundle, err := factory.Render(ctx, plan, factory.EngineGitHub)
	if err != nil {
		return nil, err
	}
	members, err := buildClosure(ctx, config, generatorVersion, plan, bundle, options.SourceRepository)
	if err != nil {
		return nil, err
	}
	if err := members.admitGeneratorStamp(ctx, generatorVersion); err != nil {
		return nil, err
	}
	directories, files := members.paths()
	actions, err := pipeline.ResolveGitHubActions(plan.Actions)
	if err != nil {
		return nil, err
	}
	versions := map[string]string{}
	for name, pin := range plan.Actions {
		versions[name] = pin.Version
	}
	tmpl, err := template.New("catalog-maintenance").Delims("[[", "]]").Parse(workflowTemplate)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "parsing maintenance workflow template")
	}
	var out bytes.Buffer
	data := struct {
		WorkflowOptions
		Actions pipeline.GitHubActionRefs
		// AppTokenAction and AppTokenVersion are the pinned App token action and its release.
		AppTokenAction, AppTokenVersion string
		AppPermissions                  []AppPermission
		Versions                        map[string]string
		LinkerFlags                     string
		// Flags reproduces the options in the generated header; Transport is the private-source Git rewrite.
		Flags     string
		Transport sourceTransport
		// Directories and Files are the exact generated paths a candidate patch may touch.
		Directories, Files []string
		// PolicyDirectory and PolicySuffix locate the policy the workflow discovers.
		PolicyDirectory, PolicySuffix string
	}{options, actions, appTokenAction, appTokenVersion, AppPermissions(), versions, linkerFlags, options.flags(), transportFor(options.SourceRepository), directories, files, PolicyDirectory, PolicySuffix}
	if err := tmpl.Execute(&out, data); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "rendering maintenance workflow")
	}
	if err := ctx.Err(); err != nil {
		return nil, contextError(ctx, err)
	}
	return out.Bytes(), nil
}

// flags returns the update-ci flags that reproduce these options, for the generated header.
// Every value is validated before it is rendered, so it cannot end the comment line.
func (options WorkflowOptions) flags() string {
	var flags string
	if options.Schedule != DefaultSchedule {
		flags += " --schedule '" + options.Schedule + "'"
	}
	if options.SourceRepository != "" {
		flags += " --source-checkout " + options.SourceRepository + ":" + options.SourceSecret
	}
	if options.AppClientSecret != DefaultAppClientSecret || options.AppKeySecret != DefaultAppKeySecret {
		flags += " --app-secrets " + options.AppClientSecret + ":" + options.AppKeySecret
	}
	return flags
}

// admitPolicyLocation refuses a symlinked Definition directory or policy file. The
// workflow lists policies with find, which does not follow symlinks, so the
// render-time and run-time counts must not depend on one.
func admitPolicyLocation(policy string) error {
	parts := strings.Split(filepath.ToSlash(policy), "/")
	for i := range parts {
		location := filepath.Join(parts[:i+1]...)
		info, err := os.Lstat(location)
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "reading maintenance policy location %q", location)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return e.New(e.CodeInvalidCatalog, "maintenance policy locations cannot be symlinks: %q", location)
		}
	}
	return nil
}

func (options WorkflowOptions) validate() error {
	if err := ValidateSchedule(options.Schedule); err != nil {
		return err
	}
	if (options.SourceRepository == "") != (options.SourceSecret == "") {
		return e.New(e.CodeUsage, "a private source requires both a repository and a secret name")
	}
	if err := ValidateSourceRepository(options.SourceRepository); err != nil {
		return err
	}
	if options.SourceRepository != "" && (!secretName.MatchString(options.SourceSecret) || strings.HasPrefix(options.SourceSecret, "GITHUB_")) {
		return e.New(e.CodeUsage, "source checkout requires a dedicated secret name")
	}
	for _, name := range []string{options.AppClientSecret, options.AppKeySecret} {
		if !secretName.MatchString(name) || strings.HasPrefix(name, "GITHUB_") {
			return e.New(e.CodeUsage, "the GitHub App requires dedicated client ID and private key secret names")
		}
	}
	if options.AppClientSecret == options.AppKeySecret || options.AppClientSecret == options.SourceSecret || options.AppKeySecret == options.SourceSecret {
		return e.New(e.CodeUsage, "the GitHub App and private source secrets must be distinct")
	}
	return nil
}

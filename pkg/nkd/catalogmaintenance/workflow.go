package catalogmaintenance

import (
	"bytes"
	"context"
	_ "embed"
	"text/template"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

//go:embed workflow.yaml.tmpl
var workflowTemplate string

// Workflow renders a separate review-candidate workflow. It never changes the
// product Run, credentials, branches, or PRs. Review and install it explicitly.
func Workflow(ctx context.Context, config Config, configPath, generatorVersion string) ([]byte, error) {
	if !safeRelative(configPath) {
		return nil, e.New(e.CodeUsage, "maintenance config must have a clean repository-relative path")
	}
	if config.PullRequest != nil {
		return nil, e.New(e.CodeEngineCapability, "PR publication workflow generation is not enabled")
	}
	linkerFlags, err := factory.GeneratorLinkerFlags(generatorVersion)
	if err != nil {
		return nil, err
	}
	plan, err := prepare(ctx, config, generatorVersion)
	if err != nil {
		return nil, err
	}
	// The candidate must also be producible by the actual generation owner; a
	// maintenance wrapper cannot enable an unsupported product Run.
	if _, err := factory.Render(ctx, plan, factory.EngineGitHub); err != nil {
		return nil, err
	}
	actions, err := pipeline.ResolveGitHubActions(plan.Actions)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("catalog-maintenance").Delims("[[", "]]").Parse(workflowTemplate)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "parsing maintenance workflow template")
	}
	var out bytes.Buffer
	versions := map[string]string{}
	for name, pin := range plan.Actions {
		versions[name] = pin.Version
	}
	data := struct {
		Config      Config
		ConfigPath  string
		Actions     pipeline.GitHubActionRefs
		Versions    map[string]string
		LinkerFlags string
	}{config, configPath, actions, versions, linkerFlags}
	if err := tmpl.Execute(&out, data); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "rendering maintenance workflow")
	}
	if err := ctx.Err(); err != nil {
		return nil, contextError(ctx, err)
	}
	return out.Bytes(), nil
}

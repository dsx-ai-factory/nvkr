package factory

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// githubProgressCommand carries only a diagnostic phase, never execution authority.
// The command remains the exact admitted helper invocation.
type githubProgressCommand struct {
	phase string
	run   string
}

// githubProgressCommands wraps generated shell without changing the primary exit
// status. Summaries are optional diagnostics: an unavailable summary file cannot
// discard a successful result or mask a failed command. No environment, raw argv,
// source URL, destination URL, or file content is included in descriptions.
func githubProgressCommands(label string, details []string, work []githubProgressCommand) []string {
	commands := []string{
		"set -euo pipefail",
		"nkd_started=$SECONDS",
		"nkd_step=" + githubShellText(label),
		"nkd_phase=prepare",
		"nkd_phase_summary=prepare",
		"nkd_finish() {",
		"  nkd_status=$?",
		"  set +e",
		"  trap - EXIT",
		"  nkd_elapsed=$((SECONDS - nkd_started))",
		`  if [ "$nkd_status" -eq 0 ]; then`,
		`    nkd_result=completed`,
		`    printf 'Completed: %s (%ss)\n' "$nkd_step" "$nkd_elapsed"`,
		`  else`,
		`    nkd_result=failed`,
		`    printf 'Failed: %s at %s (exit %s, %ss)\n' "$nkd_step" "$nkd_phase" "$nkd_status" "$nkd_elapsed" >&2`,
		`  fi`,
		`  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then`,
		`    {`,
		"      printf '%s\\n' " + githubShellText("### "+githubSummaryText(label)),
		`      printf '\n| Result | Exit | Seconds | Last phase |\n| --- | --- | --- | --- |\n'`,
		`      printf '| %s | %s | %s | <code>%s</code> |\n\n' "$nkd_result" "$nkd_status" "$nkd_elapsed" "$nkd_phase_summary"`,
	}
	for _, detail := range details {
		commands = append(commands, "      printf '%s\\n' "+githubShellText("- <code>"+githubSummaryText(detail)+"</code>"))
	}
	commands = append(commands,
		`      printf '\n'`,
		`    } >> "$GITHUB_STEP_SUMMARY" || printf 'Unable to write diagnostic job summary.\n' >&2`,
		`  fi`,
		`  exit "$nkd_status"`,
		`}`,
		`trap nkd_finish EXIT`,
		`printf 'Starting: %s\n' "$nkd_step" || :`,
	)
	for _, detail := range details {
		commands = append(commands, "printf '%s\\n' "+githubShellText(detail)+" || :")
	}
	for _, command := range work {
		commands = append(commands,
			"nkd_phase="+githubShellText(command.phase),
			"nkd_phase_summary="+githubShellText(githubSummaryText(command.phase)),
			command.run,
		)
	}
	return commands
}

func githubNodeCommands(node Node, plan *Plan) []string {
	var work []githubProgressCommand
	if len(githubSourceEnvironment(plan, node)) > 0 {
		work = append(work, githubProgressCommand{phase: "check source credential", run: requireSourceToken})
	}
	work = append(work, githubNodeWork(node.ID, generatedNodeCommands(node, plan, FactoryPlanPath, "$GITHUB_WORKSPACE"))...)
	for _, output := range node.Outputs {
		work = append(work, githubProgressCommand{phase: "report " + output,
			run: "printf '%s\\n' " + githubShellText("Produced: "+output) + " || :"})
	}
	return githubProgressCommands(githubNodeLabel(plan, node), githubNodeDetails(plan, node), work)
}

// Any commands before the final helper invocation admit execution capabilities;
// distinguish their failure from the operation itself without changing commands.
func githubNodeWork(nodeID string, commands []string) []githubProgressCommand {
	work := make([]githubProgressCommand, len(commands))
	for i, command := range commands {
		phase := "execute " + nodeID
		if i < len(commands)-1 {
			phase = "preflight " + nodeID
		}
		work[i] = githubProgressCommand{phase: phase, run: command}
	}
	return work
}

// This allowlist intentionally excludes arbitrary recipe arguments: they may
// contain literal authentication values. Exact instructions remain inspectable in
// the canonical Plan; the logs identify which instruction executed.
func githubNodeDetails(plan *Plan, node Node) []string {
	details := []string{"Node: " + node.ID, "Operation: " + node.Kind}
	if node.BuildTarget != "" {
		details = append(details, "Selected output: "+node.BuildTarget)
	}
	if node.Platform != "" {
		details = append(details, "Platform: "+node.Platform)
	}
	if node.Network != "" {
		details = append(details, "Network: "+node.Network)
	}
	for _, build := range plan.Builds {
		if build.Name != node.BuildTarget || build.Graph == nil {
			continue
		}
		for _, instruction := range build.Graph.Nodes {
			if instruction.ID != node.IRNode {
				continue
			}
			details = append(details, "Producer instruction: "+instruction.ID)
			if instruction.Component != "" {
				details = append(details, "Component: "+instruction.Component)
			}
			if instruction.BuilderType != "" {
				details = append(details, "Builder: "+instruction.BuilderType)
			}
			if instruction.Commit != "" {
				details = append(details, "Source commit: "+instruction.Commit)
			}
			if instruction.SourceCommit != "" {
				details = append(details, "Source commit: "+instruction.SourceCommit)
			}
		}
	}
	if node.PackageKind != "" {
		details = append(details, "Package format: "+node.PackageKind)
	}
	if node.PackageComponent != "" {
		details = append(details, "Component: "+node.PackageComponent)
	}
	if node.Target != "" {
		details = append(details, "Publication route: "+node.Target)
	}
	for _, input := range uniqueSorted(node.Inputs) {
		details = append(details, githubArtifactDetail(plan, "Input", input))
	}
	for _, output := range uniqueSorted(node.Outputs) {
		details = append(details, githubArtifactDetail(plan, "Expected output", output))
	}
	return details
}

func githubArtifactDetail(plan *Plan, prefix, id string) string {
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.ID == id {
			return fmt.Sprintf("%s: %s (%s; producer %s)", prefix, id, githubArtifactLabel(plan, artifact), artifact.Producer)
		}
	}
	return prefix + ": " + id
}

// Diagnostic values are one-line text. Split dollars into separate shell words
// so even a literal GitHub expression cannot be evaluated by the provider before
// Bash sees the script. Shell substitutions remain data, including backticks.
func githubShellText(value string) string {
	parts := strings.Split(githubDisplayText(value), "$")
	for i := range parts {
		parts[i] = shellQuote(parts[i])
	}
	return strings.Join(parts, "'$'")
}

func githubDisplayText(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

func githubSummaryText(value string) string {
	return strings.NewReplacer("|", "&#124;", "`", "&#96;").Replace(html.EscapeString(githubDisplayText(value)))
}

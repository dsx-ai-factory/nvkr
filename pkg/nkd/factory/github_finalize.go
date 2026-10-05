package factory

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// writeGitHubFinalizerRestoreSteps emits the complete carrier download and restore
// sequence for the lifecycle finalizer. actions/download-artifact v8.0.1 expands
// the exact minimatch brace set, downloads up to five artifacts concurrently, and
// merges their uniquely named carrier files into one directory. The action succeeds
// when the pattern matches no artifacts, so every expected artifact still reaches
// the per-producer status logic and the finalizer can persist a failed Run.
func writeGitHubFinalizerRestoreSteps(out *bytes.Buffer, plan *Plan, nodeJobs map[string]string) error {
	if out == nil || plan == nil {
		return e.New(e.CodeUsage, "GitHub finalizer restore rendering requires output and plan")
	}
	if len(plan.Graph.Artifacts) == 0 {
		return e.New(e.CodeInvalidCatalog, "GitHub lifecycle finalizer requires at least one carrier")
	}

	actions, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		return err
	}
	type carrier struct {
		artifact Artifact
		path     string
		job      string
	}
	carriers := make([]carrier, 0, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		job := nodeJobs[artifact.Producer]
		if job == "" {
			return e.New(e.CodeInvalidCatalog,
				"GitHub finalizer artifact %q has no producer job", artifact.ID)
		}
		carriers = append(carriers, carrier{artifact: artifact, path: carrierPath(artifact.ID), job: job})
	}
	sort.Slice(carriers, func(i, j int) bool { return carriers[i].artifact.ID < carriers[j].artifact.ID })
	pattern, err := githubCarrierPattern(plan.Graph.Artifacts)
	if err != nil {
		return err
	}

	var rendered bytes.Buffer
	fmt.Fprintln(&rendered, "      - name: Prepare artifact recovery\n        run: mkdir -p .nkd/restore-outcomes")
	fmt.Fprintf(&rendered, "      - name: Download artifacts and evidence\n        uses: %s\n        continue-on-error: true\n        with:\n          pattern: %s\n          path: .nkd/carriers\n          merge-multiple: 'true'\n",
		actions.Download, yamlSingle(pattern))
	recovery := []string{
		"set -euo pipefail",
		`if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then`,
		`  printf '### Artifact recovery\n\nThese transport outcomes are diagnostic; the finalizer authenticates completeness.\n\n| Artifact | Recovery |\n| --- | --- |\n' >> "$GITHUB_STEP_SUMMARY" || printf 'Unable to write diagnostic recovery summary.\n' >&2 || :`,
		`fi`,
	}
	var blocks [][]string
	for _, carrier := range carriers {
		status := filepath.ToSlash(HostedRestoreOutcomePath(".nkd/factory", carrier.artifact.ID))
		blocks = append(blocks, []string{
			fmt.Sprintf("printf '%%s\\n' %s || :", githubShellText("Recover: "+carrier.artifact.ID+" ("+githubArtifactLabel(plan, carrier.artifact)+")")),
			fmt.Sprintf("if [ '${{ needs.%s.result }}' != 'success' ]; then printf 'job-failed\\n' > %s; elif [ ! -f %s ]; then printf 'missing\\n' > %s; elif ./nkd-exec transport-unpack --archive %s --root . --path %s; then printf 'restored:success\\n' > %s; else printf 'restore-failed\\n' > %s; fi",
				carrier.job, shellQuote(status), shellQuote(carrier.path), shellQuote(status), shellQuote(carrier.path), shellQuote(carrier.artifact.Path), shellQuote(status), shellQuote(status)),
			"nkd_recovery=$(cat " + shellQuote(status) + ")",
			`printf 'Recovery: %s\n' "$nkd_recovery" || :`,
			`if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then`,
			fmt.Sprintf(`  printf '| <code>%%s</code> | %%s |\n' %s "$nkd_recovery" >> "$GITHUB_STEP_SUMMARY" || printf 'Unable to write diagnostic recovery summary.\n' >&2 || :`, githubShellText(githubSummaryText(carrier.artifact.ID))),
			`fi`,
		})
	}
	if err := writeGitHubRecoveryBatches(&rendered, recovery, blocks); err != nil {
		return err
	}
	out.Write(rendered.Bytes())
	return nil
}

// Keep every carrier's outcome block atomic while bounding provider run scripts.
// Each batch visits all its carriers despite returned restore failures. Only a
// local failure to persist/read an outcome can stop subsequent recovery steps.
func writeGitHubRecoveryBatches(out *bytes.Buffer, header []string, blocks [][]string) error {
	var batches [][]string
	current := append([]string(nil), header...)
	for _, block := range blocks {
		if githubScriptBytes(append(append([]string(nil), header...), block...)) > githubMaximumRunBytes {
			return e.New(e.CodeEngineCapability, "one GitHub artifact recovery exceeds the provider script limit")
		}
		if githubScriptBytes(append(append([]string(nil), current...), block...)) > githubMaximumRunBytes {
			batches = append(batches, current)
			current = append([]string(nil), header...)
		}
		current = append(current, block...)
	}
	batches = append(batches, current)
	for i, commands := range batches {
		writeGitHubShellStep(out, fmt.Sprintf("Recover planned carriers (%d/%d)", i+1, len(batches)), commands)
	}
	return nil
}

func githubScriptBytes(commands []string) int {
	return len(strings.Join(commands, "\n")) + 1
}

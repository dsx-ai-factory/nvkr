package factory

import (
	"bytes"
	"fmt"

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
	for _, artifact := range plan.Graph.Artifacts {
		if nodeJobs[artifact.Producer] == "" {
			return e.New(e.CodeInvalidCatalog, "GitHub finalizer artifact %q has no producer job", artifact.ID)
		}
	}
	var carriers []Artifact
	for _, artifact := range plan.Graph.Artifacts {
		if _, raw := diskPublicationRequirement(plan, artifact); !raw {
			carriers = append(carriers, artifact)
		}
	}
	pattern, err := githubCarrierPattern(carriers)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "      - name: Download artifacts and evidence\n        uses: %s\n        continue-on-error: true\n        with:\n          pattern: %s\n          path: .nkd/carriers\n          merge-multiple: 'true'\n", actions.Download, yamlSingle(pattern))
	fmt.Fprintf(out, "      - name: Recover planned carriers\n        id: recover-artifacts\n        env:\n          NKD_JOB_RESULTS: ${{ toJSON(needs) }}\n        run: %s\n", githubRunnerCommand("recover", "", ""))
	return nil
}

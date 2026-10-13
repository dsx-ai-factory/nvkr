// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"strings"
)

const (
	githubRunStartSummaryID = "summarize-run-start"
	githubRunEndSummaryID   = "summarize-run-end"
	githubRunUploadID       = "upload-run-record"
)

// Summary scripts use only shell builtins and provider descriptions. They still
// run when checkout or helper bootstrap fails and confer no execution authority.
func githubRunStartSummary(plan *Plan) githubStep {
	var platforms []string
	for _, build := range plan.Builds {
		platforms = append(platforms, build.Platform)
	}
	lines := []string{
		"## NVKR Run started", "",
		fmt.Sprintf("<code>%s</code> · %d planned outputs · <code>%s</code>",
			githubRunSummaryText(plan.Name), len(plan.LifecycleOutputs), githubRunSummaryText(strings.Join(uniqueSorted(platforms), ", "))),
		"",
		fmt.Sprintf("Lane: <code>%s</code>. Execution and completeness checks are pending.", githubRunSummaryText(plan.Qualification.String())),
		"",
	}
	var script strings.Builder
	script.WriteString(githubSummarySinkCheck)
	script.WriteString("{\n  printf '%s\\n'")
	for _, line := range lines {
		script.WriteByte(' ')
		script.WriteString(shellQuote(line))
	}
	script.WriteString("\n} >> \"$GITHUB_STEP_SUMMARY\" 2>/dev/null || true\n")
	return githubStep{ID: githubRunStartSummaryID, Name: "Summarize Run start", If: "${{ success() && " + githubDispatchCondition(plan) + " }}", ContinueOnError: true, Run: script.String()}
}

func githubRunEndSummary(expectedJobs int) githubStep {
	return githubStep{
		ID: githubRunEndSummaryID, Name: "Summarize Run result", If: "${{ always() }}", ContinueOnError: true,
		Env: map[string]string{
			"NVKR_JOB_RESULTS":      "${{ join(needs.*.result, ' ') }}",
			"NVKR_JOB_STATUS":       "${{ job.status }}",
			"NVKR_FINALIZE_OUTCOME": "${{ steps.finalize-catalog.outcome }}",
			"NVKR_UPLOAD_OUTCOME":   "${{ steps." + githubRunUploadID + ".outcome }}",
			"NVKR_RUN_URL":          "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}",
			"NVKR_RUN_ARTIFACT_URL": "${{ steps." + githubRunUploadID + ".outputs.artifact-url }}",
		},
		Run: githubSummarySinkCheck + fmt.Sprintf(githubRunEndSummaryScript, expectedJobs),
	}
}

func writeGitHubRunSummary(out *bytes.Buffer, step githubStep) {
	fmt.Fprintf(out, "      - name: %s\n        id: %s\n        if: %s\n        continue-on-error: true\n", step.Name, step.ID, step.If)
	if len(step.Env) > 0 {
		fmt.Fprintln(out, "        env:")
		for _, key := range sortedKeys(step.Env) {
			fmt.Fprintf(out, "          %s: %s\n", key, yamlSingle(step.Env[key]))
		}
	}
	fmt.Fprintln(out, "        run: |")
	for _, line := range strings.Split(strings.TrimSuffix(step.Run, "\n"), "\n") {
		fmt.Fprintf(out, "          %s\n", line)
	}
}

func githubRunSummaryText(value string) string {
	// Prevent generated text from becoming an expression before the shell starts.
	return strings.ReplaceAll(githubSummaryText(value), "$", "&#36;")
}

const githubSummarySinkCheck = `[ -n "${GITHUB_STEP_SUMMARY:-}" ] || exit 0
[ ! -L "$GITHUB_STEP_SUMMARY" ] || exit 0
[ ! -e "$GITHUB_STEP_SUMMARY" ] || [ -f "$GITHUB_STEP_SUMMARY" ] || exit 0
`

//nolint:misspell // GitHub result enum spelling.
const githubRunEndSummaryScript = `set -f
IFS=' '
success=0 failure=0 cancelled=0 skipped=0 unknown=0 total=0
for result in ${NVKR_JOB_RESULTS:-}; do
  total=$((total + 1))
  case "$result" in
    success) success=$((success + 1)) ;;
    failure) failure=$((failure + 1)) ;;
    cancelled) cancelled=$((cancelled + 1)) ;;
    skipped) skipped=$((skipped + 1)) ;;
    *) unknown=$((unknown + 1)) ;;
  esac
done
case "${NVKR_FINALIZE_OUTCOME:-}" in
  success) finalization=completed ;;
  failure) finalization=failed ;;
  cancelled) finalization=cancelled ;;
  skipped|'') finalization='not run' ;;
  *) finalization=unknown ;;
esac
status='Incomplete (unknown status)'
if [ "$cancelled" -gt 0 ] || [ "${NVKR_JOB_STATUS:-}" = cancelled ] || [ "${NVKR_FINALIZE_OUTCOME:-}" = cancelled ]; then
  status=Cancelled
elif [ "$failure" -gt 0 ] || [ "${NVKR_JOB_STATUS:-}" = failure ] || [ "${NVKR_FINALIZE_OUTCOME:-}" = failure ]; then
  status=Failed
elif [ "$skipped" -gt 0 ] || [ "${NVKR_FINALIZE_OUTCOME:-}" = skipped ]; then
  status='Incomplete (skipped)'
elif [ "$total" -eq %d ] && [ "$success" -eq "$total" ] && [ "$unknown" -eq 0 ] && [ "${NVKR_FINALIZE_OUTCOME:-}" = success ] && [ "${NVKR_JOB_STATUS:-}" = success ] && [ "${NVKR_UPLOAD_OUTCOME:-}" = success ]; then
  status=Completed
fi
{
  printf '## NVKR Run result: %%s\n\n' "$status"
  printf 'Upstream jobs: %%s succeeded · %%s failed · %%s cancelled · %%s skipped · %%s unknown.\n\n' "$success" "$failure" "$cancelled" "$skipped" "$unknown"
  printf 'Finalization: %%s.\n\n' "$finalization"
  case "${NVKR_RUN_URL:-}" in
    *[!a-zA-Z0-9:/._-]*|'') ;;
    https://*) printf '[Job logs](%%s)\n\n' "$NVKR_RUN_URL" ;;
  esac
  if [ "${NVKR_UPLOAD_OUTCOME:-}" = success ]; then
    case "${NVKR_RUN_ARTIFACT_URL:-}" in
      *[!a-zA-Z0-9:/._-]*|'') ;;
      https://*) printf '[Run record artifact](%%s)\n\n' "$NVKR_RUN_ARTIFACT_URL" ;;
    esac
  fi
  printf 'This summary is diagnostic; the Run record carries the completeness result.\n'
} >> "$GITHUB_STEP_SUMMARY" 2>/dev/null || true
`

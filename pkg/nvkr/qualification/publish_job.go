// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish"
)

// qualificationAttemptOutput carries the run attempt the qualify job last ran in,
// so the publish job can tell which attempt its report describes (ADR-31 §2).
const qualificationAttemptOutput = "qualify-attempt"

// definitionsRoot holds every Definition; its first directory names the group.
const definitionsRoot = "inputs/definitions/"

// publishOutcomesDirectory is where the publish job downloads the Run's outcomes
// and writes any outcome it builds.
const publishOutcomesDirectory = "${{ runner.temp }}/nvkr-qualification-outcomes"

// resultsCoordinates derives the index group and Definition name (ADR-31 §1): the
// Definition's top-level directory under inputs/definitions/ and its name.
func resultsCoordinates(plan *factory.Plan) (group, name string, err error) {
	relative, ok := strings.CutPrefix(plan.DefinitionPath, definitionsRoot)
	group, rest, nested := strings.Cut(relative, "/")
	if !ok || !nested || group == "" || rest == "" {
		return "", "", e.New(e.CodeUsage, "publishing qualification results requires a Definition under %s<group>/", definitionsRoot)
	}
	if err := resultspublish.CheckNames(group, plan.Name); err != nil {
		return "", "", err
	}
	return group, plan.Name, nil
}

// qualificationPublishJob renders the job that publishes the Run's outcomes
// (ADR-31 §7). It is defined in the release qualify workflow itself: the results
// identity trusts only jobs defined there, in its own GitHub Environment. It runs
// for every admitted producer conclusion, including the failures that skip the
// qualify job, and its failure never fails the Run or changes the verdict.
func qualificationPublishJob(out *bytes.Buffer, plan *factory.Plan, policy lifecycle.QualificationPolicy, actions pipeline.GitHubActionRefs, auth, safety string) error {
	results := plan.Environment.QualificationResults
	group, name, err := resultsCoordinates(plan)
	if err != nil {
		return err
	}
	events, err := json.Marshal(qualificationoutcome.ProducerEvents())
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "rendering the producer events")
	}
	conclusions, err := json.Marshal(qualificationoutcome.ProducerConclusions())
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "rendering the producer conclusions")
	}
	bucket, provider, account := results.SecretRefs()
	condition := "!cancelled() && " + safety + //nolint:misspell // GitHub status function uses this exact spelling.
		" && github.event.workflow_run.path == " + qualificationExpression(policy.Source.Workflow) +
		" && contains(fromJSON(" + qualificationExpression(string(events)) + "), github.event.workflow_run.event)" +
		" && contains(fromJSON(" + qualificationExpression(string(conclusions)) + "), github.event.workflow_run.conclusion)"
	fmt.Fprintf(out, "  publish:\n    name: Publish qualification outcomes\n    needs: [native-smoke, qualify]\n    if: ${{ %s }}\n    runs-on: ubuntu-24.04\n    timeout-minutes: 15\n    continue-on-error: true\n    environment: %s\n    permissions:\n      contents: read\n      id-token: write\n    steps:\n", condition, results.GitHubEnvironment)
	trustedQualifierCommit(out, plan, actions)
	out.WriteString("      - name: Build trusted publisher\n        timeout-minutes: 5\n        env:\n          GOTOOLCHAIN: local\n          CGO_ENABLED: '0'\n        run: go build -trimpath -o nvkr-results-publish ./cmd/nvkr-results-publish\n")
	fmt.Fprintf(out, "      - name: Download the qualification outcomes\n        uses: %s # %s\n        timeout-minutes: 5\n        with:\n          pattern: %s${{ github.run_id }}-*\n          merge-multiple: true\n          path: %s\n", actions.Download, plan.Actions["download-artifact"].Version, qualificationoutcome.ArtifactPrefix, publishOutcomesDirectory)
	report := "          OUTCOMES: " + publishOutcomesDirectory + "\n          QUALIFY_RESULT: ${{ needs.qualify.result }}\n          QUALIFY_ATTEMPT: ${{ needs.qualify.outputs." + qualificationAttemptOutput + " }}\n          OUTCOME_WRITTEN: ${{ needs.qualify.outputs." + qualificationOutcomeWrittenOutput + " }}\n"
	selection := "--outcomes \"$OUTCOMES\" --event \"$GITHUB_EVENT_PATH\" --source-workflow " + qualificationShell(policy.Source.Workflow) +
		" --source-branch " + qualificationShell(policy.Source.DefaultBranch) + " --qualify-workflow " + qualificationShell(policy.QualificationWorkflow) +
		" --qualify-result \"$QUALIFY_RESULT\" --qualify-attempt \"$QUALIFY_ATTEMPT\" --outcome-written \"$OUTCOME_WRITTEN\""
	fmt.Fprintf(out, "      - name: Select the outcomes to publish\n        id: select\n        timeout-minutes: 2\n        env:\n%s        run: |\n          set -euo pipefail\n          mkdir -p \"$OUTCOMES\"\n          ./nvkr-results-publish select %s --write \"$OUTCOMES/built-$GITHUB_RUN_ATTEMPT.json\" >> \"$GITHUB_OUTPUT\"\n", report, selection)
	// A re-run must publish this record, not build a different one, so the job
	// stops here rather than publish an outcome it could not keep.
	fmt.Fprintf(out, "      - name: Upload the built outcome\n        if: steps.select.outputs.built == 'true'\n        uses: %s # %s\n        timeout-minutes: 5\n        with:\n          name: %s${{ github.run_id }}-${{ github.run_attempt }}-built\n          path: %s/built-${{ github.run_attempt }}.json\n          if-no-files-found: error\n          retention-days: %d\n", actions.Upload, plan.Actions["upload-artifact"].Version, qualificationoutcome.ArtifactPrefix, publishOutcomesDirectory, plan.GitHubArtifactRetentionDays())
	// The auth action's exported variables would print the cloud project in the
	// public log; the publisher reads only the credentials file.
	fmt.Fprintf(out, "      - name: Authenticate to the results store\n        id: auth\n        uses: %s # %s\n        timeout-minutes: 2\n        with:\n          workload_identity_provider: ${{ secrets.%s }}\n          service_account: ${{ secrets.%s }}\n          create_credentials_file: true\n          export_environment_variables: false\n", auth, plan.Actions["google-auth"].Version, provider, account)
	fmt.Fprintf(out, "      - name: Publish the outcomes\n        timeout-minutes: 8\n        env:\n%s          GOOGLE_APPLICATION_CREDENTIALS: ${{ steps.auth.outputs.credentials_file_path }}\n          NVKR_RESULTS_BUCKET: ${{ secrets.%s }}\n        run: |\n          set -euo pipefail\n          ./nvkr-results-publish publish %s --group %s --definition %s\n", report, bucket, selection, qualificationShell(group), qualificationShell(name))
	return nil
}

// publishesQualificationResults reports whether the release qualifier publishes
// its outcomes: only a production Plan whose Environment declares a target.
func publishesQualificationResults(plan *factory.Plan, policy lifecycle.QualificationPolicy) bool {
	return !policy.Source.Development && plan.Environment.QualificationResults != nil &&
		plan.Environment.QualificationResults.Mode == definition.QualificationResultsModeTestGridGCS
}

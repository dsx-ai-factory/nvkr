// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubCollectedProviderResultsPreserveExactLeaves(t *testing.T) {
	collectors := map[string][]string{"collector-a": {"a", "b"}, "collector-b": {"c", "d"}}
	for _, statuses := range []string{"success failure", "failure success"} {
		t.Run(statuses, func(t *testing.T) {
			raw := carrierTestJSON(t, map[string]any{
				"collector-a": map[string]any{"result": "success", "outputs": map[string]string{
					"results": `{"a":{"result":"failure","outputs":{"ignored":"MUST-NOT-BE-LOGGED"}},"b":{"result":"success"}}`, "statuses": statuses}},
				"collector-b": map[string]any{"result": "success", "outputs": map[string]string{
					"results": `{"c":{"result":"cancelled"},"d":{"result":"skipped","outputs":{"results":"opaque, not a nested report"}}}`, "statuses": "skipped cancelled"}}, //nolint:misspell // GitHub result enum spelling.
			})
			got, err := parseGitHubCollectedProviderResults(t.Context(), raw, []string{"a", "b", "c", "d"}, collectors)
			want := map[string]string{"a": "failure", "b": "success", "c": "cancelled", "d": "skipped"} //nolint:misspell // GitHub result enum spelling.
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("results=%v want=%v error=%v", got, want, err)
			}
		})
	}
}

func TestGitHubCollectedProviderResultsRejectInexactReports(t *testing.T) {
	nested := `{"a":{"result":"success"},"b":{"result":"failure"}}`
	outer := func(report string) string {
		return `{"collector":{"result":"success","outputs":{"results":` + strconv.Quote(report) + `,"statuses":"success failure"}}}`
	}
	valid := outer(nested)
	for _, test := range []struct{ name, raw string }{
		{"missing collector", `{}`},
		{"flat leaves", nested},
		{"unexpected direct leaf", strings.TrimSuffix(valid, "}") + `,"a":{"result":"success"}}`},
		{"duplicate collector", strings.TrimSuffix(valid, "}") + `,"collector":{"result":"success"}}`},
		{"failed collector", strings.Replace(valid, `"result":"success"`, `"result":"failure"`, 1)},
		{"skipped collector", strings.Replace(valid, `"result":"success"`, `"result":"skipped"`, 1)},
		{"cancelled collector", strings.Replace(valid, `"result":"success"`, `"result":"cancelled"`, 1)}, //nolint:misspell // GitHub result enum spelling.
		{"collector unknown field", strings.Replace(valid, `"outputs":`, `"unsafe":"MUST-NOT-BE-LOGGED","outputs":`, 1)},
		{"collector duplicate result", strings.Replace(valid, `"result":"success"`, `"result":"success","result":"failure"`, 1)},
		{"missing outputs", `{"collector":{"result":"success"}}`},
		{"missing results", `{"collector":{"result":"success","outputs":{"statuses":"success failure"}}}`},
		{"missing statuses", strings.Replace(valid, `,"statuses":"success failure"`, "", 1)},
		{"empty report", outer("")},
		{"empty statuses", strings.Replace(valid, `"statuses":"success failure"`, `"statuses":""`, 1)},
		{"extra output", strings.Replace(valid, `"outputs":{`, `"outputs":{"unsafe":"MUST-NOT-BE-LOGGED",`, 1)},
		{"duplicate output", strings.Replace(valid, `"results":`, `"results":"{}","results":`, 1)},
		{"nonstring report", strings.Replace(valid, strconv.Quote(nested), nested, 1)},
		{"missing nested member", outer(`{"a":{"result":"success"}}`)},
		{"unexpected nested member", outer(strings.TrimSuffix(nested, "}") + `,"unknown":{"result":"success"}}`)},
		{"collector nested", outer(strings.TrimSuffix(nested, "}") + `,"collector":{"result":"success"}}`)},
		{"duplicate nested member", outer(strings.TrimSuffix(nested, "}") + `,"a":{"result":"success"}}`)},
		{"duplicate nested result", outer(strings.Replace(nested, `"result":"success"`, `"result":"success","result":"failure"`, 1))},
		{"unknown nested status", outer(strings.Replace(nested, `"success"`, `"MUST-NOT-BE-LOGGED"`, 1))},
		{"missing nested status", outer(strings.Replace(nested, `"result":"success"`, `"outputs":{}`, 1))},
		{"nested output object", outer(strings.Replace(nested, `"result":"success"`, `"result":"success","outputs":{"unsafe":{}}`, 1))},
		{"nested duplicate output", outer(strings.Replace(nested, `"result":"success"`, `"result":"success","outputs":{"unsafe":"a","unsafe":"b"}`, 1))},
		{"nested extra field", outer(strings.Replace(nested, `"result":"success"`, `"result":"success","unsafe":"MUST-NOT-BE-LOGGED"`, 1))},
		{"nested null", outer("null")},
		{"nested array", outer("[]")},
		{"nested truncated", outer(nested[:len(nested)-1])},
		{"nested trailing data", outer(nested + "{}")},
		{"outer trailing data", valid + "{}"},
		{"outer invalid UTF-8", valid + string([]byte{0xff})},
		{"status count", strings.Replace(valid, `"statuses":"success failure"`, `"statuses":"success"`, 1)},
		{"status multiset", strings.Replace(valid, `"statuses":"success failure"`, `"statuses":"success success"`, 1)},
		{"status unknown", strings.Replace(valid, `"statuses":"success failure"`, `"statuses":"success MUST-NOT-BE-LOGGED"`, 1)},
		{"status repeated space", strings.Replace(valid, `"statuses":"success failure"`, `"statuses":"success  failure"`, 1)},
		{"status tab", strings.Replace(valid, `"statuses":"success failure"`, `"statuses":"success\tfailure"`, 1)},
		{"status newline", strings.Replace(valid, `"statuses":"success failure"`, `"statuses":"success\nfailure"`, 1)},
		{"status leading space", strings.Replace(valid, `"statuses":"success failure"`, `"statuses":" success failure"`, 1)},
		{"status trailing space", strings.Replace(valid, `"statuses":"success failure"`, `"statuses":"success failure "`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseGitHubCollectedProviderResults(t.Context(), test.raw, []string{"a", "b"}, map[string][]string{"collector": {"a", "b"}})
			if got != nil || factoryErrorCode(err) != e.CodeInvalidCatalog || strings.Contains(err.Error(), "MUST-NOT-BE-LOGGED") {
				t.Fatalf("unexpected result or unsafe error: %v / %v", got, err)
			}
		})
	}
}

func TestGitHubCollectedProviderMembershipRequiresExactPartition(t *testing.T) {
	for _, test := range []struct {
		name       string
		leaves     []string
		collectors map[string][]string
	}{
		{"duplicate leaf", []string{"a", "a"}, map[string][]string{"collector": {"a"}}},
		{"empty leaf", []string{""}, map[string][]string{"collector": {""}}},
		{"unknown member", []string{"a"}, map[string][]string{"collector": {"other"}}},
		{"duplicate member", []string{"a"}, map[string][]string{"collector": {"a", "a"}}},
		{"shared member", []string{"a"}, map[string][]string{"collector-a": {"a"}, "collector-b": {"a"}}},
		{"empty collector", []string{"a"}, map[string][]string{"collector": {}}},
		{"empty name", []string{"a"}, map[string][]string{"": {"a"}}},
		{"collector leaf collision", []string{"a", "b"}, map[string][]string{"a": {"b"}}},
		{"uncollected member", []string{"a", "b"}, map[string][]string{"collector": {"a"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseGitHubCollectedProviderResults(t.Context(), "{}", test.leaves, test.collectors)
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
		})
	}
}

func TestGitHubCollectedProviderResultsShareResourceBudgets(t *testing.T) {
	for _, test := range []struct {
		name string
		fill func(map[string]string)
		want string
	}{
		{"decoded bytes", func(outputs map[string]string) { outputs["ignored"] = strings.Repeat("x", maxGitHubProviderBytes/3) }, "size bound"},
		{"decoded tokens", func(outputs map[string]string) {
			for i := 0; i < maxGitHubProviderTokens/4; i++ {
				outputs[fmt.Sprintf("key-%d", i)] = ""
			}
		}, "complexity bound"},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputs := map[string]string{}
			test.fill(outputs)
			collectors := map[string][]string{"collector-a": {"a"}, "collector-b": {"b"}}
			outer := map[string]any{}
			for collector, members := range collectors {
				report := carrierTestJSON(t, map[string]any{members[0]: map[string]any{"result": "success", "outputs": outputs}})
				if _, err := parseGitHubProviderResults(t.Context(), report, members); err != nil {
					t.Fatalf("each report must fit independently: %v", err)
				}
				outer[collector] = map[string]any{"result": "success", "outputs": map[string]string{"results": report, "statuses": "success"}}
			}
			raw := carrierTestJSON(t, outer)
			if len(raw) >= maxGitHubProviderBytes {
				t.Fatal("fixture must fit before reports are decoded")
			}
			_, err := parseGitHubCollectedProviderResults(t.Context(), raw, []string{"a", "b"}, collectors)
			if factoryErrorCode(err) != e.CodeInvalidCatalog || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("combined reports bypassed %s: %v", test.want, err)
			}
		})
	}
}

func TestGitHubCollectedProviderResultsPreserveLegacyAndCancellation(t *testing.T) {
	for _, raw := range []string{
		`{"a":{"result":"success"}}`,
		`{"a":{"result":"failure","outputs":{"ignored":"opaque"}}}`,
		`{"a":{"result":"success","outputs":{"nested":{}}}}`,
		`{"a":{"result":"success"},"a":{"result":"failure"}}`,
		`{}`,
	} {
		got, err := parseGitHubCollectedProviderResults(t.Context(), raw, []string{"a"}, nil)
		want, wantErr := parseGitHubProviderResults(t.Context(), raw, []string{"a"})
		if !reflect.DeepEqual(got, want) || factoryErrorCode(err) != factoryErrorCode(wantErr) {
			t.Fatalf("ungrouped contract changed: %v / %v", err, wantErr)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		want e.Code
	}{{"nil context", nil, e.CodeUsage}, {"canceled context", ctx, e.CodeTimeout}} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseGitHubCollectedProviderResults(test.ctx, `{}`, []string{"leaf"}, map[string][]string{"collector": {"leaf"}})
			assertFactoryErrorCode(t, err, test.want)
		})
	}
}

func TestGitHubCollectedRecoveryRejectsIncompleteReportsBeforeWork(t *testing.T) {
	plan, projection, _ := collectedRecoveryPlan(t)
	for _, status := range []string{"success", "failure", "skipped"} {
		t.Run(status, func(t *testing.T) {
			t.Chdir(t.TempDir())
			results := collectorTestReports(t, projection.Collectors, nil)
			collector := sortedKeys(projection.Collectors)[0]
			if status == "success" {
				results[collector] = map[string]any{"result": status, "outputs": map[string]string{}}
			} else {
				results[collector].(map[string]any)["result"] = status
			}
			var output bytes.Buffer
			err := RecoverGitHubArtifacts(t.Context(), plan, carrierTestJSON(t, results), GitHubStepOptions{Stdout: &output, Stderr: &output, SummaryPath: "summary.md"})
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			entries, readErr := os.ReadDir(".")
			if readErr != nil || len(entries) != 0 || output.Len() != 0 {
				t.Fatalf("incomplete reports performed recovery: %v / %v / %s", entries, readErr, output.String())
			}
		})
	}
}

func TestGitHubCollectedRecoveryUsesOriginalLeafResults(t *testing.T) {
	plan, projection, nodeJobs := collectedRecoveryPlan(t)
	if !PerOutputPublication(plan) {
		t.Fatal("fixture requires complete provider closure")
	}
	var restoredLeaf, unconsumedLeaf string
	for _, artifact := range plan.Graph.Artifacts {
		if finalizerArtifact(plan, artifact) {
			restoredLeaf = nodeJobs[artifact.Producer]
			break
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodePackage {
			unconsumedLeaf = nodeJobs[node.ID]
			break
		}
	}
	if restoredLeaf == "" || unconsumedLeaf == "" || restoredLeaf == unconsumedLeaf {
		t.Fatal("fixture requires distinct restored and metadata-only producer jobs")
	}
	for _, test := range []struct{ name, leaf, status string }{
		{"all success", restoredLeaf, "success"},
		{"producer failure", restoredLeaf, "failure"},
		{"producer skipped", restoredLeaf, "skipped"},
		{"unrestored package failure", unconsumedLeaf, "failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			reports := collectorTestReports(t, projection.Collectors, map[string]string{test.leaf: test.status})
			restored := map[string]bool{}
			err := recoverGitHubArtifacts(t.Context(), plan, carrierTestJSON(t, reports), GitHubStepOptions{Stdout: io.Discard, Stderr: io.Discard},
				func(_ context.Context, artifact Artifact) (string, error) {
					restored[artifact.ID] = true
					return "restored:success", nil
				}, writeGitHubRestoreOutcome)
			if err != nil {
				t.Fatal(err)
			}
			for _, artifact := range plan.Graph.Artifacts {
				if finalizerArtifact(plan, artifact) {
					want := test.status == "success" || nodeJobs[artifact.Producer] != test.leaf
					if restored[artifact.ID] != want {
						t.Fatalf("artifact %s ignored original producer status", artifact.ID)
					}
				}
			}
			err = ValidateHostedRestores(t.Context(), ".nvkr/factory", plan)
			if test.status != "success" {
				assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitHubCollectedRecoveryRejectsCollectorFailureWithoutPublication(t *testing.T) {
	plan := recompileGitHubStabilityPlan(t, buildOnlyPlan(t, "ubuntu-24.04"), func(inputs *githubStabilityInputs) {
		for index := 1; index < 12; index++ {
			output := inputs.definition.Outputs[0]
			output.Name = fmt.Sprintf("worker-%02d", index)
			inputs.definition.Outputs = append(inputs.definition.Outputs, output)
		}
	})
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil || len(projection.Collectors) == 0 || PerOutputPublication(plan) {
		t.Fatalf("fixture requires build-only result collectors: %v", err)
	}
	for _, status := range []string{"failure", "skipped", "cancelled"} { //nolint:misspell // GitHub result enum spelling.
		t.Run(status, func(t *testing.T) {
			t.Chdir(t.TempDir())
			reports := collectorTestReports(t, projection.Collectors, nil)
			reports[sortedKeys(projection.Collectors)[0]].(map[string]any)["result"] = status
			calls := 0
			err := recoverGitHubArtifacts(t.Context(), plan, carrierTestJSON(t, reports), GitHubStepOptions{Stdout: io.Discard, Stderr: io.Discard},
				func(context.Context, Artifact) (string, error) {
					calls++
					return "restored:success", nil
				}, writeGitHubRestoreOutcome)
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			entries, readErr := os.ReadDir(".")
			if readErr != nil || len(entries) != 0 || calls != 0 {
				t.Fatalf("unsuccessful collector performed build-only recovery: %v / %v / %d", entries, readErr, calls)
			}
		})
	}
}

func collectedRecoveryPlan(t *testing.T) (*Plan, *githubNeedsProjection, map[string]string) {
	t.Helper()
	plan, err := LoadPlanContext(t.Context(), "../../../"+FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil || len(projection.Collectors) == 0 {
		t.Fatalf("fixture requires result collectors: %v", err)
	}
	_, jobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	return plan, projection, jobs
}

func collectorTestReports(t *testing.T, collectors map[string][]string, overrides map[string]string) map[string]any {
	t.Helper()
	reports := map[string]any{}
	for id, members := range collectors {
		leaves := map[string]any{}
		var statuses []string
		for _, member := range members {
			status := "success"
			if override, present := overrides[member]; present {
				status = override
			}
			statuses = append(statuses, status)
			leaves[member] = map[string]string{"result": status}
		}
		reports[id] = map[string]any{"result": "success", "outputs": map[string]string{"results": carrierTestJSON(t, leaves), "statuses": strings.Join(statuses, " ")}}
	}
	return reports
}

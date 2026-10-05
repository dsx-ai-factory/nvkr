package factory

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"sigs.k8s.io/yaml"
)

func TestGitHubTransfersBatchPresentationAndFailAtExactArtifact(t *testing.T) {
	for _, direction := range []string{"restore", "archive"} {
		t.Run(direction, func(t *testing.T) {
			plan := &Plan{Actions: githubTestPlan(t).Actions}
			actions, err := resolveGitHubActions(plan.Actions)
			if err != nil {
				t.Fatal(err)
			}
			artifacts, jobs := map[string]Artifact{}, map[string]string{}
			var ids []string
			for _, name := range []string{"a", "b", "c"} {
				id := "input/" + name
				ids = append(ids, id)
				artifact := Artifact{ID: id, Name: name, Path: ".nkd/factory/" + name, Producer: "producer/" + name}
				artifacts[id] = artifact
				plan.Graph.Artifacts = append(plan.Graph.Artifacts, artifact)
				jobs[artifact.Producer] = "job-" + name
			}
			var rendered bytes.Buffer
			var run string
			if direction == "restore" {
				stage := githubStage{ID: "consumer-job", Nodes: []Node{{ID: "consumer", Inputs: ids}}}
				if err := writeGitHubStageInputSteps(&rendered, plan, stage, artifacts, jobs, actions); err != nil {
					t.Fatal(err)
				}
				steps := decodeGitHubSteps(t, rendered.Bytes())
				if len(steps) != 2 || steps[0].ContinueOnError || steps[0].With["pattern"] != "{nkd-input-a,nkd-input-b,nkd-input-c}" || steps[0].With["merge-multiple"] != "true" {
					t.Fatalf("stage input batch not exact and mandatory: %+v", steps)
				}
				run = steps[1].Run
			} else {
				node := Node{ID: "package/test", Kind: NodePackage, Outputs: ids}
				writeGitHubOutputSteps(&rendered, plan, node, artifacts, actions)
				steps := decodeGitHubSteps(t, rendered.Bytes())
				if len(steps) != 4 {
					t.Fatalf("want one archive + three independent uploads, got %d", len(steps))
				}
				for _, id := range ids {
					if !actions.stepUploads(steps, githubArtifactName(id), carrierPath(id)) {
						t.Fatalf("exact output carrier %q lost", id)
					}
				}
				run = steps[0].Run
			}
			root := t.TempDir()
			writeTransferFixture(t, root)
			summary := filepath.Join(root, "summary.md")
			log := runGitHubScript(t, root, run, summary, 23)
			if !strings.Contains(log, "at "+direction+" input/b (exit 23") || strings.Contains(log, "helper: transport-unpack --archive .nkd/carriers/input-c.tar") || strings.Contains(log, "helper: transport-pack --archive .nkd/carriers/input-c.tar") || strings.Contains(log, "Completed:") {
				t.Fatalf("batch failed to stop at exact carrier: %s", log)
			}
			raw, err := os.ReadFile(summary)
			if err != nil || !strings.Contains(string(raw), "| failed | 23 |") || !strings.Contains(string(raw), "<code>"+direction+" input/b</code>") {
				t.Fatalf("wrong transfer failure summary: %v\n%s", err, raw)
			}
		})
	}
}

func TestGitHubBatchedRecoveryRecordsEveryOutcomeAfterFailure(t *testing.T) {
	plan := &Plan{Actions: githubTestPlan(t).Actions}
	jobs := map[string]string{}
	for _, name := range []string{"a", "b", "c", "d"} {
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, Artifact{ID: "input/" + name, Name: name, Path: ".nkd/factory/" + name, Producer: "producer-" + name})
		jobs["producer-"+name] = "job-" + name
	}
	var rendered bytes.Buffer
	if err := writeGitHubFinalizerRestoreSteps(&rendered, plan, jobs); err != nil {
		t.Fatal(err)
	}
	steps := decodeGitHubSteps(t, rendered.Bytes())
	if len(steps) != 3 || !steps[1].ContinueOnError {
		t.Fatalf("recovery must retain tolerant exact download then all-carrier accounting: %+v", steps)
	}
	root := t.TempDir()
	for _, dir := range []string{".nkd/carriers", ".nkd/restore-outcomes"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a", "b", "d"} {
		if err := os.WriteFile(filepath.Join(root, carrierPath("input/"+name)), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeTransferFixture(t, root)
	script := steps[2].Run
	for _, name := range []string{"a", "b", "c", "d"} {
		result := "success"
		if name == "a" {
			result = "failure"
		}
		script = strings.ReplaceAll(script, "${{ needs.job-"+name+".result }}", result)
	}
	summary := filepath.Join(root, "summary.md")
	log := runGitHubScript(t, root, script, summary, 0)
	raw, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, outcome string }{{"a", "job-failed"}, {"b", "restore-failed"}, {"c", "missing"}, {"d", "restored:success"}} {
		status, err := os.ReadFile(filepath.Join(root, HostedRestoreOutcomePath(".nkd/factory", "input/"+test.name)))
		if err != nil || string(status) != test.outcome+"\n" {
			t.Fatalf("%s outcome = %q, %v; log %s", test.name, status, err, log)
		}
		if !strings.Contains(string(raw), "| <code>input/"+test.name+"</code> | "+test.outcome+" |") {
			t.Fatalf("summary omits recovery outcome: %s", raw)
		}
	}
}

func TestGitHubCarrierPatternRejectsAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name      string
		artifacts []Artifact
	}{
		{"empty", nil},
		{"duplicate", []Artifact{{ID: "a"}, {ID: "a"}}},
		{"lossy normalization", []Artifact{{ID: "a/b-c"}, {ID: "a-b/c"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pattern, err := githubCarrierPattern(test.artifacts)
			var coded *e.Error
			if pattern != "" || !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("ambiguous selection returned %q, %v", pattern, err)
			}
		})
	}
}

func decodeGitHubSteps(t *testing.T, raw []byte) []githubStep {
	t.Helper()
	var document struct {
		Steps []githubStep `json:"steps"`
	}
	if err := yaml.UnmarshalStrict(append([]byte("steps:\n"), raw...), &document); err != nil {
		t.Fatal(err)
	}
	return document.Steps
}

func writeTransferFixture(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "nkd-exec"), []byte("#!/bin/sh\nprintf 'helper: %s\\n' \"$*\"\ncase \"$*\" in *input-b.tar*) exit 23;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubRecoveryBatchesRespectProviderScriptLimit(t *testing.T) {
	plan := &Plan{Actions: githubTestPlan(t).Actions}
	jobs := map[string]string{}
	for i := range 150 {
		name := fmt.Sprintf("artifact-%03d", i)
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, Artifact{ID: name, Name: name, Path: ".nkd/factory/" + name, Producer: name})
		jobs[name] = "job-" + name
	}
	var rendered bytes.Buffer
	if err := writeGitHubFinalizerRestoreSteps(&rendered, plan, jobs); err != nil {
		t.Fatal(err)
	}
	steps := decodeGitHubSteps(t, rendered.Bytes())
	if len(steps) < 4 {
		t.Fatal("large recovery did not split into bounded batches")
	}
	for _, step := range steps {
		if len(step.Run) > githubMaximumRunBytes {
			t.Fatalf("step %q has %d bytes", step.Name, len(step.Run))
		}
	}
	runs := joinRuns(steps)
	for _, artifact := range plan.Graph.Artifacts {
		if strings.Count(runs, "nkd_recovery=$(cat "+HostedRestoreOutcomePath(".nkd/factory", artifact.ID)+")") != 1 {
			t.Fatalf("recovery batching duplicated or omitted %s", artifact.ID)
		}
	}
}

func TestGitHubRunScriptLimitAdmission(t *testing.T) {
	for _, size := range []int{githubMaximumRunBytes, githubMaximumRunBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			doc := githubWorkflowDocument{Jobs: map[string]githubJob{"job": {Steps: []githubStep{{Name: "oversize", Run: strings.Repeat("a", size)}}}}}
			err := validateGitHubScriptLimits(doc)
			if size == githubMaximumRunBytes {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("oversized script must fail capability admission: %v", err)
			}
		})
	}
}

func TestGitHubRecoveryRejectsOversizedCarrierBeforeOutput(t *testing.T) {
	var rendered bytes.Buffer
	err := writeGitHubRecoveryBatches(&rendered, []string{"set -eu"}, [][]string{{"true"}, {strings.Repeat("a", githubMaximumRunBytes)}})
	var coded *e.Error
	if rendered.Len() != 0 || !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("oversized recovery produced bytes=%d, error=%v", rendered.Len(), err)
	}
}

func TestGitHubOversizedScriptRejectsBundleBeforeOutput(t *testing.T) {
	plan := githubTestPlan(t)
	plan.Environment.Runtime.Setup = []string{"true # " + strings.Repeat("x", githubMaximumRunBytes)}
	var err error
	plan.EnvironmentDigest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	var coded *e.Error
	if bundle != nil || !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("oversized script returned bundle=%v, err=%v", bundle != nil, err)
	}
}

func TestGitHubBatchSelectionCannotExpandRequiredClosure(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	mutated := *bundle
	mutated.Files = make(map[string][]byte, len(bundle.Files))
	for name, raw := range bundle.Files {
		mutated.Files[name] = append([]byte(nil), raw...)
	}
	document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
	var pattern string
	for _, id := range sortedKeys(document.Jobs) {
		if id == "nkd-lifecycle-run" {
			continue
		}
		for _, step := range document.Jobs[id].Steps {
			if step.With["pattern"] != "" {
				pattern = step.With["pattern"]
				break
			}
		}
		if pattern != "" {
			break
		}
	}
	if pattern == "" {
		t.Fatal("test plan has no external input batch")
	}
	workflow := bytes.Replace(bundle.Files[GitHubWorkflowPath], []byte("pattern: "+yamlSingle(pattern)), []byte("pattern: '*'"), 1)
	mutated.Files[GitHubWorkflowPath], mutated.WorkflowDigest = workflow, pipeline.FileDigest(workflow)
	var coded *e.Error
	if err := ValidateGitHubBundle(t.Context(), plan, &mutated); !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("broadened download set was admitted: %v", err)
	}
}

func TestGitHubInputRestoreBatchesRespectProviderLimit(t *testing.T) {
	for _, count := range []int{1, 34, 150} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			plan := &Plan{Actions: githubTestPlan(t).Actions}
			actions, err := resolveGitHubActions(plan.Actions)
			if err != nil {
				t.Fatal(err)
			}
			artifacts, jobs := map[string]Artifact{}, map[string]string{}
			var inputs []string
			for i := range count {
				id := fmt.Sprintf("result/cwe/linux/arm64/component-%03d", i)
				inputs = append(inputs, id)
				artifact := Artifact{ID: id, Name: id, Path: ".nkd/factory/" + id, Producer: "producer/" + id}
				artifacts[id], jobs[artifact.Producer] = artifact, "job-"+id
				plan.Graph.Artifacts = append(plan.Graph.Artifacts, artifact)
			}
			stage := githubStage{ID: "consumer", Nodes: []Node{{ID: "consumer", Inputs: inputs}}}
			var rendered bytes.Buffer
			if err := writeGitHubStageInputSteps(&rendered, plan, stage, artifacts, jobs, actions); err != nil {
				t.Fatal(err)
			}
			steps := decodeGitHubSteps(t, rendered.Bytes())
			if count > 1 && len(steps) < 3 {
				t.Fatal("large restore did not split into bounded steps")
			}
			for _, step := range steps {
				if step.ContinueOnError || len(step.Run) > githubMaximumRunBytes {
					t.Fatalf("input restore is tolerant or oversized: %s", step.Name)
				}
			}
			runs := joinRuns(steps)
			for _, id := range inputs {
				command := "./nkd-exec transport-unpack --archive " + shellQuote(carrierPath(id)) + " --root . --path " + shellQuote(artifacts[id].Path)
				if strings.Count(runs, command) != 1 {
					t.Fatalf("restore duplicated or omitted %s", id)
				}
			}
		})
	}
}

func TestGitHubInputRestoreRejectsOversizeBeforeOutput(t *testing.T) {
	var rendered bytes.Buffer
	err := writeGitHubRestoreBatches(&rendered, []string{"small", "oversized"}, []githubProgressCommand{
		{phase: "small", run: "true"}, {phase: "oversized", run: strings.Repeat("a", githubMaximumRunBytes)},
	})
	var coded *e.Error
	if rendered.Len() != 0 || !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("oversized restore returned bytes=%d, error=%v", rendered.Len(), err)
	}
}

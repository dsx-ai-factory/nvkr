package factory

import (
	"bytes"
	"errors"
	"sort"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

func TestGitHubFinalizerDownloadsExactCarriersAndDelegatesRecovery(t *testing.T) {
	plan := githubTestPlan(t)
	nodeJobs := make(map[string]string, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		nodeJobs[node.ID] = githubJobID(node.ID)
	}

	var rendered bytes.Buffer
	if err := writeGitHubFinalizerRestoreSteps(&rendered, plan, nodeJobs); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Steps []githubStep `json:"steps"`
	}
	if err := yaml.UnmarshalStrict(append([]byte("steps:\n"), rendered.Bytes()...), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Steps) != 2 {
		t.Fatalf("finalizer steps = %d, want download and recovery helper", len(document.Steps))
	}
	download := document.Steps[0]
	if download.Uses != plan.Actions["download-artifact"].Uses() || !download.ContinueOnError ||
		download.With["path"] != ".nkd/carriers" || download.With["merge-multiple"] != "true" {
		t.Fatalf("batch download is incomplete: %+v", download)
	}
	wantNames := make([]string, len(plan.Graph.Artifacts))
	for index, artifact := range plan.Graph.Artifacts {
		wantNames[index] = githubArtifactName(artifact.ID)
	}
	sort.Strings(wantNames)
	wantPattern := wantNames[0]
	if len(wantNames) > 1 {
		wantPattern = "{" + strings.Join(wantNames, ",") + "}"
	}
	if download.With["pattern"] != wantPattern || strings.Contains(download.With["pattern"], "nkd-exec") ||
		strings.Contains(download.With["pattern"], "nkd-lifecycle-run") {
		t.Fatalf("batch pattern = %q, want exact carriers %q", download.With["pattern"], wantPattern)
	}
	recovery := document.Steps[1]
	want := "./nkd-exec factory github recover --plan " + FactoryPlanPath
	if strings.TrimSpace(recovery.Run) != want || recovery.ID != "recover-artifacts" || recovery.ContinueOnError || recovery.If != "" || recovery.Uses != "" {
		t.Fatalf("recovery helper is not exact and mandatory: %+v", recovery)
	}
	if len(recovery.Env) != 1 || recovery.Env["NKD_JOB_RESULTS"] != "${{ toJSON(needs) }}" {
		t.Fatalf("recovery does not receive only provider job descriptions: %+v", recovery.Env)
	}
}

func TestGitHubFinalizerRejectsMergedCarrierNameCollisionsBeforeOutput(t *testing.T) {
	plan := &Plan{Actions: githubTestPlan(t).Actions, Graph: ArtifactGraph{Artifacts: []Artifact{
		{ID: "result/a-b", Producer: "producer-a", Path: ".nkd/factory/a"},
		{ID: "result/a/b", Producer: "producer-b", Path: ".nkd/factory/b"},
	}}}
	var rendered bytes.Buffer
	err := writeGitHubFinalizerRestoreSteps(&rendered, plan, map[string]string{
		"producer-a": "producer-a", "producer-b": "producer-b",
	})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog || rendered.Len() != 0 {
		t.Fatalf("colliding carrier names returned bytes=%d error=%v", rendered.Len(), err)
	}
}

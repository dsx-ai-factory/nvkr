package factory

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

func TestWorkflowCompactionPreservesExpandedGraph(t *testing.T) {
	raw := []byte("'on': {workflow_dispatch: {}}\njobs:\n  first:\n    steps:\n      - uses: actions/checkout@" + testCosignArchiveDigest[7:] + "\n        with: {persist-credentials: 'false'}\n  second:\n    steps:\n      - uses: actions/checkout@" + testCosignArchiveDigest[7:] + "\n        with: {persist-credentials: 'false'}\n")
	compact, err := compactGitHubWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(compact, []byte("*nkd_")) || len(compact) >= len(raw) {
		t.Fatal("repeated steps not compacted")
	}
	expected, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := yaml.YAMLToJSONStrict(compact)
	if err != nil || !bytes.Equal(expected, actual) {
		t.Fatalf("graph changed: %v", err)
	}
	var tree yamlv3.Node
	if err := yamlv3.Unmarshal(compact, &tree); err != nil {
		t.Fatal(err)
	}
	definitions, uses := map[string]bool{}, map[string]bool{}
	var visit func(*yamlv3.Node)
	visit = func(node *yamlv3.Node) {
		if node.Anchor != "" {
			definitions[node.Anchor] = true
		}
		if node.Kind == yamlv3.AliasNode {
			uses[node.Value] = true
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(&tree)
	for name := range definitions {
		if !uses[name] {
			t.Fatalf("unused anchor %s", name)
		}
	}
	second, err := compactGitHubWorkflow(raw)
	if err != nil || !bytes.Equal(compact, second) {
		t.Fatalf("nondeterministic compaction: %v", err)
	}
}

func TestGitHubWorkflowSizeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   []byte
		valid bool
	}{
		{"under", bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes-1), true},
		{"exact", bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes), true},
		{"over", bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes+1), false},
		{"UTF8 bytes", []byte(strings.Repeat("é", maxGitHubWorkflowBytes/2) + "x"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGitHubWorkflowSize(tc.raw)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			}
		})
	}
}

func TestOversizedWorkflowCannotWriteGeneratedBundle(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes+1)
	bundle.Files[GitHubWorkflowPath] = raw
	bundle.WorkflowDigest = pipeline.FileDigest(raw)
	out := filepath.Join(t.TempDir(), "must-not-exist")
	assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), out, bundle), e.CodeEngineCapability)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("oversized workflow wrote output: %v", err)
	}
}

func TestMaintainedWorkflowFitsGitHubFileLimit(t *testing.T) {
	raw, err := os.ReadFile("../../../" + GitHubWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGitHubWorkflowSize(raw); err != nil {
		t.Fatal(err)
	}
}

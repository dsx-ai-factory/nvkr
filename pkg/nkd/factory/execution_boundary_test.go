package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func TestExecuteNodeObservesAndPackagesCompiledInputs(t *testing.T) {
	plan := githubTestPlan(t)
	t.Chdir(t.TempDir())
	ctx := context.Background()
	nodes, artifacts := planIndex(plan)
	var terminal Node
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeVerify {
			terminal = node
			break
		}
	}
	if terminal.ID == "" {
		t.Fatal("fixture has no terminal observation")
	}
	for _, id := range terminal.Inputs {
		if err := writeFactoryFile(ctx, artifacts[id].Path, []byte("fixture for "+id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("plan.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPlanContext(ctx, "plan.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	got, err := loaded.Digest()
	if err != nil || got != want {
		t.Fatalf("loaded digest = %q, error = %v", got, err)
	}
	if err := ExecuteNode(ctx, loaded, terminal.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(artifacts[terminal.Outputs[0]].Path)
	if err != nil {
		t.Fatal(err)
	}
	var observation factoryObservation
	if err := json.Unmarshal(raw, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Qualification != TierStaticUnqualified || observation.RecipeDigest != plan.Recipes[0].RecipeDigest || len(observation.Inputs) != len(terminal.Inputs) {
		t.Fatalf("observation = %+v", observation)
	}
	for i, input := range observation.Inputs {
		id := terminal.Inputs[i]
		digest, err := digestFactoryPath(ctx, artifacts[id].Path)
		if err != nil {
			t.Fatal(err)
		}
		if input.ArtifactID != id || input.Kind != artifacts[id].Kind || input.Digest != digest {
			t.Fatalf("input = %+v", input)
		}
	}
	packageNode := nodes["package/tools"]
	if err := ExecuteNode(ctx, loaded, packageNode.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	bundle, _, err := delivery.InspectBinaryBundle(ctx, artifacts[packageNode.Outputs[0]].Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Entries) == 0 {
		t.Fatal("package contains no executable payloads")
	}
	assertFactoryErrorCode(t, ExecuteNode(ctx, loaded, "missing", nil, nil), e.CodeInvalidCatalog)
	for _, node := range loaded.Graph.Nodes {
		if node.IRNode != "" {
			assertFactoryErrorCode(t, ExecuteNode(ctx, loaded, node.ID, nil, nil), e.CodeUsage)
			break
		}
	}
}

func TestFactoryPlanLoaderRejectsMalformedFiles(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"invalid json", "{"},
		{"unknown field", `{"unknown":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadPlanContext(context.Background(), path)
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
		})
	}
	_, err := LoadPlanContext(context.Background(), filepath.Join(t.TempDir(), "absent"))
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	assertFactoryErrorCode(t, ExecuteNode(context.Background(), nil, "missing", nil, nil), e.CodeUsage)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertFactoryErrorCode(t, ExecuteNode(ctx, nil, "missing", nil, nil), e.CodeTimeout)
}

func TestFactoryTreeObservationBindsBytesPathsAndModes(t *testing.T) {
	ctx := context.Background()
	makeTree := func() string {
		t.Helper()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "sub", "tool"), []byte("tool"), 0o755); err != nil {
			t.Fatal(err)
		}
		return root
	}
	base, err := digestFactoryPath(ctx, makeTree())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		change       func(string) error
		same, reject bool
	}{
		{name: "relocation", same: true},
		{name: "changed bytes", change: func(root string) error {
			return os.WriteFile(filepath.Join(root, "sub", "tool"), []byte("changed"), 0o755)
		}},
		{name: "changed mode", change: func(root string) error { return os.Chmod(filepath.Join(root, "sub", "tool"), 0o644) }},
		{name: "changed path", change: func(root string) error {
			return os.Rename(filepath.Join(root, "sub", "tool"), filepath.Join(root, "sub", "renamed"))
		}},
		{name: "symlink", change: func(root string) error { return os.Symlink("tool", filepath.Join(root, "sub", "alias")) }, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := makeTree()
			if tc.change != nil {
				if err := tc.change(root); err != nil {
					t.Fatal(err)
				}
			}
			got, err := digestFactoryPath(ctx, root)
			if tc.reject {
				if err == nil {
					t.Fatal("unsupported entry accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got == base) != tc.same {
				t.Fatalf("digest = %s, base = %s", got, base)
			}
		})
	}
}

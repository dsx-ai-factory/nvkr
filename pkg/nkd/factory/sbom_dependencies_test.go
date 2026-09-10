package factory

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func TestMaintainedSBOMGraphRequiresScannerAndPayloads(t *testing.T) {
	for _, tc := range []struct {
		name, omit string
		code       e.Code
	}{
		{"complete", "", ""},
		{"missing scanner", "tool/syft", e.CodeEngineCapability},
		{"missing payload", "artifact/nke-control-plane/cwe", e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := LoadPlanContext(t.Context(), "../../../.github/nkd-run/plan.json")
			if err != nil {
				t.Fatal(err)
			}
			if plan.ExecutionSettings.SBOMTool != "syft" {
				t.Fatal("maintained workflow does not request dependency inspection")
			}
			if tc.omit != "" {
				for i := range plan.Graph.Nodes {
					node := &plan.Graph.Nodes[i]
					if node.ID == "nke-control-plane/sbom" {
						var inputs []string
						for _, input := range node.Inputs {
							if input != tc.omit {
								inputs = append(inputs, input)
							}
						}
						node.Inputs = inputs
					}
				}
			}
			err = plan.validateSBOMGraph()
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// This opt-in test runs the actual executor with downloaded artifacts. Producer
// records are measured fixtures; this is offline execution evidence, not a new
// GitHub run, signature or qualified producer receipt.
func TestRealArtifactsThroughSBOMExecutor(t *testing.T) {
	dir := os.Getenv("NKD_SBOM_ARTIFACT_DIR")
	if dir == "" {
		t.Skip("requires downloaded real artifacts")
	}
	planPath := os.Getenv("NKD_SBOM_PLAN")
	if planPath == "" {
		t.Fatal("NKD_SBOM_PLAN is required")
	}
	plan, err := LoadPlanContext(t.Context(), planPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Chdir(root)
	_, artifacts := planIndex(plan)
	scanner := artifacts["tool/"+plan.ExecutionSettings.SBOMTool]
	raw, err := os.ReadFile(os.Getenv("NKD_SBOM_SCANNER"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFactoryFile(t.Context(), scanner.Path, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if !strings.HasPrefix(artifact.ID, "artifact/") {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(dir, filepath.Base(artifact.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFactoryFile(t.Context(), artifact.Path, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		result := pipeline.ArtifactResult{Component: artifact.Component, SHA256: fmt.Sprintf("sha256:%x", sha256.Sum256(payload)), Size: int64(len(payload))}
		encoded, err := canonical.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range artifacts {
			if candidate.Kind == "producer-result" && candidate.Component == artifact.Component && candidate.RecipeTarget == artifact.RecipeTarget {
				if err := writeFactoryFile(t.Context(), candidate.Path, encoded, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	count := 0
	for _, node := range plan.Graph.Nodes {
		if node.Kind != NodeSBOM {
			continue
		}
		if err := ExecuteNode(t.Context(), plan, node.ID, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
		output := artifacts[node.Outputs[0]]
		raw, err := os.ReadFile(filepath.Join(output.Path, "index.json"))
		if err != nil {
			t.Fatal(err)
		}
		var index sbom.Index
		if err := canonical.UnmarshalExact(raw[:len(raw)-1], &index); err != nil {
			t.Fatal(err)
		}
		for _, entry := range index.Entries {
			raw, err := os.ReadFile(filepath.Join(output.Path, entry.Artifact+".cdx.json"))
			if err != nil {
				t.Fatal(err)
			}
			var bom sbom.BOM
			if err := canonical.UnmarshalExact(raw[:len(raw)-1], &bom); err != nil {
				t.Fatal(err)
			}
			digest, err := bom.Digest()
			if err != nil || digest != entry.SBOMDigest || len(bom.Components) == 0 {
				t.Fatalf("invalid dependency evidence %s: %v", entry.Artifact, err)
			}
			if out := os.Getenv("NKD_SBOM_OUTPUT_DIR"); out != "" {
				if err := os.WriteFile(filepath.Join(out, entry.Artifact+".cdx.json"), raw, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("%s: %d dependencies, SBOM %s", entry.Artifact, len(bom.Components), digest)
			count++
		}
	}
	if count != 3 {
		t.Fatalf("expected three populated SBOMs, got %d", count)
	}
}

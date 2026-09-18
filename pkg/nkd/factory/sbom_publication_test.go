package factory

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func publicationSBOMFixture(t *testing.T) (*Plan, map[string]Artifact, Node) {
	t.Helper()
	plan := sbomTestPlan(t)
	t.Chdir(t.TempDir())
	nodes, artifacts := planIndex(plan)
	for _, a := range artifacts {
		if a.Kind != "producer-result" {
			continue
		}
		raw := []byte("built " + a.Component)
		for _, payload := range artifacts {
			if strings.HasPrefix(payload.ID, "artifact/") && payload.Component == a.Component && payload.BuildTarget == a.BuildTarget {
				if err := writeFactoryFile(t.Context(), payload.Path, raw, 0o755); err != nil {
					t.Fatal(err)
				}
				writeProducerRecordFixture(t, plan, payload, a.Path)
			}
		}
	}
	if err := executeSBOM(t.Context(), plan, nodes["node-worker/sbom"], artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := executePackage(t.Context(), plan, nodes["package/node-worker"], artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	inputs := []string{"package/node-worker"}
	inputs = append(inputs, packageProducerResults(inputs, nodes, artifacts)...)
	inputs = uniqueSorted(append(inputs, publicationSBOMInputs(inputs, nodes, artifacts)...))
	node := Node{ID: "package/release", Kind: NodePackage, PackageKind: PackageOCI, Inputs: inputs, Outputs: []string{"package/release"}}
	artifacts[node.Outputs[0]] = Artifact{ID: node.Outputs[0], Kind: PackageOCI, Name: "publication", Path: "publication", Producer: node.ID}
	return plan, artifacts, node
}

func TestPublicationContainsExactBoundSBOMBytes(t *testing.T) {
	plan, artifacts, node := publicationSBOMFixture(t)
	if err := executePackage(t.Context(), plan, node, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	inspected, err := delivery.Inspect(t.Context(), "publication")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, object := range inspected.Root.Objects {
		if object.Kind != "sbom" && object.Kind != "sbom-index" {
			continue
		}
		_, raw, err := delivery.ReadObject(t.Context(), "publication", object.Name)
		if err != nil {
			t.Fatal(err)
		}
		var path string
		if object.Kind == "sbom-index" {
			path = "index.json"
		} else {
			var bom sbom.BOM
			if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &bom); err != nil {
				t.Fatal(err)
			}
			digest, err := bom.Digest()
			if err != nil || digest != object.SemanticDigest {
				t.Fatal("invalid published SBOM semantic identity", err)
			}
			path = bom.Metadata.Component.Name + ".cdx.json"
		}
		source, err := os.ReadFile(filepath.Join(artifacts["sbom/node-worker"].Path, path))
		if err != nil {
			t.Fatal(err)
		}
		var sourceBOM sbom.BOM
		if err := canonical.UnmarshalExact(bytes.TrimSuffix(source, []byte{'\n'}), &sourceBOM); err != nil {
			t.Fatal(err)
		}
		subject := "sha256:" + sourceBOM.Metadata.Component.Hashes[0].Content
		bound, err := sbom.ForSubject(&sourceBOM, subject, "build-output", subject)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := bound.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if object.Evidence == nil || object.Evidence.Stage != "build-output" || object.Evidence.SubjectDigest != subject || !bytes.Equal(raw, expected) {
			t.Fatal("publication changed the exact stage-bound SBOM bytes or subject")
		}
		count++
	}
	expected := len(plan.Builds[0].ResolvedComponents)
	if count != expected {
		t.Fatalf("got %d evidence objects, want %d", count, expected)
	}
	clone := artifacts[node.Outputs[0]]
	clone.Path = "publication-again"
	artifacts[node.Outputs[0]] = clone
	if err := executePackage(t.Context(), plan, node, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	again, err := delivery.Inspect(t.Context(), clone.Path)
	if err != nil || again.RootDigest != inspected.RootDigest {
		t.Fatal("publication was not deterministic", err)
	}
	// Altering any evidence blob must invalidate the root's authenticated closure.
	for _, object := range inspected.Root.Objects {
		if object.Kind != "sbom" {
			continue
		}
		path := filepath.Join("publication", "blobs", "sha256", strings.TrimPrefix(object.BlobDigest, "sha256:"))
		if err := os.WriteFile(path, []byte("substituted evidence"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := delivery.Inspect(t.Context(), "publication"); err == nil {
			t.Fatal("tampered SBOM accepted")
		}
		break
	}
}

func TestPublicationRejectsInvalidSBOMBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sbom.Index)
		remove bool
		code   e.Code
	}{
		{name: "missing document", remove: true, code: e.CodeProvenanceMissing},
		{name: "wrong subject", change: func(i *sbom.Index) { i.Entries[0].ArtifactSHA = "sha256:" + strings.Repeat("a", 64) }, code: e.CodeVerifyFailed},
		{name: "wrong build", change: func(i *sbom.Index) { i.BuildDigest = "sha256:" + strings.Repeat("b", 64) }, code: e.CodeVerifyFailed},
		{name: "wrong evidence digest", change: func(i *sbom.Index) { i.Entries[0].SBOMDigest = "sha256:" + strings.Repeat("c", 64) }, code: e.CodeVerifyFailed},
		{name: "omitted entry", change: func(i *sbom.Index) { i.Entries = i.Entries[1:] }, code: e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, artifacts, node := publicationSBOMFixture(t)
			dir := artifacts["sbom/node-worker"].Path
			raw, err := os.ReadFile(filepath.Join(dir, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			var index sbom.Index
			if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &index); err != nil {
				t.Fatal(err)
			}
			if tc.remove {
				if err := os.Remove(filepath.Join(dir, index.Entries[0].Artifact+".cdx.json")); err != nil {
					t.Fatal(err)
				}
			} else {
				tc.change(&index)
				raw, err = canonical.Marshal(index)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err = executePackage(t.Context(), plan, node, artifacts, io.Discard)
			assertFactoryErrorCode(t, err, tc.code)
			if _, err := os.Stat("publication"); !os.IsNotExist(err) {
				t.Fatal("failed evidence check wrote a publication")
			}
		})
	}
}

func TestPublicationGraphRequiresSBOMInputs(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nkd-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for i := range plan.Graph.Nodes {
		node := &plan.Graph.Nodes[i]
		if node.Kind != NodePackage || node.PackageKind != PackageOCI {
			continue
		}
		original := append([]string(nil), node.Inputs...)
		for _, id := range original {
			if !strings.HasPrefix(id, "sbom/") {
				continue
			}
			node.Inputs = nil
			for _, input := range original {
				if input != id {
					node.Inputs = append(node.Inputs, input)
				}
			}
			assertFactoryErrorCode(t, plan.Validate(), e.CodeProvenanceMissing)
			node.Inputs = original
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("maintained publications have no SBOM inputs")
	}
}

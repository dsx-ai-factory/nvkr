package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// This fixture exercises packaging/observation of real standard OCI bytes. Its
// synthetic producer records and declaration SBOMs establish no runtime claim.
func indexObservationFixture(t *testing.T) (*Plan, Node, map[string]Artifact) {
	t.Helper()
	ctx := t.Context()
	root := t.TempDir()
	plan := &Plan{}
	artifacts := map[string]Artifact{}
	required := &ImageIndexPlan{RequiredPlatforms: []string{"linux/amd64", "linux/arm64"}}
	node := Node{ID: "package/cwe", Kind: NodePackage, PackageKind: PackageImage, Outputs: []string{"package/cwe"}, ImageIndex: required}
	for i, platform := range required.RequiredPlatforms {
		arch := strings.TrimPrefix(platform, "linux/")
		target := "cwe/" + platform
		component := "controller"
		inputDir := filepath.Join(root, "input-"+arch)
		if err := os.MkdirAll(inputDir, 0o755); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(inputDir, "controller")
		if err := os.WriteFile(binary, []byte("fixture "+arch), 0o755); err != nil {
			t.Fatal(err)
		}
		layout := filepath.Join(inputDir, "layout")
		if _, err := assemble.OCIImage(binary, component, 1, arch, layout); err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(layout, "image.tar")
		if err := transportarchive.Pack(ctx, archive, layout, []string{"blobs", "index.json", "oci-layout"}); err != nil {
			t.Fatal(err)
		}
		digest, err := digestFactoryPath(ctx, archive)
		if err != nil {
			t.Fatal(err)
		}
		stat, err := os.Stat(archive)
		if err != nil {
			t.Fatal(err)
		}
		buildDigest := "sha256:" + strings.Repeat(string(rune('a'+i)), 64)
		child := ImageIndexChildPlan{Platform: platform, BuildTarget: target, BuildDigest: buildDigest, Component: component, ArtifactID: "artifact/" + target + "/" + component, ResultArtifactID: "result/" + target + "/" + component, SBOMArtifactID: "sbom/" + target}
		producer := target + "/build-controller"
		resultPath := filepath.Join(inputDir, "result.json")
		record := pipeline.ArtifactResult{Component: component, OutputType: "oci-image", SHA256: digest, Size: stat.Size()}
		raw, err := canonical.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(resultPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		artifacts[child.ArtifactID] = Artifact{ID: child.ArtifactID, Name: component, Kind: "oci-image", BuildTarget: target, Component: component, Producer: producer, Path: archive}
		artifacts[child.ResultArtifactID] = Artifact{ID: child.ResultArtifactID, Name: component + "-result", Kind: "producer-result", BuildTarget: target, Component: component, Producer: producer, Path: resultPath}
		artifacts[child.SBOMArtifactID] = Artifact{ID: child.SBOMArtifactID, Name: arch + "-sbom", Kind: "sbom-set", BuildTarget: target, Producer: target + "/sbom", Path: filepath.Join(inputDir, "sbom")}
		bom, err := sbom.NewSubject(buildDigest, component, digest)
		if err != nil {
			t.Fatal(err)
		}
		bomRaw, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		bomDigest, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		index := sbom.Index{SchemaVersion: sbom.IndexSchemaVersion, BuildDigest: buildDigest, Entries: []sbom.IndexEntry{{Artifact: component, ArtifactSHA: digest, SBOMDigest: bomDigest}}}
		indexRaw, err := canonical.Marshal(index)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFactoryFile(ctx, filepath.Join(artifacts[child.SBOMArtifactID].Path, "index.json"), indexRaw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeFactoryFile(ctx, filepath.Join(artifacts[child.SBOMArtifactID].Path, component+".cdx.json"), bomRaw, 0o600); err != nil {
			t.Fatal(err)
		}
		plan.Builds = append(plan.Builds, BuildPlan{Name: target, Platform: platform, BuildDigest: buildDigest, Components: []string{component}, Graph: &ir.Graph{Nodes: []ir.Node{{ID: "build-controller", Kind: ir.KindBuild, Component: component, OutputType: "oci-image"}}}})
		plan.Graph.Nodes = append(plan.Graph.Nodes, Node{ID: target + "/sbom", Kind: NodeSBOM, BuildTarget: target, Inputs: []string{child.ResultArtifactID}, Outputs: []string{child.SBOMArtifactID}})
		required.Children = append(required.Children, child)
		node.Inputs = append(node.Inputs, child.ArtifactID, child.ResultArtifactID, child.SBOMArtifactID)
	}
	slices.Sort(node.Inputs)
	artifacts[node.Outputs[0]] = Artifact{ID: node.Outputs[0], Name: "cwe", Kind: PackageImage, MediaType: delivery.MediaOCIIndex, Producer: node.ID, Path: filepath.Join(root, "packaged")}
	plan.Graph.Nodes = append(plan.Graph.Nodes, node)
	for _, a := range artifacts {
		plan.Graph.Artifacts = append(plan.Graph.Artifacts, a)
	}
	digest, err := required.buildDigest()
	if err != nil {
		t.Fatal(err)
	}
	plan.LifecycleOutputs = []LifecycleOutputPlan{{Name: "cwe", Kind: "oci", ArtifactID: node.Outputs[0], BuildDigest: digest, Components: []string{"controller"}, ImageIndex: required.Clone()}}
	return plan, node, artifacts
}

func TestImageIndexPackagingObservesEveryExactChild(t *testing.T) {
	p, node, artifacts := indexObservationFixture(t)
	if err := executePackage(t.Context(), p, node, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	observed, err := ObserveLifecyclePayload(t.Context(), p, p.LifecycleOutputs[0], artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if observed.ImageIndex == nil || len(observed.ImageIndex.Children) != 2 {
		t.Fatal("lost native child bindings")
	}
	digest, err := factoryImagePayloadDigest(t.Context(), p, artifacts[node.Outputs[0]])
	if err != nil || digest != observed.Digest {
		t.Fatalf("Helm-bound aggregate digest %s: %v", digest, err)
	}
	publication := Node{Inputs: append(slices.Clone(node.Inputs), node.Outputs[0])}
	evidence, err := releaseEvidenceObjects(t.Context(), p, publication, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 2 {
		t.Fatalf("got %d child inventories", len(evidence))
	}
	for _, object := range evidence {
		binding := object.Evidence
		found := false
		for _, child := range observed.ImageIndex.Children {
			if binding.Platform == child.Platform {
				found = true
				if binding.SubjectDigest != child.ManifestDigest || binding.InspectionInputDigest != child.BuildSubject || binding.Component != child.Component {
					t.Fatalf("child evidence mismatch: %+v", binding)
				}
			}
		}
		if !found {
			t.Fatal("evidence omitted native platform")
		}
	}

	p.DefinitionDigest = "sha256:" + strings.Repeat("1", 64)
	p.EnvironmentDigest = "sha256:" + strings.Repeat("2", 64)
	publication.ID, publication.Kind, publication.PackageKind = "package/release", NodePackage, PackageOCI
	publication.Outputs = []string{"package/release"}
	rootArtifact := Artifact{ID: "package/release", Name: "release", Kind: PackageOCI, Producer: publication.ID, Path: filepath.Join(t.TempDir(), "release")}
	artifacts[rootArtifact.ID] = rootArtifact
	p.Graph.Nodes = append(p.Graph.Nodes, publication)
	p.Graph.Artifacts = append(p.Graph.Artifacts, rootArtifact)
	if err := executePackage(t.Context(), p, publication, artifacts, io.Discard); err != nil {
		t.Fatal(err)
	}
	inspection, err := delivery.Inspect(t.Context(), rootArtifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	held, err := delivery.OpenPublication(t.Context(), rootArtifact.Path, inspection.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	var image delivery.PublishedObject
	for _, object := range inspection.Root.Objects {
		if object.Name == "cwe" {
			image = object
		}
	}
	if image.Output == nil || image.Output.ImageIndex == nil || image.ArtifactType != delivery.MediaOCIIndex || image.DescriptorDigest != observed.Digest || image.OS != "" || image.Arch != "" {
		t.Fatalf("aggregate publication binding: %+v", image)
	}
	if err := executePackage(t.Context(), p, node, artifacts, io.Discard); err == nil {
		t.Fatal("overwrote completed index")
	}
}

func TestImageIndexPackagingFailsBeforeOutputOnMissingChildEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Plan, Node, map[string]Artifact)
		code   e.Code
	}{
		{"missing result", func(_ *Plan, n Node, a map[string]Artifact) { delete(a, n.ImageIndex.Children[1].ResultArtifactID) }, e.CodeReleaseIncomplete},
		{"missing inventory", func(_ *Plan, n Node, a map[string]Artifact) { delete(a, n.ImageIndex.Children[1].SBOMArtifactID) }, e.CodeReleaseIncomplete},
		{"sibling result identity", func(_ *Plan, n Node, a map[string]Artifact) {
			id := n.ImageIndex.Children[1].ResultArtifactID
			b := a[id]
			b.BuildTarget = n.ImageIndex.Children[0].BuildTarget
			a[id] = b
		}, e.CodeVerifyFailed},
		{"sibling inventory bytes", func(_ *Plan, n Node, a map[string]Artifact) {
			id := n.ImageIndex.Children[1].SBOMArtifactID
			b := a[id]
			b.Path = a[n.ImageIndex.Children[0].SBOMArtifactID].Path
			a[id] = b
		}, e.CodeVerifyFailed},
		{"same bytes wrong architecture", func(_ *Plan, n Node, a map[string]Artifact) {
			first, second := n.ImageIndex.Children[0], n.ImageIndex.Children[1]
			b := a[second.ArtifactID]
			b.Path = a[first.ArtifactID].Path
			a[second.ArtifactID] = b
			b = a[second.ResultArtifactID]
			b.Path = a[first.ResultArtifactID].Path
			a[second.ResultArtifactID] = b
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, n, a := indexObservationFixture(t)
			tc.mutate(p, n, a)
			err := executePackage(t.Context(), p, n, a, io.Discard)
			assertFactoryErrorCode(t, err, tc.code)
			if _, err := os.Lstat(a[n.Outputs[0]].Path); !os.IsNotExist(err) {
				t.Fatalf("partial output: %v", err)
			}
		})
	}
}

func TestImageIndexPackagingCancellationHasNoOutput(t *testing.T) {
	p, n, a := indexObservationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertFactoryErrorCode(t, executePackage(ctx, p, n, a, io.Discard), e.CodeTimeout)
	if _, err := os.Lstat(a[n.Outputs[0]].Path); !os.IsNotExist(err) {
		t.Fatalf("partial output: %v", err)
	}
}

// Exercise the consumer joins independently of generic packageInputs admission.
func TestImageIndexConsumerRejectsMissingOrSubstitutedEvidence(t *testing.T) {
	for _, route := range []string{"prepare", "observe"} {
		for _, tc := range []struct {
			name    string
			child   int
			missing bool
			code    e.Code
		}{
			{"missing inventory", 1, true, e.CodeProvenanceMissing},
			{"first result substitution", 0, false, e.CodeReleaseIncomplete},
			{"second result substitution", 1, false, e.CodeVerifyFailed},
			{"later result unrelated target", 1, false, e.CodeReleaseIncomplete},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				p, n, a := indexObservationFixture(t)
				// Materialize a valid aggregate before corrupting only the consumer handoff.
				if err := executePackage(t.Context(), p, n, a, io.Discard); err != nil {
					t.Fatal(err)
				}
				child := n.ImageIndex.Children[tc.child]
				if tc.missing {
					delete(a, child.SBOMArtifactID)
				} else {
					result := a[child.ResultArtifactID]
					result.BuildTarget = n.ImageIndex.Children[1-tc.child].BuildTarget
					if tc.name == "later result unrelated target" {
						result.BuildTarget = "unrelated-target"
						result.Component = "unrelated-component"
					}
					a[child.ResultArtifactID] = result
				}
				if route == "prepare" {
					index, binding, err := prepareImageIndex(t.Context(), p, n.ImageIndex, a)
					if index != nil {
						defer index.Close()
					}
					assertFactoryErrorCode(t, err, tc.code)
					if tc.name == "later result unrelated target" && (!strings.Contains(err.Error(), "omits exact native producer result") || !strings.Contains(err.Error(), child.ResultArtifactID)) {
						t.Fatalf("did not reach later-child exact-record join: %v", err)
					}
					if index != nil || binding != nil {
						t.Fatal("failed consumer returned held index or binding")
					}
				} else {
					observed, err := ObserveLifecyclePayload(t.Context(), p, p.LifecycleOutputs[0], a)
					assertFactoryErrorCode(t, err, tc.code)
					if tc.name == "later result unrelated target" && (!strings.Contains(err.Error(), "omits exact native producer result") || !strings.Contains(err.Error(), child.ResultArtifactID)) {
						t.Fatalf("did not reach later-child exact-record join: %v", err)
					}
					if !reflect.DeepEqual(observed, LifecyclePayloadObservation{}) {
						t.Fatalf("failed consumer returned observation: %+v", observed)
					}
				}
			})
		}
	}
}

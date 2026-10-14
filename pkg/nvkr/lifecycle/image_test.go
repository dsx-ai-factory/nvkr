// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/assemble"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"sigs.k8s.io/yaml"
)

func TestCombinedBuildKitRuntimeAdmission(t *testing.T) {
	ctx := context.Background()
	t.Chdir(filepath.Join("..", "..", ".."))
	root := filepath.Join("definitions", "nvke-v1")
	definitionPath := filepath.Join(root, "definition.yaml")
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environmentPath := filepath.Join(root, "environment-github.yaml")
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, authored, resolved string
	}{
		{name: "docker", authored: "docker", resolved: "docker"},
		{name: "podman", authored: "podman", resolved: "podman"},
		{name: "default", resolved: "podman"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			environment.Runtime.Runtime = tc.authored
			plan, err := Compile(ctx, definitionPath, definition, environmentPath, environment, "v0.0.0 (test)")
			if err != nil {
				t.Fatal(err)
			}
			seenReceipts := map[string]bool{}
			for _, artifact := range plan.Graph.Artifacts {
				if artifact.Kind != "source-receipt" {
					continue
				}
				if seenReceipts[artifact.Path] || !strings.HasSuffix(artifact.Path, ".json") {
					t.Fatalf("source receipt carrier overlaps another fetch: %s", artifact.Path)
				}
				seenReceipts[artifact.Path] = true
			}
			if len(seenReceipts) < 3 {
				t.Fatal("combined fixture must exercise independent fetches within one build")
			}
			bundle, err := factory.Render(ctx, plan, factory.EngineGitHub)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Environment.RuntimeBinary() != tc.resolved {
				t.Fatalf("CWE Plan lost its admitted %s runtime", tc.resolved)
			}
			if err := factory.ValidateGitHubBundle(ctx, plan, bundle); err != nil {
				t.Fatal(err)
			}
			assertGitHubSharedNodeInvocation(t, bundle, "cwe/build-cwe")
		})
	}
}

// Inspect the concrete call, its canonical selector data and the referenced
// template together. Factory validation authenticates their complete expansion.
func assertGitHubSharedNodeInvocation(t *testing.T, bundle *factory.GeneratedBundle, selector string) {
	t.Helper()
	type step struct {
		ID   string            `json:"id"`
		Uses string            `json:"uses"`
		Run  string            `json:"run"`
		With map[string]string `json:"with"`
		Env  map[string]string `json:"env"`
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []step `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(bundle.Files[bundle.WorkflowPath], &workflow); err != nil {
		t.Fatal(err)
	}
	var data struct {
		Entries map[string]struct {
			Template string            `json:"template"`
			Values   map[string]string `json:"values"`
		} `json:"entries"`
	}
	if err := canonical.UnmarshalExact(bundle.Files[factory.GitHubActionDataPath], &data); err != nil {
		t.Fatal(err)
	}
	found := 0
	for jobID, job := range workflow.Jobs {
		for _, call := range job.Steps {
			binding := call.With["parameters"]
			if binding == "" {
				continue
			}
			const prefix = "${{ fromJSON(steps.nvkr-context.outputs.data)['"
			part := strings.TrimSuffix(strings.TrimPrefix(binding, prefix), "'] }}")
			if binding != prefix+part+"'] }}" || data.Entries[jobID].Template != "stage-context" {
				t.Fatal("shared execution lacks an exact stage-context binding")
			}
			var values map[string]string
			if err := canonical.UnmarshalExact([]byte(data.Entries[jobID].Values[part]), &values); err != nil {
				t.Fatal(err)
			}
			if values["execute_present"] != "true" || values["execute_selector"] != selector {
				continue
			}
			found++
			if !strings.HasPrefix(call.Uses, "./") {
				t.Fatal("node selector is not passed to a generated local action")
			}
			var action struct {
				Runs struct {
					Steps []step `json:"steps"`
				} `json:"runs"`
			}
			if err := yaml.Unmarshal(bundle.Files[strings.TrimPrefix(call.Uses, "./")+"/action.yml"], &action); err != nil {
				t.Fatal(err)
			}
			executors := 0
			for _, operation := range action.Runs.Steps {
				if operation.ID != "operation_execute" {
					continue
				}
				executors++
				want := "./nvkr-exec factory github run-node --plan " + bundle.PlanPath + ` --node "$NVKR_ACTION_SELECTOR"`
				if strings.TrimSpace(operation.Run) != want || operation.Env["NVKR_ACTION_SELECTOR"] != "${{ fromJSON(inputs.parameters).execute_selector }}" {
					t.Fatal("shared execution changed the Plan-bound helper or quoted node selector")
				}
			}
			if executors != 1 {
				t.Fatalf("node invocation has %d execution roles, want one", executors)
			}
		}
	}
	if found != 1 {
		t.Fatalf("workflow has %d invocations of %q, want one", found, selector)
	}
}

// Synthetic native OCI bytes exercise local and restored hosted observation,
// not actual runner execution, registry delivery, signing or Run acceptance.
func TestRelocatedLifecycleImageObservation(t *testing.T) {
	for _, hosted := range []bool{false, true} {
		for _, substituted := range []bool{false, true} {
			name := "local"
			if hosted {
				name = "pipeline-attested-restored"
			}
			if substituted {
				name += "/copied-result-at-foreign-path"
			}
			t.Run(name, func(t *testing.T) {
				t.Chdir(t.TempDir())
				plan, slot, artifacts, subject := relocatedImageFixture(t, hosted)
				identity, err := plan.IdentityDigest()
				if err != nil {
					t.Fatal(err)
				}
				workspace := ".nvkr/restored-image-attempt"
				if err := os.Rename(".nvkr/factory", workspace); err != nil {
					t.Fatal(err)
				}
				if substituted {
					id := slot.ComponentResults[0]
					record := artifacts[id]
					original, err := lifecycleArtifactPath(record.Path, workspace)
					if err != nil {
						t.Fatal(err)
					}
					bytes, err := os.ReadFile(original)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(original+".substituted", bytes, 0o600); err != nil {
						t.Fatal(err)
					}
					record.Path += ".substituted"
					artifacts[id] = record
				}
				var observed []Artifact
				if hosted {
					observed, err = observeHostedOutputs(t.Context(), workspace, []requiredOutput{slot}, artifacts, plan)
				} else {
					observed, err = observeOutputs(t.Context(), workspace, []requiredOutput{slot}, artifacts, plan, nil)
				}
				if substituted {
					assertCode(t, err, e.CodeProvenanceMissing)
					if observed != nil {
						t.Fatal("foreign result path exposed partial outputs")
					}
				} else if err != nil || len(observed) != 1 || observed[0].Digest != subject || observed[0].BuildDigest != slot.BuildDigest {
					t.Fatalf("relocated output=%+v, error=%v, subject=%s", observed, err, subject)
				}
				after, err := plan.IdentityDigest()
				if err != nil || after != identity {
					t.Fatal("observation changed the original compiled identity")
				}
			})
		}
	}
}

func relocatedImageFixture(t *testing.T, hosted bool) (*factory.Plan, requiredOutput, map[string]factory.Artifact, string) {
	t.Helper()
	root := t.TempDir()
	binary, source := filepath.Join(root, "worker"), filepath.Join(root, "image")
	if err := os.WriteFile(binary, []byte("fixture worker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := assemble.OCIImage(binary, "worker", 1, "arm64", source); err != nil {
		t.Fatal(err)
	}
	image, err := delivery.InspectContainerImage(t.Context(), source, "linux/arm64")
	if err != nil {
		t.Fatal(err)
	}
	pin := "sha256:" + repeat("1", 64)
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"worker": {
		Platforms: []string{"linux/arm64"}, Tag: "1.2.3", OutputType: "oci-image", Prebuilt: &catalog.Prebuilt{
			Image: "example.test/worker:1.2.3", Digests: map[string]string{"arm64": image.ManifestDigest}, Evidence: &catalog.ExternalEvidence{None: true},
		},
	}}, Tools: map[string]catalog.ToolPin{"cosign": {Format: "raw", URL: "https://example.test/cosign", Platform: "linux/arm64", ExecutablePath: "cosign", ArchiveDigest: pin, ExecutableDigest: pin}}})
	if err != nil {
		t.Fatal(err)
	}
	doc := Definition{APIVersion: DefinitionAPIVersion, Kind: DefinitionKind, Metadata: Metadata{Name: "relocated-image"}, Catalog: Catalog{Base: "catalog"}, Outputs: []DefinitionOutput{{Name: "worker", Platform: "linux/arm64", Format: OutputOCI, Components: []string{"worker"}}}}
	env := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "test"}, UtilityContainer: "example.test/utility@" + pin, UtilityPlatform: "linux/arm64"}
	if hosted {
		doc.CosignTool = "cosign"
		env.Publication = &Publication{RepositoryPrefix: "example.test/outputs", Anonymous: true}
		env.Signing = &Signing{Mode: factory.SigningModeCosign, Identity: "^builder$", Issuer: "issuer"}
	}
	plan, err := factory.Compile(t.Context(), doc, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	run, err := PrepareRun(doc, env, plan, "relocated-image")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := make(map[string]factory.Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	input := artifacts["artifact/worker/worker"]
	if _, err := delivery.ImportAcquiredContainerImage(t.Context(), source, image.ManifestDigest, "linux/arm64", input.Path); err != nil {
		t.Fatal(err)
	}
	var size int64
	if err := filepath.WalkDir(input.Path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err == nil {
			size += info.Size()
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ref := "example.test/worker:1.2.3@" + image.ManifestDigest
	writeCanonicalTestFile(t, artifacts["result/worker/worker"].Path, pipeline.ArtifactResult{
		Component: "worker", OutputType: "oci-image", Qualification: pipeline.ArtifactProfileFreeAcquisition,
		EngineClass: ir.EnginePrebuiltAcquire, Ref: ref, Path: ref, SHA256: image.ManifestDigest, Size: size, Packaged: true,
	})
	if err := factory.ExecuteNode(t.Context(), plan, "package/worker", nil, nil); err != nil {
		t.Fatal(err)
	}
	slot := run.Expected[0]
	if hosted {
		member := artifacts[slot.MemberArtifactID]
		packaged, err := delivery.InspectContainerImage(t.Context(), member.Path, "linux/arm64")
		if err != nil {
			t.Fatal(err)
		}
		exact := Artifact{Name: slot.Name, Kind: slot.Kind, Digest: image.ManifestDigest, BuildDigest: slot.BuildDigest, Components: slot.Components}
		request := testReleaseRequest(t, []Artifact{exact}, []delivery.ObjectInput{{Name: slot.Name, Kind: factory.PackageImage, Path: member.Path, Layout: true, ArtifactType: delivery.MediaOCIManifest, SemanticDigest: image.ManifestDigest, BlobDigest: packaged.RootDigest, OS: "linux", Arch: "arm64"}}, map[string]string{"worker": image.ManifestDigest})
		if _, err := delivery.BuildPublicationRoot(t.Context(), artifacts[slot.ArtifactID].Path, request); err != nil {
			t.Fatal(err)
		}
		if plan.Qualification != factory.TierPipelineAttested {
			t.Fatal("hosted fixture does not exercise the production Plan lane")
		}
	}
	return plan, slot, artifacts, image.ManifestDigest
}

func TestCompileDerivesImageEnvelope(t *testing.T) {
	t.Chdir(filepath.Join("..", "..", ".."))
	definitionPath := filepath.Join("definitions", "nvke-v1", "definition.yaml")
	definition, err := LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	definition.Outputs = definition.Outputs[:1]
	environment, err := LoadEnvironment(t.Context(), filepath.Join(filepath.Dir(definitionPath), "environment-local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definitionPath, definition, "environment.yaml", environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	packages := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == factory.NodePackage {
			packages[node.ID] = node.PackageKind
		}
	}
	if len(packages) != 2 || packages["package/cwe"] != factory.PackageImage || packages["package/release"] != factory.PackageOCI {
		t.Fatalf("image packages: %#v", packages)
	}
	definition.Outputs[0].Components = []string{"first", "second"}
	assertCode(t, definition.Validate(), e.CodeInvalidCatalog)
}

func TestConsumerImageBinding(t *testing.T) {
	for _, tc := range []struct {
		name, arch string
		reject     bool
	}{
		{"exact platform", "amd64", false}, {"substituted platform", "arm64", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			binary := filepath.Join(root, "worker")
			if err := os.WriteFile(binary, []byte("fixture worker"), 0o755); err != nil {
				t.Fatal(err)
			}
			imagePath := filepath.Join(root, "image")
			if _, err := assemble.OCIImage(binary, "worker", 1, "amd64", imagePath); err != nil {
				t.Fatal(err)
			}
			image, err := delivery.InspectContainerImage(ctx, imagePath, "linux/amd64")
			if err != nil {
				t.Fatal(err)
			}
			artifact := Artifact{Name: "cwe", Kind: OutputOCI, Digest: image.ManifestDigest, BuildDigest: "sha256:" + repeat("a", 64), Components: []string{"cwe"}}
			content, err := delivery.ContentDigest([]Artifact{artifact})
			if err != nil {
				t.Fatal(err)
			}
			layout := filepath.Join(root, "release")
			request := testReleaseRequest(t, []Artifact{artifact}, []delivery.ObjectInput{{Name: "cwe", Kind: factory.PackageImage, Path: imagePath, Layout: true, ArtifactType: delivery.MediaOCIManifest, SemanticDigest: image.ManifestDigest, BlobDigest: image.RootDigest, OS: "linux", Arch: tc.arch}}, map[string]string{"cwe": image.RootDigest})
			_, err = delivery.BuildPublicationRoot(ctx, layout, request)
			if tc.reject {
				assertCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := delivery.Inspect(ctx, layout)
			if err != nil {
				t.Fatal(err)
			}
			artifacts, subject, err := inspectTestPublication(t, ctx, layout, inspection.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			if subject != content || len(artifacts) != 1 || artifacts[0].Digest != image.ManifestDigest || artifacts[0].Kind != OutputOCI {
				t.Fatalf("image projection: %#v %s", artifacts, subject)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

func layoutBundlePlan(t *testing.T) *Plan {
	t.Helper()
	definition, environment, stack := pinnedToolFactoryInputs(t, func(cat *catalog.Catalog, d *authored.Definition) {
		c := cat.Components["recipe"]
		c.OutputType, c.ArtifactPath = "oci-layout", ""
		c.Materials = map[string]string{"recipe.yaml": "recipe"}
		c.ToolArgs = []string{"bundle", "--recipe", `{{.Material "recipe.yaml"}}`, "--deployer", "flux", "--output", "oci-layout:{{.Output}}"}
		cat.Components["consumer"] = c
		d.Outputs[0].Format = authored.OutputBundle
	})
	environment.Publication = &authored.Publication{RepositoryPrefix: "localhost:5000/nvkr", Anonymous: true, PlainHTTP: true}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// These records model a successful producer; they do not exercise a real tool or
// establish execution acceptance. The real Linux run is recorded separately.
func layoutBundleWorkspace(t *testing.T) (*Plan, map[string]Artifact) {
	t.Helper()
	plan := layoutBundlePlan(t)
	_, artifacts := planIndex(plan)
	workspace := t.TempDir()
	for id, a := range artifacts {
		a.Path = filepath.Join(workspace, a.Path)
		artifacts[id] = a
	}
	for _, n := range plan.Builds[0].Graph.Nodes {
		if n.Kind != ir.KindBuild {
			continue
		}
		a := artifacts["artifact/rendered/"+n.Component]
		var raw []byte
		var validation *pipeline.AICRValidation
		if n.OutputType == "oci-layout" {
			if err := os.CopyFS(a.Path, os.DirFS("../delivery/testdata/aicr-v1.1.2-helm")); err != nil {
				t.Fatal(err)
			}
			var err error
			raw, err = os.ReadFile(filepath.Join(a.Path, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			descriptor, layer, err := delivery.ReadArtifact(t.Context(), a.Path)
			if err != nil {
				t.Fatal(err)
			}
			validation = &pipeline.AICRValidation{ToolDigest: n.Digest, IndexDigest: pipeline.FileDigest(raw), ManifestDigest: descriptor.Digest, LayerDigest: pipeline.FileDigest(layer)}
		} else {
			raw = []byte("service: generic\naccelerator: gb300\n")
			if err := writeFactoryFile(t.Context(), a.Path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		result := pipeline.ArtifactResult{Component: n.Component, OutputType: n.OutputType, Qualification: pipeline.ArtifactUnqualified,
			Toolchain: n.Toolchain, BuilderType: n.BuilderType, EngineClass: n.BuilderType, Path: n.ArtifactPath,
			SHA256: pipeline.FileDigest(raw), Size: int64(len(raw)), InputDigest: n.Digest, AICRValidation: validation}
		if n.OutputType == "oci-layout" {
			rawManifest, err := material.MarshalProjected([]material.Observation{{Binding: "recipe.yaml", Component: "recipe", Kind: "file", Architecture: "amd64", Digest: pipeline.FileDigest([]byte("service: generic\naccelerator: gb300\n"))}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			result.MaterialManifestDigest = pipeline.FileDigest(rawManifest)
		}
		encoded, err := canonical.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFactoryFile(t.Context(), artifacts["result/rendered/"+n.Component].Path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return plan, artifacts
}

func TestBundleLayoutPublicationPreservesManifestAndClosure(t *testing.T) {
	plan, artifacts := layoutBundleWorkspace(t)
	root, _, err := delivery.ReadArtifact(t.Context(), artifacts["artifact/rendered/consumer"].Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"rendered/sbom", "package/rendered", "package/release"} {
		node := planNode(plan, id)
		var err error
		if node.Kind == NodeSBOM {
			err = executeSBOM(t.Context(), plan, node, artifacts, io.Discard)
		} else {
			err = executePackage(t.Context(), plan, node, artifacts, io.Discard)
		}
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	observed, err := ObserveLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], artifacts)
	if err != nil || observed.Digest != root.Digest {
		t.Fatalf("observation %+v: %v", observed, err)
	}
	publication, err := delivery.Inspect(t.Context(), artifacts["package/release"].Path)
	if err != nil {
		t.Fatal(err)
	}
	slots := map[string]bool{}
	for _, object := range publication.Root.Objects {
		if object.Output != nil {
			if object.DescriptorDigest != root.Digest || object.Output.Bundle == nil || object.Output.Bundle.Component != "consumer" || len(object.Output.BuildSubjects) != 2 {
				t.Fatalf("lost layout/member binding: %+v", object)
			}
		}
		if object.Evidence != nil {
			slots[object.Evidence.Component+"/"+object.Evidence.SubjectKind] = true
		}
	}
	if !reflect.DeepEqual(slots, map[string]bool{"consumer/oci-layout": true, "recipe/file": true, "rendered/bundle": true}) {
		t.Fatalf("evidence slots: %v", slots)
	}
	metadata := filepath.Join(t.TempDir(), "metadata")
	if _, err := delivery.ExportMetadata(t.Context(), artifacts["package/release"].Path, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.InspectMetadata(t.Context(), metadata); err != nil {
		t.Fatalf("metadata-only admission: %v", err)
	}
}

func TestBundleLayoutRefusesChangedValidationSubjects(t *testing.T) {
	for _, field := range []string{"tool", "index", "manifest", "layer", "material", "missing"} {
		t.Run(field, func(t *testing.T) {
			plan, artifacts := layoutBundleWorkspace(t)
			record := artifacts["result/rendered/consumer"]
			result, err := readProducerResult(t.Context(), record.Path)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "tool":
				result.AICRValidation.ToolDigest = pipeline.FileDigest([]byte("other"))
			case "index":
				result.AICRValidation.IndexDigest = pipeline.FileDigest([]byte("other"))
			case "manifest":
				result.AICRValidation.ManifestDigest = pipeline.FileDigest([]byte("other"))
			case "layer":
				result.AICRValidation.LayerDigest = pipeline.FileDigest([]byte("other"))
			case "material":
				result.MaterialManifestDigest = pipeline.FileDigest([]byte("other"))
			case "missing":
				result.AICRValidation = nil
			}
			raw, err := canonical.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(record.Path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			err = executePackage(t.Context(), plan, planNode(plan, "package/rendered"), artifacts, io.Discard)
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			if _, err := os.Stat(artifacts["package/rendered"].Path); !os.IsNotExist(err) {
				t.Fatal("refusal left output")
			}
		})
	}
}

func TestBundleLayoutProvenanceRequiresToolSubjects(t *testing.T) {
	plan := layoutBundlePlan(t)
	if productionBuildType(plan) != provenance.BuildTypeWrappedBuild {
		t.Fatal("layout plan lost wrapped-build lane")
	}
	dependencies := resolvedDependencies(plan)
	if err := requirePinnedToolDependencies(plan, dependencies); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"archive", "executable"} {
		t.Run(kind, func(t *testing.T) {
			var changed []provenance.Dependency
			for _, dependency := range resolvedDependencies(plan) {
				if kind == "archive" && dependency.URI == pinnedToolFixturePin.URL || kind == "executable" && strings.HasPrefix(dependency.URI, "nvkr:tool/") {
					continue
				}
				changed = append(changed, dependency)
			}
			assertFactoryErrorCode(t, requirePinnedToolDependencies(plan, changed), e.CodeProvenanceMissing)
		})
	}
}

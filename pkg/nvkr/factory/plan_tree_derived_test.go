// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPlanTreeLegacyExecutableGraph(t *testing.T) {
	plan := wliAgentPlan(t)
	changed := 0
	for i := range plan.Graph.Artifacts {
		artifact := &plan.Graph.Artifacts[i]
		if artifact.Kind == "host-package" && artifact.MediaType == delivery.MediaOctetStream {
			artifact.MediaType = artifactMediaType("host-package", "")
			changed++
		}
	}
	if changed == 0 {
		t.Fatal("fixture has no legacy executable encoding")
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, storage := range []string{legacyPlanTreeStorage, storedGraphPlanTreeStorage} {
		t.Run(storage, func(t *testing.T) {
			planPath := githubLayoutFor(plan).planPath
			files, err := renderPlanTreeFormat(t.Context(), plan, planPath, storage)
			if err != nil {
				t.Fatal(err)
			}
			_, raw, err := joinPlanTree(t.Context(), planPath, files)
			if err != nil || !bytes.Equal(raw, mustCanonical(t, plan)) {
				t.Fatal("legacy reader changed the historical graph", err)
			}
		})
	}
	// V3 must refuse a historical encoding the current compiler cannot reproduce,
	// rather than silently rewriting its graph or publishing an unloadable tree.
	bundle, err := RenderGitHub(t.Context(), plan)
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	if bundle != nil || !strings.Contains(err.Error(), "graphDigest") {
		t.Fatal("v3 did not fail before emission at the graph identity check", err)
	}
}

func TestPlanTreeDerivedGraphAdmission(t *testing.T) {
	plan := githubRuntimePlanFixture(t)
	planPath := githubLayoutFor(plan).planPath
	for _, mutation := range []string{"missing digest", "invalid digest", "changed digest", "stored graph", "unknown field", "node bindings"} {
		t.Run(mutation, func(t *testing.T) {
			files, err := renderPlanTree(t.Context(), plan, planPath)
			if err != nil {
				t.Fatal(err)
			}
			var root planFields
			if err := json.Unmarshal(files[planPath], &root); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "missing digest":
				delete(root, "graphDigest")
			case "invalid digest":
				root["graphDigest"] = mustCanonical(t, "invalid")
			case "changed digest":
				root["graphDigest"] = mustCanonical(t, "sha256:"+strings.Repeat("0", 64))
			case "stored graph":
				root["graph"] = mustCanonical(t, plan.Graph)
			case "unknown field":
				root["ignored"] = mustCanonical(t, true)
			case "node bindings":
				var ref map[string]string
				if err := json.Unmarshal(root["targets"], &ref); err != nil {
					t.Fatal(err)
				}
				var targets planFields
				if err := json.Unmarshal(files[ref["file"]], &targets); err != nil {
					t.Fatal(err)
				}
				targets["nodes"] = json.RawMessage(`{}`)
				files[ref["file"]], err = treeJSON(targets)
				if err != nil {
					t.Fatal(err)
				}
			}
			files[planPath], err = treeJSON(root)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = joinPlanTree(t.Context(), planPath, files)
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
		})
	}
}

func TestPlanTreeGraphMigrationSweepsStoredGraphs(t *testing.T) {
	plan := githubRuntimePlanFixture(t)
	for _, storage := range []string{legacyPlanTreeStorage, storedGraphPlanTreeStorage} {
		t.Run(storage, func(t *testing.T) {
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			current := *bundle
			current.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				current.Files[name] = raw
				if name == bundle.PlanPath || isPlanStorePath(name) {
					delete(bundle.Files, name)
				}
			}
			old, err := renderPlanTreeFormat(t.Context(), plan, bundle.PlanPath, storage)
			if err != nil {
				t.Fatal(err)
			}
			var graphPath string
			for name, raw := range old {
				bundle.Files[name] = raw
				if strings.Contains(name, "/plan/graph-") {
					graphPath = name
				}
			}
			if graphPath == "" {
				t.Fatal("legacy fixture has no stored graph")
			}
			root := t.TempDir()
			for name, raw := range bundle.Files {
				writeGeneratedFixture(t, filepath.Join(root, name), raw)
			}
			_, manifest, err := ManifestFor(bundle)
			if err != nil {
				t.Fatal(err)
			}
			writeGeneratedFixture(t, filepath.Join(root, bundle.ManifestPath), manifest)
			loaded, err := LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath))
			if err != nil || !bytes.Equal(mustCanonical(t, loaded), mustCanonical(t, plan)) {
				t.Fatal("legacy load changed Plan", err)
			}
			writeGeneratedFixture(t, filepath.Join(root, graphPath), append(bytes.Clone(old[graphPath]), '\n'))
			_, err = LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath))
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			writeGeneratedFixture(t, filepath.Join(root, graphPath), old[graphPath])
			if err := WriteGeneratedBundle(t.Context(), root, &current); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, graphPath)); !os.IsNotExist(err) {
				t.Fatal("migration retained the unreferenced graph", err)
			}
			loaded, err = LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath))
			if err != nil || !bytes.Equal(mustCanonical(t, loaded), mustCanonical(t, plan)) {
				t.Fatal("migration changed Plan", err)
			}
			if err := CheckGeneratedBundle(t.Context(), root, &current); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlanTreeDerivedGraphRequiresManifestIdentity(t *testing.T) {
	before := githubRuntimePlanFixture(t)
	after := recompileGitHubStabilityPlan(t, before, func(in *githubStabilityInputs) {
		in.environment.RunsOn = "ubuntu-22.04"
	})
	if bytes.Equal(mustCanonical(t, before.Graph), mustCanonical(t, after.Graph)) {
		t.Fatal("fixture did not change derivation inputs")
	}
	bundle, err := RenderGitHub(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	// Even an internally consistent replacement tree with matching file hashes
	// and graphDigest cannot retain the prior manifest's executable identity.
	bundle.PlanDigest, err = before.Digest()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, raw := range bundle.Files {
		writeGeneratedFixture(t, filepath.Join(root, name), raw)
	}
	_, manifest, err := ManifestFor(bundle)
	if err != nil {
		t.Fatal(err)
	}
	writeGeneratedFixture(t, filepath.Join(root, bundle.ManifestPath), manifest)
	_, err = LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath))
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	if !strings.Contains(err.Error(), "manifest identity") {
		t.Fatal("replacement did not reach the Plan identity check", err)
	}
}

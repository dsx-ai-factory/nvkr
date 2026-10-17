// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

func TestNativeConformanceUsesOneRepositoryRun(t *testing.T) {
	repo, err := resolveExisting(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repo, ".nvkr", "runs", "fresh")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name string
		edit func(*Config)
		want bool
	}{
		{"same fresh Run", func(*Config) {}, true},
		{"different site", func(c *Config) { c.Runs.Site = filepath.Join(repo, ".nvkr/runs/old") }, false},
		{"different profiles", func(c *Config) { c.Runs.Profiles = filepath.Join(repo, ".nvkr/runs/old") }, false},
		{"outside repository", func(c *Config) { c.Runs.OS = filepath.Join(filepath.Dir(repo), "outside") }, false},
		{"missing repo", func(c *Config) { c.Repository = "" }, false},
		{"missing run", func(c *Config) { c.Runs.OS = "" }, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			cfg := Config{Conformance: true, Repository: repo, NVKR: filepath.Join(repo, "nvkr"), Runs: RunInputs{OS: root, Components: root, Providers: root, Site: root, Profiles: root}}
			row.edit(&cfg)
			selected, err := selectNativeConformance(cfg)
			if row.want {
				if err != nil || selected.run != ".nvkr/runs/fresh" || selected.repository != repo {
					t.Fatalf("selected=%+v err=%v", selected, err)
				}
			} else {
				assertConfigUsage(t, err)
			}
		})
	}
	if selected, err := selectNativeConformance(Config{}); err != nil || selected != nil {
		t.Fatalf("disabled conformance changed ordinary demo: %v", err)
	}
}

func TestNativeConformanceAliasSelectionIsExact(t *testing.T) {
	for _, row := range []struct {
		name, raw string
		want      bool
	}{
		{"same alias", `{"items":[{"spec":{"containers":[{"name":"ovnkube-controller","image":"registry.example.test/ovn:demo"}]}},{"spec":{"containers":[{"name":"ovnkube-controller","image":"registry.example.test/ovn:demo"}]}}]}`, true},
		{"one node", `{"items":[{"spec":{"containers":[{"name":"ovnkube-controller","image":"registry.example.test/ovn:demo"}]}}]}`, false},
		{"different aliases", `{"items":[{"spec":{"containers":[{"name":"ovnkube-controller","image":"registry.example.test/ovn:first"}]}},{"spec":{"containers":[{"name":"ovnkube-controller","image":"registry.example.test/ovn:other"}]}}]}`, false},
		{"missing controller", `{"items":[{"spec":{"containers":[]}},{"spec":{"containers":[]}}]}`, false},
		{"ambiguous JSON", `{"items":[],"items":[]}`, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			alias, err := conformanceOVNAlias([]byte(row.raw))
			if row.want && (err != nil || alias != "registry.example.test/ovn:demo") || !row.want && err == nil {
				t.Fatalf("alias=%q err=%v", alias, err)
			}
		})
	}
}

func TestNativeConformanceRequiresCompleteOriginalCaptureAndNaturalZero(t *testing.T) {
	for _, row := range []struct {
		name string
		edit func(*testresults.Output)
		want bool
	}{
		{"complete", func(*testresults.Output) {}, true},
		{"partial", func(o *testresults.Output) { o.CaptureStatus = testresults.CapturePartial }, false},
		{"missing report", func(o *testresults.Output) { o.Reports = nil }, false},
		{"empty report", func(o *testresults.Output) { o.Reports[0].ByteLength = 0 }, false},
		{"failed harness", func(o *testresults.Output) { o.Environment[0].Metadata.Value = "1" }, false},
		{"unknown exit", func(o *testresults.Output) { o.Environment = nil }, false},
		{"unbound", func(o *testresults.Output) { o.Binding = nil }, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			digest := "sha256:" + strings.Repeat("a", 64)
			o := testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete,
				Producer:    testresults.ProducerIdentity{Name: "conformance", Revision: testresults.Metadata{Status: "unknown"}},
				Suite:       testresults.SuiteSelection{Name: "kubernetes-conformance", Selection: []string{"[Conformance]"}, Revision: testresults.Metadata{Status: "declared", Value: "1.34.2"}},
				Reports:     []testresults.ReportReference{{Format: testresults.FormatJUnit, Profile: "ginkgo", Reference: "/private/original.xml", Digest: digest, ByteLength: 100}},
				Binding:     &testresults.Binding{Subjects: []testresults.SubjectBinding{{Output: "ovn-image-arm64", Platform: "linux/arm64", Subject: testresults.SubjectDescription{Ref: "registry.example.test/ovn@" + digest, Digest: digest}}}},
				Environment: []testresults.EnvironmentFact{{Scope: "harness", Name: "process.exitCode", Phase: "after", Metadata: testresults.Metadata{Status: "observed", Value: "0"}}}}
			row.edit(&o)
			raw, err := json.Marshal([]testresults.Output{o})
			if err != nil {
				t.Fatal(err)
			}
			err = checkConformanceCapture(raw)
			if row.want && err != nil || !row.want && err == nil {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestConformanceDefinitionSelectionIsExplicit(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn"} {
		ordinary, env, err := LocalArtifactInputs(network)
		if err != nil {
			t.Fatal(err)
		}
		selected, selectedEnv, err := LocalArtifactInputsFor(network, true)
		if err != nil || selected != strings.TrimSuffix(ordinary, ".json")+"-conformance.json" || env != selectedEnv {
			t.Fatalf("selected=%s env=%s err=%v", selected, selectedEnv, err)
		}
	}
	_, _, err := LocalArtifactInputsFor("kind-calico", true)
	assertConfigUsage(t, err)
}

func TestConformanceDefinitionsPreserveOrdinaryDeploymentOutputs(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn"} {
		ordinary, _, err := LocalArtifactInputs(network)
		if err != nil {
			t.Fatal(err)
		}
		selected, _, err := LocalArtifactInputsFor(network, true)
		if err != nil {
			t.Fatal(err)
		}
		a, err := definition.LoadDefinition(t.Context(), filepath.Join("../..", ordinary))
		if err != nil {
			t.Fatal(err)
		}
		b, err := definition.LoadDefinition(t.Context(), filepath.Join("../..", selected))
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Outputs) != 32 || len(b.Outputs) != 36 || !reflect.DeepEqual(a.Catalog, b.Catalog) {
			t.Fatal("combined Definition changed ordinary selection")
		}
		for _, output := range a.Outputs {
			matches := 0
			for _, added := range b.Outputs {
				if added.Name == output.Name {
					matches++
					if !reflect.DeepEqual(output, added) {
						t.Fatalf("%s changed: %s", network, output.Name)
					}
				}
			}
			if matches != 1 {
				t.Fatalf("ordinary output lost: %s", output.Name)
			}
		}
	}
}

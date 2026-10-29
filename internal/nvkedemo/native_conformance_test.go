// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

func TestNativeConformanceSelectorsFollowTheSelectedPlatform(t *testing.T) {
	for _, tc := range []struct{ platform, suffix, arch string }{
		{"linux/arm64", "arm64", "arm64"},
		{"linux/amd64", "amd64", "amd64"},
		{"darwin/arm64", "darwin-arm64", "arm64"},
		{"windows/amd64", "", ""},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			got, err := nativeConformanceSelectors(tc.platform)
			if tc.suffix == "" {
				assertNightlyCode(t, err, nvkrerr.CodeEngineCapability)
				return
			}
			if err != nil || got.Hydrophone.Output != "hydrophone-"+tc.suffix || got.Kubectl.Output != "kubectl-"+tc.suffix || got.Hydrophone.Platform != tc.platform || got.Kubectl.Platform != tc.platform || got.ConformanceImage.Output != "conformance-image-"+tc.arch || got.Busybox.Output != "busybox-image-"+tc.arch || got.ConformanceImage.Platform != "linux/"+tc.arch || got.Busybox.Platform != "linux/"+tc.arch {
				t.Fatalf("host and guest selectors differ: %+v %v", got, err)
			}
		})
	}
}

func TestNativeConformanceUsesOneRepositoryRun(t *testing.T) {
	repo, err := resolveExisting(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repo, ".nvkr", "runs", "fresh")
	selectors, err := nativeConformanceSelectors(runtime.GOOS + "/" + runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	capturedArtifactRunFixture(t, root, []lifecycle.DefinitionOutput{
		{Name: selectors.Hydrophone.Output, Platform: selectors.Hydrophone.Platform, Format: definition.OutputExecutable, Components: []string{"tool"}},
		{Name: selectors.Kubectl.Output, Platform: selectors.Kubectl.Platform, Format: definition.OutputExecutable, Components: []string{"tool"}},
		{Name: selectors.ConformanceImage.Output, Platform: selectors.ConformanceImage.Platform, Format: definition.OutputOCI, Components: []string{"image"}},
		{Name: selectors.Busybox.Output, Platform: selectors.Busybox.Platform, Format: definition.OutputOCI, Components: []string{"image"}},
	}, nil)
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
			selected, err := selectNativeConformance(t.Context(), cfg, runtime.GOOS+"/"+runtime.GOARCH)
			if row.want {
				if err != nil || selected.run != ".nvkr/runs/fresh" || selected.repository != repo {
					t.Fatalf("selected=%+v err=%v", selected, err)
				}
			} else {
				assertConfigUsage(t, err)
			}
		})
	}
	if selected, err := selectNativeConformance(t.Context(), Config{}, runtime.GOOS+"/"+runtime.GOARCH); err != nil || selected != nil {
		t.Fatalf("disabled conformance changed ordinary demo: %v", err)
	}
}

func TestNativeConformanceAdmitsOnlyCompleteMatchingRunBeforeDeployment(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, mode := range []string{"complete", "ordinary Run", "missing tool", "missing image", "foreign platform"} {
			t.Run(arch+"/"+mode, func(t *testing.T) {
				repo, err := resolveExisting(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				run := filepath.Join(repo, ".nvkr", "runs", "suite")
				platform := "linux/" + arch
				outputs := []lifecycle.DefinitionOutput{
					{Name: "hydrophone-" + arch, Platform: platform, Format: definition.OutputExecutable, Components: []string{"tool"}},
					{Name: "kubectl-" + arch, Platform: platform, Format: definition.OutputExecutable, Components: []string{"tool"}},
					{Name: "conformance-image-" + arch, Platform: platform, Format: definition.OutputOCI, Components: []string{"image"}},
					{Name: "busybox-image-" + arch, Platform: platform, Format: definition.OutputOCI, Components: []string{"image"}},
				}
				switch mode {
				case "ordinary Run":
					outputs = []lifecycle.DefinitionOutput{{Name: "workload", Platform: platform, Format: definition.OutputOCI, Components: []string{"image"}}}
				case "missing tool":
					outputs = outputs[1:]
				case "missing image":
					outputs = outputs[:3]
				case "foreign platform":
					outputs[0].Platform = "linux/" + map[string]string{"amd64": "arm64", "arm64": "amd64"}[arch]
				}
				capturedArtifactRunFixture(t, run, outputs, nil)
				cfg := Config{Conformance: true, Repository: repo, NVKR: filepath.Join(repo, "nvkr"), Runs: RunInputs{OS: run}, Out: filepath.Join(repo, "must-not-create")}
				selected, err := selectNativeConformance(t.Context(), cfg, platform)
				if mode == "complete" {
					if err != nil || selected == nil {
						t.Fatal("complete matching suite rejected", err)
					}
					return
				}
				assertNightlyCode(t, err, nvkrerr.CodeEngineCapability)
				if selected != nil {
					t.Fatal("incomplete suite admitted")
				}
				_, err = nativeDeploymentConfig(t.Context(), cfg, platform)
				assertNightlyCode(t, err, nvkrerr.CodeEngineCapability)
				if _, err := os.Stat(cfg.Out); !os.IsNotExist(err) {
					t.Fatal("incomplete suite created deployment state", err)
				}
			})
		}
	}
}

func TestNativeConformanceAliasSelectionIsExact(t *testing.T) {
	for _, mode := range []string{"exact", "missing pod", "extra pod", "duplicate pod node", "unrelated node", "duplicate expected node", "different alias", "missing controller", "duplicate controller", "ambiguous JSON"} {
		t.Run(mode, func(t *testing.T) {
			nodes := nativeTopologyNodes(3)
			pods := []map[string]any{}
			for _, node := range nodes {
				pods = append(pods, map[string]any{"spec": map[string]any{"nodeName": node.Metadata.Name, "containers": []map[string]string{{"name": "ovnkube-controller", "image": "registry.example.test/ovn:demo"}}}})
			}
			spec := func(i int) map[string]any { return pods[i]["spec"].(map[string]any) }
			switch mode {
			case "missing pod":
				pods = pods[:2]
			case "extra pod":
				pods = append(pods, pods[0])
			case "duplicate pod node":
				spec(2)["nodeName"] = nodes[1].Metadata.Name
			case "unrelated node":
				spec(2)["nodeName"] = "foreign"
			case "duplicate expected node":
				nodes[2].Metadata.Name = nodes[1].Metadata.Name
			case "different alias":
				spec(2)["containers"] = []map[string]string{{"name": "ovnkube-controller", "image": "registry.example.test/ovn:other"}}
			case "missing controller":
				spec(2)["containers"] = []map[string]string{}
			case "duplicate controller":
				spec(2)["containers"] = append(spec(2)["containers"].([]map[string]string), map[string]string{"name": "ovnkube-controller", "image": "registry.example.test/ovn:demo"})
			}
			raw, err := json.Marshal(map[string]any{"items": pods})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "ambiguous JSON" {
				raw = []byte("{\"items\":[],\"items\":[]}")
			}
			alias, err := conformanceOVNAlias(raw, nodes)
			if mode == "exact" && (err != nil || alias != "registry.example.test/ovn:demo") || mode != "exact" && err == nil {
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
	for _, host := range []struct{ os, environment string }{
		{"linux", "inputs/environments/local-arm64-kvm.json"},
		{"darwin", "inputs/environments/local-native-mac.json"},
	} {
		for _, network := range []string{"ovn", "upstream-ovn"} {
			t.Run(host.os+"/"+network, func(t *testing.T) {
				ordinary, linuxEnv, err := LocalArtifactInputs(network)
				if err != nil || linuxEnv != "inputs/environments/local-arm64-kvm.json" {
					t.Fatalf("Linux wrapper changed: %s %s %v", ordinary, linuxEnv, err)
				}
				selectedOrdinary, env, err := LocalArtifactInputsFor(network, false, host.os)
				if err != nil || selectedOrdinary != ordinary || env != host.environment {
					t.Fatalf("ordinary=%s env=%s err=%v", selectedOrdinary, env, err)
				}
				selected, selectedEnv, err := LocalArtifactInputsFor(network, true, host.os)
				if err != nil || selected != strings.TrimSuffix(ordinary, ".json")+"-conformance.json" || selectedEnv != host.environment {
					t.Fatalf("selected=%s env=%s err=%v", selected, selectedEnv, err)
				}
			})
		}
		_, _, err := LocalArtifactInputsFor("kind-calico", true, host.os)
		assertConfigUsage(t, err)
	}
	_, _, err := LocalArtifactInputsFor("ovn", false, "unsupported")
	assertNightlyCode(t, err, nvkrerr.CodeEngineCapability)
}

func TestConformanceDefinitionsPreserveOrdinaryDeploymentOutputs(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn"} {
		ordinary, _, err := LocalArtifactInputs(network)
		if err != nil {
			t.Fatal(err)
		}
		selected, _, err := LocalArtifactInputsFor(network, true, "linux")
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
		if len(a.Outputs) != 32 || len(b.Outputs) != 38 || !reflect.DeepEqual(a.Catalog, b.Catalog) {
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

func TestNativeConformanceChildUsesAttemptScratch(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native conformance requires a Unix host")
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"unset", "other directory", "not a directory"} {
		t.Run(mode, func(t *testing.T) {
			work := privateTestDir(t)
			repository, scratch := filepath.Join(work, "repository"), filepath.Join(work, "capture-scratch")
			for _, directory := range []string{repository, scratch} {
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			env := []string{"NVKR_TEST_CONFORMANCE_SCRATCH=1"}
			if mode != "unset" {
				ambient := filepath.Join(work, "ambient")
				if mode == "not a directory" {
					err = os.WriteFile(ambient, nil, 0600)
				} else {
					err = os.Mkdir(ambient, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
				env = append(env, "TMPDIR="+ambient)
			}
			runner := Runner{Work: work, Env: env, directory: work}
			original := append([]string(nil), runner.Env...)
			invocation := nativeConformanceInvocation(runner, repository, scratch)
			raw, err := invocation.Capture(t.Context(), []string{program, "-test.run=^TestNativeConformanceScratchHelper$"}, "capture-temp", 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Directory, WorkingDirectory string
				Mode                        uint32
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Directory != scratch || got.WorkingDirectory != repository || got.Mode != 0600 {
				t.Fatalf("child scratch observation: %+v", got)
			}
			if !reflect.DeepEqual(runner.Env, original) || runner.directory != work {
				t.Fatal("conformance invocation mutated its caller")
			}
		})
	}
}

func TestNativeConformanceScratchHelper(t *testing.T) {
	if os.Getenv("NVKR_TEST_CONFORMANCE_SCRATCH") != "1" {
		return
	}
	file, err := os.CreateTemp("", "nvkr-capture-")
	if err != nil {
		os.Exit(2)
	}
	info, statErr := file.Stat()
	cwd, cwdErr := os.Getwd()
	if statErr != nil || cwdErr != nil || file.Close() != nil || os.Remove(file.Name()) != nil {
		os.Exit(3)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Directory, WorkingDirectory string
		Mode                        uint32
	}{filepath.Dir(file.Name()), cwd, uint32(info.Mode().Perm())}); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

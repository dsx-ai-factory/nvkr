// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// These contracts compile the maintained inputs through the public loaders and
// compiler. They establish selection integrity, not live
// rendering, publication, deployment compatibility or hardware acceptance.
func TestMaintainedAICRBundleInputs(t *testing.T) {
	aicrInputTestRoot(t)
	for _, test := range []struct {
		file      string
		recipe    []string // positional recipe coordinate preceding the renderer
		renderers []string
		contract  []string // selection-specific rendering contract
	}{
		{
			file:      "definition.yaml",
			recipe:    []string{"generic", "gb300"},
			renderers: []string{"argocd", "argocd-helm", "flux", "helm", "helmfile", "terraform"},
			contract:  []string{"--os ubuntu --intent training", `--deployer "$3"`},
		},
		{
			file:      "nvke-gb200.yaml",
			recipe:    []string{"nvke", "gb200", "kubeflow"},
			renderers: []string{"argocd-helm", "flux", "helm"},
			contract: []string{
				`--service "$1" --accelerator "$2" --platform "$3"`, "--os ubuntu --intent training",
				`leaf="$2-$1-ubuntu-training-$3"`, `[ "$applied" = "$leaf" ] ||`, `--deployer "$4"`,
			},
		},
	} {
		t.Run(test.file, func(t *testing.T) {
			plan := compileAICRInput(t, test.file, "aicr-local-arm64.yaml")
			count := len(test.renderers)
			if len(plan.Definition.Outputs) != count || len(plan.Builds) != count || len(plan.LifecycleOutputs) != count {
				t.Fatalf("AICR selection must retain all %d independent renderer outputs", count)
			}
			for _, output := range plan.Definition.Outputs {
				if output.Format != definition.OutputBundle || output.Platform != "linux/arm64" ||
					!slices.Equal(output.Components, []string{output.Name}) || output.Helm != nil {
					t.Fatalf("renderer lost its exact generic archive output: %+v", output)
				}
			}
			renderers, digests := make(map[string]bool), make(map[string]bool)
			for _, build := range plan.Builds {
				var producer, fetch ir.Node
				for _, node := range build.Graph.Nodes {
					switch node.Kind {
					case ir.KindBuild:
						if producer.ID != "" {
							t.Fatal("bundle gained a second source producer")
						}
						producer = node
					case ir.KindFetch:
						fetch = node
					case ir.KindPull:
						t.Fatal("maintained AICR producer became an acquired artifact")
					}
				}
				assertAICRSourceProducer(t, producer, fetch, test.contract)
				command := producer.Command
				coordinate := len(command) - 1 - len(test.recipe)
				if coordinate < 0 || !slices.Equal(command[coordinate:len(command)-1], test.recipe) {
					t.Fatalf("recipe coordinate changed: %q", command)
				}
				renderer := command[len(command)-1]
				if renderers[renderer] || digests[build.BuildDigest] || !strings.HasSuffix(build.Name, "-"+renderer) {
					t.Fatalf("renderer lost its independent named build: %s/%s", build.Name, renderer)
				}
				renderers[renderer], digests[build.BuildDigest] = true, true
			}
			for _, renderer := range test.renderers {
				if !renderers[renderer] {
					t.Fatalf("missing maintained renderer %q", renderer)
				}
			}
			if plan.Environment.Publication != nil || plan.Environment.Signing != nil {
				t.Fatal("example must remain build-only")
			}
		})
	}
}

// The NVKE producers admit a recipe only when its most specific applied overlay is
// the coordinate's own leaf. This runs each compiled producer's exact admission
// lines, with its own positional arguments, against recipe shapes.
func TestAICRNVKELeafAdmission(t *testing.T) {
	aicrInputTestRoot(t)
	plan := compileAICRInput(t, "nvke-gb200.yaml", "aicr-local-arm64.yaml")
	const leaf = "gb200-nvke-ubuntu-training-kubeflow"
	const overlays = "  appliedOverlays:\n    - base\n    - monitoring-hpa\n    - nvke\n    - gb200-any\n" +
		"    - gb200-nvke-ubuntu-training\n"
	for _, test := range []struct {
		name, recipe string
		admit        bool
	}{
		{"curated leaf last", "componentRefs: []\nmetadata:\n" + overlays + "    - " + leaf + "\n  version: v1.2.0\n", true},
		// Verbatim top-level neighbors and metadata of the recipe AICR v1.2.0 wrote in local
		// attempt aicr-nvke-gb200-local-20261103-r7 (sha256:80c875d6…).
		{"AICR v1.2.0 writer output", "kind: RecipeResult\nmetadata:\n  appliedOverlays:\n    - base\n    - monitoring-hpa\n" +
			"    - gb200-any\n    - nvke\n    - gb200-nvke-ubuntu-training\n    - " + leaf + "\n  version: 1.2.0\nvalidation:\n", true},
		{"wildcard merge without the leaf", "metadata:\n" + overlays + "  excludedOverlays:\n    - name: " + leaf + "\n", false},
		{"leaf not last", "metadata:\n" + overlays + "    - " + leaf + "\n    - later\n", false},
		{"unreadable item after the leaf", "metadata:\n" + overlays + "    - " + leaf + "\n    - \"later overlay\"\n", false},
		{"commented item after the leaf", "metadata:\n" + overlays + "    - " + leaf + "\n    - later # note\n", false},
		{"blank line inside the list", "metadata:\n" + overlays + "    - " + leaf + "\n\n    - later\n", false},
		{"comment line inside the list", "metadata:\n" + overlays + "    - " + leaf + "\n    # note\n", false},
		{"flow sequence", "metadata:\n  appliedOverlays: [base, " + leaf + "]\n", false},
		{"duplicate lists", "metadata:\n  appliedOverlays:\n    - " + leaf + "\nother:\n  appliedOverlays:\n    - " + leaf + "\n", false},
		{"absent list", "metadata:\n  version: v1.2.0\n", false},
		{"quoted leaf", "metadata:\n  appliedOverlays:\n    - \"" + leaf + "\"\n", false},
		{"CRLF line endings", "metadata:\r\n  appliedOverlays:\r\n    - " + leaf + "\r\n", false},
		{"metadata child at another indent", "metadata:\n    appliedOverlays:\n      - " + leaf + "\n", true},
		{"comments before the metadata child", "metadata:\n# note\n    # note\n  appliedOverlays:\n    - " + leaf + "\n", true},
		{"lone list outside metadata", "spec:\n  appliedOverlays:\n    - " + leaf + "\n", false},
		{"lone top-level list", "appliedOverlays:\n  - " + leaf + "\n", false},
		{"lone list nested below metadata", "metadata:\n  labels:\n    appliedOverlays:\n      - " + leaf + "\n", false},
		{"lone list after metadata ends", "metadata:\n  version: v1.2.0\nspec:\n  appliedOverlays:\n    - " + leaf + "\n", false},
	} {
		for _, build := range plan.Builds {
			t.Run(test.name+"/"+build.Name, func(t *testing.T) {
				var command []string
				for _, node := range build.Graph.Nodes {
					if node.Kind == ir.KindBuild {
						command = node.Command
					}
				}
				if len(command) < 6 || !slices.Equal(command[:4], []string{"bash", "-euo", "pipefail", "-c"}) {
					t.Fatalf("producer is not one bash script with positional arguments: %q", command)
				}
				lines, ok := leafAdmissionLines(command[4])
				if !ok {
					t.Fatalf("leaf admission must be whole lines preceding bundle rendering:\n%s", command[4])
				}
				out := t.TempDir()
				if err := os.WriteFile(filepath.Join(out, "recipe.yaml"), []byte(test.recipe), 0o600); err != nil {
					t.Fatal(err)
				}
				args := append([]string{"-euo", "pipefail", "-c", "out=\"$NVKR_TEST_OUT\"\n" + lines}, command[5:]...)
				admission := exec.CommandContext(t.Context(), "bash", args...)
				admission.Env = append(os.Environ(), "NVKR_TEST_OUT="+out)
				output, err := admission.CombinedOutput()
				if test.admit != (err == nil) {
					t.Fatalf("admission = %v, want %v: %v\n%s", err == nil, test.admit, err, output)
				}
			})
		}
	}
}

// The NVKE producers pass the DRA eviction label to every deployer and readiness hooks
// to every deployer except flux, which rejects them. This runs each compiled producer's
// bundle call, with its own positional arguments, against a stub aicr that records argv.
func TestAICRNVKEBundleFlags(t *testing.T) {
	aicrInputTestRoot(t)
	plan := compileAICRInput(t, "nvke-gb200.yaml", "aicr-local-arm64.yaml")
	for _, build := range plan.Builds {
		t.Run(build.Name, func(t *testing.T) {
			var command []string
			for _, node := range build.Graph.Nodes {
				if node.Kind == ir.KindBuild {
					command = node.Command
				}
			}
			if len(command) < 6 || !slices.Equal(command[:4], []string{"bash", "-euo", "pipefail", "-c"}) {
				t.Fatalf("producer is not one bash script with positional arguments: %q", command)
			}
			script := command[4]
			start, end := strings.Index(script, "hooks=()"), strings.Index(script, "# Publish AICR's own layer bytes")
			if start < 0 || end < start {
				t.Fatalf("bundle invocation not found:\n%s", script)
			}
			out := t.TempDir()
			stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$(dirname \"$0\")/argv\"\n"
			if err := os.WriteFile(filepath.Join(out, "aicr"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			// The stub replaces timeout's two duration arguments, leaving the bundle argv.
			bundle := "out=\"$NVKR_TEST_OUT\"\ntimeout() { shift 2; \"$@\"; }\n" + script[start:end]
			run := exec.CommandContext(t.Context(), "bash", append([]string{"-euo", "pipefail", "-c", bundle}, command[5:]...)...)
			run.Env = append(os.Environ(), "NVKR_TEST_OUT="+out)
			if output, err := run.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, output)
			}
			raw, err := os.ReadFile(filepath.Join(out, "argv"))
			if err != nil {
				t.Fatal(err)
			}
			// The output name, not the producer's positional arguments, names the expected deployer.
			deployer, ok := strings.CutPrefix(build.Name, "aicr-nvke-gb200-ubuntu-training-kubeflow-")
			if !ok {
				t.Fatalf("unexpected GB200 output %q", build.Name)
			}
			argv := "\n" + string(raw)
			for _, pair := range []string{"--recipe\n" + out + "/recipe.yaml", "--deployer\n" + deployer, "--output\noci-layout:" + out + "/layout"} {
				if !strings.Contains(argv, "\n"+pair+"\n") {
					t.Fatalf("%s bundle omits %q: %q", deployer, pair, argv)
				}
			}
			if !strings.Contains(argv, "\n--dra-eviction-node-label\nnvidia.com/dra-kubelet-plugin=true\n") {
				t.Fatalf("%s bundle omits the DRA eviction label: %q", deployer, argv)
			}
			if hooks := strings.Contains(argv, "\n--readiness-hooks\n"); hooks != (deployer != "flux") {
				t.Fatalf("%s readiness hooks = %v: %q", deployer, hooks, argv)
			}
		})
	}
}

// leafAdmissionLines returns the producer's admission lines, from the leaf assignment
// through the end of the check line. The check line must end in a newline: without
// one the cut would drop the check and every rejection case would fail misleadingly.
func leafAdmissionLines(script string) (string, bool) {
	start := strings.Index(script, `leaf="`)
	check := strings.Index(script, `[ "$applied" = "$leaf" ] ||`)
	bundle := strings.Index(script, `"$out/aicr" bundle`)
	if start < 0 || check < start || bundle < check {
		return "", false
	}
	end := strings.IndexByte(script[check:], '\n')
	if end < 0 {
		return "", false
	}
	return script[start : check+end], true
}

func TestLeafAdmissionLines(t *testing.T) {
	const assign, check, bundle = `leaf="x"` + "\n", `[ "$applied" = "$leaf" ] || exit 1`, `"$out/aicr" bundle`
	for _, test := range []struct {
		name, script, want string
		ok                 bool
	}{
		{"whole lines", assign + check + "\n" + bundle + "\n", assign + check, true},
		{"check without newline", assign + check + "; " + bundle, "", false},
		{"check after bundle", bundle + "\n" + assign + check + "\n", "", false},
		{"no assignment", check + "\n" + bundle + "\n", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := leafAdmissionLines(test.script)
			if got != test.want || ok != test.ok {
				t.Fatalf("leafAdmissionLines() = %q, %v; want %q, %v", got, ok, test.want, test.ok)
			}
		})
	}
}

func assertAICRSourceProducer(t *testing.T, producer, fetch ir.Node, contract []string) {
	t.Helper()
	const commit = "935e0746dc54da890a23c1ecca998244b3a25de7"
	const tree = "1d4bfa6d824159dbb4c8077303fe8de154946405"
	const image = "golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c"
	if fetch.Repo != "https://github.com/NVIDIA/aicr" || fetch.Commit != commit || fetch.TreeHash != tree ||
		fetch.VendorImage != image || fetch.VendorSpecDigest == "" || fetch.Network != ir.NetworkDefault {
		t.Fatalf("AICR fetch lost its pinned source/vendor identity: %+v", fetch)
	}
	if producer.SourceCommit != commit || producer.SourceTreeHash != tree || producer.Toolchain != image ||
		producer.OutputType != "tarball" || producer.Network != ir.NetworkNone || !producer.SourceReadOnly ||
		producer.WriteDir == "" || !strings.HasPrefix(producer.ArtifactPath, producer.WriteDir+"/") {
		t.Fatalf("AICR build lost its offline archive/private-output contract: %+v", producer)
	}
	environment := make(map[string]string)
	for _, value := range producer.Env {
		environment[value.Name] = value.Value
	}
	if environment["GIT_VERSION"] != commit || environment["BUILD_VERSION"] != "1.2.0" || environment["SOURCE_DATE_EPOCH"] != "1793648938" {
		t.Fatalf("AICR build stamps do not bind the frozen source: %+v", environment)
	}
	for _, required := range []string{"go mod download", "go mod verify", "go mod vendor", "sha256sum -c -"} {
		if !strings.Contains(strings.Join(fetch.VendorCmd, "\n"), required) {
			t.Fatalf("AICR dependency fetch omits %q", required)
		}
	}
	for _, required := range append([]string{"-mod=vendor", "GOPROXY=off", `--output "oci-layout:$out/layout"`, `"$out/aicr" verify`, `cp "$layer" "$out/bundle.tar.gz"`}, contract...) {
		if !strings.Contains(strings.Join(producer.Command, "\n"), required) {
			t.Fatalf("AICR build omits its offline rendering/layer contract %q", required)
		}
	}
}

func aicrInputTestRoot(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
}

func compileAICRInput(t *testing.T, file, environment string) *Plan {
	t.Helper()
	definitionPath := filepath.Join("inputs/definitions/aicr", file)
	environmentPath := filepath.Join("inputs/environments", environment)
	authored, err := definition.LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := definition.LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := CompileDocuments(t.Context(), definitionPath, authored, environmentPath, selected, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

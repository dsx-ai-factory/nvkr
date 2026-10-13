// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
	"sigs.k8s.io/yaml"
)

func TestMaintainedM2EnvironmentUsesSupportedRuntimeControls(t *testing.T) {
	environment, err := definition.LoadEnvironment(t.Context(), "../../../definitions/nvke-m2/environment-github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if environment.Runtime.Runtime != "docker" || environment.Runtime.BuildNetworkPolicy != "producer-declared" {
		t.Fatalf("candidate runtime controls changed: %+v", environment.Runtime)
	}
	if len(environment.Runtime.Variables) != 0 || len(environment.Runtime.Setup) != 0 {
		t.Fatal("candidate supplies an ambient storage/isolation workaround")
	}
	if environment.GitHubARC != "native-kvm-v1" || len(environment.ExecutionClasses) != 2 {
		t.Fatal("explicit native ARC routes must be selected")
	}
	if len(environment.DiskInspectionProfiles) != 2 {
		t.Fatal("both measured native inspector profiles must be selected")
	}
}

// These isolated test catalogs exercise the unchanged tag-backed declarations
// and native pins through their owning loader. They are not alternative product
// inputs: TestMaintainedM2DeploymentImageMembership still requires the entire
// actual catalog to retain all 37 outputs and compile all 24 image subjects.
func TestMaintainedM2TagImageResolveCompile(t *testing.T) {
	provider := catalog.Dir("../../../definitions/nvke-m2/image-catalog").(catalog.ContextDataProvider)
	registryBytes, err := provider.ReadFileContext(t.Context(), catalog.RegistryFile)
	if err != nil {
		t.Fatal(err)
	}
	var declarations struct {
		APIVersion string                     `json:"apiVersion"`
		Kind       string                     `json:"kind"`
		Metadata   json.RawMessage            `json:"metadata"`
		Sources    map[string]json.RawMessage `json:"sources"`
		Components map[string]json.RawMessage `json:"components"`
	}
	if err := yaml.UnmarshalStrict(registryBytes, &declarations); err != nil {
		t.Fatal(err)
	}
	lockBytes, err := provider.ReadFileContext(t.Context(), catalog.LockFile)
	if err != nil {
		t.Fatal(err)
	}
	var lock catalog.PinLock
	if err := yaml.UnmarshalStrict(lockBytes, &lock); err != nil {
		t.Fatal(err)
	}
	writeFixture := func(directory, name string, value any) {
		t.Helper()
		// Serialization is only fixture transport, never structured identity.
		raw, err := yaml.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	components := []string{
		"csi-node-driver-registrar-image", "spiffe-csi-image", "spire-agent-image",
		"spire-controller-manager-image", "spire-oidc-image", "spire-server-image",
		"spire-tpm-server-image", "temporal-admin-tools-image", "temporal-server-image", "temporal-ui-image",
	}
	rows := 0
	for _, component := range components {
		t.Run(component, func(t *testing.T) {
			raw, exists := declarations.Components[component]
			if !exists {
				t.Fatalf("maintained tag declaration %q is absent", component)
			}
			var selected struct {
				Source   string
				Prebuilt struct{ Image string }
			}
			if err := json.Unmarshal(raw, &selected); err != nil {
				t.Fatal(err)
			}
			pin, exists := lock.Artifacts[component]
			if !exists || len(pin.Digests) != 2 || pin.ImageParentDigest == "" {
				t.Fatalf("%s must retain both native artifact pins", component)
			}
			fixtureRegistry := declarations
			fixtureRegistry.Components = map[string]json.RawMessage{component: raw}
			fixtureLock := lock
			fixtureLock.Artifacts = map[string]catalog.ArtifactPin{component: pin}
			directory := t.TempDir()
			writeFixture(directory, catalog.RegistryFile, fixtureRegistry)
			writeFixture(directory, catalog.LockFile, fixtureLock)
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(directory))
			if err != nil {
				t.Fatal(err)
			}
			sourcePin, err := cat.SourcePin(selected.Source)
			if err != nil || !reflect.DeepEqual(sourcePin, lock.Pins[selected.Source]) {
				t.Fatalf("source pin changed in fixture: pin=%+v error=%v", sourcePin, err)
			}
			if cat.Components[component].Prebuilt.Image != selected.Prebuilt.Image ||
				!reflect.DeepEqual(cat.Components[component].Prebuilt.Digests, pin.Digests) {
				t.Fatal("loaded fixture changed the authored transport or native pins")
			}
			buildDigests := make(map[string]string)
			for _, arch := range []string{"amd64", "arm64"} {
				t.Run(arch, func(t *testing.T) {
					platform := "linux/" + arch
					resolved, err := Resolve(cat, Selection{
						Components: []string{component}, Platform: platform, OutputFormat: definition.OutputOCI,
					})
					if err != nil {
						t.Fatal(err)
					}
					graph, err := Compile(resolved)
					if err != nil {
						t.Fatal(err)
					}
					if err := graph.Validate(); err != nil {
						t.Fatal(err)
					}
					if graph.Platform != platform || len(graph.Nodes) != 2 {
						t.Fatalf("want native pull/verify closure, got %+v", graph)
					}
					pull, verify := graph.Nodes[0], graph.Nodes[1]
					if pull.Kind != ir.KindPull || pull.Component != component || pull.Artifact != selected.Prebuilt.Image ||
						pull.Platform != platform || pull.Digest != pin.Digests[arch] || pull.ImageParentDigest != pin.ImageParentDigest || pull.OutputType != "oci-image" ||
						pull.Network != ir.NetworkDefault || len(pull.Needs) != 0 {
						t.Fatalf("wrong native pull identity/transport: %+v", pull)
					}
					if verify.Kind != ir.KindVerify || !slices.Equal(verify.Needs, []string{pull.ID}) {
						t.Fatalf("verify does not cover the exact native pull: %+v", verify)
					}
					buildDigests[arch], err = graph.Digest()
					if err != nil {
						t.Fatal(err)
					}
					rows++
				})
			}
			if buildDigests["amd64"] == buildDigests["arm64"] {
				t.Fatal("native platform and subject changes must change BuildDigest")
			}
		})
	}
	if rows != 20 {
		t.Fatalf("got %d compiled tag-backed native rows, want 20", rows)
	}
}

func TestMaintainedM2DeploymentImageMembership(t *testing.T) {
	const root = "../../../definitions/nvke-m2"
	const commit = "33fb71b2f4224b470ebc628a9165fc4b92870406"
	expected := map[string]string{
		"csi-node-driver-registrar-image": "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.15.0",
		"postgresql-image":                "registry-1.docker.io/bitnami/postgresql@sha256:b74b23f091dcc87041bcae557d840c36458af46b955279cb13aedec4c32d7f71",
		"spiffe-csi-image":                "ghcr.io/spiffe/spiffe-csi-driver:0.2.7",
		"spire-agent-image":               "ghcr.io/spiffe/spire-agent:1.15.3",
		"spire-bash-image":                "cgr.dev/chainguard/bash@sha256:90041f375e30f41aa7e0390075d8a69dc61900771d52fa98d63ee5d03d866a58",
		"spire-controller-manager-image":  "ghcr.io/spiffe/spire-controller-manager:0.7.0",
		"spire-oidc-image":                "ghcr.io/spiffe/oidc-discovery-provider:1.15.3",
		"spire-server-image":              "ghcr.io/spiffe/spire-server:1.15.3",
		"spire-tpm-server-image":          "ghcr.io/spiffe/spire-tpm-plugin-tpm-attestor-server:v1.9.0",
		"temporal-admin-tools-image":      "temporalio/admin-tools:1.30.3",
		"temporal-server-image":           "temporalio/server:1.30.3",
		"temporal-ui-image":               "temporalio/ui:2.48.1",
	}
	// Literal maintained native bindings plus public static Resolve/Compile checks.
	// These do not prove live registry acquisition or production admission.
	native := map[string][2]string{
		"csi-node-driver-registrar-image": {"sha256:1ca23bdbb1504e41233d527e826d02dac98571c45a6cd74c5fb28be485d680fa", "sha256:0898bf569379e23a39b9dbd640934c4b9e2563b2c023cbad3e8f686167fd8b99"},
		"postgresql-image":                {"sha256:9ca5fe8db49a439acdfda55fcbd6ff1c44bc84facf5e775b7e170434d61ce18c", "sha256:b3dec5235745879ca095c08c230e5a53825f7cd795bcee6e6ac88eed91267d28"},
		"spiffe-csi-image":                {"sha256:b98766c90387033702ef64a3a4dce1aae252cb741b3ac2ef13f271dd7660bb1e", "sha256:6bfd786d4ffd3f7435659a7b923d819a7a0bcc55cd05673908915d433e86c0a2"},
		"spire-agent-image":               {"sha256:0d9c792d7f409b748d3a1fc93fe70b6d1450474c177d2a731ddc36e97a17e9fe", "sha256:0c94e29f29a13c885294ae73755033040438117ca6c1e257318c5df0868290ac"},
		"spire-bash-image":                {"sha256:d49c2b6585ac8e6341adfe8cb214a656914ab5390fb7804a7ed9e90a5b785fba", "sha256:8d29611142077489b9863426dfccf935c5ee9a27a3548d726c16de32692e71e3"},
		"spire-controller-manager-image":  {"sha256:fb3bfc8f98f24c769004543d6b760142380c8f2d3a0354391ee2bfdd48039e59", "sha256:99dae65b7e207a7d34f9bef2aff901d6cbba40bd5ed31093ebf51e6a063783bc"},
		"spire-oidc-image":                {"sha256:32f5e7d7b6329ed9dff97ad374e11d9156c43611ecfcb0693b7ca085b5328cdf", "sha256:6adc68d480f285e586edb67f4053bfb8923816a02486bf22d117e29bdee54a78"},
		"spire-server-image":              {"sha256:3aa2dce70fc1098d6718a87c629aa8cadb83e4672c71dd63c5575ea5d0603789", "sha256:b2fb59ac8ec292b2d0e3685008b4a72726fc2298923a569ada9b2700f8b86380"},
		"spire-tpm-server-image":          {"sha256:0ba43be0bd5cee7afc54fb351989692ddd07c106111ecda3cf6e9cbe23da137c", "sha256:d5a0bd84d06ce4a7cd8d0cc5f7f05ca041ed3e1cdd0f8fd129831d871669f2bf"},
		"temporal-admin-tools-image":      {"sha256:f24c619953ba5724f36446cca514ba7dd865c2367873cbad70977caf58a02448", "sha256:2f90b7c95151bb7a3ddf41749681918f70528fd6c90b160a89e05970b678144a"},
		"temporal-server-image":           {"sha256:1a71947754cd244b3e6f1beb61e2eb2408e9180f7f092fd30182849625db0ac7", "sha256:77c887f311db18f086ce2b8ac3144e7f0ad59d73765d01c3e412dea9b8fb6e2c"},
		"temporal-ui-image":               {"sha256:6734b3988aa3615286ac8edc83f4d40f10fba587b7109570702efe09478807f9", "sha256:0e7478fad0eebac6d135a9c1efcc21f50f5026e017064b32f034585b13674788"},
	}
	authored, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(authored.Outputs) != 37 {
		t.Fatalf("got %d outputs, want thirteen build/chart/tool outputs plus 24 native image members", len(authored.Outputs))
	}
	provider := catalog.Dir(filepath.Join(root, "image-catalog")).(catalog.ContextDataProvider)
	cat, err := catalog.LoadContext(t.Context(), provider)
	if err != nil {
		t.Fatal(err)
	}
	// Public catalog admission preserves the exact authored parent. Runtime
	// acquisition still authenticates its relationship before output work.
	raw, err := provider.ReadFileContext(t.Context(), catalog.RegistryFile)
	if err != nil {
		t.Fatal(err)
	}
	var declarations struct {
		Components map[string]struct {
			Source   string
			Prebuilt struct{ Image string }
		}
	}
	if err := yaml.Unmarshal(raw, &declarations); err != nil {
		t.Fatal(err)
	}
	if len(declarations.Components) != len(expected) {
		t.Fatalf("got %d image declarations, want %d", len(declarations.Components), len(expected))
	}
	raw, err = provider.ReadFileContext(t.Context(), catalog.LockFile)
	if err != nil {
		t.Fatal(err)
	}
	var lock catalog.PinLock
	if err := yaml.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Pins["nvke-deployment-selection"].Commit != commit {
		t.Fatal("deployment selection source pin changed")
	}
	seen := map[string]map[string]bool{}
	selections := 0
	for _, output := range authored.Outputs {
		if output.Catalog == nil || output.Catalog.Base != "./image-catalog" {
			continue
		}
		if output.Format != definition.OutputOCI || len(output.Components) != 1 {
			t.Fatalf("bad image output %+v", output)
		}
		component := output.Components[0]
		want, ok := expected[component]
		if !ok {
			t.Fatalf("undeclared deployment image %s", component)
		}
		selected := declarations.Components[component]
		if selected.Source != "nvke-deployment-selection" || selected.Prebuilt.Image != want {
			t.Fatalf("wrong source/selection for %s", component)
		}
		if seen[component] == nil {
			seen[component] = map[string]bool{}
		}
		if seen[component][output.Platform] {
			t.Fatalf("duplicate native image member %s/%s", component, output.Platform)
		}
		seen[component][output.Platform] = true
		arch := target.Arch(output.Platform)
		index := 0
		if arch == "arm64" {
			index = 1
		} else if arch != "amd64" {
			t.Fatalf("unexpected native platform %q", output.Platform)
		}
		if got := lock.Artifacts[component].Digests[arch]; got != native[component][index] {
			t.Fatalf("native lock binding for %s/%s=%s, want %s", component, arch, got, native[component][index])
		}
		resolved, err := Resolve(cat, Selection{Components: output.Components, Platform: output.Platform, OutputFormat: output.Format})
		if err != nil {
			t.Fatal(err)
		}
		graph, err := Compile(resolved)
		if err != nil {
			t.Fatal(err)
		}
		pull := graph.Nodes[0]
		if pull.Digest != native[component][index] || pull.Platform != output.Platform || pull.Artifact != want ||
			pull.ImageParentDigest == "" || pull.ImageParentDigest != lock.Artifacts[component].ImageParentDigest {
			t.Fatalf("changed maintained image binding: %+v", pull)
		}
		if at := strings.LastIndex(want, "@"); at >= 0 && pull.ImageParentDigest != want[at+1:] {
			t.Fatal("authored parent did not enter frozen IR")
		}
		selections++
	}
	if selections != 24 {
		t.Fatalf("got %d literal image selections; all 24 must remain selected and statically compiled", selections)
	}
	for component := range expected {
		if !seen[component]["linux/amd64"] || !seen[component]["linux/arm64"] {
			t.Fatalf("missing native image member for %s", component)
		}
	}
}

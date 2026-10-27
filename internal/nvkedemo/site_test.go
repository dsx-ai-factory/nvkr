// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestBootstrapCARetainedLifetime(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("real bootstrap CA verification requires OpenSSL")
	}
	work := privateTestDir(t)
	if err := bootstrapCA(t.Context(), ManagementSession{Work: work, Runner: Runner{Work: work, Env: NativeEnvironment()}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(work, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		t.Fatal("bootstrap did not produce one certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Fatalf("bootstrap CA is not self-signed: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	for _, tc := range []struct {
		name string
		at   time.Time
		ok   bool
	}{
		{"current", time.Now(), true},
		{"retained for two days", time.Now().Add(48 * time.Hour), true},
		{"expired", cert.NotAfter.Add(time.Second), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: tc.at, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
			if (err == nil) != tc.ok {
				t.Fatalf("certificate validity at %s: %v", tc.at, err)
			}
		})
	}
	key, err := os.Stat(filepath.Join(work, "ca.key"))
	if err != nil {
		t.Fatal(err)
	}
	if key.Mode().Perm() != 0o600 {
		t.Fatal("bootstrap CA key must remain private")
	}
}

func TestKindNetworkAdmission(t *testing.T) {
	for _, row := range []struct {
		name, gateway, subnet, driver string
		ok                            bool
	}{
		{"discovered-subnet", "172.29.0.1", "172.29.0.0/16", "bridge", true},
		{"invalid-gateway", "invalid", "172.29.0.0/16", "bridge", false},
		{"public-gateway", "8.8.8.8", "8.8.8.0/24", "bridge", false},
		{"outside-subnet", "172.29.0.1", "172.30.0.0/16", "bridge", false},
		{"no-ipv4", "fd00::1", "fd00::/64", "bridge", false},
		{"wrong-driver", "172.29.0.1", "172.29.0.0/16", "host", false},
		{"guest-overlap", "172.30.0.1", "172.30.0.0/24", "bridge", false},
		{"vip-overlap", "172.31.0.1", "172.31.0.0/24", "bridge", false},
		{"pod-overlap", "10.244.0.1", "10.244.0.0/16", "bridge", false},
		{"service-overlap", "10.96.0.1", "10.96.0.0/16", "bridge", false},
		{"noncanonical-subnet", "172.29.0.1", "172.29.0.1/16", "bridge", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			root := privateTestDir(t)
			output, err := json.Marshal([]map[string]any{{"Id": "exact-kind-network", "Driver": row.driver, "IPAM": map[string]any{"Config": []map[string]string{{"Gateway": row.gateway, "Subnet": row.subnet}}}}})
			if err != nil {
				t.Fatal(err)
			}
			body := "#!/bin/sh\n[ \"$1:$2:$3\" = network:inspect:kind ] || exit 99\nprintf '%s' '" + string(output) + "'\n"
			if err = os.WriteFile(filepath.Join(root, "docker"), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			actual, err := admitKindNetwork(t.Context(), Runner{Env: []string{"PATH=" + root}})
			if (err == nil) != row.ok {
				t.Fatalf("network=%+v error=%v", actual, err)
			}
			if row.ok && actual != (kindNetwork{Gateway: row.gateway, CIDR: row.subnet, ID: "exact-kind-network"}) {
				t.Fatalf("did not retain exact discovered network: %+v", actual)
			}
			if !row.ok && actual != (kindNetwork{}) {
				t.Fatal("invalid network returned an admitted gateway")
			}
		})
	}
}

func TestRuntimeValuesUseObservedNativeDigests(t *testing.T) {
	_, outputs := runtimeValueInputs(t)
	outputs["postgresql"] = SelectedOutput{Observation: lifecycle.Artifact{Digest: "chart-pin"}}
	values := runtimeValues("172.18.0.1:15501", outputs)
	postgres := values["postgresql"].(postgresOverrides)
	if postgres.Image.Digest != outputs["runtime-postgresql"].Observation.Digest || outputs["postgresql"].Observation.Digest != "chart-pin" {
		t.Fatal("chart/image namespaces confused")
	}
	spire := values["spire"].(spireOverrides)
	if spire.Agent.SocketAlternate.Image.Tag != outputs["runtime-spire-bash"].Observation.Digest || spire.Agent.FSGroupFix.Image != spire.Agent.SocketAlternate.Image || spire.CSI.NodeDriverRegistrar.Image.Tag != outputs["runtime-csi-node-driver-registrar"].Observation.Digest {
		t.Fatal("SPIRE roles not pinned")
	}
	temporal := values["temporal"].(temporalOverrides)
	if temporal.Server.Image.Tag != outputs["runtime-temporal-server"].imageTag+"@"+outputs["runtime-temporal-server"].Observation.Digest {
		t.Fatal("Temporal native digest missing")
	}
}

// Keep the consumer fixture aligned with the actual catalog declarations. These
// detached descriptions stand in for the already admitted Plan/Run boundary.
func runtimeValueInputs(t *testing.T) (*factory.Plan, map[string]SelectedOutput) {
	t.Helper()
	selected, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "inputs", "catalogs", "runtime-dependencies")))
	if err != nil {
		t.Fatal(err)
	}
	plan := &factory.Plan{}
	outputs := make(map[string]SelectedOutput)
	for _, role := range runtimeImages {
		component := role + "-image"
		producer := selected.Components[component]
		output := lifecycle.Artifact{Name: component + "-arm64", Kind: "oci", Components: []string{component}, BuildDigest: "sha256:" + strings.Repeat("b", 64), Digest: producer.Prebuilt.Digests["arm64"]}
		plan.Builds = append(plan.Builds, factory.BuildPlan{Name: output.Name, BuildDigest: output.BuildDigest, Platform: "linux/arm64", ResolvedComponents: []catalog.NamedComponent{{Name: component, Component: producer}}})
		value := SelectedOutput{Observation: output}
		if strings.HasPrefix(role, "temporal-") {
			value.imageTag, err = runtimeImageTag(plan, output)
			if err != nil {
				t.Fatal(err)
			}
		}
		outputs["runtime-"+role] = value
	}
	return plan, outputs
}

func TestRuntimeValuesFollowDeclaredTemporalTag(t *testing.T) {
	for _, role := range []string{"temporal-server", "temporal-admin-tools", "temporal-ui"} {
		t.Run(role, func(t *testing.T) {
			plan, outputs := runtimeValueInputs(t)
			selected := outputs["runtime-"+role]
			for i := range plan.Builds {
				if plan.Builds[i].Name == selected.Observation.Name {
					// Change only the declared image tag, leaving NVKE's source tag
					// and the exact observed payload digest distinct and unchanged.
					plan.Builds[i].ResolvedComponents[0].Prebuilt.Image = "temporalio/selected:9.3.2"
				}
			}
			tag, err := runtimeImageTag(plan, selected.Observation)
			if err != nil {
				t.Fatal(err)
			}
			selected.imageTag = tag
			outputs["runtime-"+role] = selected
			values := runtimeValues("registry.example", outputs)["temporal"].(temporalOverrides)
			got := map[string]pinnedImage{"temporal-server": values.Server.Image, "temporal-admin-tools": values.AdminTools.Image, "temporal-ui": values.Web.Image}[role]
			if got.Tag != "9.3.2@"+selected.Observation.Digest || got.Repository != "registry.example/nvkr-local/"+role {
				t.Fatalf("declared tag or observed digest changed: %+v", got)
			}
		})
	}
}

func TestRuntimeImageTagRequiresOneExplicitDeclaration(t *testing.T) {
	for _, scenario := range []string{"missing plan", "missing output", "missing producer", "foreign producer", "ambiguous producer", "ambiguous output", "absent image", "source tag only", "implicit tag", "digest only", "invalid tag"} {
		t.Run(scenario, func(t *testing.T) {
			plan, outputs := runtimeValueInputs(t)
			selected := outputs["runtime-temporal-server"].Observation
			index := 0
			for i := range plan.Builds {
				if plan.Builds[i].Name == selected.Name {
					index = i
				}
			}
			build := &plan.Builds[index]
			switch scenario {
			case "missing plan":
				plan = nil
			case "missing output":
				build.Name = "other-output"
			case "missing producer":
				build.ResolvedComponents = nil
			case "foreign producer":
				build.ResolvedComponents[0].Name = "other-component"
			case "ambiguous producer":
				build.ResolvedComponents = append(build.ResolvedComponents, build.ResolvedComponents[0])
			case "ambiguous output":
				plan.Builds = append(plan.Builds, *build)
			case "absent image":
				build.ResolvedComponents[0].Prebuilt.Image = ""
			case "source tag only":
				build.ResolvedComponents[0].Prebuilt = nil
			case "implicit tag":
				build.ResolvedComponents[0].Prebuilt.Image = "temporalio/server"
			case "digest only":
				build.ResolvedComponents[0].Prebuilt.Image = "temporalio/server@sha256:" + strings.Repeat("a", 64)
			case "invalid tag":
				build.ResolvedComponents[0].Prebuilt.Image = "temporalio/server:bad tag"
			}
			tag, err := runtimeImageTag(plan, selected)
			var coded *nvkrerr.Error
			if tag != "" || !errors.As(err, &coded) || coded.Code != nvkrerr.CodeVerifyFailed {
				t.Fatalf("declaration refusal: tag=%q err=%v", tag, err)
			}
		})
	}
}

func TestRuntimeImageTagSurvivesCompiledPlan(t *testing.T) {
	root := filepath.Join("..", "..", "inputs")
	declared, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definitions", "demo", "native-mac", "definition-runtime-images.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Compile the three unchanged authored Temporal outputs through the actual
	// factory owner, without fetching payloads or admitting unrelated producers.
	outputs := declared.Outputs[:0]
	for _, output := range declared.Outputs {
		if strings.HasPrefix(output.Name, "temporal-") {
			outputs = append(outputs, output)
		}
	}
	declared.Outputs = outputs
	loaded, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, "catalogs", "runtime-dependencies")))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(loaded)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := definition.LoadEnvironment(t.Context(), filepath.Join(root, "environments", "local-native-linux.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := factory.Compile(t.Context(), declared, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := factory.LoadPlanContext(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Builds) != 3 {
		t.Fatal("compiled Temporal output set changed")
	}
	for _, build := range restored.Builds {
		producer := build.ResolvedComponents[0]
		if producer.Prebuilt.Image != loaded.Components[producer.Name].Prebuilt.Image {
			t.Fatal("compiled/captured declaration lost its original image tag")
		}
		observed := lifecycle.Artifact{Name: build.Name, Components: build.Components, BuildDigest: build.BuildDigest, Digest: producer.Prebuilt.Digests["arm64"], Kind: "oci"}
		tag, err := runtimeImageTag(restored, observed)
		if err != nil || tag == "" || "v"+tag == producer.Tag {
			t.Fatalf("compiled Plan lost the runtime tag or used NVKE's source tag: tag=%q err=%v", tag, err)
		}
	}
}

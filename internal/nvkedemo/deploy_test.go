// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDeploymentSelectsSameThreeVMWorkflowOnBothHosts(t *testing.T) {
	for _, platform := range []string{"darwin/arm64", "linux/arm64"} {
		for _, network := range []string{"ovn", "upstream-ovn"} {
			t.Run(platform+"/"+network, func(t *testing.T) {
				root := privateTestDir(t)
				name, err := selectedDiskOutput(network)
				if err != nil {
					t.Fatal(err)
				}
				disk := filepath.Join(root, "outputs", name+".qcow2")
				if err := os.Mkdir(filepath.Dir(disk), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(disk, []byte("observed disk fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				digest, err := DigestFile(t.Context(), disk)
				if err != nil {
					t.Fatal(err)
				}
				record := fixtureRun()
				expected := record["expected"].([]map[string]any)
				observed := record["observed"].([]map[string]any)
				expected[1]["name"], expected[1]["kind"] = name, "disk-image"
				observed[1]["name"], observed[1]["kind"], observed[1]["digest"] = name, "disk-image", digest
				observed[1]["disk"] = delivery.DiskOutput{PayloadDigest: digest, Producer: "disk-build", Component: "component", Format: "qcow2", Encryption: "none", Size: int64(len("observed disk fixture")), PlanDigest: "sha256:" + strings.Repeat("1", 64), Outcome: "succeeded"}
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "lifecycle.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				cfg := Config{Walkthrough: network == "ovn", Network: network, NVKR: "nvkr", Out: filepath.Join(root, "deploy"), DemoExecutable: "demo", Cleanup: true, Runs: RunInputs{OS: root, Components: "components", Providers: "providers", Site: "site", Tools: "tools", Profiles: "profiles", Runtime: "runtime", HostTools: "host"}}
				selected, err := nativeDeploymentConfig(t.Context(), cfg, platform)
				want := "hvf"
				if platform == "linux/arm64" {
					want = "kvm"
				}
				if err != nil || selected.Image != disk || selected.Accelerator != want || selected.Network != network || !selected.Cleanup || selected.DemoExecutable != cfg.DemoExecutable || selected.Walkthrough != cfg.Walkthrough {
					t.Fatalf("native deployment selection: %+v %v", selected, err)
				}
				assets := selected.Assets
				if assets.ToolsRun != "tools" || assets.HostToolsRun != "host" || assets.ControllerRun != "site" || assets.BootstrapRun != "components" || assets.ProvidersRun != "providers" || assets.DeploymentRun != "profiles" || assets.RuntimeRun != "runtime" {
					t.Fatalf("Run bindings changed: %+v", assets)
				}
				if err := os.WriteFile(disk, []byte("changed bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := nativeDeploymentConfig(t.Context(), cfg, platform); err == nil {
					t.Fatal("substituted disk admitted")
				}
			})
		}
	}
}

func TestDeploymentRejectsUnsupportedHostBeforeArtifactIO(t *testing.T) {
	for _, tc := range []struct{ platform, accelerator string }{{"linux/arm64", "tcg"}, {"darwin/arm64", "kvm"}, {"darwin/arm64", "tcg"}, {"windows/arm64", ""}} {
		t.Run(tc.platform+tc.accelerator, func(t *testing.T) {
			_, err := nativeDeploymentConfig(t.Context(), Config{Accelerator: tc.accelerator}, tc.platform)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
				t.Fatalf("unsupported host reached artifact work: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := DeployArtifacts(ctx, Config{}); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
}

func TestNativeDeploymentRejectsRetiredNetworkBeforeBuildOrArtifactIO(t *testing.T) {
	for _, platform := range []string{"darwin/arm64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			cfg := Config{Network: "kind-calico"}
			for _, err := range []error{AdmitDemoDeployment(t.Context(), cfg), func() error {
				_, err := nativeDeploymentConfig(t.Context(), cfg, platform)
				return err
			}()} {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability || !strings.Contains(err.Error(), "selected OVN") {
					t.Fatalf("retired network reached later admission: %v", err)
				}
			}
		})
	}
}

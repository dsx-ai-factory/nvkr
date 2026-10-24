// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestDeploymentSelectsSameNativeWorkflowOnBothHosts(t *testing.T) {
	for _, platform := range []string{"darwin/arm64", "linux/arm64"} {
		for _, network := range []string{"ovn", "upstream-ovn"} {
			t.Run(platform+"/"+network, func(t *testing.T) {
				root := filepath.Join(privateTestDir(t), "disk-run")
				name, err := selectedDiskOutput(network)
				if err != nil {
					t.Fatal(err)
				}
				disk := filepath.Join(root, "outputs", name+".qcow2")
				if err := os.MkdirAll(filepath.Dir(disk), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(disk, []byte("observed disk fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				digest, err := DigestFile(t.Context(), disk)
				if err != nil {
					t.Fatal(err)
				}
				capturedArtifactRunFixture(t, root, []lifecycle.DefinitionOutput{
					{Name: name, Platform: "linux/arm64", Format: "disk-image", Components: []string{"disk"}},
				}, map[string]lifecycle.Artifact{name: {Digest: digest,
					Disk: &delivery.DiskOutput{PayloadDigest: digest, Producer: "build-disk", Component: "disk", Format: "qcow2", Encryption: "none", Size: int64(len("observed disk fixture")), Outcome: "succeeded"},
				}})
				cfg := Config{TenantWorkers: 2, Walkthrough: network == "ovn", Network: network, NVKR: "nvkr", Out: filepath.Join(root, "deploy"), DemoExecutable: "demo", Cleanup: true, Runs: RunInputs{OS: root, Components: "components", Providers: "providers", Site: "site", Tools: "tools", Profiles: "profiles", Runtime: "runtime", HostTools: "host"}}
				selected, err := nativeDeploymentConfig(t.Context(), cfg, platform)
				want := "hvf"
				if platform == "linux/arm64" {
					want = "kvm"
				}
				if err != nil || selected.Image != disk || selected.Accelerator != want || selected.Network != network || !selected.Cleanup || selected.DemoExecutable != cfg.DemoExecutable || selected.Walkthrough != cfg.Walkthrough || selected.TenantWorkers != 2 || selected.conformance != nil {
					t.Fatalf("native deployment selection: %+v %v", selected, err)
				}
				assets := selected.Assets
				if assets.ToolsRun != "tools" || assets.HostToolsRun != "host" || assets.ControllerRun != "site" || assets.BootstrapRun != "components" || assets.ProvidersRun != "providers" || assets.DeploymentRun != "profiles" || assets.RuntimeRun != "runtime" {
					t.Fatalf("Run bindings changed: %+v", assets)
				}
				if platform == "darwin/arm64" {
					cfg.Accelerator = "tcg"
					selected, err = nativeDeploymentConfig(t.Context(), cfg, platform)
					if err != nil || selected.Accelerator != "tcg" || selected.DemoExecutable != cfg.DemoExecutable {
						t.Fatal("explicit TCG did not reach the shared native runtime", err)
					}
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

func TestNativeTenantWorkersRefusedBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		workers     int
		conformance bool
	}{{-1, false}, {3, false}, {1, true}} {
		for _, boundary := range []string{"deployment admission", "deployment selection", "direct native", "capacity"} {
			t.Run(fmt.Sprintf("%d/%t/%s", tc.workers, tc.conformance, boundary), func(t *testing.T) {
				root := privateTestDir(t)
				cfg := Config{TenantWorkers: tc.workers, Conformance: tc.conformance, Out: filepath.Join(root, "output")}
				var err error
				switch boundary {
				case "deployment admission":
					err = AdmitDemoDeployment(t.Context(), cfg)
				case "deployment selection":
					_, err = nativeDeploymentConfig(t.Context(), cfg, "darwin/arm64")
				case "direct native":
					selected := NativeConfig{TenantWorkers: tc.workers, Out: cfg.Out}
					if tc.conformance {
						selected.conformance = &nativeConformanceConfig{}
					}
					err = RunNative(t.Context(), selected)
				case "capacity":
					admission := nativeResourceAdmission{tenantWorkers: tc.workers, conformance: tc.conformance, observe: func(context.Context, string) (execution.ResourceCapacity, error) {
						t.Fatal("invalid worker selection reached capacity observation")
						return execution.ResourceCapacity{}, nil
					}}
					err = admission.check(t.Context(), root, false, nil)
				}
				var coded *nvkrerr.Error
				wantCode := nvkrerr.CodeUsage
				if boundary == "direct native" {
					wantCode = nvkrerr.CodePreflightFailed
				}
				if !errors.As(err, &coded) || coded.Code != wantCode {
					t.Fatalf("invalid worker selection reached later admission: %v", err)
				}
				if boundary == "direct native" {
					var cause *nvkrerr.Error
					if !errors.As(coded.Unwrap(), &cause) || cause.Code != nvkrerr.CodeUsage {
						t.Fatalf("native preflight did not preserve worker usage cause: %v", err)
					}
				}
				entries, readErr := os.ReadDir(root)
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("invalid worker selection created output: %v %v", entries, readErr)
				}
			})
		}
	}
}

func TestDeploymentRejectsUnsupportedHostBeforeArtifactIO(t *testing.T) {
	for _, tc := range []struct{ platform, accelerator string }{{"linux/arm64", "tcg"}, {"darwin/arm64", "kvm"}, {"windows/arm64", ""}} {
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

func TestDemoRejectsInvalidOutputBeforeBootstrap(t *testing.T) {
	if err := AdmitNativeDeployment(t.Context(), ""); err != nil {
		t.Skipf("native host admission unavailable: %v", err)
	}
	root := privateTestDir(t)
	for _, tc := range []struct{ name, out string }{
		{"empty", ""},
		{"existing", root},
		{"missing parent", filepath.Join(root, "missing", "runtime")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := AdmitDemoDeployment(t.Context(), Config{Out: tc.out})
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeUsage || !strings.Contains(err.Error(), "deployment output") {
				t.Fatalf("invalid output reached bootstrap admission: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("output admission created files: %v %v", entries, err)
			}
		})
	}
}

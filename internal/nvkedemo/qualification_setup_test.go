// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestQualificationSetupScopeMatchesMaintainedDefinitions(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn", "kind-calico"} {
		for _, change := range []string{"exact", "missing", "extra", "unexpected output", "duplicate", "member", "kind", "unknown network", "other network"} {
			t.Run(network+"/"+change, func(t *testing.T) {
				selected, _, err := LocalArtifactInputs(network)
				if err != nil {
					t.Fatal(err)
				}
				d, err := definition.LoadDefinition(t.Context(), filepath.Join("../..", selected))
				if err != nil {
					t.Fatal(err)
				}
				if len(d.Outputs) != 32 {
					t.Fatal("maintained local selection changed; review the authored qualification scope")
				}
				plan := &factory.Plan{}
				for _, output := range d.Outputs {
					plan.LifecycleOutputs = append(plan.LifecycleOutputs, factory.LifecycleOutputPlan{Name: output.Name, Kind: output.Format, Components: output.Components})
				}
				mode := network
				switch change {
				case "missing":
					plan.LifecycleOutputs = plan.LifecycleOutputs[1:]
				case "extra":
					plan.LifecycleOutputs = append(plan.LifecycleOutputs, factory.LifecycleOutputPlan{Name: "foreign"})
				case "unexpected output":
					plan.LifecycleOutputs[0].Name = "foreign"
				case "duplicate":
					plan.LifecycleOutputs[0] = plan.LifecycleOutputs[1]
				case "member":
					plan.LifecycleOutputs[0].Components = []string{"foreign"}
				case "kind":
					plan.LifecycleOutputs[0].Kind = "executable"
				case "unknown network":
					mode = "unknown"
				case "other network":
					mode = "upstream-ovn"
					if network == "upstream-ovn" {
						mode = "ovn"
					}
				}
				var policy lifecycle.LocalQualificationPolicy
				err = bindDemoQualificationScope(&policy, plan, mode)
				if change != "exact" {
					assertNightlyCode(t, err, e.CodeUsage)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(policy.Smoke.NonToolOutputs) != 20 {
					t.Fatal("deployment-image scope changed")
				}
				for _, output := range d.Outputs {
					if output.Format != lifecycle.OutputOCI {
						continue
					}
					index := slices.IndexFunc(policy.Smoke.NonToolOutputs, func(rule lifecycle.QualificationNonToolOutput) bool { return rule.Output == output.Name })
					if index < 0 {
						t.Fatalf("selected image %q has no explicit policy role", output.Name)
					}
					rule := policy.Smoke.NonToolOutputs[index]
					if rule.Kind != lifecycle.OutputOCI || rule.Reason != "deployment-image" || !slices.Equal(rule.Components, output.Components) {
						t.Fatalf("policy does not bind exact selected image %q: %+v", output.Name, rule)
					}
				}
				for i := 1; i < len(policy.Smoke.NonToolOutputs); i++ {
					if policy.Smoke.NonToolOutputs[i-1].Output >= policy.Smoke.NonToolOutputs[i].Output {
						t.Fatal("nondeterministic scope")
					}
				}
			})
		}
	}
}

func TestQualificationSetupScopeRejectsConformanceDefinitions(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn"} {
		t.Run(network, func(t *testing.T) {
			selected, _, err := LocalArtifactInputsFor(network, true, "linux")
			if err != nil {
				t.Fatal(err)
			}
			d, err := definition.LoadDefinition(t.Context(), filepath.Join("../..", selected))
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Outputs) != 38 {
				t.Fatal("explicit conformance selection changed; qualification scope must be reviewed")
			}
			plan := &factory.Plan{}
			for _, output := range d.Outputs {
				plan.LifecycleOutputs = append(plan.LifecycleOutputs, factory.LifecycleOutputPlan{Name: output.Name, Kind: output.Format, Components: output.Components})
			}
			var policy lifecycle.LocalQualificationPolicy
			assertNightlyCode(t, bindDemoQualificationScope(&policy, plan, network), e.CodeUsage)
		})
	}
}

func TestQualificationRegistryOwnership(t *testing.T) {
	for _, change := range []string{"exact", "id", "owner", "root", "image", "user", "mount", "extra mount", "public port", "extra port", "network", "writable"} {
		t.Run(change, func(t *testing.T) {
			id, owner, out := strings.Repeat("a", 64), rand.Text(), "/private/qualification"
			if !qualificationOwner.MatchString(owner) {
				t.Fatal("generated owner outside admission")
			}
			var row qualificationRegistryInspect
			raw := `{"Id":"` + id + `","Config":{"Image":"` + registryImage + `","User":"` + qualificationRegistryUser() + `","Labels":{"` + qualificationRegistryLabel + `":"` + owner + `","` + qualificationRegistryLabel + `-root":"` + out + `"}},"Mounts":[{"Type":"bind","Source":"` + out + `/registry-data","Destination":"/var/lib/registry"}],"HostConfig":{"NetworkMode":"bridge","ReadonlyRootfs":true,"PortBindings":{"5000/tcp":[{"HostIP":"127.0.0.1","HostPort":""}]}}}`
			if err := json.Unmarshal([]byte(raw), &row); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "id":
				row.ID = strings.Repeat("b", 64)
			case "owner":
				row.Config.Labels[qualificationRegistryLabel] = "foreign"
			case "root":
				row.Config.Labels[qualificationRegistryLabel+"-root"] = "/foreign"
			case "image":
				row.Config.Image = "registry:latest"
			case "user":
				row.Config.User = "foreign"
			case "mount":
				row.Mounts[0].Source = "/foreign"
			case "extra mount":
				row.Mounts = append(row.Mounts, row.Mounts[0])
			case "public port":
				row.HostConfig.PortBindings["5000/tcp"][0].HostIP = "0.0.0.0"
			case "extra port":
				row.HostConfig.PortBindings["1234/tcp"] = row.HostConfig.PortBindings["5000/tcp"]
			case "network":
				row.HostConfig.NetworkMode = "host"
			case "writable":
				row.HostConfig.ReadonlyRootfs = false
			}
			err := validateQualificationRegistry(row, out, qualificationRegistry{ID: id, Owner: owner})
			if change == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertNightlyCode(t, err, e.CodeUsage)
		})
	}
}

func TestQualificationRegistryReadiness(t *testing.T) {
	for _, mode := range []string{"registry", "other service", "redirect", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/" {
					t.Error("unexpected readiness route")
				}
				if mode == "registry" {
					w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
				}
				if mode == "redirect" {
					w.Header().Set("Location", "http://untrusted.invalid")
					w.WriteHeader(http.StatusFound)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			err := waitQualificationRegistry(ctx, strings.TrimPrefix(server.URL, "http://127.0.0.1:"))
			if mode == "registry" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("readiness refusal: %v", err)
			}
		})
	}
}

func TestQualificationSetupRejectsForeignCosign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cosign")
	if err := os.WriteFile(path, []byte("untrusted executable"), 0600); err != nil {
		t.Fatal(err)
	}
	assertNightlyCode(t, checkSetupCosign(t.Context(), path), e.CodeUnpinnedInput)
}

func TestQualificationSetupPlatformBeforeWork(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		t.Skip("unsupported-host contract")
	}
	out := filepath.Join(t.TempDir(), "untouched")
	_, err := SetupQualification(t.Context(), DemoFile{}, out, nil)
	assertNightlyCode(t, err, e.CodeEngineCapability)
	if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsupported setup wrote files")
	}
}

func TestBuildConfigPreservesQualificationNetwork(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn", "kind-calico", "unknown"} {
		t.Run(network, func(t *testing.T) {
			cfg, err := (DemoFile{Version: 1, Build: BuildFile{Repo: "/repo", Attempt: "fresh", Jobs: 2}, Run: RunFile{Network: network}}).BuildConfig(nil)
			if network == "unknown" {
				assertNightlyCode(t, err, e.CodeUsage)
				return
			}
			if err != nil || cfg.Network != network {
				t.Fatalf("network=%q err=%v", cfg.Network, err)
			}
		})
	}
}

func TestAutomaticQualificationAdmission(t *testing.T) {
	for _, mode := range []string{"automatic", "prepared", "policy", "keys", "manual"} {
		t.Run(mode, func(t *testing.T) {
			file := DemoFile{Version: 1, Build: BuildFile{Repo: "/repo", Attempt: "fresh", Jobs: 1, QualifyLocal: true}}
			switch mode {
			case "prepared":
				file.Build.Prepared = true
			case "policy":
				file.Build.QualificationPolicy = "/policy"
			case "keys":
				file.Build.Qualification.BuilderKey = "/key"
			case "manual":
				file.Build.QualifyLocal = false
			}
			cfg, err := file.BuildConfig(nil)
			if mode != "automatic" && mode != "manual" {
				assertNightlyCode(t, err, e.CodeUsage)
				_, err = BuildArtifacts(t.Context(), BuildConfig{QualifyLocal: true, Prepared: file.Build.Prepared, QualificationPolicy: file.Build.QualificationPolicy, Qualification: file.Build.Qualification})
				if err == nil {
					t.Fatal("direct build accepted conflicting trust")
				}
				return
			}
			if err != nil || cfg.QualifyLocal != file.Build.QualifyLocal {
				t.Fatal(cfg, err)
			}
		})
	}
}

func TestAutomaticQualificationCleanupSurvivesCancellation(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "stop failure"}[fails], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var expected error
			if fails {
				expected = e.New(e.CodePreflightFailed, "stop refused")
			}
			calls := 0
			err := finishAutomaticQualification(ctx, "/owned", func(cleanup context.Context, out string) error {
				calls++
				deadline, ok := cleanup.Deadline()
				if cleanup.Err() != nil || !ok || time.Until(deadline) > time.Minute || out != "/owned" {
					t.Fatal("cleanup lost its lifetime or selector")
				}
				return expected
			})
			if calls != 1 || err != expected {
				t.Fatal(calls, err)
			}
		})
	}
}

func TestQualificationSetupFailureRecovery(t *testing.T) {
	for _, retained := range []string{"", "/private/failed-setup"} {
		t.Run(retained, func(t *testing.T) {
			cause := e.New(e.CodeUnpinnedInput, "pin mismatch")
			err := qualificationSetupFailure(cause, retained)
			assertNightlyCode(t, err, e.CodeUnpinnedInput)
			if !errors.Is(err, cause) {
				t.Fatal("lost original cause")
			}
			if retained != "" {
				for _, want := range []string{retained, "fresh --out", "fresh --attempt", "not successful evidence"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("missing recovery %q: %v", want, err)
					}
				}
			} else if !strings.Contains(err.Error(), "before creating") {
				t.Fatal(err)
			}
			if qualificationSetupFailure(nil, retained) != nil {
				t.Fatal("success changed")
			}
		})
	}
}

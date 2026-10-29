// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestEndToEnd is opt-in: ordinary tests do not build or grant host controls.
// The existing Go owners build fresh Runs and bootstrap the site into a real
// native VM. The demo executable supplies only its existing private helpers.
func TestEndToEnd(t *testing.T) {
	enabled, err := nativeE2EOptIn("NVKR_NVKE_E2E", os.Getenv("NVKR_NVKE_E2E"))
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set NVKR_NVKE_E2E=1 on a dedicated native host to build and bootstrap real NVKE")
	}
	build, cfg, tenantChecks, err := nativeE2EConfig(t.Context(), os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if err := admitNativeBuildHost(runtime.GOOS + "/" + runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	workBudget := 8 * time.Hour
	if cfg.Conformance {
		workBudget += 4 * time.Hour
	}
	workDeadline := time.Now().Add(workBudget)
	// Keep the Go test alive after work cancellation for independent guest
	// cleanup, network restoration, bridge stop and bounded diagnostics.
	if deadline, ok := t.Deadline(); ok {
		latestWork := deadline.Add(-2 * time.Hour)
		if !latestWork.After(time.Now()) {
			t.Fatal("native E2E requires a go test timeout with more than two hours reserved for owned cleanup")
		}
		if latestWork.Before(workDeadline) {
			workDeadline = latestWork
		}
	}
	signalCtx, stop := signal.NotifyContext(t.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithDeadline(signalCtx, workDeadline)
	defer cancel()
	if err := AdmitDemoDeployment(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	built, err := BuildArtifacts(ctx, build)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Runs, cfg.NVKR = built.Runs, built.NVKR
	err = WithNativeSite(ctx, cfg, func(ctx context.Context, site *NativeSite) error {
		// A child failure must return through the lifecycle owner, preserving its
		// original error and joined cleanup result before failing the parent.
		run := func(name string, check func(context.Context) error) error {
			var checkErr error
			passed := t.Run(name, func(t *testing.T) {
				checkErr = check(ctx)
				if checkErr != nil {
					t.Error(checkErr)
				}
			})
			if !passed {
				return errors.Join(fmt.Errorf("native E2E %s subtest failed", name), checkErr)
			}
			return nil
		}
		if err := run("site", func(ctx context.Context) error { return nativeE2ESiteSmoke(ctx, site) }); err != nil {
			return err
		}
		if !tenantChecks {
			return nil
		}
		var tenant *NativeTenant
		if err := run("tenant", func(ctx context.Context) error {
			var err error
			tenant, err = site.CreateTenant(ctx, "native-e2e-tenant")
			return err
		}); err != nil {
			return err
		}
		if cfg.Conformance {
			return run("conformance", func(ctx context.Context) error {
				// A Go subtest filter may omit the tenant subtest. Conformance
				// still requires its real tenant rather than silently succeeding.
				if tenant == nil {
					var err error
					tenant, err = site.CreateTenant(ctx, "native-e2e-tenant")
					if err != nil {
						return err
					}
				}
				return tenant.RunConformance(ctx, filepath.Join(cfg.Out, "conformance"))
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func nativeE2EOptIn(name, value string) (bool, error) {
	switch value {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, configUsage(name + " must be unset, 0 or 1")
	}
}

func nativeE2EConfig(ctx context.Context, getenv func(string) string) (BuildConfig, Config, bool, error) {
	tenant, err := nativeE2EOptIn("NVKR_NVKE_E2E_TENANT", getenv("NVKR_NVKE_E2E_TENANT"))
	if err != nil {
		return BuildConfig{}, Config{}, false, err
	}
	conformance, err := nativeE2EOptIn("NVKR_NVKE_E2E_CONFORMANCE", getenv("NVKR_NVKE_E2E_CONFORMANCE"))
	if err != nil {
		return BuildConfig{}, Config{}, false, err
	}
	var file DemoFile
	if path := getenv("NVKR_NVKE_E2E_CONFIG"); path != "" {
		file, err = LoadDemoFile(ctx, path)
		if err != nil {
			return BuildConfig{}, Config{}, false, err
		}
	} else {
		for _, key := range []string{"NVKR_NVKE_E2E_REPO", "NVKR_NVKE_E2E_OUT", "NVKR_NVKE_E2E_ATTEMPT"} {
			if !configString(getenv(key)) {
				return BuildConfig{}, Config{}, false, configUsage("explicit " + key + " required")
			}
		}
		file = DemoFile{Version: 1,
			Build: BuildFile{Repo: getenv("NVKR_NVKE_E2E_REPO"), Attempt: getenv("NVKR_NVKE_E2E_ATTEMPT"), Jobs: 1, NVKEClone: getenv("NVKE_LOCAL_CLONE")},
			Run:   RunFile{Out: getenv("NVKR_NVKE_E2E_OUT"), Network: "ovn", TimeoutSeconds: DefaultTenantTimeoutSeconds},
		}
	}
	file.Run.Conformance = file.Run.Conformance || conformance
	file.Run.Cleanup = true
	binary := getenv("NVKR_NVKE_E2E_BINARY")
	if binary == "" {
		binary = filepath.Join(file.Build.Repo, "bin", "nvke-demo")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return BuildConfig{}, Config{}, false, err
	}
	build, cfg, err := file.E2EConfig(os.Stdout)
	build.DemoExecutable, cfg.DemoExecutable = binary, binary
	return build, cfg, tenant || cfg.Conformance, err
}

func nativeE2ESiteSmoke(ctx context.Context, site *NativeSite) error {
	kubeconfig, err := site.Kubeconfig(ctx)
	if err != nil {
		return err
	}
	return site.operate(ctx, nil, func(ctx context.Context) error {
		scope := site.scope
		kubectl := filepath.Join(scope.tools, "kubectl")
		args := []string{kubectl, "--kubeconfig", kubeconfig, "--request-timeout=10s"}
		raw, err := scope.runner.Capture(ctx, append(args, "get", "--raw=/readyz"), "e2e-site-api", 15*time.Second)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(raw)) != "ok" {
			return errors.New("site Kubernetes API did not report ready")
		}
		if err := scope.runner.Run(ctx, []string{kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Ready", "nodes", "--all", "--timeout=2m"}, "e2e-site-wait-nodes", 3*time.Minute); err != nil {
			return err
		}
		nodes, err := captureJSON[struct{ Items []tenantNode }](ctx, scope.runner, append(args, "get", "nodes", "-o", "json"), "e2e-site-nodes")
		if err != nil {
			return err
		}
		if len(nodes.Items) != 1 || nodes.Items[0].Metadata.Name == "" || nodes.Items[0].Metadata.UID == "" {
			return errors.New("site smoke requires exactly one identified Kubernetes node")
		}
		arch := scope.attempt.assets.arch
		if arch == "" {
			arch = "arm64"
		}
		if _, err := nativeQEMUArchitecture(arch); err != nil {
			return err
		}
		if nodes.Items[0].Status.NodeInfo.Architecture != arch {
			return errors.New("site Kubernetes node architecture differs from selected native artifacts")
		}
		ready := 0
		for _, condition := range nodes.Items[0].Status.Conditions {
			if condition.Type == "Ready" {
				if condition.Status != "True" {
					return errors.New("site Kubernetes node is not Ready")
				}
				ready++
			}
		}
		if ready != 1 {
			return errors.New("site Kubernetes node must report one Ready condition")
		}
		if scope.attempt.guard != nil {
			resources, err := selectedNativeResources(scope.attempt.resources)
			if err != nil {
				return err
			}
			domains, err := nativeLinuxVMEvidenceForResources(ctx, scope.runner, scope.attempt.simulatorWork, scope.attempt.diskDigest, 1, arch, resources.VCPUs)
			if err != nil {
				return err
			}
			return privateJSON(filepath.Join(scope.out, "native-site-domains.json"), domains)
		}
		return nil
	})
}

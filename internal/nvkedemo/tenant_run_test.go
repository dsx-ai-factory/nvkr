// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// This exercises tenant.run with owned tool subprocesses and the existing VM
// evidence fixtures. It never starts QEMU or claims real cluster acceptance.
func tenantRunFixture(t *testing.T, network, fail, block string) (tenant, string) {
	t.Helper()
	root := privateTestDir(t)
	work := filepath.Join(root, "site")
	simulatorWork := filepath.Join(root, "simulator")
	for _, path := range []string{work, simulatorWork, filepath.Join(work, "cli")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	domains, _, image, _, _ := tenantVMFixtureAt(t, simulatorWork)
	for name, value := range map[string]any{
		"rows.json":   []tenantRow{{Name: "owned", ID: "owned-id", NICo: TenantProvider{OrgID, SiteID, VPCID}}},
		"config.json": map[string]any{"clusters": []any{map[string]any{"cluster": map[string]string{"server": "https://" + TenantVIP + ":6443"}}}},
		"nodes.json": map[string]any{"items": []any{
			map[string]any{"metadata": map[string]any{"name": "control-plane", "labels": map[string]string{"node-role.kubernetes.io/control-plane": ""}}, "status": map[string]any{"conditions": []any{map[string]string{"type": "Ready", "status": "True"}}, "nodeInfo": map[string]string{"architecture": "arm64", "kubeletVersion": "v1.34.2"}}},
			map[string]any{"metadata": map[string]string{"name": "worker"}, "status": map[string]any{"conditions": []any{map[string]string{"type": "Ready", "status": "True"}}, "nodeInfo": map[string]string{"architecture": "arm64", "kubeletVersion": "v1.34.2"}}},
		}},
		"pods.json": map[string]any{"items": []any{map[string]any{"status": map[string]any{"containerStatuses": []any{map[string]string{"imageID": WorkloadImage}}}}}},
	} {
		if err := privateJSON(filepath.Join(root, name), value); err != nil {
			t.Fatal(err)
		}
	}
	writeTenantFixture(t, filepath.Join(work, "cli", installerCLIName), []byte(`#!/bin/sh
set -eu
case "$1 $2" in
  'get clusters') if test -f "$FIXTURE/created"; then cat "$FIXTURE/rows.json"; else printf '[]\n'; fi ;;
  'create cluster') : > "$FIXTURE/created" ;;
  'get kubeconfig') printf 'fixture only\n' > "$5" ;;
  *) exit 22 ;;
esac
`), 0o700)
	writeTenantFixture(t, filepath.Join(work, "kubectl"), []byte(`#!/bin/sh
set -eu
step() {
  printf '%s\n' "$1" >> "$FIXTURE/events"
  if test "$FAIL" = "$1"; then exit 7; fi
  if test "$BLOCK" = "$1"; then : > "$FIXTURE/started"; exec sleep 60; fi
}
test "$1" = --kubeconfig
shift 2
if test "$1" = --request-timeout=10s; then shift; fi
if test "$1" = -n; then shift 2; fi
case "$1 $2" in
  'get secret') step kubeconfig; printf '{}\n' ;;
  'config view') cat "$FIXTURE/config.json" ;;
  'get nodes') step nodes; cat "$FIXTURE/nodes.json" ;;
  'get machines.cluster.x-k8s.io,nicomachines.infrastructure.cluster.x-k8s.io') step guest-bindings; printf '{"items":[]}\n' ;;
  'rollout status') step "$3" ;;
  'apply -f') step workload-apply ;;
  'wait job/nvkr-smoke') step workload-complete ;;
  'logs job/nvkr-smoke') step workload-log; printf 'NVKE_LOCAL_WORKLOAD_PASS\n' ;;
  'get pods') step workload-pod; cat "$FIXTURE/pods.json" ;;
  *) exit 22 ;;
esac
`), 0o700)
	runner := Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "FIXTURE=" + root, "FAIL=" + fail, "BLOCK=" + block}}
	simulator, err := (Runner{Env: []string{"PATH=/usr/bin:/bin"}}).Start(t.Context(), []string{"/bin/sh", "-c", "exec sleep 60"}, "fixture-simulator")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := simulator.Stop(ctx); err != nil {
			t.Errorf("join fixture simulator: %v", err)
		}
	})
	return tenant{name: "owned", network: network, session: ManagementSession{Work: work, Tools: work, Runner: runner}, simulator: simulator, domains: domains, image: image}, root
}

func TestTenantRunAdmitsCNIAndDNSBeforeSmoke(t *testing.T) {
	for _, test := range []struct{ name, network, fail string }{
		{"Calico ready", "kind-calico", ""}, {"OVN ready", "ovn", ""}, {"upstream OVN ready", "upstream-ovn", ""},
		{"Calico failed", "kind-calico", "daemonset/calico-node"},
		{"CoreDNS failed", "kind-calico", "deployment/coredns"},
		{"node-local DNS failed", "kind-calico", "daemonset/node-local-dns"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, root := tenantRunFixture(t, test.network, test.fail, "")
			err := selected.run(t.Context(), 10*time.Second)
			want := []string{"kubeconfig", "nodes"}
			if test.network == "kind-calico" {
				want = append(want, "daemonset/calico-node")
			}
			want = append(want, "deployment/coredns", "daemonset/node-local-dns")
			if test.fail != "" {
				for i, stage := range want {
					if stage == test.fail {
						want = want[:i+1]
						break
					}
				}
				if CommandExitCode(err) != 7 {
					t.Fatalf("rollout failure lost: %v", err)
				}
				assertTenantWaitDiagnostic(t, err, selected.session.Work, test.fail, nvkrerr.CodePreflightFailed)
				for _, name := range []string{"workload.json", "tenant-result.json"} {
					if _, err := os.Stat(filepath.Join(selected.session.Work, name)); !os.IsNotExist(err) {
						t.Fatalf("failed rollout admitted %s: %v", name, err)
					}
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want = append(want, "workload-apply", "workload-complete", "workload-log", "workload-pod")
				if _, err := os.Stat(filepath.Join(selected.session.Work, "tenant-result.json")); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(filepath.Join(root, "events"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSpace(string(raw)), "\n"); !reflect.DeepEqual(got, want) {
				t.Fatalf("tenant.run stage order = %v; want %v", got, want)
			}
		})
	}
}

func TestTenantRunWaitCancellationAndBudgetKeepDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, block, stage string
		deadline           bool
	}{
		{"kubeconfig canceled", "kubeconfig", "tenant kubeconfig", false},
		{"nodes canceled", "nodes", "two Ready tenant nodes", false},
		{"guest observation canceled", "guest-bindings", "tenant kubeconfig", false},
		{"CNI canceled", "daemonset/calico-node", "daemonset/calico-node", false},
		{"CoreDNS canceled", "deployment/coredns", "deployment/coredns", false},
		{"node-local DNS canceled", "daemonset/node-local-dns", "daemonset/node-local-dns", false},
		{"kubeconfig budget", "kubeconfig", "tenant kubeconfig", true},
		{"nodes budget", "nodes", "two Ready tenant nodes", true},
		{"guest observation budget", "guest-bindings", "tenant kubeconfig", true},
		{"DNS budget", "daemonset/node-local-dns", "daemonset/node-local-dns", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, root := tenantRunFixture(t, "kind-calico", "", test.block)
			if test.block == "guest-bindings" {
				selected.session.Runner.Progress = &recordedProgress{}
				selected.session.Runner.Env = append(selected.session.Runner.Env, "SITE_KUBECONFIG=fixture-site-config")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			budget := 10 * time.Second
			var observed chan error
			if test.deadline {
				// The tenant budget covers every preceding owned subprocess too.
				// Give those fixtures headroom under race instrumentation/load,
				// while remaining below the outer guard and the blocked child's
				// 60-second sleep. Assertions below still require reaching exactly
				// the selected stage, an independent deadline and joined cleanup.
				budget = 8 * time.Second
			} else {
				observed = make(chan error, 1)
				go func() {
					ticker := time.NewTicker(5 * time.Millisecond)
					defer ticker.Stop()
					for {
						if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
							cancel()
							observed <- nil
							return
						} else if !os.IsNotExist(err) {
							cancel()
							observed <- err
							return
						}
						select {
						case <-ctx.Done():
							observed <- ctx.Err()
							return
						case <-ticker.C:
						}
					}
				}()
			}
			err := selected.run(ctx, budget)
			if observed != nil {
				cancel()
				if watchErr := <-observed; watchErr != nil {
					t.Fatalf("did not reach blocked stage: %v; run: %v", watchErr, err)
				}
			}
			cause := context.Canceled
			if test.deadline {
				cause = context.DeadlineExceeded
				if ctx.Err() != nil {
					t.Fatalf("tenant budget must expire independently of outer context: %v", ctx.Err())
				}
			}
			if !errors.Is(err, cause) || errors.Is(err, ErrUnjoined) {
				t.Fatalf("lost wait cause or did not join child: %v", err)
			}
			assertTenantWaitDiagnostic(t, err, selected.session.Work, test.stage, nvkrerr.CodeTimeout)
			if nvkrerr.ExitCode(localDemoError(err)) != nvkrerr.ExitCode(nvkrerr.New(nvkrerr.CodeTimeout, "fixture")) {
				t.Fatalf("public Run mapping hides tenant timeout: %v", localDemoError(err))
			}
			raw, readErr := os.ReadFile(filepath.Join(root, "events"))
			if readErr != nil || !strings.HasSuffix(string(raw), test.block+"\n") || strings.Contains(string(raw), "workload-") {
				t.Fatalf("blocked rollout admitted smoke or was not reached: %q %v", raw, readErr)
			}
			for _, name := range []string{"workload.json", "tenant-result.json"} {
				if _, err := os.Stat(filepath.Join(selected.session.Work, name)); !os.IsNotExist(err) {
					t.Fatalf("canceled wait admitted %s: %v", name, err)
				}
			}
		})
	}
}

func TestTenantWaitErrorPreservesCodesAndUnjoinedCauses(t *testing.T) {
	sentinel := errors.New("fixture failure")
	for _, test := range []struct {
		name  string
		cause error
		code  nvkrerr.Code
	}{
		{"ordinary", sentinel, nvkrerr.CodePreflightFailed},
		{"capability", nvkrerr.Wrap(nvkrerr.CodeEngineCapability, sentinel, "fixture capability"), nvkrerr.CodeEngineCapability},
		{"coded timeout", nvkrerr.Wrap(nvkrerr.CodeTimeout, sentinel, "fixture budget"), nvkrerr.CodeTimeout},
		{"canceled", errors.Join(ErrUnjoined, context.Canceled), nvkrerr.CodeTimeout},
		{"deadline", errors.Join(ErrUnjoined, context.DeadlineExceeded), nvkrerr.CodeTimeout},
		{"unjoined", ErrUnjoined, nvkrerr.CodePreflightFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := tenant{session: ManagementSession{Work: "retained-fixture-work"}}
			err := selected.waitError(test.cause, "fixture stage")
			assertTenantWaitDiagnostic(t, err, selected.session.Work, "fixture stage", test.code)
			public := localDemoError(err)
			publicCode := nvkrerr.CodePreflightFailed
			if test.code == nvkrerr.CodeTimeout {
				publicCode = nvkrerr.CodeTimeout
			}
			assertTenantWaitDiagnostic(t, public, selected.session.Work, "fixture stage", publicCode)
			if !errors.Is(public, test.cause) || errors.Is(public, ErrUnjoined) != errors.Is(test.cause, ErrUnjoined) {
				t.Fatalf("public wrapping lost original cause or cleanup retention: %v", public)
			}
		})
	}
	selected := tenant{}
	if err := selected.waitError(nil, "unused"); err != nil || localDemoError(nil) != nil {
		t.Fatal("success must remain success")
	}
}

func assertTenantWaitDiagnostic(t *testing.T, err error, work, stage string, code nvkrerr.Code) {
	t.Helper()
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != code || !strings.Contains(err.Error(), stage) || !strings.Contains(err.Error(), "private diagnostics: "+work) {
		t.Fatalf("missing coded stage and retained diagnostic location: %v", err)
	}
}

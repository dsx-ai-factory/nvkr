// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

// Use the actual tenant routines and owned subprocesses, without a replacement
// deployment engine or real guests. The existing native fixtures provide the
// node/cloud-init/DNS/workload answers; this helper owns only NVKE's wire API.
func nativeSessionFixture(t *testing.T, conformance bool, workers int) (nativeSiteAttempt, Runner, string) {
	t.Helper()
	selected, root := tenantRunFixture(t, "", "")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := selected.session.Runner
	// Each wire helper finishes synchronously. Avoid the race runtime's default
	// one-second successful-exit sleep on every repeated child invocation.
	runner.Env = append(runner.Env, "SESSION_TOOL="+executable, "NVKR_TEST_NATIVE_SESSION_TOOL=1", "CLOUDINIT_TOOL="+executable, "NVKR_TEST_CLOUD_INIT_TOOL=1", "GORACE=atexit_sleep_ms=0")
	cli := filepath.Join(runner.Work, "cli", installerCLIName)
	writeTenantFixture(t, cli, []byte("#!/bin/sh\nexec \"$SESSION_TOOL\" -test.run='^TestNativeSessionFixtureTool$' -- \"$@\"\n"), 0700)
	writeTenantFixture(t, filepath.Join(runner.Work, "site.kubeconfig"), []byte("private site fixture\n"), 0600)
	attempt := nativeCAPITenantFixture(t, GuestCIDR, "ovn")
	attempt.tenantNodes, err = selectedNativeTenantNodes(workers, conformance)
	if err != nil {
		t.Fatal(err)
	}
	workloadPods, err := json.Marshal(map[string]any{"items": []any{map[string]any{"status": map[string]any{"containerStatuses": []any{map[string]string{"imageID": attempt.assets.Images["workload"].Reference}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	writeTenantFixture(t, filepath.Join(root, "pods.json"), workloadPods, 0600)
	attempt.assets.CLI, attempt.assets.HostTools = cli, selected.session.Tools
	attempt.tenantTimeout = time.Minute
	if conformance {
		runner.Env = append(runner.Env, "SESSION_SUITE=1")
		run := filepath.Join(root, "conformance-run")
		capturedArtifactRunFixture(t, run, []lifecycle.DefinitionOutput{
			{Name: "busybox-image-arm64", Platform: "linux/arm64", Format: "oci", Components: []string{"image"}},
			{Name: "conformance-image-arm64", Platform: "linux/arm64", Format: "oci", Components: []string{"image"}},
		}, nil)
		nvkr := filepath.Join(root, "nvkr")
		writeTenantFixture(t, nvkr, []byte("#!/bin/sh\nexec \"$SESSION_TOOL\" -test.run='^TestNativeSessionConformanceTool$' -- \"$@\"\n"), 0700)
		attempt.conformance = &nativeConformanceConfig{repository: root, run: "conformance-run", nvkr: nvkr}
		var pods []map[string]any
		for _, node := range nativeTopologyNodes(attempt.tenantNodeCount()) {
			pods = append(pods, map[string]any{"spec": map[string]any{"nodeName": node.Metadata.Name, "containers": []map[string]string{{"name": "ovnkube-controller", "image": "registry.example.test/ovn:fixture"}}}})
		}
		if err := privateJSON(filepath.Join(root, "ovn-pods.json"), map[string]any{"items": pods}); err != nil {
			t.Fatal(err)
		}
		kubectl := filepath.Join(selected.session.Tools, "kubectl")
		if err := os.Rename(kubectl, kubectl+"-fixture"); err != nil {
			t.Fatal(err)
		}
		writeTenantFixture(t, kubectl, []byte("#!/bin/sh\ncase \"$*\" in *'get pods -n ovn-kubernetes '*) exec cat \"$FIXTURE/ovn-pods.json\";; esac\nexec \"$0-fixture\" \"$@\"\n"), 0700)
	}
	raw, err := json.Marshal(map[string]any{"items": nativeTopologyNodes(attempt.tenantNodeCount())})
	if err != nil {
		t.Fatal(err)
	}
	writeTenantFixture(t, filepath.Join(root, "nodes.json"), raw, 0600)
	return attempt, runner, root
}

func TestNativeSessionReusesSiteAndRetainsFailedSuiteUntilExplicitDeletion(t *testing.T) {
	for _, conformance := range []bool{false, true} {
		t.Run(fmt.Sprint(conformance), func(t *testing.T) {
			attempt, runner, root := nativeSessionFixture(t, conformance, 0)
			var escapedSite *NativeSite
			var escapedTenant *NativeTenant
			ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
			defer cancel()
			err := withNativeSiteScope(ctx, &attempt, runner, runner.Work, runner.Work, func(ctx context.Context, site *NativeSite) error {
				escapedSite = site
				siteConfig, err := site.Kubeconfig(ctx)
				if err != nil || siteConfig != filepath.Join(runner.Work, "site.kubeconfig") {
					t.Fatalf("site access: %q %v", siteConfig, err)
				}
				for _, name := range []string{"first", "second"} {
					tenant, err := site.CreateTenant(ctx, name)
					if err != nil {
						t.Fatal(err)
					}
					escapedTenant = tenant
					copied := *tenant
					config, err := copied.Kubeconfig(ctx)
					if err != nil || filepath.Dir(config) == runner.Work {
						t.Fatalf("tenant diagnostic/access directory not independent: %q %v", config, err)
					}
					if _, err := site.CreateTenant(ctx, "must-not-exist"); nvkrerr.ExitCode(err) != 2 {
						t.Fatalf("concurrent tenant admitted: %v", err)
					}
					// Ordinary mode rejects missing suite selection. Conformance
					// mode reaches a nonzero fixture validator with failed JUnit.
					// Both retain this same tenant for an explicit next action.
					for _, suite := range []string{"suite-1", "suite-2"} {
						out := filepath.Join(root, name+"-"+suite)
						err := tenant.RunConformance(ctx, out)
						if !conformance && nvkrerr.ExitCode(err) != 2 || conformance && CommandExitCode(err) != 22 {
							t.Fatalf("suite admission or nonzero validator exit lost: %v", err)
						}
						if conformance {
							junit, readErr := os.ReadFile(filepath.Join(out, "conformance", "original.xml"))
							capture, captureErr := os.ReadFile(filepath.Join(out, "conformance-output.json"))
							if readErr != nil || captureErr != nil || !strings.Contains(string(junit), `failures="1"`) || !strings.Contains(string(capture), `"process.exitCode"`) {
								t.Fatalf("original failed JUnit/capture lost: %v %v", readErr, captureErr)
							}
						}
						if _, err := os.Stat(filepath.Join(root, "active-request.json")); err != nil {
							t.Fatal("suite failure deleted the tenant")
						}
					}
					if name == "first" {
						writeTenantFixture(t, filepath.Join(root, "delete-fails"), []byte("failure fixture"), 0600)
						if err := tenant.Delete(ctx); err == nil || errors.Is(err, ErrUnjoined) {
							t.Fatalf("natural deletion failure/join lost: %v", err)
						}
						if _, err := copied.Kubeconfig(ctx); err != nil {
							t.Fatalf("failed deletion invalidated live tenant: %v", err)
						}
						if err := os.Remove(filepath.Join(root, "delete-fails")); err != nil {
							t.Fatal(err)
						}
					}
					if err := tenant.Delete(ctx); err != nil {
						t.Fatal(err)
					}
					if _, err := copied.Kubeconfig(ctx); nvkrerr.ExitCode(err) != 2 {
						t.Fatalf("deleted tenant copy remains usable: %v", err)
					}
					if _, err := site.Kubeconfig(ctx); err != nil {
						t.Fatalf("tenant deletion stopped site: %v", err)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range []func() error{
				func() error { _, err := escapedSite.Kubeconfig(t.Context()); return err },
				func() error { _, err := escapedSite.CreateTenant(t.Context(), "escaped"); return err },
				func() error { return escapedTenant.Delete(t.Context()) },
			} {
				if err := operation(); nvkrerr.ExitCode(err) != 2 {
					t.Fatalf("escaped handle remained usable: %v", err)
				}
			}
			events, err := os.ReadFile(filepath.Join(root, "session-events"))
			if err != nil || string(events) != "create first\ndelete first\ncreate second\ndelete second\n" {
				t.Fatalf("site/tenant operation order: %q %v", events, err)
			}
		})
	}
}

func TestNativeOrdinaryDemoChecksRequestedWorkersWithoutSuite(t *testing.T) {
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			attempt, runner, root := nativeSessionFixture(t, false, workers)
			// The ordinary CLI callback must complete the same tenant gates without
			// selecting suite tools or creating any conformance output.
			runner.Progress = nil
			err := withNativeSiteScope(t.Context(), &attempt, runner, runner.Work, runner.Work, func(ctx context.Context, site *NativeSite) error {
				if err := runNativeDemo(ctx, site); err != nil {
					return err
				}
				raw, err := os.ReadFile(filepath.Join(root, "active-request.json"))
				if err != nil {
					return err
				}
				var request TenantRequest
				if err := json.Unmarshal(raw, &request); err != nil {
					return err
				}
				if request.ControlPlane.Count != 1 || len(request.NodePools) != 1 || request.NodePools[0].Count != workers || len(attempt.nodes) != workers+1 || nativeGuestLimit(&attempt) != workers+2 || attempt.conformance != nil {
					t.Fatalf("ordinary request lost selected topology: %+v", request)
				}
				for _, name := range []string{"native-result.json", fmt.Sprintf("cloud-init-status-%d-stdout.json", workers)} {
					if _, err := os.Stat(filepath.Join(runner.Work, name)); err != nil {
						t.Fatalf("ordinary acceptance omitted %s: %v", name, err)
					}
				}
				for _, name := range []string{"conformance", "conformance-output.json"} {
					if _, err := os.Stat(filepath.Join(runner.Work, name)); !os.IsNotExist(err) {
						t.Fatalf("ordinary callback selected suite work: %s %v", name, err)
					}
				}
				return (&NativeTenant{state: site.scope.active}).Delete(ctx)
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeSessionExitCancelsAndJoinsInFlightCommand(t *testing.T) {
	root := privateTestDir(t)
	command := filepath.Join(root, "block")
	started := filepath.Join(root, "started")
	writeTenantFixture(t, command, []byte("#!/bin/sh\n: > \"$STARTED\"\nexec sleep 60\n"), 0700)
	runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "STARTED=" + started}}
	finished := make(chan error, 1)
	process := make(chan *OwnedProcess, 1)
	err := withNativeSiteScope(t.Context(), &nativeSiteAttempt{}, runner, root, root, func(_ context.Context, site *NativeSite) error {
		go func() {
			// Deliberately use an independent context: scope exit must still
			// cancel the owned child and wait for its Runner to join.
			finished <- site.operate(t.Context(), nil, func(ctx context.Context) error {
				owner, err := runner.start(ctx, []string{command}, "session-cancel", false, false)
				if err != nil {
					return err
				}
				process <- owner
				return errors.Join(owner.waitOwned(ctx), ctx.Err())
			})
		}()
		deadline, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		for {
			if _, err := os.Stat(started); err == nil {
				return nil
			}
			if err := pause(deadline, 10*time.Millisecond); err != nil {
				return err
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := <-process
	if exited, err := owner.Exited(); !exited || errors.Is(err, ErrUnjoined) {
		t.Fatalf("scope returned before the actual child/process-group joined: %v", err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnjoined) || nvkrerr.ExitCode(err) != 31 {
			t.Fatalf("scope cancellation or join lost: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scope exit did not join its in-flight command")
	}
}

func TestNativeSessionRejectsMissingOwnershipAndNilCallback(t *testing.T) {
	for _, operation := range []func() error{
		func() error { return WithNativeSite(t.Context(), Config{}, nil) },
		func() error { _, err := (&NativeSite{}).Kubeconfig(t.Context()); return err },
		func() error { _, err := (&NativeTenant{}).Kubeconfig(t.Context()); return err },
		func() error { return (*NativeTenant)(nil).Delete(t.Context()) },
	} {
		if err := operation(); nvkrerr.ExitCode(err) != 2 {
			t.Fatalf("unissued handle or callback admitted: %v", err)
		}
	}
}

func TestNativeSessionUnjoinedOutcomeCannotBeSwallowed(t *testing.T) {
	for _, mode := range []string{"returned", "swallowed", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			attempt, runner, root := nativeSessionFixture(t, false, 0)
			finished := make(chan error, 1)
			original := errors.New("fixture child join failed")
			err := withNativeSiteScope(t.Context(), &attempt, runner, runner.Work, runner.Work, func(ctx context.Context, site *NativeSite) error {
				tenant, err := site.CreateTenant(ctx, "owned")
				if err != nil {
					t.Fatal(err)
				}
				if mode == "concurrent" {
					entered := make(chan struct{})
					go func() {
						finished <- site.operate(t.Context(), nil, func(ctx context.Context) error {
							close(entered)
							<-ctx.Done()
							return errors.Join(original, ErrUnjoined, ctx.Err())
						})
					}()
					<-entered
					return nil
				}
				operationErr := site.operate(ctx, nil, func(context.Context) error { return errors.Join(original, ErrUnjoined) })
				if mode == "returned" {
					return operationErr
				}
				if err := tenant.Delete(ctx); !errors.Is(err, ErrUnjoined) || !errors.Is(err, original) {
					t.Fatalf("delete admitted after swallowed unjoined work: %v", err)
				}
				if _, err := site.CreateTenant(ctx, "must-not-exist"); !errors.Is(err, ErrUnjoined) {
					t.Fatalf("new tenant admitted after unjoined work: %v", err)
				}
				return nil
			})
			if !errors.Is(err, ErrUnjoined) || !errors.Is(err, original) {
				t.Fatalf("scope lost sticky unjoined outcome: %v", err)
			}
			if strings.Count(err.Error(), original.Error()) != 1 {
				t.Fatalf("scope reported the same child failure repeatedly: %v", err)
			}
			if mode == "concurrent" {
				if err := <-finished; !errors.Is(err, ErrUnjoined) {
					t.Fatalf("concurrent outcome lost: %v", err)
				}
			}
			events, err := os.ReadFile(filepath.Join(root, "session-events"))
			if err != nil || string(events) != "create owned\n" {
				t.Fatalf("unjoined scope deleted or restarted resources: %q %v", events, err)
			}
		})
	}
}

func TestNativeSessionQueuedOperationHonorsCallerCancellation(t *testing.T) {
	err := withNativeSiteScope(t.Context(), &nativeSiteAttempt{}, Runner{}, "", "", func(ctx context.Context, site *NativeSite) error {
		entered, release := make(chan struct{}), make(chan struct{})
		finished := make(chan error, 1)
		go func() {
			finished <- site.operate(ctx, nil, func(context.Context) error {
				close(entered)
				<-release
				return nil
			})
		}()
		<-entered
		defer func() {
			close(release)
			if err := <-finished; err != nil {
				t.Error(err)
			}
		}()
		caller, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := site.Kubeconfig(caller); !errors.Is(err, context.Canceled) || nvkrerr.ExitCode(err) != 31 {
			t.Fatalf("queued operation ignored cancellation: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeSessionUnjoinedOwnershipSurvivesPanicAndGoexit(t *testing.T) {
	for _, mode := range []string{"panic", "Goexit"} {
		t.Run(mode, func(t *testing.T) {
			attempt, runner, root := nativeSessionFixture(t, false, 0)
			bootstrapDeleted := filepath.Join(root, "bootstrap-deleted")
			writeTenantFixture(t, filepath.Join(attempt.assets.HostTools, "kind"), []byte("#!/bin/sh\n: > "+operatorQuote(bootstrapDeleted)+"\n"), 0o700)
			attempt.bootstrapRunner, attempt.bootstrapName = &runner, "owned-bootstrap"
			unwound, operationFinished := make(chan struct{}), make(chan error, 1)
			go func() {
				defer close(unwound)
				defer func() {
					if caught := recover(); caught != nil && caught != "fixture panic" {
						t.Errorf("unexpected panic: %v", caught)
					}
				}()
				_ = withNativeSiteScope(t.Context(), &attempt, runner, runner.Work, runner.Work, func(ctx context.Context, site *NativeSite) error {
					if _, err := site.CreateTenant(ctx, "owned"); err != nil {
						t.Error(err)
						operationFinished <- err
						return err
					}
					entered := make(chan struct{})
					go func() {
						operationFinished <- site.operate(t.Context(), nil, func(ctx context.Context) error {
							close(entered)
							<-ctx.Done()
							return errors.Join(ErrUnjoined, ctx.Err())
						})
					}()
					<-entered
					if mode == "panic" {
						panic("fixture panic")
					}
					runtime.Goexit()
					return nil
				})
			}()
			<-unwound
			if err := <-operationFinished; !errors.Is(err, ErrUnjoined) {
				t.Fatalf("operation lost unjoined outcome: %v", err)
			}
			// The outer guest owner cannot rely on a return value during panic
			// or Goexit. Its own sticky state must forbid destructive teardown.
			cleanup, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			if !errors.Is(attempt.unjoinedWork, ErrUnjoined) || !errors.Is(attempt.close(cleanup), ErrUnjoined) {
				t.Fatal("unwinding lost the owner's unsafe-join state")
			}
			if _, err := os.Stat(bootstrapDeleted); !os.IsNotExist(err) || attempt.bootstrapRunner == nil {
				t.Fatal("unjoined tenant work released its dependent bootstrap")
			}
			events, err := os.ReadFile(filepath.Join(root, "session-events"))
			if err != nil || string(events) != "create owned\n" {
				t.Fatalf("unwinding deleted resources despite unjoined work: %q %v", events, err)
			}
		})
	}
}

// Optional row transitions for the existing owned NVKE wire fixture. Reads
// before creation do not consume them; the final answer remains in force.
type nativeTenantRowObservation struct {
	Provisioning, Provisioned           TenantConditionStatus
	Missing, ChangedID, ChangedProvider bool
	ExitCode                            int
}

func TestNativeSessionFixtureTool(t *testing.T) {
	if os.Getenv("NVKR_TEST_NATIVE_SESSION_TOOL") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 3 {
		os.Exit(42)
	}
	args = args[1:]
	root := os.Getenv("FIXTURE")
	state := filepath.Join(root, "active-request.json")
	event := func(text string) {
		f, err := os.OpenFile(filepath.Join(root, "session-events"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(43)
		}
		_, err = fmt.Fprintln(f, text)
		if errors.Join(err, f.Close()) != nil {
			os.Exit(44)
		}
	}
	var request TenantRequest
	raw, readErr := os.ReadFile(state)
	if readErr == nil {
		if err := json.Unmarshal(raw, &request); err != nil {
			os.Exit(45)
		}
	}
	switch strings.Join(args[:2], " ") {
	case "create cluster":
		raw, err := os.ReadFile(args[3])
		if err != nil || json.Unmarshal(raw, &request) != nil || request.Name == "" || readErr == nil {
			os.Exit(46)
		}
		if err := os.WriteFile(state, raw, 0600); err != nil {
			os.Exit(47)
		}
		// A newly created cluster has no previous namespace/Pod state. The
		// reused cloud-init fixture keys these records by Pod name only.
		for _, name := range []string{"deleted", "status-0.json", "status-1.json", "status-2.json"} {
			if err := os.Remove(filepath.Join(root, "cloud-init-tool-"+name)); err != nil && !os.IsNotExist(err) {
				os.Exit(53)
			}
		}
		event("create " + request.Name)
	case "get clusters":
		rows := []tenantRow{}
		if readErr == nil {
			row := tenantRow{Name: request.Name, ID: "owned-" + request.Name, Infra: request.Infra, ControlPlane: &request.ControlPlane}
			var observed nativeTenantRowObservation
			if raw, err := os.ReadFile(filepath.Join(root, "tenant-row-observations.json")); err == nil {
				var observations []nativeTenantRowObservation
				if json.Unmarshal(raw, &observations) != nil || len(observations) == 0 {
					os.Exit(54)
				}
				var count int
				if raw, err := os.ReadFile(filepath.Join(root, "tenant-row-count")); err == nil && json.Unmarshal(raw, &count) != nil {
					os.Exit(55)
				}
				observed = observations[min(count, len(observations)-1)]
				if os.WriteFile(filepath.Join(root, "tenant-row-count"), fmt.Appendf(nil, "%d", count+1), 0600) != nil {
					os.Exit(56)
				}
			}
			if observed.ExitCode != 0 {
				os.Exit(observed.ExitCode)
			}
			row.Conditions.Provisioning.Status, row.Conditions.Provisioned.Status = observed.Provisioning, observed.Provisioned
			if observed.ChangedID {
				row.ID = "replacement-id"
			}
			if observed.ChangedProvider {
				row.NICo.Org = "replacement-provider"
			}
			if !observed.Missing {
				rows = append(rows, row)
			}
		}
		if json.NewEncoder(os.Stdout).Encode(rows) != nil {
			os.Exit(48)
		}
	case "get nodepools":
		if readErr != nil || json.NewEncoder(os.Stdout).Encode([]map[string]any{{"id": "owned-worker", "name": "worker", "clusterID": "owned-" + request.Name, "infra": request.NodePools[0].Infra, "desiredNodeCount": request.NodePools[0].Count}}) != nil {
			os.Exit(49)
		}
	case "get kubeconfig":
		if os.WriteFile(args[4], []byte("current-context: fixture\n"), 0600) != nil {
			os.Exit(50)
		}
	case "delete cluster":
		if _, err := os.Stat(filepath.Join(root, "delete-fails")); err == nil {
			os.Exit(7)
		}
		if readErr != nil || args[2] != request.Name || os.Remove(state) != nil {
			os.Exit(51)
		}
		event("delete " + request.Name)
	default:
		os.Exit(52)
	}
	os.Exit(0)
}

// The actual demo conformance adapter invokes this owned CLI fixture. It does
// not authenticate real artifacts or run Hydrophone; those remain the NVKR
// conformance owner's separate contracts. A failed external validation process
// must preserve original output and never trigger deletion in the session API.
func TestNativeSessionConformanceTool(t *testing.T) {
	if os.Getenv("SESSION_SUITE") != "1" {
		return
	}
	var reports string
	for i, arg := range os.Args {
		if arg == "--workspace" && i+1 < len(os.Args) {
			reports = os.Args[i+1]
		}
	}
	if reports == "" {
		os.Exit(40)
	}
	raw := []byte(`<testsuite tests="1" failures="1"><testcase name="fixture"><failure message="fixture failure"/></testcase></testsuite>`)
	path := filepath.Join(reports, "original.xml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		os.Exit(41)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	output := testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete,
		Producer:    testresults.ProducerIdentity{Name: "fixture-validator", Revision: testresults.Metadata{Status: "unknown"}},
		Suite:       testresults.SuiteSelection{Name: "kubernetes-conformance", Selection: []string{"[Conformance]"}, Revision: testresults.Metadata{Status: "unknown"}},
		Reports:     []testresults.ReportReference{{Format: testresults.FormatJUnit, Profile: "ginkgo", Reference: path, Digest: digest, ByteLength: int64(len(raw))}},
		Environment: []testresults.EnvironmentFact{{Scope: "harness", Name: "process.exitCode", Phase: "after", Metadata: testresults.Metadata{Status: "observed", Value: "1"}}}}
	if err := json.NewEncoder(os.Stdout).Encode([]testresults.Output{output}); err != nil {
		os.Exit(42)
	}
	os.Exit(22)
}

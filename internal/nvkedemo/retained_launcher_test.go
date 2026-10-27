// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type retainedLauncherOutput struct {
	bytes.Buffer
	cancel context.CancelFunc
	auth   int
}

func (out *retainedLauncherOutput) Write(raw []byte) (int, error) {
	if out.cancel != nil && bytes.Contains(raw, []byte("fixture service running")) {
		out.cancel()
	}
	return out.Buffer.Write(raw)
}

func (out *retainedLauncherOutput) WriteString(text string) (int, error) {
	return out.Write([]byte(text))
}

func (out *retainedLauncherOutput) SudoAuthentication() func(context.Context) error {
	return func(context.Context) error { out.auth++; return nil }
}

// This child exercises the real launcher/control/peer/setsid boundary with an
// owned sleep process. It never starts NVKE, a VM, a bridge or Kubernetes.
func TestRetainedNativeLauncherChild(t *testing.T) {
	root := os.Getenv("NVKR_TEST_RETAINED_LAUNCHER")
	if root == "" {
		return
	}
	launch, err := readRetainedJSON[retainedLaunch](t.Context(), filepath.Join(root, retainedRequestFile))
	if err != nil {
		t.Fatal(err)
	}
	if launch.CachedSudo != (launch.Config.Network != "unattended") {
		t.Fatal("foreground authentication selection was lost")
	}
	if launch.Config.Network == "unpublished" {
		time.Sleep(time.Minute)
		return
	}
	if launch.Config.Network == "production-owner" {
		if err := serveRetained(t.Context(), root, launch, func(ctx context.Context, cfg Config) error {
			nodes, err := selectedNativeTenantNodes(cfg.TenantWorkers, cfg.Conformance)
			if err != nil || cfg.TenantWorkers != 2 || cfg.Conformance || nodes != 3 {
				return errors.New("retained child lost ordinary two-worker selection")
			}
			if deadline, bounded := ctx.Deadline(); !bounded || !deadline.Equal(launch.WorkDeadline) {
				return errors.New("retained owner reset the caller work deadline")
			}
			return retainedLauncherProductionWork(ctx, root, cfg)
		}); err != nil {
			t.Fatal("production retained worker/control did not join", err)
		}
		return
	}
	operation, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	defer cancel()
	owner, err := newRetainedRuntime(context.WithoutCancel(t.Context()), cancel)
	if err != nil {
		t.Fatal(err)
	}
	operation = context.WithValue(operation, retainedRuntimeKey{}, owner)
	progress := &retainedProgress{}
	control, err := newRetainedControlWithProgress(t.Context(), owner, filepath.Join(launch.Config.Out, "runtime"), root, func() error {
		if launch.Config.Network == "detach-refused" {
			return errors.New("fixture cannot acknowledge detachment")
		}
		return detachRetainedProcess()
	}, progress)
	if err != nil {
		t.Fatal(err)
	}
	description := control.description
	if launch.Config.Network == "wrong-pid" {
		description.PID++
	}
	if err := privateJSON(filepath.Join(root, retainedDescriptionFile), description); err != nil {
		t.Fatal(err)
	}
	workDone := make(chan error, 1)
	go func() {
		select {
		case <-control.start:
		case <-owner.cleanup:
			owner.recordTeardownReturn(nil)
			workDone <- control.recordJoined(owner.lifetime, nil)
			return
		}
		service, err := (Runner{Work: root, Env: NativeEnvironment()}).Start(operation, []string{"/bin/sleep", "60"}, "launcher-fixture-service")
		if err == nil {
			err = privateWrite(filepath.Join(root, "fixture-started"), []byte("started\n"))
		}
		if err == nil {
			_, _ = progress.Write([]byte("fixture service running\n"))
			switch launch.Config.Network {
			case "partial":
				err = nvkrerr.New(nvkrerr.CodeVerifyFailed, "synthetic suite failure")
				_ = owner.retainFailure(err)
			case "cancel":
				<-operation.Done()
				err = operation.Err()
			default:
				if launch.Config.Network == "ready-failure" {
					_ = owner.retainFailure(nvkrerr.New(nvkrerr.CodeVerifyFailed, "synthetic recorded failure"))
				}
				err = owner.markReady(operation, OperatorAccess{kubectl: "/fixture/kubectl", site: "/fixture/site.kubeconfig", tenant: "/fixture/tenant.kubeconfig"})
			}
		}
		if err != nil {
			_ = owner.retainFailure(err)
		}
		err = errors.Join(err, owner.hold())
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer stop()
		var cleanupErr error
		if service != nil {
			cleanupErr = service.Stop(cleanup)
		}
		cleanupErr = errors.Join(cleanupErr, privateWrite(filepath.Join(root, "fixture-joined"), []byte("joined\n")))
		recordRetainedCleanup(operation, cleanupErr)
		owner.recordTeardownReturn(err)
		workDone <- control.recordJoined(cleanup, cleanupErr)
	}()
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 15*time.Second)
		defer stop()
		_ = owner.requestCleanup(cleanup) // Only this isolated fixture's teardown.
		select {
		case err := <-workDone:
			if err != nil {
				t.Error(err)
			}
		case <-cleanup.Done():
			t.Error("fixture service did not join")
		}
		if err := control.close(cleanup); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-control.finish:
	case <-time.After(25 * time.Second):
		t.Error("fixture was not explicitly cleaned up")
	}
}

func retainedLauncherProductionWork(ctx context.Context, root string, cfg Config) (result error) {
	service, err := (Runner{Work: root, Env: NativeEnvironment()}).Start(ctx, []string{"/bin/sleep", "60"}, "launcher-production-owner-fixture")
	if err != nil {
		return err
	}
	defer func() {
		if err := waitRetainedCleanup(ctx, result); err != nil {
			result = errors.Join(result, err)
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		cleanupErr := service.Stop(cleanup)
		if cleanupErr == nil {
			cleanupErr = privateWrite(filepath.Join(root, "fixture-joined"), []byte("joined\n"))
		}
		recordRetainedCleanup(ctx, cleanupErr)
		result = errors.Join(result, cleanupErr)
	}()
	if err := privateWrite(filepath.Join(root, "fixture-started"), []byte("started\n")); err != nil {
		return err
	}
	_, _ = io.WriteString(cfg.Progress, "fixture service running\n")
	return runtimeRetention(ctx).markReady(ctx, OperatorAccess{kubectl: "/fixture/kubectl", site: "/fixture/site.kubeconfig", tenant: "/fixture/tenant.kubeconfig"})
}

func TestRetainedNativeLauncherReturnsOnlyAfterDetachedReady(t *testing.T) {
	if err := checkRetainedPeerCapability(t.Context()); err != nil {
		t.Skipf("native connected-peer capability unavailable: %v", err)
	}
	for _, test := range []struct {
		name string
		code nvkrerr.Code
		auth int
	}{
		{"ready", "", 1},
		{"unattended", "", 0},
		{"production-owner", "", 1},
		{"partial", nvkrerr.CodeVerifyFailed, 1},
		{"ready-failure", nvkrerr.CodeVerifyFailed, 1},
		{"cancel", nvkrerr.CodeTimeout, 1},
		{"detach-refused", nvkrerr.CodePreflightFailed, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := privateTestDir(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			out := &retainedLauncherOutput{}
			if test.name == "cancel" {
				out.cancel = cancel
			}
			cfg := Config{Out: filepath.Join(root, "deployment"), DemoExecutable: os.Args[0], Network: test.name, Progress: out}
			if test.auth == 0 {
				cfg.Progress = &out.Buffer
			}
			if test.name == "production-owner" {
				cfg.TenantWorkers = 2
			}
			var selector string
			err := launchRetainedNativeWithCommand(ctx, cfg, func(executable, session string, environment []string) *exec.Cmd {
				selector = session
				command := exec.Command(executable, "-test.run=^TestRetainedNativeLauncherChild$", "-test.count=1", "-test.timeout=45s")
				command.Env = append(environment, "NVKR_TEST_RETAINED_LAUNCHER="+session)
				return command
			})
			if selector == "" {
				t.Fatalf("launcher did not create the fixture child: %v", err)
			}
			cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 20*time.Second)
			defer stop()
			defer func() {
				if cleanupErr := CleanupRetained(cleanup, selector, io.Discard); cleanupErr != nil {
					t.Error("actual retained child/fixture did not join", cleanupErr)
				}
			}()
			if test.code == "" {
				if err != nil || !strings.Contains(out.String(), "PASS (retained):") || !strings.Contains(out.String(), "TENANT_CONFIG=") {
					t.Fatal("Ready owner did not return complete access", err, out.String())
				}
			} else {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != test.code || !errors.Is(err, ErrRetainedControls) || strings.Contains(out.String(), "PASS (retained):") {
					t.Fatal("non-Ready owner claimed success or lost ownership classification", err, out.String())
				}
			}
			if out.auth != test.auth || strings.Count(out.String(), "Retained session:") != 1 || !strings.Contains(out.String(), " status --session ") || !strings.Contains(out.String(), " cleanup --session ") {
				t.Fatal("authentication or original session instructions lost", out.auth, out.String())
			}
			if _, err := os.Stat(filepath.Join(selector, "fixture-started")); err != nil {
				t.Fatal("actual service never started", err)
			}
			if _, err := os.Stat(filepath.Join(selector, "fixture-joined")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("frontend result unexpectedly stopped the retained service", err)
			}
			session, err := openRetainedSession(cleanup, selector)
			if err != nil {
				t.Fatal("frontend did not preserve the actual owner", err)
			}
			state, err := session.request(cleanup, http.MethodGet, "/state")
			if err != nil || state.Joined || state.State == retainedCleanupRequested || test.name != "detach-refused" && !state.Detached {
				t.Fatal("frontend exit/cancellation requested teardown or invented handoff", state, err)
			}
		})
	}
}

func TestRetainedNativeLauncherRejectsUnstartedChildAndReapsIt(t *testing.T) {
	if err := checkRetainedPeerCapability(t.Context()); err != nil {
		t.Skipf("native connected-peer capability unavailable: %v", err)
	}
	for _, tc := range []struct {
		name string
		code nvkrerr.Code
	}{{"wrong-pid", nvkrerr.CodePreflightFailed}, {"unpublished", nvkrerr.CodeTimeout}} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTestDir(t)
			var command *exec.Cmd
			var selector string
			out := &retainedLauncherOutput{}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := launchRetainedNativeWithCommand(ctx, Config{Out: filepath.Join(root, "deployment"), DemoExecutable: os.Args[0], Network: tc.name, Progress: out}, func(executable, session string, environment []string) *exec.Cmd {
				selector = session
				command = exec.Command(executable, "-test.run=^TestRetainedNativeLauncherChild$", "-test.count=1", "-test.timeout=45s")
				command.Env = append(environment, "NVKR_TEST_RETAINED_LAUNCHER="+session)
				return command
			})
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != tc.code || errors.Is(err, ErrRetainedControls) || command == nil || command.ProcessState == nil {
				t.Fatal("unstarted child was adopted or not reaped", err)
			}
			if _, err := os.Stat(filepath.Join(selector, "fixture-started")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unbound owner admitted service work", err)
			}
			if strings.Contains(out.String(), "Retained session:") || strings.Contains(out.String(), " --session ") {
				t.Fatal("unstarted child exposed unusable session commands", out.String())
			}
		})
	}
}

type retainedInstructionOutput struct {
	bytes.Buffer
	instructions RetainedInstructions
	calls        int
}

func (out *retainedInstructionOutput) WriteRetainedInstructions(instructions RetainedInstructions) error {
	out.instructions = instructions
	out.calls++
	return nil
}

func TestRetainedNativeInstructionsKeepCompleteCommandsOutsideActivity(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed or unknown", true: "Ready"}[ready], func(t *testing.T) {
			var access OperatorAccess
			if ready {
				access = OperatorAccess{kubectl: "/private/operator's directory/kubectl", site: "/private/site.kubeconfig", tenant: "/private/tenant.kubeconfig"}
			}
			var typed retainedInstructionOutput
			var plain bytes.Buffer
			for _, out := range []io.Writer{&typed, &plain} {
				if err := writeRetainedNativeInstructions(out, "/private/nvke-demo", "/private/session", access); err != nil {
					t.Fatal(err)
				}
			}
			if typed.calls != 1 || typed.Len() != 0 || typed.instructions.Text() != plain.String() || strings.Count(plain.String(), "Retained session:") != 1 {
				t.Fatal("retained commands were duplicated, truncated or routed into activity")
			}
			for _, command := range []string{"'/private/nvke-demo' status --session '/private/session'", "'/private/nvke-demo' cleanup --session '/private/session'"} {
				if !strings.Contains(plain.String(), command) {
					t.Fatal("complete session command missing", plain.String())
				}
			}
			if strings.Contains(plain.String(), "TENANT_CONFIG=") != ready || (RetainedInstructions{}).Text() != "" {
				t.Fatal("unobserved access or an unissued presentation gained instructions")
			}
		})
	}
}

func TestRetainedNativeLauncherAdmissionCreatesNoPartialOutput(t *testing.T) {
	for _, test := range []struct {
		name string
		ctx  context.Context
		cfg  Config
		code nvkrerr.Code
	}{
		{"nil context", nil, Config{}, nvkrerr.CodeUsage},
		{"explicit cleanup", t.Context(), Config{Cleanup: true}, nvkrerr.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := privateTestDir(t)
			test.cfg.Out = filepath.Join(root, "deployment")
			err := launchRetainedNative(test.ctx, test.cfg)
			var coded *nvkrerr.Error
			entries, readErr := os.ReadDir(root)
			if !errors.As(err, &coded) || coded.Code != test.code || readErr != nil || len(entries) != 0 {
				t.Fatal("admission produced partial launch state", err, readErr, entries)
			}
		})
	}
}

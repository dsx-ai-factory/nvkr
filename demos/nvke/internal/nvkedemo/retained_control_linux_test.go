// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"golang.org/x/sys/unix"
)

// These fixtures use actual processes, HTTP, connected peer handles and the
// kernel session change. The synthetic access projection is NOT NVKE readiness,
// a root restoration or a successful customer E2E.
func TestRetainedBackgroundFixture(t *testing.T) {
	root := os.Getenv("NVKE_TEST_RETAINED_BACKGROUND")
	if root == "" {
		return
	}
	owner, work, _ := retainedTestOwner(t)
	sleep, err := (Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin"}}).Start(work, []string{"/bin/sleep", "60"}, "control-fixture-service")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer cancel()
		if err := sleep.Stop(cleanup); err != nil {
			t.Error(err)
		}
	}()
	disk := filepath.Join(root, "fixture.qcow2")
	raw := []byte("exact retained fixture bytes")
	if err := os.WriteFile(disk, raw, 0o444); err != nil {
		t.Fatal(err)
	}
	httpService, err := startDiskService(work, disk, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpStopped := false
	defer func() {
		if !httpStopped {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
			defer cancel()
			if err := httpService.Stop(cleanup); err != nil {
				t.Error(err)
			}
		}
	}()
	control, err := newRetainedControl(t.Context(), owner, root, detachRetainedProcess)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if err := control.close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	if err := owner.markReady(work, OperatorAccess{kubectl: "/controlled-tool", site: "/controlled-site", tenant: "/controlled-tenant"}); err != nil {
		t.Fatal(err)
	}
	if err := privateJSON(filepath.Join(root, "fixture-control.json"), struct {
		Control retainedControlDescription
		HTTP    string
	}{control.description, httpService.url}); err != nil {
		t.Fatal(err)
	}
	// Only this isolated test's deadline authorizes its known fixture teardown.
	// The production control has no time-based or viewer-loss cleanup operation.
	select {
	case <-owner.cleanup:
	case <-time.After(25 * time.Second):
		t.Error("fixture cleanup was not explicitly requested before test deadline")
		if err := owner.requestCleanup(context.WithoutCancel(t.Context())); err != nil {
			t.Fatal(err)
		}
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
	defer cancel()
	if err := sleep.Stop(cleanup); err != nil {
		t.Fatal(err)
	}
	if err := httpService.Stop(cleanup); err != nil {
		t.Fatal(err)
	}
	httpStopped = true
	owner.recordTeardownReturn(nil) // This fixture's actual service stack joined.
	if err := control.recordJoined(cleanup, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-control.finish:
	case <-time.After(10 * time.Second):
		t.Error("fixture control finish was not requested")
	}
}

func TestRetainedCleanupReturnsFailedOutcomeBeforeBudgetAndRelaysProgress(t *testing.T) {
	owner, ctx, cancelWork := retainedTestOwner(t)
	root := privateTestDir(t)
	progress := &retainedProgress{}
	_, _ = progress.Write([]byte("pre-invocation deployment history\n"))
	owner.cancelOperations = func() {
		// The synchronous observation is already in the /cleanup acknowledgment.
		// Its caller must skip old deployment history without dropping this frame.
		_, _ = progress.Write([]byte("public owner cleanup observation\n"))
		cancelWork()
	}
	control, err := newRetainedControlWithProgress(t.Context(), owner, root, root, func() error { return nil }, progress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
		defer cancel()
		if err := control.close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	if err := privateJSON(filepath.Join(root, retainedDescriptionFile), control.description); err != nil {
		t.Fatal(err)
	}
	recordRetainedCleanup(ctx, errors.Join(ErrUnjoined, errors.New("private-cleanup-cause")))
	owner.recordTeardownReturn(nil)
	bounded, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var output strings.Builder
	err = CleanupRetained(bounded, root, &output)
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed || !errors.Is(err, ErrRetainedControls) || bounded.Err() != nil {
		t.Fatal("terminal teardown waited out its budget", err)
	}
	if !strings.Contains(output.String(), "public owner cleanup observation") || strings.Contains(output.String(), "pre-invocation deployment history") || strings.Contains(err.Error()+output.String(), "private-cleanup-cause") {
		t.Fatal("cleanup lost early activity, replayed deployment history or exposed cause", err)
	}
	select {
	case <-control.finish:
		t.Fatal("unjoined producer authorized owner exit")
	default:
	}
}

func readControlFixture(ctx context.Context, root string) (retainedControlDescription, string, error) {
	for {
		raw, err := os.ReadFile(filepath.Join(root, "fixture-control.json"))
		if err == nil {
			var fixture struct {
				Control retainedControlDescription
				HTTP    string
			}
			if err := json.Unmarshal(raw, &fixture); err == nil {
				return fixture.Control, fixture.HTTP, nil
			}
		}
		if err := pause(ctx, 10*time.Millisecond); err != nil {
			return retainedControlDescription{}, "", err
		}
	}
}

func controlFixtureRequest(ctx context.Context, description retainedControlDescription, method, action string) (retainedControlSnapshot, error) {
	client, err := connectRetained(ctx, description)
	if err != nil {
		return retainedControlSnapshot{}, err
	}
	defer client.close()
	return client.request(ctx, method, action)
}

func finishControlFixture(ctx context.Context, description retainedControlDescription) error {
	if _, err := controlFixtureRequest(ctx, description, http.MethodPost, "/cleanup"); err != nil {
		return err
	}
	for {
		state, err := controlFixtureRequest(ctx, description, http.MethodGet, "/state")
		if err != nil {
			return err
		}
		if state.Joined {
			break
		}
		if err := pause(ctx, 10*time.Millisecond); err != nil {
			return err
		}
	}
	client, err := connectRetained(ctx, description)
	if err != nil {
		return err
	}
	defer client.close()
	if _, err := client.request(ctx, http.MethodPost, "/finish"); err != nil {
		return err
	}
	return client.peer.WaitExit(ctx)
}

func TestRetainedForegroundFixture(t *testing.T) {
	root := os.Getenv("NVKE_TEST_RETAINED_FOREGROUND")
	if root == "" {
		return
	}
	log, err := os.OpenFile(filepath.Join(root, "fixture-background.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestRetainedBackgroundFixture$", "-test.count=1", "-test.timeout=45s")
	command.Env = append(os.Environ(), "NVKE_TEST_RETAINED_BACKGROUND="+root)
	command.Stdout, command.Stderr = log, log // direct files: no copy/Wait goroutines to abandon
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	handedOff := false
	defer func() {
		if handedOff {
			return
		}
		// Before handoff this fixture still owns the actual exec handle and
		// sole Wait. Try live owner cleanup; always join the child afterward.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 40*time.Second)
		defer cancel()
		description, _, readErr := readControlFixture(cleanup, root)
		if readErr == nil {
			if err := finishControlFixture(cleanup, description); err != nil {
				t.Error("fixture live owner cleanup could not be acknowledged", err)
			}
		}
		if err := command.Wait(); err != nil {
			t.Error("fixture child did not join before failed handoff returned", err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	description, _, err := readControlFixture(ctx, root)
	if err != nil || description.PID != command.Process.Pid {
		t.Fatal("fixture did not describe this actual child", err)
	}
	state, err := controlFixtureRequest(ctx, description, http.MethodPost, "/detach")
	if err != nil || !state.Detached {
		t.Fatal("live child did not acknowledge detached ownership", err)
	}
	sid, err := unix.Getsid(command.Process.Pid)
	if err != nil || sid != command.Process.Pid {
		t.Fatal("kernel did not observe actual late detachment", err)
	}
	// No OwnedProcess/Wait goroutine exists for this foreground-to-owner handoff.
	// Release follows the exact connected-peer acknowledgement, never a PID file.
	if err := command.Process.Release(); err != nil {
		t.Fatal(err)
	}
	handedOff = true
}

func TestRetainedControlSurvivesNaturalForegroundExitAndJoinsServices(t *testing.T) {
	if err := CheckSupervisorPeerCapability(t.Context()); err != nil {
		t.Fatal(err)
	}
	root := privateTestDir(t)
	log, err := os.OpenFile(filepath.Join(root, "fixture-foreground.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	foreground := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRetainedForegroundFixture$", "-test.count=1", "-test.timeout=70s")
	foreground.Env = append(os.Environ(), "NVKE_TEST_RETAINED_FOREGROUND="+root)
	foreground.Stdout, foreground.Stderr = log, log
	if err := foreground.Run(); err != nil {
		t.Fatal("foreground did not return naturally with zero", err)
	}
	description, endpoint, err := readControlFixture(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	ownerExited := false
	t.Cleanup(func() {
		if ownerExited {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 40*time.Second)
		defer cancel()
		if err := finishControlFixture(cleanup, description); err != nil {
			t.Error("handed-off fixture owner did not join before failed test returned", err)
		}
	})
	state, err := controlFixtureRequest(ctx, description, http.MethodGet, "/state")
	if err != nil || !state.Detached || state.Joined || state.State != retainedReady {
		t.Fatalf("owner did not survive foreground exit without cleanup: %+v %v", state, err)
	}
	t.Run("concurrent single use", func(t *testing.T) {
		client, err := connectRetained(ctx, description)
		if err != nil {
			t.Fatal(err)
		}
		defer client.close()
		results := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := client.request(ctx, http.MethodGet, "/state")
				results <- err
			}()
		}
		passed, refused := 0, 0
		for range 2 {
			select {
			case err := <-results:
				var coded *nvkrerr.Error
				if err == nil {
					passed++
				} else if errors.As(err, &coded) && coded.Code == nvkrerr.CodeUsage {
					refused++
				} else {
					t.Error("unexpected connected request failure", err)
				}
			case <-ctx.Done():
				t.Fatal("connected fixture requests did not join")
			}
		}
		if passed != 1 || refused != 1 {
			t.Fatal("same connected session admitted multiple requests", passed, refused)
		}
	})
	if _, err := controlFixtureRequest(ctx, description, http.MethodPost, "/finish"); err == nil {
		t.Fatal("caller invented joined cleanup from a wire request")
	}
	if _, err := controlFixtureRequest(ctx, description, http.MethodPost, "/abort"); err != nil {
		t.Fatal(err)
	}
	t.Run("copied session selector", func(t *testing.T) {
		selected := privateTestDir(t)
		record := filepath.Join(selected, retainedDescriptionFile)
		if err := privateJSON(record, description); err != nil {
			t.Fatal(err)
		}
		if _, err := openRetainedSession(ctx, selected); err == nil {
			t.Fatal("copied record selected another live owner")
		}
		forged := description
		forged.Selector = selected
		bytes, err := json.Marshal(forged)
		if err != nil || os.WriteFile(record, bytes, 0o600) != nil {
			t.Fatal("could not construct copied-selector negative fixture", err)
		}
		if _, err := openRetainedSession(ctx, selected); err == nil {
			t.Fatal("changed selector supplied another owner's live capability")
		}
		if _, err := controlFixtureRequest(ctx, forged, http.MethodPost, "/cleanup"); err == nil {
			t.Fatal("copied/changed selector admitted cleanup of another owner")
		}
	})
	raw := []byte("exact retained fixture bytes")
	image := lifecycle.Artifact{Digest: "sha256:" + plainSHA(raw), Disk: &delivery.DiskOutput{Size: int64(len(raw))}}
	if err := verifyHTTPDisk(ctx, endpoint, image); err != nil {
		t.Fatal("foreground exit/work abort destroyed actual retained HTTP service", err)
	}
	for _, mutation := range []string{"wrong PID", "wrong work", "wrong nonce"} {
		t.Run(mutation, func(t *testing.T) {
			wrong := description
			switch mutation {
			case "wrong PID":
				wrong.PID++
			case "wrong work":
				wrong.Work += "-different"
			case "wrong nonce":
				replacement := "0"
				if wrong.Token[32] == '0' {
					replacement = "1"
				}
				wrong.Token = wrong.Token[:32] + replacement + wrong.Token[33:]
			}
			if _, err := controlFixtureRequest(ctx, wrong, http.MethodGet, "/state"); err == nil {
				t.Fatal("substitution was admitted")
			}
			if _, err := controlFixtureRequest(ctx, wrong, http.MethodPost, "/cleanup"); err == nil {
				t.Fatal("substituted workspace/nonce admitted a mutation")
			}
		})
	}
	if got, err := controlFixtureRequest(ctx, description, http.MethodGet, "/state"); err != nil || got.State != retainedReady {
		t.Fatal("rejected mutation changed actual retained state", got, err)
	}
	if _, err := controlFixtureRequest(ctx, description, http.MethodPost, "/cleanup"); err != nil {
		t.Fatal(err)
	}
	for {
		state, err = controlFixtureRequest(ctx, description, http.MethodGet, "/state")
		if err != nil {
			t.Fatal(err)
		}
		if state.Joined {
			break
		}
		if err := pause(ctx, 10*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	client, err := connectRetained(ctx, description)
	if err != nil {
		t.Fatal(err)
	}
	defer client.close()
	if _, err := client.request(ctx, http.MethodPost, "/finish"); err != nil {
		t.Fatal(err)
	}
	if err := client.peer.WaitExit(ctx); err != nil {
		t.Fatal("same actual owner did not exit after joined cleanup", err)
	}
	ownerExited = true
	if _, err := controlFixtureRequest(ctx, description, http.MethodGet, "/state"); err == nil {
		t.Fatal("dead owner description was adopted as a live session")
	}
}

func TestRetainedControlDescriptionsDoNotSupplyCapabilities(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for _, value := range []retainedControlDescription{{}, {Version: 1, Work: "/work", PID: os.Getpid(), Socket: "@invented", Token: strings.Repeat("a", 64)}} {
		if client, err := connectRetained(ctx, value); client != nil || err == nil {
			t.Fatal("unissued endpoint description constructed a connected owner")
		}
	}
	owner, _, _ := retainedTestOwner(t)
	control, err := newRetainedControl(t.Context(), owner, privateTestDir(t), func() error { return errors.New("private-detach-error") })
	if err != nil {
		t.Fatal(err)
	}
	defer control.close(ctx)
	if err := control.recordJoined(ctx, nil); err == nil {
		t.Fatal("joined state was accepted before explicit cleanup")
	}
	if err := control.detachOwner(ctx); err == nil {
		t.Fatal("unheld owner was detached")
	}
	var missingContext context.Context
	if _, err := control.snapshot(missingContext); err == nil {
		t.Fatal("missing context supplied a state")
	}
	if err := owner.requestCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := control.recordJoined(ctx, ErrUnjoined); !errors.Is(err, ErrUnjoined) {
		t.Fatal("unjoined cleanup was not retained", err)
	}
	var coded *nvkrerr.Error
	if err := control.detachOwner(ctx); !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
		t.Fatal("cleanup state permitted a late detach", err)
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeFailedTenantCleanupRetainsDiagnosticsBeforeDeletion(t *testing.T) {
	for _, failure := range []string{"", "diagnostics fail"} {
		t.Run(failure, func(t *testing.T) {
			root := privateTestDir(t)
			events := filepath.Join(root, "events")
			cli := filepath.Join(root, "nvkectl")
			writeTenantFixture(t, cli, []byte(`#!/bin/sh
set -eu
test "$*" = 'get clusters -o json'
test "$XDG_CONFIG_HOME" = "$FIXTURE/config"
printf 'nvke-api\n' >> "$EVENTS"
printf 'private-api-failure\n' >&2
exit 7
`), 0o700)
			writeTenantFixture(t, filepath.Join(root, "kubectl"), []byte(`#!/bin/sh
set -eu
test "$1" = --kubeconfig
test "$2" = "$FIXTURE/site.kubeconfig"
test "$3" = --request-timeout=5s
shift 3
printf 'diagnostic %s\n' "$*" >> "$EVENTS"
printf 'private-site-diagnostic\n'
if test "$FAIL" != ''; then exit 9; fi
`), 0o700)
			writeTenantFixture(t, filepath.Join(root, "kind"), []byte("#!/bin/sh\nset -eu\nprintf 'bootstrap-delete\\n' >> \"$EVENTS\"\n"), 0o700)
			var public bytes.Buffer
			runner := Runner{Work: root, Progress: &public, Env: []string{"PATH=/usr/bin:/bin", "FIXTURE=" + root, "EVENTS=" + events, "FAIL=" + failure, "XDG_CONFIG_HOME=" + filepath.Join(root, "config")}}
			attempt := nativeSiteAttempt{assets: NativeAssets{HostTools: root, CLI: cli}, bootstrapName: "owned", bootstrapRunner: &runner,
				tenantOwner: &tenant{name: "native-demo-tenant", id: "owned-id", intent: true, cliPath: cli, session: ManagementSession{Work: root, Runner: runner}}}
			err := attempt.close(t.Context())
			if CommandExitCode(err) != 7 {
				t.Fatalf("original inventory failure replaced: %v", err)
			}
			raw, err := os.ReadFile(events)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
			if len(lines) != 9 || lines[0] != "nvke-api" || lines[1] != "nvke-api" || lines[8] != "bootstrap-delete" {
				t.Fatalf("diagnostics must follow restore failure and precede deletion: %s", raw)
			}
			for i, fragment := range []string{"get pods --all-namespaces -o json", "get events --all-namespaces -o json", "get services --all-namespaces -o json", "get endpointslices.discovery.k8s.io --all-namespaces -o json", "logs --selector=app.kubernetes.io/name=nke-controller,app.kubernetes.io/instance=nke-controller --container=nke-controller", "--previous=true"} {
				if !strings.Contains(lines[i+2], fragment) {
					t.Fatalf("missing exact bounded read-only diagnostic %q: %s", fragment, raw)
				}
			}
			if strings.Contains(public.String(), "private-") {
				t.Fatalf("private diagnostic escaped: %s", &public)
			}
			folders, err := filepath.Glob(filepath.Join(root, "native-site-failure-*"))
			if err != nil || len(folders) != 1 {
				t.Fatalf("missing unique private failure observation: %v %v", folders, err)
			}
			for _, phase := range []string{"site-pods", "site-events", "site-services", "site-endpointslices", "controller-current", "controller-previous"} {
				log, err := os.ReadFile(filepath.Join(folders[0], phase+".log"))
				if err != nil || !strings.Contains(string(log), "private-site-diagnostic") {
					t.Fatalf("missing private diagnostic %s: %s %v", phase, log, err)
				}
			}
			if err := filepath.WalkDir(folders[0], func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				want := os.FileMode(0o600)
				if entry.IsDir() {
					want = 0o700
				}
				if info.Mode().Perm() != want {
					t.Errorf("nonprivate diagnostic %s: %o", path, info.Mode().Perm())
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeAPIObservationIsReadOnlyPrivateBoundedAndJoined(t *testing.T) {
	for _, name := range []string{"success", "HTTP failure", "deadline", "canceled"} {
		t.Run(name, func(t *testing.T) {
			root := privateTestDir(t)
			cli := filepath.Join(root, "nvkectl")
			calls := filepath.Join(root, "calls")
			writeTenantFixture(t, cli, []byte(`#!/bin/sh
set -eu
test "$*" = 'get clusters -o json'
test "$XDG_CONFIG_HOME" = "$FIXTURE/config"
printf '%s\n' "$*" >> "$CALLS"
printf 'private-api-observation\n'
case "$MODE" in
 'HTTP failure') exit 7 ;;
 deadline) exec sleep 60 ;;
esac
`), 0o700)
			var public bytes.Buffer
			runner := Runner{Work: root, Progress: &public, output: &public, Env: []string{"PATH=/usr/bin:/bin", "FIXTURE=" + root, "CALLS=" + calls, "MODE=" + name, "XDG_CONFIG_HOME=" + filepath.Join(root, "config")}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			if name == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 2*time.Second)
				defer stop()
			}
			err := observeNativeAPI(ctx, runner, cli)
			switch name {
			case "success":
				if err != nil {
					t.Fatal(err)
				}
			case "HTTP failure":
				if CommandExitCode(err) != 7 {
					t.Fatalf("lost API exit status: %v", err)
				}
			case "deadline":
				if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnjoined) {
					t.Fatalf("API observation not bounded/joined: %v", err)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled observation: %v", err)
				}
			}
			if public.Len() != 0 {
				t.Fatalf("private API observation reached output: %s", &public)
			}
			raw, readErr := os.ReadFile(calls)
			if name == "canceled" {
				if !os.IsNotExist(readErr) {
					t.Fatal("canceled API observation launched work")
				}
				return
			}
			if readErr != nil || string(raw) != "get clusters -o json\n" {
				t.Fatalf("API probe mutated or retried: %s %v", raw, readErr)
			}
			files, err := filepath.Glob(filepath.Join(root, "native-api-observation-*", "nvke-api-status.json"))
			if err != nil || len(files) != 1 {
				t.Fatalf("missing private command status: %v %v", files, err)
			}
			statusRaw, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			var status struct {
				StartedAt, FinishedAt time.Time
				ExitCode              int
			}
			if err := json.Unmarshal(statusRaw, &status); err != nil || status.StartedAt.IsZero() || status.FinishedAt.Before(status.StartedAt) {
				t.Fatalf("invalid observation metadata: %s %v", statusRaw, err)
			}
			want := 0
			switch name {
			case "HTTP failure":
				want = 7
			case "deadline":
				want = -1
			}
			if status.ExitCode != want {
				t.Fatalf("status code=%d want=%d", status.ExitCode, want)
			}
		})
	}
}

func TestNativeSiteDiagnosticsDoNotRaceUnjoinedOrCanceledWork(t *testing.T) {
	for _, cause := range []error{ErrUnjoined, errors.New("original restore failure")} {
		root := privateTestDir(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if !errors.Is(cause, ErrUnjoined) {
			cancel()
		}
		result := retainNativeSiteFailure(ctx, Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin"}}, root, "unused", cause)
		if !errors.Is(result, cause) || errors.Is(result, ErrUnjoined) != errors.Is(cause, ErrUnjoined) {
			t.Fatalf("diagnostic changed original failure: %v", result)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("canceled/unjoined work started observation: %v %v", entries, err)
		}
	}
}

func TestNativeRetainedStatusAlsoObservesConfiguredAPIPrivately(t *testing.T) {
	access, runner := operatorStatusFixture(t)
	cli := filepath.Join(runner.Work, "nvkectl")
	calls := filepath.Join(runner.Work, "api-calls")
	writeTenantFixture(t, cli, []byte(`#!/bin/sh
set -eu
test "$*" = 'get clusters -o json'
test "$XDG_CONFIG_HOME" = "$CONFIG"
printf 'observed\n' >> "$CALLS"
printf 'private-api-unavailable\n' >&2
exit 7
`), 0o700)
	runner.Env = SetEnv(runner.Env, map[string]string{"CALLS": calls, "CONFIG": filepath.Join(runner.Work, "config"), "XDG_CONFIG_HOME": filepath.Join(runner.Work, "config")})
	var public bytes.Buffer
	runner.Progress = &public
	attempt := nativeSiteAttempt{assets: NativeAssets{CLI: cli}}
	for range 2 {
		if err := attempt.presentStatus(t.Context(), runner, access, false); err != nil {
			t.Fatalf("diagnostic API failure changed retained status behavior: %v", err)
		}
	}
	raw, err := os.ReadFile(calls)
	if err != nil || string(raw) != "observed\nobserved\n" {
		t.Fatalf("retained observations did not call configured API once each: %s %v", raw, err)
	}
	folders, err := filepath.Glob(filepath.Join(runner.Work, "native-api-observation-*"))
	if err != nil || len(folders) != 2 {
		t.Fatalf("repeated API observations reused logs: %v %v", folders, err)
	}
	if strings.Contains(public.String(), "private-api") || !strings.Contains(public.String(), "Tenant / QCOW2") {
		t.Fatalf("private API data leaked or Kubernetes observation lost: %s", &public)
	}
}

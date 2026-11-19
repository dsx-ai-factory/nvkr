// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type recordedProgress struct {
	bytes.Buffer
	events      []ProgressEvent
	panicReport bool
}

func (writer *recordedProgress) Report(event ProgressEvent) {
	writer.events = append(writer.events, event)
	if writer.panicReport {
		panic("presenter failure")
	}
}

func phaseEvents(name, last string) []ProgressEvent {
	status := "finished"
	if last == PhaseFailed {
		status = "failed"
	}
	return []ProgressEvent{{Kind: PhaseStarted, Name: name, Status: "started"}, {Kind: last, Name: name, Status: status}}
}

func TestProgressRunnerObservedBoundaries(t *testing.T) {
	for _, name := range []string{"success", "exit failure", "lookup failure", "exec failure", "timeout", "cancellation", "canceled before start", "invalid phase", "invalid timeout", "storage failure"} {
		t.Run(name, func(t *testing.T) {
			writer := &recordedProgress{}
			work := privateTestDir(t)
			runner := Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "PRIVATE_ENV=secret-progress-sentinel"}, Progress: writer}
			args := []string{"/bin/sh", "-c", "printf secret-progress-sentinel; printf secret-progress-sentinel >&2"}
			ctx := t.Context()
			timeout := 5 * time.Second
			phase := "public-phase"
			wantLast := PhaseFailed
			switch name {
			case "success":
				wantLast = PhaseFinished
			case "exit failure":
				args[2] += "; exit 7"
			case "lookup failure":
				args = []string{"absent-secret-progress-sentinel"}
			case "exec failure":
				path := filepath.Join(work, "executable")
				if err := os.WriteFile(path, []byte("#!/absent/secret-progress-sentinel\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				args = []string{path}
			case "timeout", "cancellation":
				args[2] += "; exec sleep 60"
				if name == "timeout" {
					timeout = 100 * time.Millisecond
				} else {
					cancelCtx, cancel := context.WithCancel(ctx)
					ctx = cancelCtx
					defer cancel()
					timer := time.AfterFunc(100*time.Millisecond, cancel)
					defer timer.Stop()
				}
			case "canceled before start":
				cancelCtx, cancel := context.WithCancel(ctx)
				ctx = cancelCtx
				cancel()
			case "invalid phase":
				phase = "../secret-progress-sentinel"
			case "invalid timeout":
				timeout = -time.Second
			case "storage failure":
				if err := os.Chmod(work, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			_, err := runner.Capture(ctx, args, phase, timeout)
			if (err == nil) != (name == "success") {
				t.Fatalf("unexpected result: %v", err)
			}
			if name == "invalid phase" {
				phase = "command"
			}
			if !reflect.DeepEqual(writer.events, phaseEvents(phase, wantLast)) {
				t.Fatalf("events=%+v", writer.events)
			}
			for _, event := range writer.events {
				if strings.Contains(event.Kind+event.Name+event.Status, "secret-progress-sentinel") {
					t.Fatal("events exposed private command data")
				}
			}
			if strings.Contains(writer.String(), "secret-progress-sentinel") {
				t.Fatal("private output reached progress writer")
			}
			if errors.Is(err, ErrUnjoined) {
				t.Fatal("fixture producer did not join", err)
			}
		})
	}
}

func TestProgressPublicLauncherProcessObservations(t *testing.T) {
	for _, scenario := range []string{"success", "exit failure", "timeout", "start failure", "private command"} {
		t.Run(scenario, func(t *testing.T) {
			writer := &recordedProgress{}
			runner := Runner{Env: []string{"PATH=/usr/bin:/bin", "PRIVATE=secret-process-sentinel"}, Progress: writer, output: io.Discard}
			args := []string{"/bin/sh", "-c", "exit 0", "secret-process-sentinel"}
			timeout := 5 * time.Second
			last := PhaseFinished
			switch scenario {
			case "exit failure":
				args[2], last = "exit 7", PhaseFailed
			case "timeout":
				args[2], last, timeout = "exec sleep 60", PhaseFailed, 100*time.Millisecond
			case "start failure":
				args, last = []string{"/absent/secret-process-sentinel"}, PhaseFailed
			case "private command":
				runner.output = nil
			}
			err := runner.Run(t.Context(), args, "build-all", timeout)
			if (err == nil) != (last == PhaseFinished) || errors.Is(err, ErrUnjoined) {
				t.Fatalf("unexpected lifecycle outcome: %v", err)
			}
			want := phaseEvents("build-all", last)
			if scenario != "start failure" && scenario != "private command" {
				want = []ProgressEvent{want[0],
					{Kind: ProcessStarted, Name: "build-all", Status: "started"},
					{Kind: ProcessExited, Name: "build-all", Status: "exited"}, want[1]}
			}
			if !reflect.DeepEqual(writer.events, want) {
				t.Fatalf("wrong process observations: %+v want %+v", writer.events, want)
			}
		})
	}
}

func TestProgressOwnedProcessObservationIsSynchronousAndJoined(t *testing.T) {
	for _, scenario := range []string{"exit success", "exit failure", "stop joined", "join unconfirmed"} {
		t.Run(scenario, func(t *testing.T) {
			writer := &recordedProgress{}
			if scenario == "join unconfirmed" {
				process := &OwnedProcess{phase: "build-all", done: make(chan struct{}), processWriter: writer, joinErr: ErrUnjoined}
				close(process.done)
				if exited, err := process.Exited(); !exited || !errors.Is(err, ErrUnjoined) || len(writer.events) != 0 {
					t.Fatalf("unconfirmed group fabricated an exit observation: %+v %v", writer.events, err)
				}
				return
			}
			command, last := "exit 0", PhaseFinished
			switch scenario {
			case "exit failure":
				command, last = "exit 7", PhaseFailed
			case "stop joined":
				command = "exec sleep 60"
			}
			process, err := (Runner{Env: []string{"PATH=/usr/bin:/bin"}, Progress: writer, output: io.Discard}).Start(t.Context(), []string{"/bin/sh", "-c", command}, "build-all")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "stop joined" {
				if err := process.Stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else {
				select {
				case <-process.done:
				case <-time.After(5 * time.Second):
					t.Fatal("owner did not join")
				}
				if len(writer.events) != 2 {
					t.Fatal("Wait goroutine reported presentation or inferred liveness")
				}
				_, _ = process.Exited()
			}
			_, _ = process.Exited()
			_ = process.Stop(t.Context())
			phases := phaseEvents("build-all", last)
			want := []ProgressEvent{phases[0], {Kind: ProcessStarted, Name: "build-all", Status: "started"},
				{Kind: ProcessExited, Name: "build-all", Status: "exited"}, phases[1]}
			if !reflect.DeepEqual(writer.events, want) {
				t.Fatalf("wrong synchronous observations: %+v want %+v", writer.events, want)
			}
		})
	}
}

func TestProgressOwnedStartStopObservedSynchronously(t *testing.T) {
	for _, name := range []string{"exit success", "exit failure", "stop first exit success", "stop first exit failure", "stop joined", "start failure", "stop unjoined"} {
		t.Run(name, func(t *testing.T) {
			writer := &recordedProgress{}
			var process *OwnedProcess
			if name == "stop unjoined" {
				process = &OwnedProcess{cmd: &exec.Cmd{Process: &os.Process{Pid: 123}}, phase: "daemon", done: make(chan struct{}), groupSignal: func(int, unix.Signal) error { return unix.EPERM }, progress: beginProgress(writer, "daemon", PhaseStarted, PhaseFinished, PhaseFailed)}
			} else {
				command := "exit 0"
				if name == "exit failure" || name == "stop first exit failure" {
					command = "exit 7"
				}
				if name == "stop joined" {
					command = "exec sleep 60"
				}
				args := []string{"/bin/sh", "-c", command}
				if name == "start failure" {
					args = []string{"/absent/executable"}
				}
				var err error
				process, err = (Runner{Env: []string{"PATH=/usr/bin:/bin"}, Progress: writer}).Start(t.Context(), args, "daemon")
				if name == "start failure" {
					if err == nil || !reflect.DeepEqual(writer.events, phaseEvents("daemon", PhaseFailed)) {
						t.Fatalf("events=%+v error=%v", writer.events, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if len(writer.events) != 1 {
				t.Fatal("terminal event before synchronous observation")
			}
			last := PhaseFinished
			if strings.HasPrefix(name, "stop first") {
				select {
				case <-process.done:
				case <-time.After(5 * time.Second):
					t.Fatal("process did not join")
				}
				if len(writer.events) != 1 {
					t.Fatal("Wait goroutine reported a presentation event")
				}
				if err := process.Stop(t.Context()); err != nil {
					t.Fatal("joined failed child must not change cleanup return", err)
				}
				if name == "stop first exit failure" {
					last = PhaseFailed
				}
				_, _ = process.Exited()
				_ = process.Stop(t.Context())
			} else if name == "stop joined" || name == "stop unjoined" {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				err := process.Stop(ctx)
				if name == "stop unjoined" {
					last = PhaseFailed
					if !errors.Is(err, ErrUnjoined) {
						t.Fatal(err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				_ = process.Stop(ctx)
			} else {
				select {
				case <-process.done:
				case <-time.After(5 * time.Second):
					t.Fatal("process did not join")
				}
				if len(writer.events) != 1 {
					t.Fatal("Wait goroutine reported a presentation event")
				}
				exited, err := process.Exited()
				if !exited || (err != nil) != (name == "exit failure") {
					t.Fatalf("exited=%t error=%v", exited, err)
				}
				if name == "exit failure" {
					last = PhaseFailed
				}
				_, _ = process.Exited()
			}
			if !reflect.DeepEqual(writer.events, phaseEvents("daemon", last)) {
				t.Fatalf("events=%+v", writer.events)
			}
		})
	}
}

func TestProgressCombinedBuildOrdering(t *testing.T) {
	for _, failLane := range []string{"", "all", "prepare"} {
		t.Run("failure-"+failLane, func(t *testing.T) {
			writer := &recordedProgress{}
			dir := privateTestDir(t)
			cfg := BuildConfig{Repository: dir, AttemptPrefix: "fixture", Jobs: 4}
			bootstrap := filepath.Join(dir, ".nvkr/laptop-bootstrap/fixture")
			if err := os.MkdirAll(bootstrap, 0o700); err != nil {
				t.Fatal(err)
			}
			args, err := json.Marshal(preparedInvocationFixture(cfg))
			if err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(dir, "launcher.sh")
			body := "#!/bin/sh\nset -eu\n[ \"$1\" = --prepare-only ]\n[ \"$4\" = all ]\n[ \"$FAIL_LANE\" != prepare ] || exit 7\numask 077\nprintf '%s' \"$BUILD_INVOCATION_JSON\" >\"$BUILD_INVOCATION\"\n"
			if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bootstrap, "nvkr"), []byte("#!/bin/sh\n[ \"$FAIL_LANE\" != all ] || exit 7\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			runner := Runner{Env: []string{"PATH=/usr/bin:/bin", "FAIL_LANE=" + failLane,
				"BUILD_INVOCATION_JSON=" + string(args), "BUILD_INVOCATION=" + filepath.Join(bootstrap, "build-invocation.json")}, Progress: writer}
			err = buildLocalArtifacts(t.Context(), runner, script, cfg)
			phaseLast, laneLast, status := PhaseFinished, LaneFinished, "finished"
			if failLane != "" {
				phaseLast, laneLast, status = PhaseFailed, LaneFailed, "failed"
				if CommandExitCode(err) != 7 {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := []ProgressEvent{{Kind: LaneStarted, Name: "artifacts", Status: "started"}}
			if failLane == "prepare" {
				want = append(want, phaseEvents("build-prepare", PhaseFailed)...)
			} else {
				want = append(want, phaseEvents("build-prepare", PhaseFinished)...)
				phases := phaseEvents("build-all", phaseLast)
				want = append(want, phases[0], ProgressEvent{Kind: ProcessStarted, Name: "build-all", Status: "started"},
					ProgressEvent{Kind: ProcessExited, Name: "build-all", Status: "exited"}, phases[1])
			}
			want = append(want, ProgressEvent{Kind: laneLast, Name: "artifacts", Status: status})
			selected := slices.IndexFunc(writer.events, func(event ProgressEvent) bool { return event.Kind == BuildPlanSelected })
			if failLane == "prepare" && selected >= 0 || failLane != "prepare" && (selected < 0 || writer.events[selected].BuildPlan != nil) {
				t.Fatal("missing fixture Plan did not preserve the presentation-only fallback")
			}
			boundaries := slices.DeleteFunc(slices.Clone(writer.events), func(event ProgressEvent) bool { return event.Kind == BuildPlanSelected })
			if !reflect.DeepEqual(boundaries, want) {
				t.Fatalf("events=%+v want=%+v", writer.events, want)
			}
		})
	}
}

func TestProgressPlainWriterAndPresenterFailure(t *testing.T) {
	for _, panicReport := range []bool{false, true} {
		writer := &recordedProgress{panicReport: panicReport}
		var plain bytes.Buffer
		for _, progress := range []io.Writer{&plain, writer} {
			if err := (Runner{Env: []string{"PATH=/usr/bin:/bin"}, Progress: progress}).Run(t.Context(), []string{"/bin/sh", "-c", "exit 0"}, "preflight-os", time.Second); err != nil {
				t.Fatal(err)
			}
		}
		if plain.String() != "Demo phase: preflight-os\n" || writer.String() != plain.String() {
			t.Fatal("plain progress output changed")
		}
		if !reflect.DeepEqual(writer.events, phaseEvents("preflight-os", PhaseFinished)) {
			t.Fatal(writer.events)
		}
	}
}

func TestTenantProgressReportsTransitionsAndBoundedHeartbeat(t *testing.T) {
	writer := &recordedProgress{}
	start := time.Unix(1000, 0)
	p := tenantProgress{writer: writer, started: start, deadline: start.Add(time.Minute)}
	p.update(start, "tenant-kubeconfig", nil)
	p.update(start.Add(10*time.Second), "tenant-kubeconfig", nil)
	p.update(start.Add(30*time.Second), "tenant-kubeconfig", nil)
	nodes := readyTenantFixture()
	nodes[0].Status.Conditions[0].Status = "False"
	p.update(start.Add(31*time.Second), "tenant-nodes", nodes[:1])
	p.update(start.Add(32*time.Second), "tenant-nodes", nodes)
	p.update(start.Add(33*time.Second), "tenant-nodes", nodes)
	if len(writer.events) != 4 {
		t.Fatalf("expected initial wait, heartbeat and two transitions: %+v", writer.events)
	}
	for i, event := range writer.events {
		wantRegistered, wantReady := 0, 0
		if i == 2 {
			wantRegistered = 1
		}
		if i == 3 {
			wantRegistered, wantReady = 2, 1
		}
		if event.Kind != TenantWaiting || event.RegisteredNodes != wantRegistered || event.ReadyNodes != wantReady || event.Elapsed+event.Remaining != time.Minute {
			t.Fatalf("wrong interpreted observation: %+v", event)
		}
	}
}

func TestTenantProgressProjectsOnlyPinnedInventoryConditionStatuses(t *testing.T) {
	for _, test := range []struct {
		name, provisioning, provisioned   string
		wantProvisioning, wantProvisioned TenantConditionStatus
	}{
		{"provisioning", "True", "False", TenantConditionTrue, TenantConditionFalse},
		{"provisioned is not nodes Ready", "False", "True", TenantConditionFalse, TenantConditionTrue},
		{"upstream unknown", "Unknown", "Unknown", TenantConditionUnknown, TenantConditionUnknown},
		{"absent statuses", "", "", "", ""},
		{"unsupported status omitted", "private-response-sentinel", "ready", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := fmt.Sprintf(`[{"name":"owned","id":"owned-id","nico":{"orgID":%q,"siteID":%q,"vpcID":%q},"conditions":{"provisioning":{"status":%q,"message":"private-response-sentinel","lastTransitionTime":"private-response-sentinel"},"provisioned":{"status":%q,"message":"private-response-sentinel"},"private":"private-response-sentinel"},"credentials":"private-response-sentinel"}]`, OrgID, SiteID, VPCID, test.provisioning, test.provisioned)
			var rows []tenantRow
			if err := json.Unmarshal([]byte(raw), &rows); err != nil {
				t.Fatal(err)
			}
			row, err := selectedTenant(rows, "owned", "owned-id")
			if err != nil || row == nil {
				t.Fatalf("existing tenant ownership checks rejected inventory: %v", err)
			}
			writer := &recordedProgress{}
			start := time.Unix(1000, 0)
			progress := tenantProgress{writer: writer, started: start, deadline: start.Add(time.Minute), conditions: row.Conditions}
			progress.update(start, "tenant-kubeconfig", nil)
			if len(writer.events) != 1 {
				t.Fatal("missing inventory observation")
			}
			event := writer.events[0]
			if event.Provisioning != test.wantProvisioning || event.Provisioned != test.wantProvisioned || event.RegisteredNodes != 0 || event.ReadyNodes != 0 {
				t.Fatalf("unexpected presentation or fabricated node readiness: %+v", event)
			}
			encoded, err := json.Marshal(event)
			if err != nil || strings.Contains(string(encoded), "private-response-sentinel") || strings.Contains(string(encoded), "owned-id") || strings.Contains(string(encoded), "owned") {
				t.Fatalf("inventory details escaped into presentation: %s %v", encoded, err)
			}
			progress.conditions.Provisioning.Status = TenantConditionTrue
			if event.Provisioning == TenantConditionTrue {
				progress.conditions.Provisioning.Status = TenantConditionFalse
			}
			progress.update(start.Add(time.Second), "tenant-kubeconfig", nil)
			if len(writer.events) != 2 {
				t.Fatal("condition transition was hidden by unchanged-wait throttle")
			}
		})
	}
}

func TestTenantPollingKeepsPrivateLogWithoutSuccessSpam(t *testing.T) {
	writer := &recordedProgress{}
	work := privateTestDir(t)
	tenant := tenant{session: ManagementSession{Runner: Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}, Progress: writer}}}
	raw, err := tenant.capture(t.Context(), []string{"/bin/sh", "-c", "printf private-response; exit 1"}, "kubeconfig-secret", time.Second)
	if CommandExitCode(err) != 1 || string(raw) != "private-response" || len(writer.events) != 0 || writer.Len() != 0 {
		t.Fatalf("polling output escaped interpretation: events=%+v error=%v", writer.events, err)
	}
	if _, err := os.Stat(filepath.Join(work, "tenant-001-kubeconfig-secret.log")); err != nil {
		t.Fatal("private command diagnostics lost", err)
	}
}

func TestTenantCleanupReportsBalancedPrivateOperations(t *testing.T) {
	for _, scenario := range []string{"deleted", "no-intent", "absent", "inventory-failed", "delete-failed", "wait-failed", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			work := privateTestDir(t)
			cliDir := filepath.Join(work, "cli")
			if err := os.Mkdir(cliDir, 0o700); err != nil {
				t.Fatal(err)
			}
			attempt := nativeCAPITenantFixture(t, GuestCIDR, "ovn")
			request, err := attempt.namedTenantRequest(t.Context(), "private-tenant")
			if err != nil {
				t.Fatal(err)
			}
			ownership, err := newNativeCAPITenantOwnership(request)
			if err != nil {
				t.Fatal(err)
			}
			row := tenantRow{Name: request.Name, ID: "private-owned-id", Infra: request.Infra, ControlPlane: &request.ControlPlane}
			encoded, err := json.Marshal([]tenantRow{row})
			if err != nil {
				t.Fatal(err)
			}
			pools, err := json.Marshal([]map[string]any{{"id": "private-worker-id", "name": request.NodePools[0].Name, "clusterID": row.ID, "infra": request.NodePools[0].Infra, "desiredNodeCount": request.NodePools[0].Count}})
			if err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(work, "state")
			script := `#!/bin/sh
set -eu
case "$1:$2" in
 get:clusters)
  [ "$SCENARIO" != inventory-failed ] || exit 7
  if [ -f "$STATE" ]; then
   [ "$SCENARIO" != wait-failed ] || exit 7
   printf '[]'
  elif [ "$SCENARIO" = absent ]; then printf '[]'; else printf '%s' "$ROW"; fi;;
 get:nodepools) printf '%s' "$POOLS";;
 delete:cluster) [ "$SCENARIO" != delete-failed ] || exit 7; touch "$STATE";;
 *) exit 42;;
esac
`
			if err = os.WriteFile(filepath.Join(cliDir, "nvkectl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			writer := &recordedProgress{}
			env := []string{"PATH=/usr/bin:/bin", "SCENARIO=" + scenario, "STATE=" + state, "ROW=" + string(encoded), "POOLS=" + string(pools)}
			selected := tenant{name: row.Name, id: row.ID, intent: scenario != "no-intent", session: ManagementSession{Work: work, Runner: Runner{Work: work, Env: env, Progress: writer}}, nativeCAPI: ownership}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			err = selected.restore(ctx)
			wantFailure := scenario == "inventory-failed" || scenario == "delete-failed" || scenario == "wait-failed" || scenario == "canceled"
			if (err != nil) != wantFailure {
				t.Fatalf("cleanup policy changed: %v", err)
			}
			var want []ProgressEvent
			if scenario == "deleted" || scenario == "delete-failed" || scenario == "wait-failed" {
				deleteLast := PhaseFinished
				if scenario == "delete-failed" {
					deleteLast = PhaseFailed
				}
				want = append(want, phaseEvents("tenant-delete", deleteLast)...)
				if scenario != "delete-failed" {
					waitLast := PhaseFinished
					if scenario == "wait-failed" {
						waitLast = PhaseFailed
					}
					want = append(want, phaseEvents("tenant-deletion-wait", waitLast)...)
				}
			}
			if !reflect.DeepEqual(writer.events, want) {
				t.Fatalf("unbalanced cleanup observations: %+v want %+v", writer.events, want)
			}
			if strings.Contains(writer.String(), row.Name) || strings.Contains(writer.String(), row.ID) {
				t.Fatal("private tenant identity reached progress output")
			}
		})
	}
}

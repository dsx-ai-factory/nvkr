// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/planview"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/gdamore/tcell/v2"
)

func TestDashboardOSLogsExactOperationAndIndependentClocks(t *testing.T) {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
	_, _ = io.WriteString(d, "[dry-run] os/build-disk (build)\n[start] os/build-disk (build)\n[done] other/build-disk\n")
	before := d.snapshot()
	event := nvkedemo.ProgressEvent{Kind: nvkedemo.OSBuildLogs, BuildLogs: buildactivity.Record{Version: 1, Operation: "os/build-disk", Available: true, Files: 2, Bytes: 2048, GrowthObserved: true, GrowthAgeMillis: 4000, SampleAgeMillis: 2000}}
	d.Report(event)
	after := d.snapshot()
	if len(after.buildLogs) != 1 || after.phases != before.phases || after.buildChangedAt != before.buildChangedAt || after.buildOutputAt != before.buildOutputAt || len(after.activity) != len(before.activity) {
		t.Fatal("OS metadata became build progress or activity-wall output")
	}
	now := after.buildLogs["os/build-disk"].received.Add(3 * time.Second)
	text := dashboardBuildDetail(after, now)
	for _, want := range []string{"OS build logs: 2 files, 2 KiB", "observed growth 9s ago", "report received 5s ago", "not guest health or readiness"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	for _, operation := range []string{"unknown/build-disk", "other/build-disk", "private host:token"} {
		event.BuildLogs.Operation = operation
		d.Report(event)
	}
	if len(d.snapshot().buildLogs) != 1 {
		t.Fatal("telemetry invented/updated an unknown or nonrunning operation")
	}
	after.buildLogs["os/build-disk"] = dashboardOSLogs{}
	if d.snapshot().buildLogs["os/build-disk"].record.Files != 2 {
		t.Fatal("OS metadata snapshot shares mutable map")
	}
	event.BuildLogs = buildactivity.Record{Version: 1, Operation: "os/build-disk"}
	d.Report(event)
	text = dashboardBuildDetail(d.snapshot(), now)
	if !strings.Contains(text, "metadata unavailable") || strings.Contains(text, "observed growth") || strings.Contains(text, "stuck") {
		t.Fatal(text)
	}
}

func TestDashboardOSLogsSmallTerminalAndSafeKeys(t *testing.T) {
	for _, details := range []bool{false, true} {
		d := newDashboard()
		d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
		_, _ = io.WriteString(d, "[dry-run] os/build-disk (build)\n[start] os/build-disk (build)\n")
		d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.OSBuildLogs, BuildLogs: buildactivity.Record{Version: 1, Operation: "os/build-disk", Available: true, Files: 1, Bytes: 4096}})
		d.buildDetails = details
		screen := dashboardScreen(t, 80)
		screen.SetSize(80, 24)
		d.draw(screen, time.Now(), false)
		text := dashboardText(screen)
		for _, want := range []string{"OS build logs", "growth not yet observed", "Operations", "q / Ctrl-C confirm cancel"} {
			if !strings.Contains(text, want) {
				t.Fatalf("details=%t missing %s:\n%s", details, want, text)
			}
		}
		if strings.Contains(text, "guest healthy") || strings.Contains(text, "ETA 0") {
			t.Fatal(text)
		}
	}
}

func TestDashboardOSLogsOnlyWhileSelectedBuildRuns(t *testing.T) {
	for _, state := range []string{"running", "done", "queued", "stopped"} {
		t.Run(state, func(t *testing.T) {
			s := dashboardSnapshot{buildSelected: 0, buildSteps: []dashboardBuildStep{{id: "os/build-disk", kind: "build", state: state}}, buildLogs: map[string]dashboardOSLogs{
				"os/build-disk": {record: buildactivity.Record{Version: 1, Operation: "os/build-disk", Available: true, Files: 1}, received: time.Now()},
			}}
			if got := strings.Contains(dashboardBuildDetail(s, time.Now()), "OS build logs:"); got != (state == "running") {
				t.Fatalf("state %s rendered live OS telemetry=%t", state, got)
			}
		})
	}
}

func TestDashboardProjectionFailureDoesNotInventCapReason(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		t.Run(fmt.Sprint(truncated), func(t *testing.T) {
			s := dashboardSnapshot{buildTruncated: truncated, buildSteps: []dashboardBuildStep{{id: "os/build-disk", kind: "build", state: "running"}}}
			text := dashboardBuildRibbon(s, time.Now())
			if !strings.Contains(text, "Required output total unknown") || strings.Contains(text, "listing cap reached") {
				t.Fatal(text)
			}
			if strings.Contains(text, "projection unavailable, rejected or capped") != truncated {
				t.Fatal(text)
			}
		})
	}
}

func TestDashboardSelectedPlansReplaceRunState(t *testing.T) {
	t.Chdir("../..")
	artifacts, err := factory.LoadPlanContext(t.Context(), ".github/nvkr-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	tools, _, err := factory.CaptureCompileFiles(t.Context(), ".", "inputs/definitions/demo/native-linux/definition-tools.json", "inputs/environments/local-native-linux.json", "test")
	if err != nil {
		t.Fatal(err)
	}
	project := func(plan *factory.Plan, name string) nvkedemo.ProgressEvent {
		t.Helper()
		monitor, err := factory.NewProgress(plan, "same-attempt")
		if err != nil {
			t.Fatal(err)
		}
		document, err := planview.Inspect(plan)
		if err != nil {
			t.Fatal(err)
		}
		graph, err := document.GraphSnapshot("artifacts")
		if err != nil {
			t.Fatal(err)
		}
		snapshot := monitor.Snapshot()
		return nvkedemo.ProgressEvent{Kind: nvkedemo.BuildPlanSelected, Name: name, BuildPlan: &snapshot, BuildGraph: &graph}
	}
	artifactPlan, toolPlan := project(artifacts, "artifacts"), project(tools, "native-tools")
	for _, scenario := range []string{"tools Plan", "oversized prior line", "tools Plan unavailable", "artifact failure"} {
		t.Run(scenario, func(t *testing.T) {
			d := newDashboard()
			w := &checkedWriter{out: d, observer: d, started: time.Now(), phases: make(map[string]time.Time)}
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			w.Report(artifactPlan)
			before := d.snapshot()
			if before.buildRequired != len(artifacts.LifecycleOutputs) ||
				!strings.Contains(dashboardBuildRibbon(before, time.Now()), fmt.Sprintf("Required outputs[-::-] %d", len(artifacts.LifecycleOutputs))) {
				t.Fatal("display lost the exact selected output count")
			}
			step := before.buildSteps[0]
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.ProcessStarted, Name: "build-all"})
			_, _ = fmt.Fprintf(w, "[start] %s (%s)\n[done] %s\n", step.id, step.kind, step.id)
			_, _ = io.WriteString(w, "==> nvke-node-non-fde.qemu.nvke-non-fde: Connected to SSH!\n")
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.ProcessExited, Name: "build-all"})
			outcome := nvkedemo.LaneFinished
			if scenario == "artifact failure" {
				outcome = nvkedemo.LaneFailed
			}
			w.Report(nvkedemo.ProgressEvent{Kind: outcome, Name: "artifacts"})
			prior := "prior unfinished output"
			if scenario == "oversized prior line" {
				prior = strings.Repeat("x", 16<<10+1)
			}
			_, _ = io.WriteString(w, prior)
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			selected := toolPlan
			if scenario == "tools Plan unavailable" {
				selected = nvkedemo.ProgressEvent{Kind: nvkedemo.BuildPlanSelected, Name: "native-tools"}
			}
			w.Report(selected)
			after := d.snapshot()
			if scenario == "artifact failure" {
				if after.phases[1].state != "failed" || after.buildRun != "artifacts" || after.buildSteps[0].state != "done" {
					t.Fatal("a later selected Run masked the failed artifact lane")
				}
				return
			}
			if after.phases[1].state != "running" || after.buildRun != "native-tools" || after.buildProcess != "" ||
				after.buildOutputBytes != 0 || !after.buildOutputAt.IsZero() || after.buildGuestStage != "" || !after.buildChangedAt.IsZero() || len(after.buildLogs) != 0 {
				t.Fatal("new Run retained old completion or telemetry")
			}
			if scenario == "tools Plan unavailable" {
				if after.buildRequired != 0 || len(after.buildSteps) != 0 || len(after.buildVertices) != 0 || len(after.buildEdges) != 0 ||
					!strings.Contains(dashboardBuildRibbon(after, time.Now()), "Required output total unknown") {
					t.Fatal("missing new Plan retained the preceding Run's totals or links")
				}
				return
			}
			if after.buildRequired != len(tools.LifecycleOutputs) || len(after.buildSteps) != len(tools.Graph.Nodes) || len(after.buildEdges) == 0 || after.buildTruncated {
				t.Fatal("native tools did not replace the artifact graph and output count")
			}
			for _, row := range after.buildSteps {
				if row.state != "queued" {
					t.Fatal("planned operation became a completion observation")
				}
			}
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.ProcessStarted, Name: "native-tools-run"})
			step = after.buildSteps[0]
			_, _ = fmt.Fprintf(w, "[start] %s (%s)\n", step.id, step.kind)
			if d.snapshot().buildSteps[0].state != "running" {
				t.Fatal("previous Run's unfinished stdout hid the new operation start")
			}
			_, _ = fmt.Fprintf(w, "[done] %s\n", step.id)
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.ProcessExited, Name: "native-tools-run"})
			after = d.snapshot()
			if after.buildProcess != "exited" || after.buildSteps[0].state != "done" || after.phases[1].state != "running" {
				t.Fatal("tools logs were ignored or process join became artifact completion")
			}
		})
	}
}

func TestDashboardBuildElapsedIsNeverTruncated(t *testing.T) {
	for _, elapsed := range []time.Duration{75 * time.Second, time.Hour + 10*time.Second, time.Hour + 12*time.Minute + 34*time.Second, 10 * time.Hour} {
		t.Run(elapsed.String(), func(t *testing.T) {
			started := time.Now()
			s := dashboardSnapshot{buildSelected: 0, buildSteps: []dashboardBuildStep{{id: "os/build-disk", kind: "build", state: "running", started: started}}}
			for _, text := range []string{dashboardBuildList(s, 5, started.Add(elapsed)), dashboardBuildDetail(s, started.Add(elapsed))} {
				if !strings.Contains(text, elapsed.String()) {
					t.Fatalf("lost complete elapsed duration %s: %s", elapsed, text)
				}
			}
		})
	}
}

func TestDashboardBuildScrollModeTransitionsResetOffset(t *testing.T) {
	for _, tc := range []struct {
		name        string
		key         tcell.Key
		fromDetails bool
		wantDetails bool
	}{
		{"enter details", tcell.KeyEnter, false, true},
		{"enter activity", tcell.KeyEnter, true, false},
		{"escape activity", tcell.KeyEscape, true, false},
		{"end live activity", tcell.KeyEnd, true, false},
		{"end resets activity", tcell.KeyEnd, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDashboard()
			d.phases[1].state = "running"
			d.buildDetails, d.offset = tc.fromDetails, 20
			if d.input(tcell.NewEventKey(tc.key, 0, tcell.ModNone)) {
				t.Fatal("navigation canceled work")
			}
			s := d.snapshot()
			if s.offset != 0 || s.buildDetails != tc.wantDetails {
				t.Fatalf("details=%t offset=%d", s.buildDetails, s.offset)
			}
		})
	}
}

func TestDashboardCheckedWriterEscapesExactlyOnce(t *testing.T) {
	for _, line := range []string{
		`Warning: quoted path "/tmp/example" and slash \`,
		"Warning: controls\t\x1b[2J and [red]markup",
	} {
		t.Run(line, func(t *testing.T) {
			d := newDashboard()
			_, cancel := context.WithCancel(t.Context())
			defer cancel()
			writer := &checkedWriter{out: d, observer: d, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			_, err := io.WriteString(writer, line+"\n")
			activity := d.snapshot().activity
			if err != nil || len(activity) != 1 || !strings.HasSuffix(activity[0], terminalText(line)) || strings.ContainsRune(activity[0], '\x1b') {
				t.Fatalf("activity not once escaped: %q error=%v", activity, err)
			}
			if strings.HasSuffix(activity[0], terminalText(terminalText(line))) {
				t.Fatalf("activity escaped twice: %q", activity)
			}
		})
	}
}

func TestDashboardBuildLogLocatorSurvivesActivityEviction(t *testing.T) {
	for _, building := range []bool{false, true} {
		t.Run(fmt.Sprint(building), func(t *testing.T) {
			d := newDashboard()
			d.buildLogPath = "/tmp/nvke-demo-build-example.log"
			if building {
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			}
			for range dashboardLines + 1 {
				_, _ = io.WriteString(d, "routine activity\n")
			}
			screen := dashboardScreen(t, 160)
			d.draw(screen, time.Now(), false)
			if !strings.Contains(dashboardText(screen), "Private build log: "+d.buildLogPath) {
				t.Fatal("durable locator absent after ring eviction")
			}
		})
	}
}

func TestDashboardReceivesGraphAndHiddenRawOutput(t *testing.T) {
	d := newDashboard()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &checkedWriter{out: d, observer: d, started: time.Now(), phases: make(map[string]time.Time), cancel: cancel}
	w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
	vertices := []planview.Vertex{
		{Key: "operation/os/build-disk", Kind: "operation", Reference: "os/build-disk", Label: "os/build-disk (build)", Build: "os"},
		{Key: "artifact/input", Kind: "artifact", Reference: "input"},
		{Key: "artifact/disk", Kind: "artifact", Reference: "disk"},
	}
	w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.BuildPlanSelected, Name: "artifacts",
		BuildPlan: &progress.Snapshot{Outputs: []progress.Output{{Name: "disk"}}, Operations: []progress.Operation{{ID: "os/build-disk", Kind: "build"}}},
		BuildGraph: &planview.Graph{Vertices: append(vertices, planview.Vertex{Key: "route/private", Kind: "route", Label: "private-route-sentinel"}),
			Edges: []planview.Edge{{From: "artifact/input", To: "operation/os/build-disk", Relation: "consumed by"}, {From: "operation/os/build-disk", To: "artifact/disk", Relation: "produces"}, {From: "artifact/disk", To: "route/private", Relation: "publication route"}}}})
	w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.ProcessStarted, Name: "build-all"})
	_, _ = io.WriteString(w, "[dry-run] os/build-disk (build)\n[start] os/build-disk (build)\n")
	before := d.snapshot()
	_, _ = io.WriteString(w, "compilation output hidden by concise mode\n")
	after := d.snapshot()
	if !after.buildOutputAt.After(before.buildOutputAt) || after.buildOutputBytes <= before.buildOutputBytes ||
		!after.buildChangedAt.Equal(before.buildChangedAt) || len(after.activity) != len(before.activity) || after.buildTruncated {
		t.Fatalf("hidden output and step clocks were conflated: before=%+v after=%+v", before, after)
	}
	if after.buildProcess != "started" || len(after.buildEdges) != 2 || len(after.buildVertices) != 3 || ctx.Err() != nil {
		t.Fatal("owner graph/process observations lost or display canceled work")
	}
	for _, line := range after.activity {
		if strings.Contains(line, "private-route-sentinel") {
			t.Fatal("typed graph exposed a private route in the activity wall")
		}
	}
	detail := dashboardBuildDetail(after, after.buildOutputAt.Add(time.Minute))
	for _, wanted := range []string{"os/build-disk", "artifact/input", "artifact/disk", "→"} {
		if !strings.Contains(detail, wanted) {
			t.Fatalf("missing detail %q: %s", wanted, detail)
		}
	}
	for _, wanted := range []string{"Launcher start observed", "Raw build output", "Child health unobserved", "ETA unknown"} {
		if !strings.Contains(dashboardBuildRibbon(after, after.buildOutputAt.Add(time.Minute)), wanted) {
			t.Fatalf("missing build health %q", wanted)
		}
	}
	w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.ProcessExited, Name: "build-all"})
	if d.snapshot().buildProcess != "exited" || d.snapshot().phases[1].state != "running" {
		t.Fatal("process join promoted artifact completion")
	}
	// Replayed process starts must not resurrect an exited owner.
	w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.ProcessStarted, Name: "build-all"})
	if d.snapshot().buildProcess != "exited" {
		t.Fatal("late process-start regressed owner state")
	}
	after.buildVertices[0].Key = "mutated"
	after.buildEdges[0].From = "mutated"
	if d.snapshot().buildVertices[0].Key == "mutated" || d.snapshot().buildEdges[0].From == "mutated" {
		t.Fatal("graph snapshot shares mutable state")
	}
}

func TestDashboardGraphDisplayBounds(t *testing.T) {
	for _, input := range []struct {
		name   string
		vertex *planview.Vertex
		edge   *planview.Edge
	}{
		{name: "unsupported vertex", vertex: &planview.Vertex{Key: "operation/build", Kind: "route", Reference: "build"}},
		{name: "terminal controls", vertex: &planview.Vertex{Key: "operation/\x1b", Kind: "operation", Reference: "build"}},
		{name: "overlong label", vertex: &planview.Vertex{Key: "artifact/input", Kind: "artifact", Reference: "input", Label: strings.Repeat("a", 513)}},
		{name: "private relation", edge: &planview.Edge{From: "artifact/input", To: "operation/build", Relation: "private-message"}},
	} {
		t.Run(input.name, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			if input.vertex != nil {
				d.observeBuildVertex(*input.vertex)
			} else {
				d.observeBuildEdge(*input.edge)
			}
			if !d.snapshot().buildTruncated || len(d.snapshot().buildVertices) != 0 || len(d.snapshot().buildEdges) != 0 || d.snapshot().phases[1].state != "running" {
				t.Fatal("malformed display was accepted, authorized completion or omitted incomplete status")
			}
		})
	}
}

// Read through the existing catalog owner, not a second overlay parser. A
// source-selector rename must update the display deliberately, rather than
// silently losing the fixed, privacy-safe Packer reports.
func TestDashboardGuestReportsFollowMaintainedPackerSelectors(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), catalog.Dir("../../inputs/catalogs/node-images/release"))
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := catalog.LoadOverlayContext(t.Context(), "../../inputs/catalogs/node-images/variants/nvidia-tcg.json")
	if err != nil {
		t.Fatal(err)
	}
	kvm := base.Components["nvke-node-image"]
	for _, selected := range []struct {
		mode      string
		component *catalog.Component
	}{{"kvm", &kvm}, {"tcg", overlay.Components["nvke-node-image"].Component}} {
		t.Run(selected.mode, func(t *testing.T) {
			component := selected.component
			if component == nil || component.Packer == nil || component.Packer.BuildName == "" {
				t.Fatal("maintained disk selector is unavailable")
			}
			for _, report := range dashboardPackerReports {
				message := report.message
				if report.prefix {
					message += "private-suffix"
				}
				line := "==> " + component.Packer.BuildName + ": " + message
				if got := dashboardGuestReport(line); got != report.summary {
					t.Fatalf("selected %s report lost its projection: got %q, want %q", selected.mode, got, report.summary)
				}
			}
		})
	}
}

func TestDashboardLastGuestReport(t *testing.T) {
	for _, tc := range []struct{ message, want string }{
		{"Starting VM, booting from CD-ROM", "Launching OS-image VM from installer ISO"},
		{"Waiting 30s for boot...", "Waiting for the OS-image boot console"},
		{"Connecting to VM via VNC (127.0.0.1:5908)", "Connecting to the OS-image boot console"},
		{"Typing the boot commands over VNC...", "Sending installer boot commands"},
		{"Waiting for SSH to become available...", "Waiting for SSH from the OS-image guest"},
		{"Connected to SSH!", "SSH connected to the OS-image guest"},
		{"Running local shell script: /private/preparation.sh", "Running a source-owned local shell step"},
		{"Uploading /private/image-inputs => /tmp/image-inputs", "Transferring files to the OS-image guest"},
		{"Provisioning with shell script: /private/provision.sh", "Running guest shell provisioning"},
		{"Gracefully halting virtual machine...", "Shutting down the OS-image guest"},
		{"Waiting for shutdown...", "Waiting for OS-image guest shutdown"},
		{"Running post-processor: shell-local", "Starting final QCOW2 post-processing"},
		{"private guest output", ""},
		{"Provisioning with shell script: ", ""},
		{"Provisioning with shell script: /private/\x1b[2J", ""},
	} {
		t.Run(tc.message, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			w := &checkedWriter{out: d, observer: d, started: time.Now()}
			_, _ = fmt.Fprintf(w, "==> nvke-node-non-fde.qemu.nvke-non-fde: %s\n", tc.message)
			s := d.snapshot()
			if s.buildGuestStage != tc.want || (tc.want == "") != s.buildGuestAt.IsZero() ||
				s.phases[1].state != "running" || len(s.buildSteps) != 0 {
				t.Fatalf("guest last report changed authority or exposed arbitrary output: %+v", s)
			}
			if tc.want != "" {
				_, _ = io.WriteString(w, "unattributed compiler bytes\n")
				if !d.snapshot().buildGuestAt.Equal(s.buildGuestAt) {
					t.Fatal("unrelated output refreshed the guest report")
				}
			}
		})
	}
}

func TestDashboardGuestReportRejectsUnknownAndControls(t *testing.T) {
	for _, line := range []string{
		"==> other-builder: Connected to SSH!",
		"==> nvke-node-non-fde.qemu.nvke-non-fde: Provisioning with shell script: /private/\nsecret",
		"==> nvke-node-non-fde.qemu.nvke-non-fde: Uploading /private/\x7f => /tmp/input",
		"==> nvke-node-non-fde.qemu.nvke-non-fde: Uploading " + strings.Repeat("x", dashboardRunes),
		dashboardGuestReportPrefix + "Guest is healthy; artifacts complete",
		dashboardGuestReportPrefix + "Running guest shell provisioning\r",
	} {
		if got := dashboardGuestReport(line); got != "" {
			t.Fatalf("accepted private or unsupported report %q as %q", line, got)
		}
	}
}

func TestDashboardGuestReportKeepsVariableSuffixPrivate(t *testing.T) {
	for _, message := range []string{
		"Connecting to VM via VNC (10.20.30.40:5908)",
		"Running local shell script: /private/secret-preparation.sh",
		"Uploading /private/secret-input => /tmp/secret-destination",
		"Provisioning with shell script: /private/secret-provision.sh",
	} {
		t.Run(message, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			var public, private bytes.Buffer
			w := &checkedWriter{out: io.MultiWriter(&public, d), raw: &private, remaining: 8192, observer: d, started: time.Now()}
			line := "==> nvke-node-non-fde.qemu.nvke-non-fde: " + message + "\n"
			if _, err := io.WriteString(w, line); err != nil {
				t.Fatal(err)
			}
			if private.String() != line || strings.Contains(public.String(), message) ||
				strings.Contains(public.String(), "/private/") || strings.Contains(public.String(), "10.20.30.40") {
				t.Fatal("variable Packer suffix escaped the retained private log")
			}
			s := d.snapshot()
			if s.buildGuestStage == "" || s.phases[1].state != "running" || len(s.buildSteps) != 0 {
				t.Fatal("fixed report was lost or promoted artifact completion")
			}
		})
	}
}

func TestDashboardStepNavigationDoesNotChangeWork(t *testing.T) {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
	_, _ = io.WriteString(d, "[dry-run] chart/build (build)\n[dry-run] os/build (build)\n[start] os/build (build)\n")
	before := d.snapshot()
	for _, key := range []tcell.Key{tcell.KeyUp, tcell.KeyEnter, tcell.KeyDown} {
		if d.input(tcell.NewEventKey(key, 0, tcell.ModNone)) {
			t.Fatal("pipeline navigation canceled work")
		}
	}
	if d.snapshot().buildSelected != 0 || !d.snapshot().buildDetails || d.snapshot().phases != before.phases {
		t.Fatal("navigation lost selection or changed observed state")
	}
	events := strings.Join(dashboardSelectedEvents(d.snapshot()), "\n")
	if strings.Contains(events, "os/build") || !strings.Contains(events, "chart/build") || !strings.Contains(events, "Raw parallel output is shared") {
		t.Fatal("selected events misattributed other steps or raw logs")
	}
	d.input(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone))
	if d.snapshot().buildSelected != -1 || d.snapshot().buildDetails {
		t.Fatal("all-activity action did not release pinned selection")
	}
}

func TestDashboardRunningFirstSelection(t *testing.T) {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
	for _, id := range []string{"completed/build", "queued/build", "os/build", "chart/build"} {
		_, _ = fmt.Fprintf(d, "[dry-run] %s (build)\n", id)
	}
	_, _ = io.WriteString(d, "[done] completed/build\n[start] os/build (build)\n[start] chart/build (build)\n")
	s := d.snapshot()
	order := dashboardBuildOrder(s)
	if fmt.Sprint(order) != "[2 3 1 0]" {
		t.Fatalf("concurrent work buried in preview order: %v", order)
	}
	if d.input(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)) || d.snapshot().buildSelected != 3 {
		t.Fatal("navigation does not follow displayed order")
	}
	_, _ = io.WriteString(d, "[done] chart/build\n")
	if dashboardSelectedStep(d.snapshot()) != 3 || d.snapshot().buildSteps[3].id != "chart/build" {
		t.Fatal("state change selected a different operation")
	}
	if d.input(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)) || d.snapshot().buildDetails {
		t.Fatal("closing build details canceled the demo")
	}
}

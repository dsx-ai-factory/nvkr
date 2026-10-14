// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/planview"
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

func graphRecord(t *testing.T, out io.Writer, prefix string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(out, "%s%s\n", prefix, raw); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardReceivesGraphAndHiddenRawOutput(t *testing.T) {
	d := newDashboard()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &checkedWriter{out: d, observer: d, started: time.Now(), phases: make(map[string]time.Time), cancel: cancel}
	w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
	w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.ProcessStarted, Name: "build-all"})
	vertices := []planview.Vertex{
		{Key: "operation/os/build-disk", Kind: "operation", Reference: "os/build-disk", Label: "os/build-disk (build)", Build: "os"},
		{Key: "artifact/input", Kind: "artifact", Reference: "input"},
		{Key: "artifact/disk", Kind: "artifact", Reference: "disk"},
	}
	for _, vertex := range vertices {
		graphRecord(t, w, "Build vertex: ", vertex)
	}
	for _, edge := range []planview.Edge{{From: "artifact/input", To: "operation/os/build-disk", Relation: "consumed by"}, {From: "operation/os/build-disk", To: "artifact/disk", Relation: "produces"}} {
		graphRecord(t, w, "Build edge: ", edge)
	}
	graphRecord(t, w, "Build display: ", map[string]any{"vertices": 3, "edges": 2, "truncated": false})
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
		if strings.Contains(line, "Build vertex:") || strings.Contains(line, "Build edge:") {
			t.Fatal("graph records flooded the activity wall")
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
	for _, input := range []string{
		`Build vertex: {"key":"operation/build","kind":"route","reference":"build"}`,
		`Build vertex: {"key":"operation/\u001b","kind":"operation","reference":"build"}`,
		`Build vertex: {"key":"artifact/input","kind":"artifact","reference":"input","label":"` + strings.Repeat("a", 513) + `"}`,
		`Build edge: {"from":"artifact/input","to":"operation/build","relation":"private-message"}`,
		`Build display: {"vertices":1,"edges":0,"truncated":false}`,
		`Build vertex: {invalid-json}`,
	} {
		t.Run(input[:min(80, len(input))], func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
			_, _ = io.WriteString(d, input+"\n")
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
	base, err := catalog.LoadContext(t.Context(), catalog.Dir("../../definitions/nvke-m2/os-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := catalog.LoadOverlayContext(t.Context(), "../../definitions/nvke-local-dev-arm64/os-tcg.json")
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

func TestDashboardMaintainedPlanThroughLauncherProjection(t *testing.T) {
	t.Chdir("../..")
	for _, small := range []bool{false, true} {
		name := "complete M2 truncated"
		if small {
			name = "real CLI selection complete"
		}
		t.Run(name, func(t *testing.T) {
			var plan *factory.Plan
			var err error
			if small {
				const definitionPath = "definitions/nvke-v1/definition.yaml"
				const environmentPath = "definitions/nvke-v1/environment-github.yaml"
				authored, loadErr := definition.LoadDefinition(t.Context(), definitionPath)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				authored.Outputs = slices.DeleteFunc(authored.Outputs, func(output definition.DefinitionOutput) bool { return output.Name != "nvkectl" })
				if len(authored.Outputs) != 1 {
					t.Fatal("real CLI output is absent")
				}
				environment, loadErr := definition.LoadEnvironment(t.Context(), environmentPath)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				plan, err = factory.CompileDocuments(t.Context(), definitionPath, authored, environmentPath, environment, "test")
			} else {
				plan, err = factory.LoadPlanContext(t.Context(), ".github/nvkr-run/plan.json")
			}
			if err != nil {
				t.Fatal(err)
			}
			assertDashboardLauncherProjection(t, plan, !small)
		})
	}
}

func assertDashboardLauncherProjection(t *testing.T, plan *factory.Plan, wantTruncated bool) {
	t.Helper()
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("launcher projection integration requires jq")
	}
	doc, err := planview.Inspect(plan)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := doc.Graph(planview.GraphOptions{View: "complete", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("scripts/nvke-full-arm64-local.sh")
	if err != nil {
		t.Fatal(err)
	}
	// Execute the launcher's actual jq expression, not a copied projection or
	// fabricated vertex/edge fixture. Exact delimiters fail on script drift.
	start := "# verification. Preserve owner keys/edges; omit route refs and producer duplicates.\njq -r '\n"
	end := "\n' \"${bootstrap}/graph.json\" >&2"
	if strings.Count(string(script), start) != 1 {
		t.Fatal("launcher graph projection boundary changed")
	}
	_, projection, _ := strings.Cut(string(script), start)
	expression, _, found := strings.Cut(projection, end)
	if !found {
		t.Fatal("launcher graph projection terminator changed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, jq, "-r", expression)
	command.Stdin = bytes.NewReader(graph)
	command.WaitDelay = time.Second
	raw, err := command.Output()
	if err != nil {
		t.Fatal("actual launcher projection failed", err)
	}
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "artifacts"})
	if _, err := d.Write(raw); err != nil {
		t.Fatal(err)
	}
	s := d.snapshot()
	// The maintained selection may outgrow the launcher's deliberate display
	// bounds. Preserve its explicit incomplete-display marker, not an assumption
	// that every complete Release fits in the diagnostic view.
	_, summary, found := strings.Cut(string(raw), "Build display: ")
	var totals struct {
		Vertices  int  `json:"vertices"`
		Edges     int  `json:"edges"`
		Truncated bool `json:"truncated"`
	}
	if !found || json.Unmarshal([]byte(summary), &totals) != nil {
		t.Fatal("launcher projection omitted its bounded-display summary")
	}
	if totals.Truncated != wantTruncated {
		t.Fatalf("real Plan display truncation = %t, want %t", totals.Truncated, wantTruncated)
	}
	if s.buildTruncated != totals.Truncated || len(s.buildVertices) != totals.Vertices ||
		len(s.buildEdges) != totals.Edges || len(s.buildVertices) == 0 || len(s.buildEdges) == 0 || s.phases[1].state != "running" {
		t.Fatal("maintained Plan projection lost links or promoted execution authority")
	}
	vertices := make(map[string]bool, len(s.buildVertices))
	for _, vertex := range s.buildVertices {
		vertices[vertex.Key] = true
	}
	for _, edge := range s.buildEdges {
		if !vertices[edge.From] || !vertices[edge.To] {
			t.Fatal("launcher emitted a dangling Plan link")
		}
	}
	// Compute membership from the typed complete graph, independently of the
	// launcher's emitted summary and the dashboard's parser. Apply limits before
	// text filtering, matching the documented bounded display contract.
	complete, err := doc.GraphSnapshot("complete")
	if err != nil {
		t.Fatal(err)
	}
	var operations, artifacts []planview.Vertex
	for _, vertex := range complete.Vertices {
		switch vertex.Kind {
		case "operation":
			operations = append(operations, vertex)
		case "artifact", "output":
			artifacts = append(artifacts, vertex)
		}
	}
	wantVertices := append(slices.Clone(operations[:min(512, len(operations))]), artifacts[:min(1024, len(artifacts))]...)
	wantVertices = slices.DeleteFunc(wantVertices, func(vertex planview.Vertex) bool {
		return utf8.RuneCountInString(vertex.Key) > 256 || utf8.RuneCountInString(vertex.Reference) > 256 ||
			utf8.RuneCountInString(vertex.Build) > 256 || utf8.RuneCountInString(vertex.Label) > 512
	})
	retained := make(map[string]bool, len(wantVertices))
	for _, vertex := range wantVertices {
		retained[vertex.Key] = true
	}
	var wantEdges []planview.Edge
	for _, edge := range complete.Edges {
		if retained[edge.From] && retained[edge.To] && utf8.RuneCountInString(edge.Relation) <= 64 && len(wantEdges) < 4096 {
			wantEdges = append(wantEdges, edge)
		}
	}
	if !slices.Equal(s.buildVertices, wantVertices) || !slices.Equal(s.buildEdges, wantEdges) {
		t.Fatal("launcher/dashboard projection differs from independently retained real Plan membership")
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
			_, _ = fmt.Fprintf(w, "==> nke-node-non-fde.qemu.nke-non-fde: %s\n", tc.message)
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
		"==> nke-node-non-fde.qemu.nke-non-fde: Provisioning with shell script: /private/\nsecret",
		"==> nke-node-non-fde.qemu.nke-non-fde: Uploading /private/\x7f => /tmp/input",
		"==> nke-node-non-fde.qemu.nke-non-fde: Uploading " + strings.Repeat("x", dashboardRunes),
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
			line := "==> nke-node-non-fde.qemu.nke-non-fde: " + message + "\n"
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

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/planview"
	"github.com/gdamore/tcell/v2"
)

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
	w.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
	w.Report(nkedemo.ProgressEvent{Kind: nkedemo.ProcessStarted, Name: "build-all"})
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
	w.Report(nkedemo.ProgressEvent{Kind: nkedemo.ProcessExited, Name: "build-all"})
	if d.snapshot().buildProcess != "exited" || d.snapshot().phases[1].state != "running" {
		t.Fatal("process join promoted artifact completion")
	}
	// Replayed process starts must not resurrect an exited owner.
	w.Report(nkedemo.ProgressEvent{Kind: nkedemo.ProcessStarted, Name: "build-all"})
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
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
			_, _ = io.WriteString(d, input+"\n")
			if !d.snapshot().buildTruncated || len(d.snapshot().buildVertices) != 0 || len(d.snapshot().buildEdges) != 0 || d.snapshot().phases[1].state != "running" {
				t.Fatal("malformed display was accepted, authorized completion or omitted incomplete status")
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
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
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
			d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
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
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
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
	d.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "artifacts"})
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

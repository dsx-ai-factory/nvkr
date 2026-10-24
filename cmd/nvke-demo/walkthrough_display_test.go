// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/buildactivity"
	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestWalkthroughPagesStayVisibleAndSurviveLaterLiveStatus(t *testing.T) {
	access, runner := cliOperatorFixture(t)
	_, cancel := context.WithCancel(t.Context())
	defer cancel()
	view := newDashboard()
	w := &checkedWriter{out: view, observer: view, cancel: cancel}
	page := "Walkthrough page 2/5 · observed before cleanup\nSite / Kind management\n$ kubectl --kubeconfig <site> get pods -A -o json\nNAMESPACE POD PHASE READY RESTARTS\n"
	for i := 0; i < 12; i++ {
		page += fmt.Sprintf("default pod-%03d Running 1/1 0\n", i)
	}
	if err := w.writeOperatorPresentation(page, true); err != nil {
		t.Fatal(err)
	}
	for range dashboardLines + 1 {
		view.appendActivity("cleanup activity")
	}
	screen := dashboardScreen(t, 160)
	screen.SetSize(160, 42)
	view.draw(screen, time.Now(), false)
	for _, want := range []string{"get pods -A", "pod-000", "pod-011", "before cleanup"} {
		if !strings.Contains(dashboardText(screen), want) {
			t.Fatal("dedicated walkthrough page missing", want)
		}
	}
	if err := access.WriteLiveStatus(t.Context(), runner, w); err != nil {
		t.Fatal(err)
	}
	if view.snapshot().operatorPage != "" {
		t.Fatal("live view did not release walkthrough page")
	}
	var restored bytes.Buffer
	if err := finishDashboardProgress(w, &restored, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"get pods -A", "pod-000", "pod-011", "Live Kubernetes checks", "historical observations"} {
		if !strings.Contains(restored.String(), want) {
			t.Fatal("later live status erased walkthrough replay", want)
		}
	}
}

func TestWalkthroughPresentationAggregateBound(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out bytes.Buffer
	w := &checkedWriter{out: &out, cancel: cancel}
	if err := w.writeOperatorPresentation(strings.Repeat("x", 64<<10), true); err != nil {
		t.Fatal(err)
	}
	if err := w.writeOperatorPresentation(strings.Repeat("x", 64<<10), true); err == nil || ctx.Err() == nil {
		t.Fatal("unbounded walkthrough retained")
	}
}

func TestFailedWalkthroughReleasesPageForCleanupAndRetainsReplay(t *testing.T) {
	for _, width := range []int{80, 160} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			_, cancel := context.WithCancel(t.Context())
			defer cancel()
			view := newDashboard()
			view.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentFinished, Name: "tenant-provisioning"})
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			page := "Walkthrough page 2/5 · observed before cleanup\nSite / Kind management\n$ kubectl --kubeconfig <site> get pods -A -o json\nObserved partial pod inventory\n"
			if err := writer.writeOperatorPresentation(page, true); err != nil {
				t.Fatal(err)
			}
			screen := dashboardScreen(t, width)
			screen.SetSize(width, 42)
			view.draw(screen, time.Now(), false)
			if !strings.Contains(dashboardText(screen), "Recorded kubectl walkthrough") {
				t.Fatal("walkthrough page was not initially visible")
			}
			// Background observations can arrive during the page's display interval.
			// They must not dismiss it as if the workflow had advanced.
			for _, event := range []nvkedemo.ProgressEvent{
				{Kind: nvkedemo.OSBuildLogs, BuildLogs: buildactivity.Record{Version: 1, Operation: "os/build-disk"}},
				{Kind: nvkedemo.TenantGuests, Guests: [3]nvkedemo.TenantGuestObservation{{Role: nvkedemo.TenantGuestControlPlane}, {Role: nvkedemo.TenantGuestWorker}}},
				{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", RegisteredNodes: 2, ReadyNodes: 2},
				{Kind: nvkedemo.ProcessStarted, Name: "build-all"},
				{Kind: nvkedemo.ProcessExited, Name: "build-all"},
				{Kind: "unknown", Name: "tenant-cleanup"},
			} {
				writer.Report(event)
				if view.snapshot().operatorPage != page {
					t.Fatalf("metadata %q dismissed the walkthrough page", event.Kind)
				}
			}
			// The owner can fail before WriteLiveStatus and still enter cleanup.
			// No successful final snapshot is needed to release this partial page.
			failure := nvkrerr.New(nvkrerr.CodePreflightFailed, "walkthrough observation failed")
			writer.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: "tenant-cleanup"})
			view.draw(screen, time.Now(), false)
			text := dashboardText(screen)
			if !strings.Contains(text, "Clean up the owned tenant") || strings.Contains(text, "Recorded kubectl walkthrough") {
				t.Fatal("cleanup hidden behind the failed walkthrough page", text)
			}
			if snapshot := view.snapshot(); snapshot.operatorPage != "" || snapshot.phases[5].state != "running" {
				t.Fatal("cleanup did not replace the dedicated page", snapshot.operatorPage, snapshot.phases[5])
			}
			var restored bytes.Buffer
			if err := finishDashboardProgress(writer, &restored, "", failure, nil); err != failure {
				t.Fatal("presentation changed the owning failure", err)
			}
			if !strings.Contains(restored.String(), page) || !strings.Contains(restored.String(), "historical observations") {
				t.Fatal("cleanup discarded partial walkthrough replay", &restored)
			}
			if strings.Contains(restored.String(), "Demo command returned successfully") {
				t.Fatal("failed walkthrough replay claimed success")
			}
		})
	}
}

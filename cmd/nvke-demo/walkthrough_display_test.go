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

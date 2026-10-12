// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

func TestDashboardRegistryRequiresOwnerAdmission(t *testing.T) {
	for _, row := range []struct {
		name, terminal, state string
	}{
		{"curl returned but body unadmitted", "", "running"},
		{"body rejected", nvkedemo.PhaseFailed, "failed"},
		{"owner admitted route and exact body", nvkedemo.PhaseFinished, "observed"},
	} {
		t.Run(row.name, func(t *testing.T) {
			d := newDashboard()
			for _, event := range []nvkedemo.ProgressEvent{
				{Kind: nvkedemo.PhaseStarted, Name: "registry-admission"},
				{Kind: nvkedemo.PhaseFinished, Name: "start-registry"},
				{Kind: nvkedemo.PhaseFinished, Name: "node-registry-connectivity"},
			} {
				d.Report(event)
			}
			if row.terminal != "" {
				d.Report(nvkedemo.ProgressEvent{Kind: row.terminal, Name: "registry-admission"})
			}
			state, _, _ := d.snapshot().siteStages[1].status(2)
			if state != row.state {
				t.Fatalf("registry=%s, want %s", state, row.state)
			}
		})
	}
}

func TestDashboardOVNSiteNetworkChecklistUsesCompletedObservation(t *testing.T) {
	for _, row := range []struct {
		name, failed string
		ready        bool
		want         string
	}{
		{"Calico hides OVN row", "", false, "hidden"},
		{"OVS still starting", "site-ovs-ready", false, "failed"},
		{"OVN node failure visible", "site-ovn-node-ready", true, "failed"},
		{"node ready without image observation", "", true, "running"},
		{"images rejected", "site-ovn-admission", true, "failed"},
		{"exact live image observation", "complete", true, "observed"},
	} {
		t.Run(row.name, func(t *testing.T) {
			d := newDashboard()
			if row.want != "hidden" {
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: "site-ovs-ready"})
				if row.ready {
					d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "site-ovs-ready"})
					d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: "site-ovn-install"})
					d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "site-ovn-node-ready"})
					// A successful API command still leaves its body/image IDs
					// unadmitted until the source owner completes the check.
					d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "site-ovn-observation"})
				}
				if row.failed == "complete" {
					d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "site-ovn-admission"})
				} else if row.failed != "" {
					d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFailed, Name: row.failed})
				}
			}
			checklist, _ := dashboardChecklist(d.snapshot(), time.Now())
			shown := strings.Contains(checklist, "Site networking (OVN)")
			if row.want == "hidden" {
				if shown || !strings.Contains(checklist, "0/11 checked") {
					t.Fatalf("unused networking row shown: %s", checklist)
				}
				return
			}
			state, _, _ := d.snapshot().siteStages[2].status(2)
			if !shown || state != row.want || !strings.Contains(checklist, "/12 checked") {
				t.Fatalf("network=%s want=%s:\n%s", state, row.want, checklist)
			}
		})
	}
}

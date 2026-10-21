// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

func TestDashboardNativeChecklistUsesIndividualOwnerBoundaries(t *testing.T) {
	for selected, stage := range dashboardNativeSiteStages {
		t.Run(stage.operations[0], func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
			// The former coarse event must not complete missing native stages.
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "site-05-install-site-controller"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: stage.operations[0]})
			if state, _, _ := d.snapshot().nativeStages[selected].status(1); state != "running" {
				t.Fatal("actual stage start missing", state)
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: stage.operations[0]})
			snapshot := d.snapshot()
			for index, row := range snapshot.nativeStages {
				state, _, _ := row.status(1)
				if (state == "observed") != (index == selected) {
					t.Fatalf("stage %d inferred another operation's result: %s", index, state)
				}
			}
			checklist, _ := dashboardChecklist(snapshot, time.Now())
			for _, forbidden := range []string{"Create Kind cluster", "Registry reachable", "Secure and register"} {
				if strings.Contains(checklist, forbidden) {
					t.Fatal("native view retained legacy stage", forbidden)
				}
			}
			if !strings.Contains(checklist, "Native site stages · 1/7 checked") || snapshot.phases[2].state == "complete" {
				t.Fatal("partial native stages incorrectly completed the site", checklist)
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "native-site-ready"})
			if d.snapshot().phases[2].state != "complete" {
				t.Fatal("owning site's successful outcome not shown")
			}
		})
	}
}

func TestDashboardNativeConformanceAndRetention(t *testing.T) {
	for _, scenario := range []struct {
		kind, state, retained string
	}{
		{nvkedemo.PhaseStarted, "running", ""},
		{nvkedemo.PhaseFinished, "passed", "available"},
		{nvkedemo.PhaseFailed, "failed", "conformance-failed"},
	} {
		t.Run(scenario.state, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
			if d.snapshot().conformance.state != "pending" {
				t.Fatal("requested conformance omitted before execution")
			}
			d.Report(nvkedemo.ProgressEvent{Kind: scenario.kind, Name: "native-conformance"})
			if scenario.retained != "" {
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeRetained, Status: scenario.retained})
			}
			snapshot := d.snapshot()
			phases := dashboardNamedPhases(snapshot)
			if len(phases) != 7 || phases[5].name != "Conformance" || phases[5].phase.state != scenario.state || phases[6].name != "Cleanup" {
				t.Fatal("conformance/lifetime phase order or outcome lost", phases)
			}
			if snapshot.phases[5].state != "pending" || snapshot.retained != (scenario.retained != "") {
				t.Fatal("retention claimed cleanup", snapshot.phases[5], snapshot.retained)
			}
			if scenario.retained == "conformance-failed" && !strings.Contains(snapshot.current, "failed; cluster retained") {
				t.Fatal("suite failure hidden by retention", snapshot.current)
			}
		})
	}
}

func TestDashboardNativeProviderStatusDoesNotClaimQEMULiveness(t *testing.T) {
	d := newDashboard()
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-kubeconfig", ExpectedNodes: 3, Remaining: time.Hour})
	guests := [3]nvkedemo.TenantGuestObservation{}
	for index := range guests {
		role := nvkedemo.TenantGuestWorker
		if index == 0 {
			role = nvkedemo.TenantGuestControlPlane
		}
		guests[index] = nvkedemo.TenantGuestObservation{Role: role, Available: true, Domain: nvkedemo.TenantDomainUnknown, ProviderReady: nvkedemo.TenantConditionTrue, InstanceState: nvkedemo.TenantInstanceReady}
	}
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantGuests, Name: "native", ExpectedNodes: 3, Guests: guests})
	text := dashboardTenantSummary(d.snapshot(), time.Now())
	for _, want := range []string{"Worker 1", "Worker 2", "NICo Ready", "Infra Ready=True", "VM state unobserved", "Kubernetes: registered unknown", "Provider/VM snapshot:"} {
		if !strings.Contains(text, want) {
			t.Fatal("native observation missing", want, text)
		}
	}
	if strings.Contains(text, "VM running") || strings.Contains(text, "VM unavailable") || d.snapshot().tenantStages[0].states[0] != "running" {
		t.Fatal("provider observation claimed VM or tenant readiness", text)
	}
	bad := guests
	bad[0].InstanceState = "private-arbitrary-status"
	d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantGuests, Guests: bad})
	if d.snapshot().guests != guests {
		t.Fatal("unmapped private provider status entered public model")
	}
}

func TestCheckedWriterForwardsNativeConfigurationAndRetention(t *testing.T) {
	d := newDashboard()
	writer := &checkedWriter{out: d, observer: d}
	nativePresentation(writer, true)
	writer.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeRetained, Status: "conformance-failed"})
	if !d.snapshot().nativeSite || !d.snapshot().conformanceSelected || !d.snapshot().retained {
		t.Fatal("typed native configuration/lifetime was dropped before dashboard")
	}
	var plain bytes.Buffer
	writer = &checkedWriter{out: &plain}
	writer.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeRetained, Status: "private-status"})
	if plain.Len() != 0 {
		t.Fatal("unknown private lifetime status was exposed")
	}
	writer.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeRetained, Status: "conformance-failed"})
	if !strings.Contains(plain.String(), "Conformance failed; cluster retained") {
		t.Fatal("plain presentation lost failed-suite retention", plain.String())
	}
}

func TestDashboardNativeSequentialTenantStartsWithFreshObservations(t *testing.T) {
	for _, outcome := range []string{nvkedemo.PhaseFinished, nvkedemo.PhaseFailed} {
		t.Run(outcome, func(t *testing.T) {
			d := newDashboard()
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "native-site-ready"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentStarted, Name: "tenant-provisioning"})
			for _, stage := range []string{"tenant-kubeconfig", "tenant-api", "tenant-nodes", "tenant-workload"} {
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: stage, ExpectedNodes: 3, RegisteredNodes: 3, ReadyNodes: 3, Elapsed: time.Minute, Remaining: time.Hour})
			}
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentFinished, Name: "tenant-provisioning"})
			d.Report(nvkedemo.ProgressEvent{Kind: outcome, Name: "native-conformance"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeRetained, Status: "available"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "tenant-cleanup"})
			d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentStarted, Name: "tenant-provisioning"})
			snapshot := d.snapshot()
			if snapshot.phases[2].state != "complete" || snapshot.phases[3].state != "running" || snapshot.phases[4].state != "pending" || snapshot.phases[5].state != "pending" || snapshot.conformance.state != "pending" {
				t.Fatal("new tenant reused outcomes or reset the site", snapshot.phases, snapshot.conformance)
			}
			if snapshot.nodesObserved || snapshot.registered != 0 || snapshot.ready != 0 || snapshot.retained || !snapshot.observedAt.IsZero() || !snapshot.deadline.IsZero() || !snapshot.guestsObservedAt.IsZero() {
				t.Fatal("new tenant reused old observations")
			}
			for _, stage := range snapshot.tenantStages {
				if state, checked, _ := stage.status(1); state != "pending" || checked != 0 {
					t.Fatal("previous tenant checkmark survived", state, checked)
				}
			}
		})
	}
}

func TestTenantCloudInitProgressIsVisibleInBothPresenters(t *testing.T) {
	for _, mode := range []string{"plain", "dashboard"} {
		t.Run(mode, func(t *testing.T) {
			event := nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-cloud-init", ExpectedNodes: 3, RegisteredNodes: 3, ReadyNodes: 3, Remaining: time.Hour}
			var text string
			if mode == "plain" {
				var output bytes.Buffer
				writer := &checkedWriter{out: &output}
				writer.Report(event)
				text = output.String()
			} else {
				d := newDashboard()
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: true, ExpectedNodes: 3})
				d.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", ExpectedNodes: 3, RegisteredNodes: 3, ReadyNodes: 3})
				d.Report(event)
				text = dashboardTenantSummary(d.snapshot(), time.Now())
				if d.snapshot().phases[4].state != "pending" || d.snapshot().conformance.state != "pending" {
					t.Fatal("host cloud-init observation claimed workload or conformance completion")
				}
			}
			if !strings.Contains(text, "cloud-init") {
				t.Fatal("host cloud-init gate missing from presentation", text)
			}
		})
	}
}

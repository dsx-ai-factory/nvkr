// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

// Each row completes only at its own typed operation boundary. The bootstrap
// command owns its temporary Kind cluster; it is not the native site VM.
var dashboardNativeSiteStages = [...]struct {
	label      string
	operations [2]string
}{
	{"Site guest Ready (NICo)", [2]string{"native-site-guest"}},
	{"Site packages prepared", [2]string{"native-site-preparation"}},
	{"NVKE site bootstrap", [2]string{"host-nvke-bootstrap"}},
	{"Cluster API installed", [2]string{"host-site-capi"}},
	{"Site stack installed", [2]string{"host-nvke-deploy"}},
	{"CAPI SPIFFE authentication", [2]string{"host-site-spiffe-exec"}},
	{"NVKE API accessible", [2]string{"check-05-install-site-controller"}},
}

func nativePresentation(progress io.Writer, conformance bool) {
	if reporter, ok := progress.(nvkedemo.ProgressReporter); ok {
		reporter.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.NativeDeployment, Conformance: conformance})
	}
}

func dashboardNativeOperation(name string) (int, string) {
	if name == "native-site-ready" {
		return 2, "Prepare and check the native site"
	}
	for _, stage := range dashboardNativeSiteStages {
		if stage.operations[0] == name {
			return 2, stage.label
		}
	}
	return -1, ""
}

func (d *dashboard) observeNativeSiteStage(event nvkedemo.ProgressEvent, state string) string {
	for index, stage := range dashboardNativeSiteStages {
		if event.Name == stage.operations[0] {
			d.nativeStages[index].observe(0, state, time.Now())
			return stage.label
		}
	}
	return ""
}

func (d *dashboard) nativeProgress(event nvkedemo.ProgressEvent) bool {
	if d.nativeSite && event.Name == "tenant-provisioning" && event.Kind == nvkedemo.DeploymentStarted {
		d.resetNativeTenant()
	}
	switch event.Kind {
	case nvkedemo.NativeDeployment:
		if event.ExpectedNodes != 0 && (requestedTenantNodes(event.ExpectedNodes) == 0 || (event.ExpectedNodes == 3) != event.Conformance) {
			return true
		}
		d.nativeSite = true
		d.conformanceSelected = event.Conformance
		if event.ExpectedNodes != 0 {
			d.expectedNodes = event.ExpectedNodes
		}
		if d.conformance.state == "" {
			d.conformance = dashboardPhase{"pending", "Requested; suite has not started"}
		}
		return true
	case nvkedemo.NativeRetained:
		if event.Status != "available" && event.Status != "conformance-failed" {
			return true
		}
		d.retained = true
		d.tenant = "Workload and DNS checks completed; the same tenant remains available."
		d.current, d.currentStarted = "Environment retained for debugging", time.Time{}
		if event.Status == "conformance-failed" {
			d.current = "Conformance failed; cluster retained for debugging"
		}
		d.phases[5] = dashboardPhase{"pending", "Foreground retention; Ctrl-C starts owned cleanup"}
		d.appendActivity(d.current)
		return true
	}
	if event.Name != "native-conformance" {
		return false
	}
	state := ""
	switch event.Kind {
	case nvkedemo.PhaseStarted:
		state = "running"
	case nvkedemo.PhaseFinished:
		state = "passed"
	case nvkedemo.PhaseFailed:
		state = "failed"
	default:
		return true
	}
	d.conformanceSelected = true
	d.conformance = dashboardPhase{state, "Conformance validation, including artifact/tool admission and original report capture"}
	d.current = "Kubernetes conformance · " + state
	d.currentStarted = time.Time{}
	if state == "running" {
		d.currentStarted = time.Now()
	}
	d.appendActivity(d.current)
	return true
}

// A site-scoped Go caller may create another tenant after natural deletion.
// Keep the site observations, but never reuse a previous tenant's readiness or
// suite/cleanup outcome for that fresh operation.
func (d *dashboard) resetNativeTenant() {
	d.tenantStages = [len(dashboardTenantStages)]dashboardStage{}
	d.guests = [3]nvkedemo.TenantGuestObservation{}
	d.observedAt, d.deadline, d.tenantChangedAt, d.guestsObservedAt, d.tenantActivityAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
	d.elapsed, d.registered, d.ready = 0, 0, 0
	d.nodesObserved, d.retained = false, false
	d.tenantStage, d.provisioning, d.provisioned = "", "", ""
	d.tenant = "Tenant readiness has not been observed."
	for _, index := range []int{3, 4, 5} {
		d.phases[index] = dashboardPhase{"pending", "No operation observed for this tenant yet"}
	}
	d.conformance = dashboardPhase{"pending", "Requested; suite has not started"}
}

type dashboardNamedPhase struct {
	name  string
	phase dashboardPhase
}

func dashboardNamedPhases(s dashboardSnapshot) []dashboardNamedPhase {
	var result []dashboardNamedPhase
	for index, name := range []string{"Prepare", "Artifacts", "Site", "Tenant", "Workload/DNS", "Cleanup"} {
		if index == 5 && s.conformanceSelected {
			result = append(result, dashboardNamedPhase{"Conformance", s.conformance})
		}
		result = append(result, dashboardNamedPhase{name, s.phases[index]})
	}
	return result
}

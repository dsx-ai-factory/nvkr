// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeSessionSharesTenantAndConformanceProgressAcrossSiteReuse(t *testing.T) {
	attempt, runner, root := nativeSessionFixture(t, true, 0)
	writer := &recordedProgress{}
	runner.Progress = writer
	ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
	defer cancel()
	err := withNativeSiteScope(ctx, &attempt, runner, runner.Work, runner.Work, func(ctx context.Context, site *NativeSite) error {
		for _, name := range []string{"first", "second"} {
			tenant, err := site.CreateTenant(ctx, name)
			if err != nil {
				return err
			}
			owner := attempt.tenantOwner
			entries, err := os.ReadDir(owner.session.Work)
			if err != nil {
				return err
			}
			// A failed pre-launch diagnostic may consume a number without a log.
			lastSequence := 0
			for _, entry := range entries {
				if !strings.HasSuffix(entry.Name(), ".log") {
					continue
				}
				var sequence int
				if _, err := fmt.Sscanf(entry.Name(), "tenant-%d-", &sequence); err != nil || sequence <= lastSequence {
					return fmt.Errorf("tenant command capture left its increasing owner sequence at %q after %d", entry.Name(), lastSequence)
				}
				lastSequence = sequence
			}
			if lastSequence == 0 {
				return fmt.Errorf("tenant command capture retained no private logs")
			}
			if err := tenant.RunConformance(ctx, filepath.Join(root, name+"-suite")); CommandExitCode(err) != 22 {
				return fmt.Errorf("failed fixture suite outcome changed: %v", err)
			}
			if _, err := tenant.Kubeconfig(ctx); err != nil {
				return fmt.Errorf("suite failure discarded the live tenant: %w", err)
			}
			if err := tenant.Delete(ctx); err != nil {
				return err
			}
			// Cleanup reuses the sequence while retaining bounded observations
			// separately; it deliberately does not create per-command log files.
			if owner.cleanup.LastCommand == nil || owner.cleanup.LastCommand.Sequence <= lastSequence {
				return fmt.Errorf("tenant cleanup did not continue its command capture sequence")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[[2]string]int{}
	guestSnapshots := 0
	for _, event := range writer.events {
		if strings.HasPrefix(event.Name, "native-site-") {
			t.Fatalf("tenant command emitted mechanical site progress: %v", event)
		}
		counts[[2]string{event.Name, event.Kind}]++
		if event.Kind == TenantGuests {
			guestSnapshots++
			if event.Name != "native" || event.ExpectedNodes != 3 {
				t.Fatal("site-scoped API used a different tenant observation path", event)
			}
		}
	}
	for _, event := range [][2]string{
		{"tenant-provisioning", DeploymentStarted},
		{"tenant-provisioning", DeploymentFinished},
		{"native-conformance", PhaseStarted},
		{"native-conformance", PhaseFailed},
	} {
		if counts[event] != 2 {
			t.Fatalf("shared engine lost per-tenant event %v: count %d", event, counts[event])
		}
	}
	if guestSnapshots < 2 || counts[[2]string{"native-conformance", PhaseFinished}] != 0 {
		t.Fatal("missing observations or failed suite presented as completed", guestSnapshots, counts)
	}
}

// A fresh owned NVKE row must stop Secret/node polling when provisioning dies.
// Nonterminal controller states still have to pass the separate runtime gates.
func TestNativeTenantRefreshesOwnedConditionsBeforeReadiness(t *testing.T) {
	starting := nativeTenantRowObservation{Provisioning: TenantConditionTrue, Provisioned: TenantConditionFalse}
	failed := nativeTenantRowObservation{Provisioning: TenantConditionFalse, Provisioned: TenantConditionFalse}
	for _, tc := range []struct {
		name                                                                    string
		observations                                                            []nativeTenantRowObservation
		wantError, failedStage                                                  string
		wantSecret, wantNodes, quiet, nodesUnready, nodeReadFails, readDeadline bool
	}{
		{name: "failed before Secret", observations: []nativeTenantRowObservation{starting, failed}, wantError: "provisioning failed", failedStage: "tenant-kubeconfig"},
		{name: "failed without presenter", observations: []nativeTenantRowObservation{starting, failed}, wantError: "provisioning failed", quiet: true},
		{name: "failed before nodes", observations: []nativeTenantRowObservation{starting, starting, failed}, wantError: "provisioning failed", failedStage: "tenant-api", wantSecret: true},
		{name: "failed after transient node read", observations: []nativeTenantRowObservation{starting, starting, starting, failed}, wantError: "provisioning failed", failedStage: "tenant-api", wantSecret: true, wantNodes: true, nodeReadFails: true},
		{name: "missing conditions", observations: []nativeTenantRowObservation{starting, {}}, wantSecret: true, wantNodes: true},
		{name: "Unknown conditions", observations: []nativeTenantRowObservation{starting, {Provisioning: TenantConditionUnknown, Provisioned: TenantConditionUnknown}}, wantSecret: true, wantNodes: true},
		{name: "False with missing peer", observations: []nativeTenantRowObservation{starting, {Provisioning: TenantConditionFalse}}, wantSecret: true, wantNodes: true},
		{name: "False with Unknown peer", observations: []nativeTenantRowObservation{starting, {Provisioning: TenantConditionFalse, Provisioned: TenantConditionUnknown}}, wantSecret: true, wantNodes: true},
		{name: "still provisioning", observations: []nativeTenantRowObservation{starting}, wantSecret: true, wantNodes: true},
		{name: "provisioned still needs nodes", observations: []nativeTenantRowObservation{starting, {Provisioning: TenantConditionFalse, Provisioned: TenantConditionTrue}}, wantSecret: true, wantNodes: true, nodesUnready: true},
		{name: "transient reads recover after four failures", observations: []nativeTenantRowObservation{starting, {ExitCode: 1}, {ExitCode: 1}, {ExitCode: 1}, {ExitCode: 1}, starting}, wantSecret: true, wantNodes: true},
		{name: "read deadline", observations: []nativeTenantRowObservation{starting, {ExitCode: 1}}, readDeadline: true},
		{name: "missing after transient read", observations: []nativeTenantRowObservation{starting, {ExitCode: 1}, {Missing: true}}, wantError: "omitted the exact owned native tenant"},
		{name: "identity changed", observations: []nativeTenantRowObservation{starting, {ChangedID: true}}, wantError: "identity/provider changed"},
		{name: "provider changed", observations: []nativeTenantRowObservation{starting, {ChangedProvider: true}}, wantError: "infrastructure changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt, runner, root := nativeSessionFixture(t, false, 0)
			writer := &recordedProgress{}
			if !tc.quiet {
				runner.Progress = writer
			}
			if err := privateJSON(filepath.Join(root, "tenant-row-observations.json"), tc.observations); err != nil {
				t.Fatal(err)
			}
			if tc.nodesUnready || tc.readDeadline {
				attempt.tenantTimeout = 2 * time.Second
			}
			if tc.nodesUnready {
				writeTenantFixture(t, filepath.Join(root, "nodes.json"), []byte(`{"items":[]}`), 0600)
			}
			if tc.nodeReadFails {
				kubectl := filepath.Join(runner.Work, "kubectl")
				if err := os.Rename(kubectl, kubectl+"-fixture"); err != nil {
					t.Fatal(err)
				}
				writeTenantFixture(t, kubectl, []byte(`#!/bin/sh
case "$*" in *'get nodes '*) printf 'nodes\n' >> "$FIXTURE/events"; exit 1;; esac
exec "$0-fixture" "$@"
`), 0700)
			}
			request, err := attempt.namedTenantRequest(t.Context(), "owned")
			if err != nil {
				t.Fatal(err)
			}
			err = attempt.runTenantRequest(t.Context(), runner, attempt.assets.CLI, runner.Work, runner.Work, filepath.Join(runner.Work, "site.kubeconfig"), request)
			switch {
			case tc.wantError != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("fresh owned row did not stop readiness: %v", err)
				}
			case tc.readDeadline:
				if !errors.Is(err, context.DeadlineExceeded) || CommandExitCode(err) != -1 || strings.Contains(err.Error(), "provisioning failed") {
					t.Fatalf("read failure became controller failure: %v", err)
				}
			case tc.nodesUnready:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("controller Provisioned bypassed node readiness: %v", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			raw, readErr := os.ReadFile(filepath.Join(root, "events"))
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			events := "\n" + string(raw)
			if strings.Contains(events, "\nkubeconfig\n") != tc.wantSecret || strings.Contains(events, "\nnodes\n") != tc.wantNodes {
				t.Fatalf("readiness commands crossed a refused owned observation: %s", raw)
			}
			if (err == nil) != strings.Contains(events, "\nworkload-apply\n") {
				t.Fatalf("runtime gates or single workload changed: %s %v", raw, err)
			}
			if _, err := os.Stat(filepath.Join(root, "active-request.json")); err != nil {
				t.Fatalf("readiness outcome deleted the owned tenant: %v", err)
			}
			if tc.failedStage != "" {
				found := false
				for _, event := range writer.events {
					if tc.failedStage == "tenant-api" && event.Kind == TenantWaiting && event.Name == "tenant-nodes" {
						t.Fatal("node stage was published without a successful tenant API query")
					}
					if event.Kind == TenantWaiting && event.Name == tc.failedStage && event.Provisioning == TenantConditionFalse && event.Provisioned == TenantConditionFalse {
						found = true
					}
				}
				if !found {
					t.Fatal("failed fresh controller conditions were not published in the current stage")
				}
			}
			if err == nil || tc.nodesUnready {
				last := tc.observations[len(tc.observations)-1]
				found := false
				for _, event := range writer.events {
					if event.Kind == TenantWaiting && event.Name == "tenant-nodes" && event.Provisioning == presentedTenantCondition(last.Provisioning) && event.Provisioned == presentedTenantCondition(last.Provisioned) {
						found = true
					}
				}
				if !found {
					t.Fatal("fresh controller conditions were not published during node readiness")
				}
			}
		})
	}
}

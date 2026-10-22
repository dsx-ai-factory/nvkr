// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestNativeSessionSharesTenantAndConformanceProgressAcrossSiteReuse(t *testing.T) {
	attempt, runner, root := nativeSessionFixture(t, true)
	writer := &recordedProgress{}
	runner.Progress = writer
	err := withNativeSiteScope(t.Context(), &attempt, runner, runner.Work, runner.Work, func(ctx context.Context, site *NativeSite) error {
		for _, name := range []string{"first", "second"} {
			tenant, err := site.CreateTenant(ctx, name)
			if err != nil {
				return err
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
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[[2]string]int{}
	guestSnapshots := 0
	for _, event := range writer.events {
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

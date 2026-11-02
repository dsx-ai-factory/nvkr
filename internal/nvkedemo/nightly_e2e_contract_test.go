// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestNightlyRuntimeBindsProfileDiskAndNativeHost(t *testing.T) {
	for _, test := range []struct {
		name, runtimeTarget, host, diskPlatform string
		diskOutput                              string
		missing, duplicate, noPlan              bool
		code                                    e.Code
	}{
		{name: "ARM64", runtimeTarget: "native-arm64-vm", host: "linux/arm64", diskPlatform: "linux/arm64"},
		{name: "AMD64", runtimeTarget: "native-amd64-vm", host: "linux/amd64", diskPlatform: "linux/amd64"},
		{name: "cross architecture host", runtimeTarget: "native-amd64-vm", host: "linux/arm64", diskPlatform: "linux/amd64", code: e.CodeEngineCapability},
		{name: "unsupported host", runtimeTarget: "native-arm64-vm", host: "linux/s390x", diskPlatform: "linux/arm64", code: e.CodeEngineCapability},
		{name: "non Linux host", runtimeTarget: "native-arm64-vm", host: "darwin/arm64", diskPlatform: "linux/arm64", code: e.CodeEngineCapability},
		{name: "unsupported target", runtimeTarget: "native-s390x-vm", host: "linux/arm64", diskPlatform: "linux/arm64", code: e.CodeEngineCapability},
		{name: "cross architecture disk", runtimeTarget: "native-amd64-vm", host: "linux/amd64", diskPlatform: "linux/arm64", code: e.CodeVerifyFailed},
		{name: "unsupported disk", runtimeTarget: "native-arm64-vm", host: "linux/arm64", diskPlatform: "linux/s390x", code: e.CodeVerifyFailed},
		{name: "different same architecture disk", runtimeTarget: "native-arm64-vm", host: "linux/arm64", diskPlatform: "linux/arm64", diskOutput: "other-disk", code: e.CodeVerifyFailed},
		{name: "missing disk", runtimeTarget: "native-arm64-vm", host: "linux/arm64", missing: true, code: e.CodeVerifyFailed},
		{name: "ambiguous disk", runtimeTarget: "native-arm64-vm", host: "linux/arm64", diskPlatform: "linux/arm64", duplicate: true, code: e.CodeVerifyFailed},
		{name: "missing Plan", runtimeTarget: "native-arm64-vm", host: "linux/arm64", noPlan: true, code: e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			diskOutput := "nvke-node-image-arm64"
			if test.runtimeTarget == "native-amd64-vm" {
				diskOutput = "nvke-node-image-amd64"
			}
			profile := NightlyProfile{RuntimeTarget: test.runtimeTarget, Network: "ovn", DiskOutput: diskOutput}
			plan := &factory.Plan{Builds: []factory.BuildPlan{{Name: "unselected-disk", Platform: "linux/other"}}}
			if test.diskOutput != "" {
				plan.Builds = append(plan.Builds, factory.BuildPlan{Name: diskOutput, Platform: test.diskPlatform})
				profile.DiskOutput = test.diskOutput
			}
			if !test.missing {
				plan.Builds = append(plan.Builds, factory.BuildPlan{Name: profile.DiskOutput, Platform: test.diskPlatform})
			}
			if test.duplicate {
				plan.Builds = append(plan.Builds, factory.BuildPlan{Name: profile.DiskOutput, Platform: test.diskPlatform})
			}
			if test.noPlan {
				plan = nil
			}
			platform, err := nightlyRuntimePlatform(profile, plan, test.host)
			if test.code != "" {
				assertNightlyCode(t, err, test.code)
				return
			}
			if err != nil || platform != test.host {
				t.Fatalf("platform=%q error=%v, want %q", platform, err, test.host)
			}
		})
	}
}

func TestNightlyDeadlineReservesNativeCleanupBeforeStartingWork(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name     string
		deadline time.Time
		work     time.Duration
		invalid  bool
	}{
		{"workflow330minutes", now.Add(330 * time.Minute), nightlyMaximumWork, false},
		{"longer timeout caps work", now.Add(360 * time.Minute), nightlyMaximumWork, false},
		{"shorter timeout", now.Add(130 * time.Minute), 5 * time.Minute, false},
		{"no enclosing timeout", time.Time{}, nightlyMaximumWork, false},
		{"cleanup consumes deadline", now.Add(nightlyCleanupReserve), 0, true},
		{"expired deadline", now.Add(-time.Second), 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget, err := nightlyTimeBudget(now, test.deadline)
			if test.invalid {
				assertNightlyCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil || budget.WorkDeadline.Sub(now) != test.work || budget.WorkDeadline.Add(nightlyNativeCleanup+nightlyReportGrace).After(budget.CleanupDeadline) {
				t.Fatalf("work and cleanup do not fit enclosing deadline: %+v %v", budget, err)
			}
			parent, cancel := context.WithCancel(t.Context())
			cancel()
			cleanup, stop := budget.cleanupContext(parent, time.Minute)
			defer stop()
			deadline, ok := cleanup.Deadline()
			if !ok || deadline.After(budget.CleanupDeadline) || cleanup.Err() != nil {
				t.Fatal("cleanup inherited work cancellation or escaped its deadline")
			}
		})
	}
}

func TestNightlyOptInCannotSilentlySkipRuntimeJob(t *testing.T) {
	for _, test := range []struct {
		enable, actions, profile string
		enabled, refused         bool
	}{
		{"", "", "", false, false},
		{"", "true", "", false, false},
		{"", "true", "selected-profile", false, true},
		{"0", "true", "selected-profile", false, true},
		{"1", "true", "selected-profile", true, false},
	} {
		enabled, err := nightlyTestAdmission(test.enable, test.actions, test.profile)
		if enabled != test.enabled {
			t.Fatal(test, enabled)
		}
		if test.refused {
			assertNightlyCode(t, err, e.CodeUsage)
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestNightlyCycleCannotReportSuccessfulPreparationOrFailedCleanup(t *testing.T) {
	for _, test := range []struct {
		name, phase string
		result      error
		passed      bool
	}{
		{"joined runtime", "runtime", nil, true},
		{"only verified artifacts", "preparation", nil, false},
		{"cleanup failed", "runtime", errors.New("joined cleanup failed"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := t.TempDir()
			build := NightlyBuild{CoordinatorRun: "100", CoordinatorAttempt: "2", ProducerRun: 200, ProducerAttempt: 1}
			if err := writeNightlyCycle(t.Context(), out, NightlyProfile{Name: "fixture"}, build, "fixture-subject", test.phase, test.result); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(out, "cycle.json"))
			if err != nil {
				t.Fatal(err)
			}
			var cycle struct {
				RuntimePassed          bool   `json:"runtimePassed"`
				QualificationAuthority string `json:"qualificationAuthority"`
				CoordinatorRun         string `json:"coordinatorRun"`
				ProducerRun            int64  `json:"producerRun"`
			}
			if err := json.Unmarshal(raw, &cycle); err != nil || cycle.RuntimePassed != test.passed || cycle.QualificationAuthority != "none" || cycle.CoordinatorRun != build.CoordinatorRun || cycle.ProducerRun != build.ProducerRun {
				t.Fatalf("unexpected trusted verdict: %s %v", raw, err)
			}
		})
	}
}

func TestNightlySignalsCancelTheOwnedTestContext(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range []os.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestNightlySignalContextHelper$", "-test.timeout=8s")
			command.Env = []string{"NVKR_NIGHTLY_TEST_SIGNAL=1"}
			if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
				t.Fatal(err)
			}
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); _ = ownedcmd.KillDescendants(command) }()
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "nightly-signal-ready" {
				cancel()
				_ = command.Wait()
				t.Fatal("signal helper did not start", scanner.Err())
			}
			if err := command.Process.Signal(signal); err != nil {
				cancel()
				_ = command.Wait()
				t.Fatal(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatal("signal did not reach orderly context cancellation", err)
			}
		})
	}
}

func TestNightlySignalContextHelper(t *testing.T) {
	if os.Getenv("NVKR_NIGHTLY_TEST_SIGNAL") != "1" {
		t.Skip("owned signal subprocess fixture")
	}
	ctx, cancel := nightlySignalContext(t.Context(), time.Now().Add(6*time.Second))
	defer cancel()
	fmt.Println("nightly-signal-ready")
	<-ctx.Done()
	if ctx.Err() != context.Canceled {
		t.Fatal("signal was not received", ctx.Err())
	}
}

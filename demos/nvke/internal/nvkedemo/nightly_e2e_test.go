// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// TestNightlyPublishedCandidate consumes the existing producer's authenticated
// outputs. The Go lifecycle bootstraps and checks a real site; it builds no
// product artifacts and grants no qualification authority.
func TestNightlyPublishedCandidate(t *testing.T) {
	enabled, err := nightlyTestAdmission(os.Getenv("NVKR_NIGHTLY_ENABLE"), os.Getenv("GITHUB_ACTIONS"), os.Getenv("NVKR_NIGHTLY_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("nightly integration is explicitly enabled by its protected GitHub job")
	}
	deadline, _ := t.Deadline()
	timing, err := nightlyTimeBudget(time.Now(), deadline)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := nightlySignalContext(t.Context(), timing.WorkDeadline)
	defer cancel()
	if err := runNightlyPublishedCandidate(ctx, timing); err != nil {
		t.Fatal(err)
	}
}

const (
	nightlyMaximumWork    = 205 * time.Minute
	nightlyNativeCleanup  = 2 * time.Hour
	nightlyReportGrace    = time.Minute
	nightlyDeadlineMargin = 4 * time.Minute
	nightlyCleanupReserve = nightlyNativeCleanup + nightlyReportGrace + nightlyDeadlineMargin
)

type nightlyTiming struct{ WorkDeadline, CleanupDeadline time.Time }

func nightlyTimeBudget(now, testDeadline time.Time) (nightlyTiming, error) {
	if testDeadline.IsZero() {
		testDeadline = now.Add(nightlyMaximumWork + nightlyCleanupReserve)
	}
	work := now.Add(nightlyMaximumWork)
	if admitted := testDeadline.Add(-nightlyCleanupReserve); admitted.Before(work) {
		work = admitted
	}
	if !work.After(now) {
		return nightlyTiming{}, e.New(e.CodeEngineCapability, "nightly test deadline leaves no work time after bounded cleanup")
	}
	return nightlyTiming{WorkDeadline: work, CleanupDeadline: testDeadline.Add(-nightlyDeadlineMargin)}, nil
}

func nightlySignalContext(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	work, cancel := context.WithDeadline(parent, deadline)
	ctx, stop := signal.NotifyContext(work, syscall.SIGINT, syscall.SIGTERM)
	return ctx, func() { stop(); cancel() }
}

func (timing nightlyTiming) cleanupContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(maximum)
	if timing.CleanupDeadline.Before(deadline) {
		deadline = timing.CleanupDeadline
	}
	return context.WithDeadline(context.WithoutCancel(parent), deadline)
}

func nightlyTestAdmission(enable, actions, profile string) (bool, error) {
	if enable == "1" {
		return true, nil
	}
	if actions == "true" && profile != "" {
		return false, e.New(e.CodeUsage, "nightly GitHub runtime requires explicit enablement; refusing a successful skip")
	}
	return false, nil
}

func runNightlyPublishedCandidate(ctx context.Context, timing nightlyTiming) (resultErr error) {
	if runtime.GOOS != "linux" || os.Getenv("GITHUB_REF") != "refs/heads/main" || os.Getenv("GITHUB_SERVER_URL") != "https://github.com" {
		return e.New(e.CodeEngineCapability, "nightly runtime requires the protected main GitHub workflow on native Linux")
	}
	profile, err := loadNightlyProfile(ctx, os.Getenv("NVKR_NIGHTLY_PROFILES"), os.Getenv("NVKR_NIGHTLY_PROFILE"))
	if err != nil {
		return err
	}
	if profile.Network != "ovn" {
		return e.New(e.CodeEngineCapability, "native nightly bootstrap currently requires the NVIDIA OVN producer")
	}
	build, err := loadNightlyBuild(ctx, os.Getenv("NVKR_NIGHTLY_BUILD"))
	if err != nil {
		return err
	}
	if err := validateNightlyCycle(profile, build, os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_REPOSITORY_ID"), os.Getenv("GITHUB_SHA"), os.Getenv("GITHUB_RUN_ID"), os.Getenv("GITHUB_RUN_ATTEMPT")); err != nil {
		return err
	}
	tenantChecks, err := nativeE2EOptIn("NVKR_NIGHTLY_TENANT", os.Getenv("NVKR_NIGHTLY_TENANT"))
	if err != nil {
		return err
	}
	public, err := freshDestination(os.Getenv("NVKR_NIGHTLY_REPORTS"))
	if err != nil || !filepath.IsAbs(os.Getenv("NVKR_NIGHTLY_REPORTS")) {
		return e.New(e.CodeUsage, "nightly requires a fresh absolute public report directory")
	}
	if err := os.Mkdir(public, 0700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating public nightly reports")
	}
	phase, subject := "preparation", ""
	defer func() {
		// Native VM cleanup and the held publication close before this report.
		cleanup, cancel := timing.cleanupContext(ctx, nightlyReportGrace)
		defer cancel()
		if ctx.Err() != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeTimeout, ctx.Err(), "nightly cycle canceled before its final verdict"))
		}
		resultErr = errors.Join(resultErr, writeNightlyCycle(cleanup, public, profile, build, subject, phase, resultErr))
	}()
	root := os.Getenv("GITHUB_WORKSPACE")
	if !filepath.IsAbs(root) {
		return e.New(e.CodeUsage, "require an absolute trusted repository workspace")
	}
	for _, path := range []string{profile.Definition, profile.Environment, profile.Plan} {
		if !securepath.WorkspaceRelative(path) {
			return e.New(e.CodeUsage, "nightly input must remain within the trusted repository")
		}
	}
	definition, err := lifecycle.LoadDefinition(ctx, filepath.Join(root, profile.Definition))
	if err != nil {
		return err
	}
	environment, err := lifecycle.LoadEnvironment(ctx, filepath.Join(root, profile.Environment))
	if err != nil {
		return err
	}
	plan, err := factory.LoadPlanContext(ctx, filepath.Join(root, profile.Plan))
	if err != nil {
		return err
	}
	platform, err := nightlyRuntimePlatform(profile, plan, runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		return err
	}
	workspace, err := freshDestination(os.Getenv("NVKR_NIGHTLY_WORKSPACE"))
	if err != nil || !filepath.IsAbs(os.Getenv("NVKR_NIGHTLY_WORKSPACE")) {
		return e.New(e.CodeUsage, "nightly requires a fresh absolute private workspace")
	}
	if err := os.Mkdir(workspace, 0700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating private nightly workspace")
	}
	cfg := Config{Network: profile.Network, Cleanup: true, Timeout: time.Hour, Progress: os.Stdout,
		Out: filepath.Join(workspace, "runtime"), DemoExecutable: os.Getenv("NVKR_NIGHTLY_NETWORK_HELPER")}
	// Admit the existing native host/backend and cleanup route before fetching
	// any large published payload. This observes capacity; it reserves nothing.
	if err := AdmitDemoDeployment(ctx, cfg); err != nil {
		return err
	}
	if err := admitNativeQEMUAdapter(platform, "kvm", cfg.DemoExecutable, true); err != nil {
		return err
	}
	inputs, err := newNightlyInputs(ctx, profile, build, plan, definition, environment, workspace)
	if err != nil {
		return err
	}
	plan = inputs.Plan
	if _, err := nightlyRuntimePlatform(profile, plan, platform); err != nil {
		return err
	}
	nvkeCommit, err := nightlyPlanHarnessCommit(plan, profile)
	if err != nil {
		return err
	}
	expectation, err := lifecycle.PrepareExactInputs(definition, environment, plan)
	if err != nil {
		return err
	}
	if environment.Signing == nil {
		return e.New(e.CodeTrustPolicyMissing, "nightly requires the producing Environment's signing policy")
	}
	consumer, err := lifecycle.NewConsumer(lifecycle.ConsumerOptions{
		CosignBinary: os.Getenv("NVKR_NIGHTLY_COSIGN"), CosignDigest: os.Getenv("NVKR_NIGHTLY_COSIGN_DIGEST"), WorkspaceRoot: filepath.Join(workspace, "verified"),
		Registry:   delivery.RegistryOptions{Username: os.Getenv("NVKR_REGISTRY_READ_USER"), Password: os.Getenv("NVKR_REGISTRY_READ")},
		Credential: sign.Credential{Registry: "ghcr.io", Username: os.Getenv("NVKR_REGISTRY_READ_USER"), Password: os.Getenv("NVKR_REGISTRY_READ")},
	})
	if err != nil {
		return err
	}
	verifyCtx, verifyCancel := context.WithTimeout(ctx, 45*time.Minute)
	defer verifyCancel()
	policy := lifecycle.VerificationPolicy{Lane: lifecycle.LanePipeline, Scope: lifecycle.ScopeArtifactPublication, IdentityRegexp: environment.Signing.Identity,
		Issuer: environment.Signing.Issuer, MinimumDiskEvidence: lifecycle.DiskEvidenceBuildProvenance}
	verified, receipt, err := consumer.VerifyExactInputs(verifyCtx, inputs.Reference, policy, expectation)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, verified.Close()) }()
	description, err := verified.Description()
	if err != nil {
		return err
	}
	subject, phase = description.Subject, "runtime"
	fmt.Fprintf(os.Stdout, "Nightly NVKE source: %s (recorded by the producing Plan)\n", nvkeCommit)
	return withPublishedNativeSite(ctx, cfg, verified, receipt, plan, inputs.ExpectedInvocation, func(ctx context.Context, site *NativeSite) error {
		if err := nativeE2ESiteSmoke(ctx, site); err != nil {
			return err
		}
		if tenantChecks {
			_, err := site.CreateTenant(ctx, "nightly-tenant")
			return err
		}
		return nil
	})
}

// Profile and Plan descriptions admit only a matching execution route here.
// Publication authenticity and payload access remain with lifecycle receipts.
func nightlyRuntimePlatform(profile NightlyProfile, plan *factory.Plan, hostPlatform string) (string, error) {
	platform, err := nightlyRuntimeTargetPlatform(profile.RuntimeTarget)
	if err != nil {
		return "", err
	}
	if target.ValidatePlatform(hostPlatform) != nil || hostPlatform != platform {
		return "", e.New(e.CodeEngineCapability, "nightly runtime host %q differs from selected native platform %q", hostPlatform, platform)
	}
	diskOutput, err := selectedDiskOutputForArchitecture(profile.Network, target.Arch(platform))
	if err != nil {
		return "", err
	}
	if profile.DiskOutput != diskOutput {
		return "", e.New(e.CodeVerifyFailed, "nightly profile disk %q differs from selected native disk %q", profile.DiskOutput, diskOutput)
	}
	if plan == nil || profile.DiskOutput == "" {
		return "", e.New(e.CodeVerifyFailed, "nightly runtime requires its selected disk Plan")
	}
	found := false
	for _, build := range plan.Builds {
		if build.Name != profile.DiskOutput {
			continue
		}
		if found || target.ValidatePlatform(build.Platform) != nil || build.Platform != platform {
			return "", e.New(e.CodeVerifyFailed, "nightly selected disk differs from its native runtime platform")
		}
		found = true
	}
	if !found {
		return "", e.New(e.CodeVerifyFailed, "nightly Plan omits its selected disk output")
	}
	return platform, nil
}

func writeNightlyCycle(ctx context.Context, public string, profile NightlyProfile, build NightlyBuild, subject, phase string, resultErr error) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "nightly cycle report requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "nightly cycle report canceled")
	}
	if err := privateJSON(filepath.Join(public, "cycle.json"), map[string]any{
		"profile": profile.Name, "releaseSubject": subject, "phase": phase,
		"runtimePassed": phase == "runtime" && resultErr == nil, "qualificationAuthority": "none",
		"producerRun": build.ProducerRun, "producerAttempt": build.ProducerAttempt, "sourceCommit": build.SourceCommit,
		"nvkeCommit":     build.NVKECommit,
		"coordinatorRun": build.CoordinatorRun, "coordinatorAttempt": build.CoordinatorAttempt,
	}); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "writing trusted nightly cycle report")
	}
	return nil
}

func nightlyControllerOutput(profile NightlyProfile) string {
	if profile.ControllerOutput != "" {
		return profile.ControllerOutput
	}
	return "cwe"
}

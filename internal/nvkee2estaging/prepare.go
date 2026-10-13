// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkee2estaging

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// Prepare checks descriptive inputs and stages verified publication materials, optionally compiling
// the pinned suite. It never invokes the live test command or allocates hardware.
// A partial failure record is persisted only inside a directory this call created.
func Prepare(ctx context.Context, options Options) (report Report, resultErr error) {
	report = Report{Repository: options.Repository, RunID: options.RunID, RunAttempt: options.RunAttempt, PublicationRef: options.Reference,
		StreamName: options.StreamName, GPU: options.GPU, K8s: options.K8s, HarnessCommit: "not-inspected", Artifacts: map[string]any{},
		MaterializationPhase: "not-started", MaterializationOutcome: "not-run", TestOutcome: "not-run", CleanupOutcome: "not-run", QualificationAuthority: "none", RunRecordTrust: "run-record-unauthenticated", SourceTrust: "not-checked", ValidationOutcome: "not-run", CompilationOutcome: "not-run"}
	if err := admitOptions(ctx, options); err != nil {
		return report, err
	}
	origin, err := invocation(options)
	if err != nil {
		return report, err
	}
	out, err := filepath.Abs(options.Out)
	if err != nil {
		return report, e.Wrap(e.CodeUsage, err, "resolving preparation output")
	}
	options.Out = out
	var run lifecycle.Run
	if err := readDocument(ctx, options.Run, &run); err != nil {
		return report, err
	}
	named, identities, err := checkRun(run, options)
	if err != nil {
		return report, err
	}
	report.RunRecordStatus = run.Status
	report.InputIdentities = identities
	var input mapping
	if err := readDocument(ctx, options.Inputs, &input); err != nil {
		return report, err
	}
	deployCommit, err := checkedMapping(input)
	if err != nil {
		return report, err
	}
	report.ValidationOutcome = "input-descriptions-checked"
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		return report, e.New(e.CodeImmutableViolation, "refusing to reuse a cycle output directory")
	}
	var plan *factory.Plan
	var expectation *lifecycle.ExactInputExpectation
	if !options.PlanOnly {
		definition, err := lifecycle.LoadDefinition(ctx, options.Definition)
		if err != nil {
			return report, err
		}
		environment, err := lifecycle.LoadEnvironment(ctx, options.Environment)
		if err != nil {
			return report, err
		}
		plan, err = factory.LoadPlanContext(ctx, options.Plan)
		if err != nil {
			return report, err
		}
		expectation, err = lifecycle.PrepareExactInputs(definition, environment, plan)
		if err != nil {
			return report, err
		}
		planDigest, err := plan.Digest()
		if err != nil {
			return report, err
		}
		if planDigest != run.PlanDigest {
			return report, e.New(e.CodeVerifyFailed, "restored Plan differs from producing Run")
		}
	}
	if err := ctx.Err(); err != nil {
		return report, e.Wrap(e.CodeTimeout, err, "preparation canceled")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return report, e.Wrap(e.CodeInternal, err, "creating preparation parent")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return report, e.Wrap(e.CodeImmutableViolation, err, "creating new preparation directory")
	}
	defer func() {
		if resultErr == nil {
			return
		}
		recordFailure(&report, resultErr)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := writeJSON(cleanup, out, "e2e-staging-failure.json", report); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	source := factory.RestoredSourceDescription{Node: options.SourceNode, Path: "<restored exact source carrier>", Commit: "not-inspected"}
	configPath := filepath.Join(out, "e2e-config.json")
	if options.PlanOnly {
		for name, artifact := range named {
			report.Artifacts[name] = map[string]any{"trust": "run-record-unauthenticated", "description": artifact}
		}
	} else {
		source, err = factory.InspectRestoredSource(ctx, plan, options.SourceNode)
		if err != nil {
			return report, err
		}
		report.SourceTrust = "source-checked-unsigned-receipt"
		report.Source = source
		report.HarnessCommit = source.Commit
		report.MaterializationPhase = "source-inspected"
		if deployCommit != source.Commit && (input.DeployFixture.Commit != deployCommit || strings.TrimSpace(input.DeployFixture.Identity) == "") {
			return report, e.New(e.CodeUsage, "deployment source differs from harness without explicit fixture identity")
		}
		consumer, err := lifecycle.NewConsumer(lifecycle.ConsumerOptions{CosignBinary: options.Cosign, CosignDigest: options.CosignDigest, WorkspaceRoot: filepath.Join(out, "verification")})
		if err != nil {
			return report, err
		}
		policy := verificationPolicy(options)
		bounded, cancel := verificationContext(ctx, options.VerifyTimeout)
		verified, receipt, err := consumer.VerifyExactInputs(bounded, options.Reference, policy, expectation)
		if err != nil {
			cancel()
			return report, err
		}
		defer func() { _ = verified.Close(); cancel() }()
		description, err := verified.Description()
		if err != nil {
			return report, err
		}
		exact, err := receipt.Description()
		if err != nil {
			return report, err
		}
		artifactMap, err := checkedArtifacts(named, description, identities, exact, options.Reference)
		if err != nil {
			return report, err
		}
		report.ValidationOutcome = "source-and-publication-checked"
		report.MaterializationOutcome = "in-progress"
		layout := filepath.Join(out, "publication")
		if err := verified.ExportForInvocation(bounded, receipt, origin, layout); err != nil {
			return report, err
		}
		_, rootDigest, found := strings.Cut(description.Subject, "@")
		if !found {
			return report, e.New(e.CodeVerifyFailed, "verified publication omits its root digest")
		}
		held, err := delivery.OpenPublication(bounded, layout, rootDigest)
		if err != nil {
			return report, err
		}
		defer func() { _ = held.Close() }()
		report.MaterializationPhase = "publication-authenticated"
		if err := stageMaterials(ctx, options, input, artifactMap, held, &report); err != nil {
			return report, err
		}
		report.MaterializationOutcome = "core-artifacts-staged"
		if options.CompileOnly {
			// Recheck the owner-observed checkout immediately before compilation;
			// this does not authenticate vendor or other untracked/ignored source.
			current, err := factory.InspectRestoredSource(ctx, plan, options.SourceNode)
			if err != nil {
				report.ValidationOutcome = "failed"
				return report, err
			}
			if current != source {
				report.ValidationOutcome = "failed"
				return report, e.New(e.CodeVerifyFailed, "restored suite source changed before compilation")
			}
			report.CompilationOutcome = "in-progress"
			if err := compileSource(ctx, source.Path, options); err != nil {
				return report, err
			}
			report.CompilationOutcome = "succeeded"
		}
	}
	report.Source = source
	report.HarnessCommit = source.Commit
	capabilities := "provider=forge,gpu=" + options.GPU + ",arch=" + options.Arch + ",ubuntu=" + options.Ubuntu + ",kube=" + options.K8s
	report.GoTestCommand = suiteCommand(configPath, options.Versions, capabilities, out)
	report.GoTestDirectory = filepath.Join(source.Path, "controller/test/e2e")
	report.RuntimeAdmission = "pending live usable-capacity/access and owned-cleanup inventory"
	report.RemainingInputGates = []string{"QCOW2/EFI HTTPS exact-byte read-back", "post-boot material compatibility", "reviewed fixtures and suite-source capability", "authenticated vendor and all other untracked/ignored suite-source content before execution", "rendered installed dependency closure"}
	if err := writeJSON(ctx, out, "e2e-staging.json", report); err != nil {
		return report, err
	}
	return report, nil
}

func admitOptions(ctx context.Context, options Options) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "preparation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "preparation canceled")
	}
	if options.VerifyTimeout < 0 {
		return e.New(e.CodeUsage, "verification timeout must be positive; omit it to use the default")
	}
	for _, value := range []string{options.Run, options.Plan, options.Definition, options.Environment, options.Inputs, options.Out, options.SourceNode, options.Versions} {
		if strings.TrimSpace(value) == "" {
			return e.New(e.CodeUsage, "preparation requires exact input paths, output, source node and versions")
		}
	}
	if _, err := invocation(options); err != nil {
		return err
	}
	if err := validateReference(options.Reference); err != nil {
		return err
	}
	if options.Arch != "amd64" && options.Arch != "arm64" || options.GPU != "gb200" && options.GPU != "l40s" || options.Ubuntu != "24.04" && options.Ubuntu != "26.04" || options.K8s != "1.36" || options.GPU == "l40s" && options.Arch != "amd64" {
		return e.New(e.CodeEngineCapability, "capabilities do not name a maintained NVKE E2E stream")
	}
	if options.StreamName != "forge-"+options.GPU+"-"+options.Arch+"-ubuntu-"+options.Ubuntu+"-kube-"+options.K8s {
		return e.New(e.CodeUsage, "stream identity disagrees with capabilities")
	}
	if options.PlanOnly && options.CompileOnly {
		return e.New(e.CodeUsage, "compile-only requires authenticated staging")
	}
	if options.PlanOnly {
		return nil
	}
	if runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "authenticated staging requires Linux signature execution; plan-only is diagnostic")
	}
	if strings.TrimSpace(options.Cosign) == "" || !canonical.IsDigest(options.CosignDigest) || options.MinimumDiskEvidence == "" {
		return e.New(e.CodeUsage, "staging requires explicit verifier executable, digest and disk policy")
	}
	if err := verificationPolicy(options).Validate(); err != nil {
		return err
	}
	if options.CompileOnly {
		if _, err := exec.LookPath("go"); err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "compilation requires the selected Go toolchain")
		}
	}
	return nil
}

func verificationPolicy(options Options) lifecycle.VerificationPolicy {
	return lifecycle.VerificationPolicy{Lane: lifecycle.LanePipeline, Scope: lifecycle.ScopeArtifactPublication, IdentityRegexp: options.Identity, Issuer: options.Issuer, MinimumDiskEvidence: lifecycle.DiskEvidenceRequirement(options.MinimumDiskEvidence)}
}

func verificationContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout == 0 {
		timeout = DefaultVerifyTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func compileSource(ctx context.Context, source string, options Options) error {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// -c compiles without running init or TestMain. A no-match -run filter still
	// starts the test binary and cannot support a compile-only promise.
	command := exec.CommandContext(bounded, "go", "test", "-c", "-o", filepath.Join(options.Out, "nvke-e2e.test"), ".")
	command.Dir = filepath.Join(source, "controller/test/e2e")
	command.Env = compileEnvironment(os.Environ())
	command.Stdout = options.Diagnostics
	command.Stderr = options.Diagnostics
	if command.Stdout == nil {
		command.Stdout = os.Stderr
		command.Stderr = os.Stderr
	}
	if err := ownedcmd.Run(bounded, command); err != nil {
		if bounded.Err() != nil {
			return e.Wrap(e.CodeTimeout, bounded.Err(), "suite compilation canceled")
		}
		return inputError(bounded, e.CodeBuildFailed, err, "compiling pinned NVKE suite")
	}
	return nil
}

func compileEnvironment(environment []string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "GOTMPDIR": true, "GOPATH": true, "GOCACHE": true, "GOMODCACHE": true}
	result := []string{}
	for _, value := range environment {
		key, _, _ := strings.Cut(value, "=")
		if allowed[key] {
			result = append(result, value)
		}
	}
	result = append(result, "GOWORK=off", "GOENV=off", "GOFLAGS=-mod=vendor", "GOPROXY=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0")
	return result
}

func suiteCommand(config, versions, capabilities, out string) []string {
	return []string{"go", "test", "-v", "-count=1", "-timeout", "220m", ".", "-args", "-ginkgo.label-filter=site-controller", "-ginkgo.timeout=150m", "-ginkgo.fail-on-empty", "-ginkgo.no-color", "-ginkgo.junit-report=" + filepath.Join(out, "nvke-e2e.junit.xml"), "-ginkgo.json-report=" + filepath.Join(out, "nvke-e2e.report.json"), "-e2e.config=" + config, "-e2e.environment=forge", "-e2e.site-stage=full", "-e2e.keep-site=false", "-e2e.versions=" + versions, "-e2e.capabilities=" + capabilities}
}

// jsonBytes is descriptive output only; authority identities stay in owners.
func jsonBytes(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding preparation description")
	}
	return append(raw, '\n'), nil
}

// inputError preserves coded owner failures and cancellation at the adapter boundary.
func inputError(ctx context.Context, fallback e.Code, err error, message string) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "%s", message)
	}
	var coded *e.Error
	if errors.As(err, &coded) {
		return e.Wrap(coded.Code, err, "%s", message)
	}
	return e.Wrap(fallback, err, "%s", message)
}

func recordFailure(report *Report, err error) {
	if report.MaterializationOutcome == "in-progress" {
		report.MaterializationOutcome = "failed"
	}
	if report.CompilationOutcome == "in-progress" {
		report.CompilationOutcome = "failed"
	}
	if report.ValidationOutcome != "source-and-publication-checked" {
		report.ValidationOutcome = "failed"
	}
	report.ErrorClass = "preparation"
	var coded *e.Error
	if errors.As(err, &coded) {
		report.ErrorCode = string(coded.Code)
	} else {
		report.ErrorCode = string(e.CodeInternal)
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		status := exited.ExitCode()
		report.SubprocessExitCode = &status
	}
}

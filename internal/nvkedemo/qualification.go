// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// A stored preview is a description. Recompile through the same bootstrap
// executable and inputs that will run producers before admitting a prepared
// build; the final qualification also checks the captured execution inputs.
func recheckPreparedQualificationPlan(ctx context.Context, runner Runner, cfg BuildConfig, policy lifecycle.LocalQualificationPolicy) error {
	args, err := preparedBuildInvocation(cfg)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeBuildFailed, err, "invalid prepared build invocation")
	}
	runner.Env, err = preparedBuildEnvironment(runner.Env, cfg)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeBuildFailed, err, "invalid prepared build environment")
	}
	runner.directory = cfg.Repository
	raw, err := runner.Capture(ctx, []string{filepath.Join(cfg.Repository, args[0]), "run", "plan", "--definition", args[4], "--environment", args[6], "--json"}, "qualification-plan", 5*time.Minute)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeBuildFailed, err, "recompile prepared qualification Plan")
	}
	var plan factory.Plan
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeBuildFailed, err, "invalid recompiled qualification Plan JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeBuildFailed, err, "invalid recompiled qualification Plan")
	}
	return policy.RequirePlan(&plan)
}

// QualificationFile opts a build into the complete local rehearsal. These are
// explicit locators, not trust receipts; nvkr owns all signing and verification.
type QualificationFile struct {
	BuilderKey   string `yaml:"builderKey"`
	SmokeKey     string `yaml:"smokeKey"`
	QualifierKey string `yaml:"qualifierKey"`
	Cosign       string `yaml:"cosign"`
	Repository   string `yaml:"repository"`
	Scratch      string `yaml:"scratch"`
	EvidenceOut  string `yaml:"evidenceOut"`
	PlainHTTP    bool   `yaml:"plainHTTP"`
	Anonymous    bool   `yaml:"anonymous"`
}

func (q QualificationFile) validate(policy string) error {
	if q == (QualificationFile{}) {
		return nil
	}
	for _, path := range []string{policy, q.BuilderKey, q.SmokeKey, q.QualifierKey, q.Cosign, q.Scratch, q.EvidenceOut} {
		if !configString(path) || !filepath.IsAbs(path) {
			return configUsage("complete local qualification requires absolute policy, three key, cosign, scratch and evidence paths")
		}
	}
	if !configString(q.Repository) {
		return configUsage("complete local qualification requires an explicit registry repository")
	}
	return nil
}

func preflightDemoQualification(ctx context.Context, cfg BuildConfig, plan *factory.Plan) error {
	q := cfg.Qualification
	if q == (QualificationFile{}) {
		return nil
	}
	return lifecycle.PreflightLocalQualification(ctx, plan, lifecycle.LocalQualificationOptions{
		PolicyPath: cfg.QualificationPolicy, BuilderKeyPath: q.BuilderKey, SmokeKeyPath: q.SmokeKey,
		QualifierKeyPath: q.QualifierKey, CosignExecutable: q.Cosign, RuntimeExecutable: "docker",
		Repository: q.Repository, Scratch: q.Scratch, EvidenceOut: q.EvidenceOut,
		Registry: delivery.RegistryOptions{PlainHTTP: q.PlainHTTP, Anonymous: q.Anonymous,
			Username: os.Getenv("NVKR_REGISTRY_USER"), Password: os.Getenv("NVKR_REGISTRY_PASSWORD")},
	})
}

// Qualification runs before BuildArtifacts returns paths to deployment. A
// decoded result cannot enable this gate: the owned child must also succeed.
func qualifyBuiltArtifacts(ctx context.Context, cfg BuildConfig) error {
	q := cfg.Qualification
	if q == (QualificationFile{}) {
		return nil
	}
	if err := q.validate(cfg.QualificationPolicy); err != nil {
		return err
	}
	built := builtInputs(cfg.Repository, cfg.AttemptPrefix)
	// Factory's retained-output observation uses repository-relative graph paths.
	// The child below runs in cfg.Repository, independently of its private logs.
	args := []string{built.NVKR, "run", "qualify-local", "--workspace", filepath.Join(".nvkr", "runs", cfg.AttemptPrefix),
		"--policy", cfg.QualificationPolicy, "--builder-key", q.BuilderKey, "--smoke-key", q.SmokeKey,
		"--qualifier-key", q.QualifierKey, "--cosign", q.Cosign, "--runtime", "docker",
		"--repository", q.Repository, "--scratch", q.Scratch, "--evidence-out", q.EvidenceOut}
	if q.PlainHTTP {
		args = append(args, "--plain-http")
	}
	if q.Anonymous {
		args = append(args, "--anonymous")
	}
	// Compiler replay authenticates upstream pin sets through sourcegit, which
	// needs the same bounded developer transport settings as the build. The
	// isolated smoke owner independently removes source and registry credentials.
	env, err := localBuildEnvironment(ctx)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeBuildFailed, err, "invalid local qualification source transport environment")
	}
	env = SetEnv(env, map[string]string{"TMPDIR": q.Scratch})
	for _, key := range []string{"NVKR_REGISTRY_USER", "NVKR_REGISTRY_PASSWORD"} {
		if value, ok := os.LookupEnv(key); ok {
			env = SetEnv(env, map[string]string{key: value})
		}
	}
	// Reserve the diagnostic destination before a child can sign or publish.
	// Keep the descriptor through execution; an interrupted attempt leaves a
	// reserved file and cannot silently reuse this transcript on retry.
	transcript, err := os.OpenFile(filepath.Join(cfg.Repository, ".nvkr/laptop-bootstrap", cfg.AttemptPrefix, "qualification-result.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeBuildFailed, err, "cannot reserve local qualification result; qualification and deployment refused")
	}
	diagnostics := filepath.Join(cfg.Repository, ".nvkr/laptop-bootstrap", cfg.AttemptPrefix, "qualification-diagnostics")
	if err := os.Mkdir(diagnostics, 0o700); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeBuildFailed, errors.Join(err, transcript.Close()), "cannot reserve private qualification diagnostics; qualification and deployment refused")
	}
	raw, runErr := (Runner{Work: diagnostics, directory: cfg.Repository, Env: env, Progress: cfg.Progress}).Capture(ctx, args, "qualify-local", 4*time.Hour)
	// This diagnostic transcript is deliberately separate from signed evidence.
	// Preserve failed results too; never reuse or overwrite an earlier transcript.
	_, writeErr := transcript.Write(raw)
	if err := errors.Join(runErr, writeErr, transcript.Close()); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeBuildFailed, err, "local M3 qualification or result retention failed; deployment refused")
	}
	return ValidateLocalQualificationResult(raw, cfg.AttemptPrefix)
}

// ValidateLocalQualificationResult checks the CLI's descriptive result framing
// and expected attempt. It grants no authority: callers must also require their
// owned qualification child to exit successfully before deployment handoff.
func ValidateLocalQualificationResult(raw []byte, attempt string) error {
	var result lifecycle.LocalQualificationResult
	// Admit exactly one CLI framing newline; preserve all other whitespace.
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &result); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeBuildFailed, err, "invalid local qualification result; deployment refused")
	}
	if result.SchemaVersion != "nvkr.dev/local-qualification-result/v1" || result.Lane != factory.TierLocalExecution.String() || result.Status != "passed" || result.Attempt != attempt || !canonical.IsDigest(result.ContentSubject) || result.FailureCode != "" || result.Publication.Evidence == "" {
		return nvkrerr.New(nvkrerr.CodeBuildFailed, "local qualification did not pass for the built attempt; deployment refused")
	}
	return nil
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// LocalQualificationOptions selects a retained local attempt, explicit local
// keys and operator-owned transport. No private key is passed to tool smoke.
type LocalQualificationOptions struct {
	Workspace, PolicyPath                          string
	BuilderKeyPath, SmokeKeyPath, QualifierKeyPath string
	CosignExecutable, RuntimeExecutable            string
	Repository, Scratch, EvidenceOut               string
	Registry                                       delivery.RegistryOptions
}

// LocalQualificationReferences lists attempted destinations, including failures.
// Serialized results describe completed checks; they cannot restore receipts.
type LocalQualificationReferences = localPublicationReport

type LocalQualificationResult struct {
	SchemaVersion  string                       `json:"schemaVersion"`
	Lane           string                       `json:"lane"`
	Status         string                       `json:"status"`
	Attempt        string                       `json:"attempt,omitempty"`
	ContentSubject string                       `json:"contentSubject,omitempty"`
	FailureCode    string                       `json:"failureCode,omitempty"`
	Publication    LocalQualificationReferences `json:"publication"`
}

// QualifyLocalRun checks and signs a complete retained local Run, executes native
// delivered-tool smoke, signs the local gate, and verifies registry read-back.
// It retains an evidence envelope and reports the unqualified local lane only.
func QualifyLocalRun(ctx context.Context, options LocalQualificationOptions) (*LocalQualificationResult, error) {
	return qualifyLocalRunWith(ctx, options, runtime.GOOS+"/"+runtime.GOARCH, runLocalToolSmoke)
}

type localSmokeRunner func(context.Context, *localVerifiedBuild, NativeToolSmokeOptions) (*localToolSmokeReceipt, error)

func qualifyLocalRunWith(ctx context.Context, options LocalQualificationOptions, platform string, smokeRun localSmokeRunner) (result *LocalQualificationResult, err error) {
	result = &LocalQualificationResult{SchemaVersion: "nvkr.dev/local-qualification-result/v1", Lane: factory.TierLocalExecution.String(), Status: "failed", Publication: localPublicationReport{Disks: map[string]string{}}}
	defer func() {
		if err == nil {
			result.Status = "passed"
			return
		}
		var coded *e.Error
		if errors.As(err, &coded) {
			result.FailureCode = string(coded.Code)
		} else {
			err = e.Wrap(e.CodeInternal, err, "local qualification failed")
			result.FailureCode = string(e.CodeInternal)
		}
	}()
	if ctx == nil || options.Workspace == "" || options.PolicyPath == "" || options.Repository == "" || options.Scratch == "" || options.EvidenceOut == "" || options.CosignExecutable == "" || options.BuilderKeyPath == "" || options.SmokeKeyPath == "" || options.QualifierKeyPath == "" || smokeRun == nil {
		return result, e.New(e.CodeUsage, "local qualification requires attempt, policy, cosign, repository, scratch and evidence destination")
	}
	if runtime.GOOS != "linux" || !qualificationNativePlatform(platform) {
		return result, e.New(e.CodeEngineCapability, "local qualification requires native Linux")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Hour)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, e.Wrap(e.CodeTimeout, err, "local qualification canceled")
	}
	policy, err := LoadLocalQualificationPolicy(ctx, options.PolicyPath)
	if err != nil {
		return result, err
	}
	if policy.Platform != platform {
		return result, e.New(e.CodeEngineCapability, "local qualification policy differs from native platform")
	}
	if err := options.Registry.Validate(); err != nil {
		return result, err
	}
	if err := admitLocalEvidenceDestination(options.EvidenceOut); err != nil {
		return result, err
	}
	inputs, err := loadLocalQualificationInputs(ctx, options.Workspace)
	if err != nil {
		return result, err
	}
	result.Attempt, result.ContentSubject = inputs.run.Attempt, inputs.run.ContentSubject
	if err := policy.RequirePlan(inputs.plan); err != nil {
		return result, err
	}
	var diskNames []string
	for _, output := range inputs.run.Observed {
		if output.Kind == OutputDiskImage {
			diskNames = append(diskNames, output.Name)
		}
	}
	if err := validateLocalPublicationTargets(options.Repository, diskNames, options.Registry); err != nil {
		return result, err
	}
	keys, err := admitLocalSigningKeys(ctx, policy, options)
	defer clearLocalSigningKeys(keys)
	if err != nil {
		return result, err
	}
	capture, err := captureLocalQualificationPayloads(ctx, options.Workspace, inputs)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, capture.Close()) }()
	built, err := signLocalBuildEvidence(ctx, inputs, capture, policy, options.CosignExecutable, keys[0])
	if err != nil {
		return result, err
	}
	buildSignature, err := built.signed.Signature()
	if err != nil {
		return result, err
	}
	if err := capture.Close(); err != nil {
		return result, err
	}
	build, err := openLocalVerifiedBuild(ctx, options.Workspace, policy, options.CosignExecutable, []byte(built.document), buildSignature)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, build.Close()) }()
	if err := admitLocalPublicationScratch(ctx, build, options.Scratch); err != nil {
		return result, err
	}
	smoke, err := smokeRun(ctx, build, NativeToolSmokeOptions{RuntimeExecutable: options.RuntimeExecutable})
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, smoke.Close()) }()
	signedSmoke, err := signLocalSmoke(ctx, smoke, options.CosignExecutable, keys[1])
	if err != nil {
		return result, err
	}
	smokeSignature, err := signedSmoke.signed.Signature()
	if err != nil {
		return result, err
	}
	verifiedSmoke, err := verifyLocalSmoke(ctx, build, options.CosignExecutable, []byte(signedSmoke.document), smokeSignature)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, verifiedSmoke.Close()) }()
	gate, err := signLocalQualification(ctx, build, verifiedSmoke, options.CosignExecutable, keys[2])
	if err != nil {
		return result, err
	}
	gateSignature, err := gate.signed.Signature()
	if err != nil {
		return result, err
	}
	verifiedGate, err := verifyLocalQualification(ctx, build, verifiedSmoke, options.CosignExecutable, []byte(gate.document), gateSignature)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, verifiedGate.Close()) }()
	if _, err := verifiedGate.writeEvidenceBundle(ctx, options.EvidenceOut); err != nil {
		return result, err
	}
	var consumer *localQualificationConsumer
	result.Publication, consumer, err = publishLocalQualification(ctx, verifiedGate, options.Repository, options.Scratch, options.CosignExecutable, options.Registry)
	if consumer != nil {
		defer func() { err = errors.Join(err, consumer.Close()) }()
	}
	return result, err
}

func readLocalSigningKey(ctx context.Context, path string) (raw []byte, err error) {
	if path == "" {
		return nil, e.New(e.CodeUsage, "all three local signing key paths are required")
	}
	key, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "local-signing-key", MaxBytes: 64 << 10})
	if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "opening explicit local signing key")
	}
	defer func() {
		if closeErr := key.Close(); closeErr != nil {
			clear(raw)
			raw = nil
			err = errors.Join(err, e.Wrap(e.CodeTrustPolicyMissing, closeErr, "closing local signing key"))
		}
	}()
	raw, err = key.ReadAllContext(ctx, 64<<10)
	if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "reading bounded local signing key")
	}
	return raw, nil
}

func admitLocalEvidenceDestination(path string) error {
	parent, _, err := securepath.OpenDirectory(filepath.Dir(path))
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening evidence destination parent")
	}
	defer parent.Close()
	file, _, err := securepath.OpenChild(parent, filepath.Base(path), false)
	if err == nil {
		_ = file.Close()
		return e.New(e.CodeImmutableViolation, "evidence destination already exists")
	}
	if !os.IsNotExist(err) {
		return e.Wrap(e.CodeImmutableViolation, err, "checking evidence destination")
	}
	return nil
}

// admitLocalSigningKeys authenticates each private key against its independent
// public role using a fixed challenge, never artifact or qualification evidence.
// The caller clears all returned buffers, including on partial failure.
func admitLocalSigningKeys(ctx context.Context, policy LocalQualificationPolicy, options LocalQualificationOptions) (keys [][]byte, err error) {
	keys = make([][]byte, 3)
	signer, err := sign.NewLocalCosign(options.CosignExecutable, policy.CosignDigest)
	if err != nil {
		return keys, err
	}
	for i, role := range []struct{ path, public, name string }{{options.BuilderKeyPath, policy.BuilderKey, "builder"}, {options.SmokeKeyPath, policy.SmokeKey, "smoke"}, {options.QualifierKeyPath, policy.QualifierKey, "qualifier"}} {
		keys[i], err = readLocalSigningKey(ctx, role.path)
		if err != nil {
			return keys, err
		}
		// This fixed role-admission message makes no artifact or gate claim.
		challenge := []byte("nvkr.dev/local-key-admission/v1:" + role.name)
		if _, err := signer.SignDocument(ctx, challenge, keys[i], []byte(role.public)); err != nil {
			return keys, err
		}
	}
	return keys, nil
}

func clearLocalSigningKeys(keys [][]byte) {
	for _, key := range keys {
		clear(key)
	}
}

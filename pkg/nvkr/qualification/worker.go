// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
	"context"
	"path/filepath"
	"runtime"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const SmokeWorkerWorkspace = ".nvkr/smoke-worker"
const smokeWorkerEvidenceDirectory = "evidence"

// SmokeWorkerResult is diagnostic output, never a smoke receipt. EvidenceDirectory
// is set only after all four immutable transport files have been committed.
type SmokeWorkerResult struct {
	ArtifactChecks    *lifecycle.ArtifactCheckDescription `json:"artifactChecks,omitempty"`
	Smoke             *lifecycle.NativeToolSmokeReport    `json:"smoke,omitempty"`
	WorkerDigest      string                              `json:"workerDigest,omitempty"`
	EvidenceDirectory string                              `json:"evidenceDirectory,omitempty"`
}

// RunTriggeredSmokeWorker independently authenticates the workflow_run source and
// active qualifier, checks the original handoff, runs native delivered-tool smoke,
// and signs only its live owner's exact successful report. It never publishes to
// a registry. Credential and Registry options are used solely for artifact reads;
// callers must provision read-only credentials. The reusable workflow's GitHub
// context belongs to its caller, while its signing SAN identifies Smoke.Workflow.
func RunTriggeredSmokeWorker(ctx context.Context, policyPath string, options lifecycle.QualificationRuntimeOptions) (*SmokeWorkerResult, error) {
	if err := workerContext(ctx); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return nil, e.New(e.CodeEngineCapability, "native smoke worker requires Linux AMD64 or ARM64")
	}
	policy, err := lifecycle.LoadQualificationPolicy(ctx, policyPath)
	if err != nil {
		return nil, err
	}
	platform := "linux/" + runtime.GOARCH
	identity, digest, err := admitSmokeWorkerPolicy(policy, platform)
	if err != nil {
		return nil, err
	}
	if err := CheckGeneratedPolicy(ctx, ".", policy); err != nil {
		return nil, err
	}
	signer, err := sign.NewPinnedCosignSigner(policy.Cosign.Path, digest)
	if err != nil {
		return nil, err
	}
	if err := signer.Preflight(ctx); err != nil {
		return nil, err
	}
	if options.WorkspaceRoot == "" {
		options.WorkspaceRoot = SmokeWorkerWorkspace
	}
	provider := qualificationorigin.NewGitHub(options.GitHubToken)
	source, err := provider.AuthenticateTriggeredSource(ctx, policy.Source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = source.Close() }()
	invocation, err := provider.AuthenticateInvocation(ctx, source, policy.QualificationWorkflow)
	if err != nil {
		return nil, err
	}
	defer func() { _ = invocation.Close() }()
	raw, err := provider.DownloadQualificationHandoff(ctx, source)
	if err != nil {
		return nil, err
	}
	checks, report, err := lifecycle.CheckQualificationHandoff(ctx, raw, source, policy, options)
	result := &SmokeWorkerResult{ArtifactChecks: report}
	if err != nil {
		return result, err
	}
	defer func() { _ = checks.Close() }()
	receipt, smokeReport, err := lifecycle.RunNativeToolSmoke(ctx, checks, invocation, policy, lifecycle.NativeToolSmokeOptions{})
	result.Smoke = smokeReport
	if err != nil {
		return result, err
	}
	defer func() { _ = receipt.Close() }()
	policyDigest, err := policy.Digest()
	if err != nil {
		return result, err
	}
	document, err := liveSmokeWorkerDocument(ctx, receipt, policyDigest, platform)
	if err != nil {
		return result, err
	}
	signingContext, cancelSigning := context.WithTimeout(ctx, 2*time.Minute)
	signed, err := signer.SignDocument(signingContext, document, sign.QualifiedSignOptions{
		Bundle: true, ExpectedIdentity: identity, ExpectedIssuer: "https://token.actions.githubusercontent.com",
	})
	cancelSigning()
	if err != nil {
		return result, err
	}
	require := func() error {
		actual, err := liveSmokeWorkerDocument(ctx, receipt, policyDigest, platform)
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, document) {
			return e.New(e.CodeGateUnmet, "native smoke receipt changed during signing")
		}
		return signed.RequireFor(ctx, document)
	}
	if err := require(); err != nil {
		return result, err
	}
	signature, err := signed.Signature()
	if err != nil {
		return result, err
	}
	certificate, err := signed.Certificate()
	if err != nil {
		return result, err
	}
	bundle, err := signed.Bundle()
	if err != nil {
		return result, err
	}
	files := smokeWorkerFiles{document: document, signature: signature, certificate: certificate, bundle: bundle}
	if err := commitSmokeWorkerFiles(ctx, options.WorkspaceRoot, files, require); err != nil {
		return result, err
	}
	result.WorkerDigest, err = receipt.Digest()
	if err != nil {
		return result, err
	}
	result.EvidenceDirectory = filepath.Join(options.WorkspaceRoot, smokeWorkerEvidenceDirectory)
	return result, nil
}

func admitSmokeWorkerPolicy(policy lifecycle.QualificationPolicy, platform string) (string, string, error) {
	if err := admitRunPolicy(policy); err != nil {
		return "", "", err
	}
	if policy.Smoke == nil {
		return "", "", e.New(e.CodeTrustPolicyMissing, "native smoke worker requires independently selected smoke policy")
	}
	digest, err := policy.Cosign.ExecutableDigest(platform)
	if err != nil {
		return "", "", err
	}
	if policy.Smoke.Images[platform] == "" {
		return "", "", e.New(e.CodeEngineCapability, "native smoke worker has no pinned image for its host platform")
	}
	return "https://github.com/" + policy.Source.Repository + "/" + policy.Smoke.Workflow + "@refs/heads/" + policy.Source.DefaultBranch, digest, nil
}

type nativeWorkerReceipt interface {
	Description() (lifecycle.NativeToolSmokeReport, error)
}

func liveSmokeWorkerDocument(ctx context.Context, receipt nativeWorkerReceipt, policyDigest, platform string) ([]byte, error) {
	if err := workerContext(ctx); err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, e.New(e.CodeGateUnmet, "native smoke signing requires a live execution receipt")
	}
	report, err := receipt.Description()
	if err != nil {
		return nil, err
	}
	if report.PolicyDigest != policyDigest || report.Platform != platform {
		return nil, e.New(e.CodeGateUnmet, "native smoke receipt differs from worker policy or platform")
	}
	raw, err := report.Canonical()
	if err != nil {
		return nil, err
	}
	if len(raw) > 16<<20 {
		return nil, e.New(e.CodeGateUnmet, "native smoke report exceeds its transport bound")
	}
	if err := workerContext(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func workerContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "native smoke worker requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "native smoke worker canceled")
	}
	return nil
}

type smokeWorkerFiles struct{ document, signature, certificate, bundle []byte }

func (files smokeWorkerFiles) members() []struct {
	name  string
	raw   []byte
	limit int
} {
	return []struct {
		name  string
		raw   []byte
		limit int
	}{
		{"worker.json", files.document, 16 << 20}, {"worker.sig", files.signature, 64 << 10},
		{"worker.pem", files.certificate, 1 << 20}, {"worker.bundle.json", files.bundle, 16 << 20},
	}
}

func (files smokeWorkerFiles) validate() error {
	for _, member := range files.members() {
		if len(member.raw) == 0 || len(member.raw) > member.limit {
			return e.New(e.CodeSignatureInvalid, "worker evidence %s is empty or exceeds its bound", member.name)
		}
	}
	return nil
}

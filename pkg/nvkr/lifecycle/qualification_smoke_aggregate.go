// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

// ToolSmokeDescription is an inspectable claim, never transferable authority.
// The full independent policy and native reports make member-level scope visible.
type ToolSmokeDescription struct {
	ArtifactChecks ArtifactCheckDescription        `json:"artifactChecks"`
	Invocation     qualificationorigin.Description `json:"invocation"`
	PolicyDigest   string                          `json:"policyDigest"`
	Policy         QualificationPolicy             `json:"policy"`
	Workers        []NativeToolSmokeReport         `json:"workers"`
}

// ToolSmokeReceipt is issued only by authenticated hosted aggregation. It borrows
// A and the qualifier invocation; closing any owner revokes all copied handles.
type ToolSmokeReceipt struct {
	raw        string
	checks     *ArtifactCheckReceipt
	invocation nativeSmokeInvocation
	closed     *atomic.Bool
}

func (receipt *ToolSmokeReceipt) Description() (ToolSmokeDescription, error) {
	if receipt == nil || receipt.closed == nil || receipt.closed.Load() || receipt.raw == "" {
		return ToolSmokeDescription{}, e.New(e.CodeGateUnmet, "tool smoke receipt is not live")
	}
	checks, err := receipt.checks.Description()
	if err != nil {
		return ToolSmokeDescription{}, err
	}
	if err := receipt.invocation.RequireSource(checks.Origin); err != nil {
		return ToolSmokeDescription{}, err
	}
	var description ToolSmokeDescription
	if err := canonical.UnmarshalExact([]byte(receipt.raw), &description); err != nil {
		return ToolSmokeDescription{}, e.Wrap(e.CodeGateUnmet, err, "reading immutable tool smoke receipt")
	}
	return description, nil
}
func (receipt *ToolSmokeReceipt) Close() error {
	if receipt != nil && receipt.closed != nil {
		receipt.closed.Store(true)
	}
	return nil
}

// Require binds a live B receipt to the exact A, invocation and selected policy.
func (receipt *ToolSmokeReceipt) Require(checks ArtifactCheckDescription, invocation qualificationorigin.Description, policyDigest string) error {
	description, err := receipt.Description()
	if err != nil {
		return err
	}
	if description.Invocation != invocation || description.PolicyDigest != policyDigest || !sameSmokeChecks(description.ArtifactChecks, checks) {
		return e.New(e.CodeGateUnmet, "tool smoke receipt differs from selected artifacts, invocation or policy")
	}
	return nil
}

// AuthenticateHostedToolSmoke owns provider job/result admission and signature
// verification. Caller-authored JSON, flags and booleans cannot issue a receipt.
func AuthenticateHostedToolSmoke(ctx context.Context, checks *ArtifactCheckReceipt, invocation *qualificationorigin.Invocation, policy QualificationPolicy, githubToken string) (*ToolSmokeReceipt, error) {
	return AuthenticateHostedToolSmokeWithCIStorage(ctx, checks, invocation, policy, githubToken, "")
}

// AuthenticateHostedToolSmokeWithCIStorage additionally accepts an independent
// private-payload token. Private payloads also require the explicit account in
// AuthenticateHostedToolSmokeWithCIStorageCredential; no provider-token fallback exists.
func AuthenticateHostedToolSmokeWithCIStorage(ctx context.Context, checks *ArtifactCheckReceipt, invocation *qualificationorigin.Invocation, policy QualificationPolicy, githubToken, storageToken string) (*ToolSmokeReceipt, error) {
	return AuthenticateHostedToolSmokeWithCIStorageCredential(ctx, checks, invocation, policy, githubToken, qualificationorigin.CIStorageCredential{Token: storageToken})
}

// AuthenticateHostedToolSmokeWithCIStorageCredential accepts an independent
// private-package account and read token alongside the provider API credential.
func AuthenticateHostedToolSmokeWithCIStorageCredential(ctx context.Context, checks *ArtifactCheckReceipt, invocation *qualificationorigin.Invocation, policy QualificationPolicy, githubToken string, storage qualificationorigin.CIStorageCredential) (*ToolSmokeReceipt, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "hosted smoke requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "hosted smoke canceled")
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "hosted smoke authentication requires Linux pinned verification")
	}
	pin, err := policy.Cosign.ExecutableDigest("linux/" + runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	verifier, err := sign.NewPinnedCosignVerifier(policy.Cosign.Path, pin)
	if err != nil {
		return nil, err
	}
	provider := qualificationorigin.NewGitHubWithCIStorageCredential(githubToken, storage)
	return authenticateHostedToolSmoke(ctx, checks, invocation, policy, verifier, func(ctx context.Context, platforms []string) ([]qualificationorigin.NativeSmokeArtifact, error) {
		return provider.DownloadNativeSmoke(ctx, invocation, platforms)
	})
}

type smokeResultDownload func(context.Context, []string) ([]qualificationorigin.NativeSmokeArtifact, error)

func authenticateHostedToolSmoke(ctx context.Context, checks *ArtifactCheckReceipt, invocation nativeSmokeInvocation, policy QualificationPolicy, verifier trust.DocumentVerifier, download smokeResultDownload) (*ToolSmokeReceipt, error) {
	operation, finish, err := checks.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(operation, 5*time.Minute)
	defer cancel()
	frozen, err := freezeToolSmokePolicy(policy)
	if err != nil {
		return nil, err
	}
	policyDigest, err := frozen.Digest()
	if err != nil {
		return nil, err
	}
	description, err := checks.Description()
	if err != nil {
		return nil, err
	}
	if invocation == nil {
		return nil, e.New(e.CodeProvenanceMissing, "hosted smoke requires qualifier invocation")
	}
	current, err := invocation.Description()
	if err != nil {
		return nil, err
	}
	if err := nativeSmokeAlive(ctx, checks, invocation, description.Origin); err != nil {
		return nil, err
	}
	if err := matchNativeSmokeOrigins(description.Origin, current, frozen); err != nil {
		return nil, err
	}
	expected, err := qualificationSmokeMembers(checks, frozen)
	if err != nil {
		return nil, err
	}
	if len(expected) == 0 {
		return nil, e.New(e.CodeGateUnmet, "hosted smoke has no executable members")
	}
	platforms := make([]string, 0, len(frozen.Smoke.Images))
	for platform := range frozen.Smoke.Images {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	if download == nil || verifier == nil {
		return nil, e.New(e.CodeEngineCapability, "hosted smoke authenticator is unavailable")
	}
	artifacts, err := download(ctx, append([]string(nil), platforms...))
	if err != nil {
		return nil, err
	}
	if len(artifacts) != len(platforms) {
		return nil, e.New(e.CodeReleaseIncomplete, "native smoke results omit or add a platform")
	}
	workerIdentity := "https://github.com/" + frozen.Source.Repository + "/" + frozen.Smoke.Workflow + "@refs/heads/" + frozen.Source.DefaultBranch
	role, err := trust.NewRolePolicy(trust.RoleToolSmokeWorker, trust.Store{OperatorIdentities: []string{workerIdentity}}, "https://token.actions.githubusercontent.com")
	if err != nil {
		return nil, err
	}
	authenticator, err := trust.NewDocumentAuthenticator(verifier, trust.JSONSchema[NativeToolSmokeReport](NativeToolSmokeSchema))
	if err != nil {
		return nil, err
	}
	constraints := trust.GitHubInvocationConstraints{RunInvocationURI: current.InvocationID, BuildConfigURI: current.BuilderID, BuildConfigDigest: current.Commit}
	workers := make([]NativeToolSmokeReport, 0, len(platforms))
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		if _, ok := frozen.Smoke.Images[artifact.Platform]; !ok || seen[artifact.Platform] {
			return nil, e.New(e.CodeReleaseIncomplete, "native smoke platform is unexpected or duplicated")
		}
		seen[artifact.Platform] = true
		report, err := authenticateNativeSmokeArtifact(ctx, authenticator, role, constraints, artifact)
		if err != nil {
			return nil, err
		}
		if report.Platform != artifact.Platform || report.Invocation != current || report.PolicyDigest != policyDigest || report.Image != frozen.Smoke.Images[report.Platform] || !sameSmokeChecks(report.ArtifactChecks, description) {
			return nil, e.New(e.CodeGateUnmet, "signed smoke result differs from exact source, qualifier, policy or native platform")
		}
		if err := matchSmokeMembers(report, expected); err != nil {
			return nil, err
		}
		if err := nativeSmokeAlive(ctx, checks, invocation, description.Origin); err != nil {
			return nil, err
		}
		workers = append(workers, report)
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].Platform < workers[j].Platform })
	frozen.Cosign.Path = ""
	full := ToolSmokeDescription{ArtifactChecks: description, Invocation: current, PolicyDigest: policyDigest, Policy: frozen, Workers: workers}
	if err := full.Validate(); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(full)
	if err != nil {
		return nil, err
	}
	if err := nativeSmokeAlive(ctx, checks, invocation, description.Origin); err != nil {
		return nil, err
	}
	return &ToolSmokeReceipt{raw: string(raw), checks: checks, invocation: invocation, closed: new(atomic.Bool)}, nil
}

func freezeToolSmokePolicy(policy QualificationPolicy) (QualificationPolicy, error) {
	if err := policy.Validate(); err != nil {
		return QualificationPolicy{}, err
	}
	if policy.Smoke == nil {
		return QualificationPolicy{}, e.New(e.CodeTrustPolicyMissing, "hosted smoke requires independent smoke policy")
	}
	if len(policy.RequiredSuites) > 0 {
		return QualificationPolicy{}, e.New(e.CodeEngineCapability, "required external suite authentication is unavailable")
	}
	raw, err := canonical.Marshal(policy)
	if err != nil {
		return QualificationPolicy{}, err
	}
	var frozen QualificationPolicy
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return QualificationPolicy{}, e.Wrap(e.CodeTrustPolicyMissing, err, "freezing hosted smoke policy")
	}
	sort.Strings(frozen.OutsideMVPSuites)
	sort.Strings(frozen.RequiredSuites)
	sort.Slice(frozen.Smoke.NonToolMembers, func(i, j int) bool {
		return frozen.Smoke.NonToolMembers[i].key() < frozen.Smoke.NonToolMembers[j].key()
	})
	sort.Slice(frozen.Smoke.NonToolOutputs, func(i, j int) bool {
		return frozen.Smoke.NonToolOutputs[i].Output < frozen.Smoke.NonToolOutputs[j].Output
	})
	for i := range frozen.Smoke.NonToolOutputs {
		sort.Strings(frozen.Smoke.NonToolOutputs[i].Components)
	}
	return frozen, nil
}

func sameSmokeChecks(left, right ArtifactCheckDescription) bool {
	a, err := canonical.Marshal(left)
	if err != nil {
		return false
	}
	b, err := canonical.Marshal(right)
	return err == nil && string(a) == string(b)
}
func matchSmokeMembers(report NativeToolSmokeReport, expected []ToolSmokeMember) error {
	native := make([]ToolSmokeMember, 0, len(expected))
	for _, member := range expected {
		if member.Platform == report.Platform {
			member.ObservedVersion = member.ExpectedVersion
			member.ObservedCommit = member.ExpectedCommit
			native = append(native, member)
		}
	}
	if len(native) != len(report.Members) {
		return e.New(e.CodeReleaseIncomplete, "native smoke omits or adds delivered tool members")
	}
	for i := range native {
		if native[i] != report.Members[i] {
			return e.New(e.CodeGateUnmet, "native smoke member differs from exact held artifact or expected version")
		}
	}
	return nil
}

func authenticateNativeSmokeArtifact(ctx context.Context, authenticator *trust.DocumentAuthenticator, role trust.RolePolicy, invocation trust.GitHubInvocationConstraints, artifact qualificationorigin.NativeSmokeArtifact) (NativeToolSmokeReport, error) {
	// Provider transport checks these limits too. Repeat at the consumption boundary
	// before allocating private files; never write caller-controlled file names.
	for _, field := range []struct {
		raw   []byte
		limit int
	}{{artifact.Document, 16 << 20}, {artifact.Signature, 64 << 10}, {artifact.Certificate, 1 << 20}, {artifact.Bundle, 16 << 20}} {
		if len(field.raw) == 0 || len(field.raw) > field.limit {
			return NativeToolSmokeReport{}, e.New(e.CodeVerifyFailed, "smoke signature material is absent or oversized")
		}
	}
	if err := ctx.Err(); err != nil {
		return NativeToolSmokeReport{}, e.Wrap(e.CodeTimeout, err, "native smoke verification canceled")
	}
	directory, err := os.MkdirTemp("", "nvkr-smoke-proof-")
	if err != nil {
		return NativeToolSmokeReport{}, e.Wrap(e.CodeVerifyFailed, err, "creating private smoke proof storage")
	}
	defer func() { _ = os.RemoveAll(directory) }()
	for _, field := range []struct {
		name string
		raw  []byte
	}{{"worker.json", artifact.Document}, {"worker.sig", artifact.Signature}, {"worker.pem", artifact.Certificate}, {"worker.bundle.json", artifact.Bundle}} {
		if err := ctx.Err(); err != nil {
			return NativeToolSmokeReport{}, e.Wrap(e.CodeTimeout, err, "staging smoke proof canceled")
		}
		if err := os.WriteFile(filepath.Join(directory, field.name), field.raw, 0o600); err != nil {
			return NativeToolSmokeReport{}, e.Wrap(e.CodeVerifyFailed, err, "staging private smoke proof")
		}
	}
	authenticated, err := authenticator.Authenticate(ctx, trust.AuthenticateRequest{SourcePath: filepath.Join(directory, "worker.json"), Signature: trust.SignatureMaterial{SignaturePath: filepath.Join(directory, "worker.sig"), CertificatePath: filepath.Join(directory, "worker.pem"), BundlePath: filepath.Join(directory, "worker.bundle.json")}, Role: trust.RoleToolSmokeWorker, Policy: role, GitHubInvocation: &invocation})
	if err != nil {
		return NativeToolSmokeReport{}, err
	}
	report, ok := authenticated.Decoded().(NativeToolSmokeReport)
	if !ok {
		return NativeToolSmokeReport{}, e.New(e.CodeVerifyFailed, "authenticated worker report has an unsupported schema")
	}
	if err := report.Validate(); err != nil {
		return NativeToolSmokeReport{}, err
	}
	return report, nil
}

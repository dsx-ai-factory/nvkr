// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

type hostedSmokeVerifier func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error)

func (f hostedSmokeVerifier) VerifyDocument(ctx context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	return f(ctx, req)
}
func smokeAggregateFixture(t *testing.T) (*ArtifactCheckReceipt, *nativeSmokeTestInvocation, QualificationPolicy, NativeToolSmokeReport) {
	t.Helper()
	checks, invocation, policy, owner := nativeSmokeLiveFixture(t)
	description, err := checks.Description()
	if err != nil {
		t.Fatal(err)
	}
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	members := append([]ToolSmokeMember(nil), owner.expected...)
	for i := range members {
		members[i].ObservedVersion = members[i].ExpectedVersion
		members[i].ObservedCommit = members[i].ExpectedCommit
	}
	report := NativeToolSmokeReport{SchemaVersion: NativeToolSmokeSchema, ArtifactChecks: description, Invocation: invocation.description, PolicyDigest: digest, Platform: "linux/amd64", Image: policy.Smoke.Images["linux/amd64"], RuntimeImage: nativeSmokeTestDigest("4"), RuntimeIdentity: nativeSmokeTestDigest("5"), Members: members, Status: "pass"}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	return checks, invocation, policy, report
}
func smokeArtifact(t *testing.T, report NativeToolSmokeReport) qualificationorigin.NativeSmokeArtifact {
	t.Helper()
	raw, err := canonical.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return qualificationorigin.NativeSmokeArtifact{Platform: report.Platform, Invocation: report.Invocation, Document: raw, Signature: []byte("signature"), Certificate: []byte("certificate"), Bundle: []byte("bundle")}
}
func validSmokeVerification(t *testing.T, req trust.DocumentVerificationRequest) trust.VerificationEvidence {
	t.Helper()
	if req.Role != trust.RoleToolSmokeWorker || req.GitHubInvocation == nil || len(req.AllowedIdentities) != 1 || !strings.Contains(req.AllowedIdentities[0], "/.github/workflows/smoke.yml@refs/heads/main") || req.OIDCIssuer != "https://token.actions.githubusercontent.com" || len(req.ExactBytes) == 0 {
		t.Fatalf("unbound worker verification: %+v", req)
	}
	constraints := *req.GitHubInvocation
	return trust.VerificationEvidence{CertificateIdentities: append([]string(nil), req.AllowedIdentities...), OIDCIssuer: req.OIDCIssuer, CertificateSHA256: nativeSmokeTestDigest("6"), GitHubInvocation: &constraints}
}

func TestHostedSmokePartialRetryAuthenticatesOriginalWorkerInvocation(t *testing.T) {
	checks, invocation, policy, report := smokeAggregateFixture(t)
	original := report.Invocation
	invocation.description.Attempt = "12"
	invocation.description.InvocationID = "https://github.com/" + original.Repository + "/actions/runs/" + original.RunID + "/attempts/12"
	receipt, err := authenticateHostedToolSmoke(t.Context(), checks, invocation, policy, hostedSmokeVerifier(func(_ context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
		if req.GitHubInvocation == nil || req.GitHubInvocation.RunInvocationURI != original.InvocationID {
			t.Fatalf("earlier worker signature was rebound to later attempt: %+v", req.GitHubInvocation)
		}
		return validSmokeVerification(t, req), nil
	}), func(context.Context, []string) ([]qualificationorigin.NativeSmokeArtifact, error) {
		return []qualificationorigin.NativeSmokeArtifact{smokeArtifact(t, report)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receipt.Close() }()
	description, err := receipt.Description()
	if err != nil {
		t.Fatal(err)
	}
	if description.Invocation != invocation.description || description.Workers[0].Invocation != original {
		t.Fatal("aggregation did not retain original worker and current qualifier identities")
	}
	if err := description.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := receipt.Require(report.ArtifactChecks, invocation.description, report.PolicyDigest); err != nil {
		t.Fatal(err)
	}
}

func TestHostedSmokePartialRetryRejectsWorkerIdentitySubstitution(t *testing.T) {
	checks, invocation, policy, report := smokeAggregateFixture(t)
	invocation.description.Attempt = "12"
	invocation.description.InvocationID = "https://github.com/" + report.Invocation.Repository + "/actions/runs/" + report.Invocation.RunID + "/attempts/12"
	for _, tc := range []struct {
		name string
		edit func(*qualificationorigin.Description)
	}{
		{"future attempt", func(d *qualificationorigin.Description) { d.Attempt = "13" }},
		{"different run", func(d *qualificationorigin.Description) { d.RunID = "51" }},
		{"different commit", func(d *qualificationorigin.Description) { d.Commit = strings.Repeat("b", 40) }},
		{"different workflow", func(d *qualificationorigin.Description) { d.Workflow = ".github/workflows/other.yml" }},
		{"different repository", func(d *qualificationorigin.Description) { d.Repository = "other/nvkr" }},
		{"different branch", func(d *qualificationorigin.Description) { d.DefaultBranch = "other" }},
		{"different development branch", func(d *qualificationorigin.Description) { d.Branch = "feature/other" }},
		{"report differs from selected provider job", func(d *qualificationorigin.Description) { d.Attempt = "11" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact := smokeArtifact(t, report)
			tc.edit(&artifact.Invocation)
			d := &artifact.Invocation
			d.Ref = "refs/heads/" + d.SourceBranch()
			d.InvocationID = "https://github.com/" + d.Repository + "/actions/runs/" + d.RunID + "/attempts/" + d.Attempt
			d.BuilderID = "https://github.com/" + d.Repository + "/" + d.Workflow + "@" + d.Ref
			receipt, err := authenticateHostedToolSmoke(t.Context(), checks, invocation, policy, hostedSmokeVerifier(func(_ context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
				return validSmokeVerification(t, req), nil
			}), func(context.Context, []string) ([]qualificationorigin.NativeSmokeArtifact, error) {
				return []qualificationorigin.NativeSmokeArtifact{artifact}, nil
			})
			assertCode(t, err, e.CodeGateUnmet)
			if receipt != nil {
				t.Fatal("substituted worker minted receipt")
			}
		})
	}
}
func TestHostedSmokeRejectsSignedScopeSubstitution(t *testing.T) {
	checks, invocation, policy, report := smokeAggregateFixture(t)
	invocation.description.Attempt = "12"
	invocation.description.InvocationID = "https://github.com/" + report.Invocation.Repository + "/actions/runs/" + report.Invocation.RunID + "/attempts/12"
	for _, tc := range []struct {
		name string
		edit func(*NativeToolSmokeReport)
		code e.Code
	}{
		{"wrong policy", func(r *NativeToolSmokeReport) { r.PolicyDigest = nativeSmokeTestDigest("9") }, e.CodeGateUnmet},
		{"wrong native image", func(r *NativeToolSmokeReport) { r.Image = "registry.example.test/other@" + nativeSmokeTestDigest("9") }, e.CodeGateUnmet},
		{"wrong output payload", func(r *NativeToolSmokeReport) { r.Members[0].PayloadDigest = nativeSmokeTestDigest("9") }, e.CodeGateUnmet},
		{"wrong payload size", func(r *NativeToolSmokeReport) { r.Members[0].Size++ }, e.CodeGateUnmet},
		{"wrong expected version", func(r *NativeToolSmokeReport) {
			r.Members[0].ExpectedVersion = "v9.9.9"
			r.Members[0].ObservedVersion = "v9.9.9"
		}, e.CodeGateUnmet},
		{"omitted member", func(r *NativeToolSmokeReport) { r.Members = r.Members[:len(r.Members)-1] }, e.CodeReleaseIncomplete},
		{"changed exact A", func(r *NativeToolSmokeReport) { r.ArtifactChecks.CatalogDigest = nativeSmokeTestDigest("9") }, e.CodeGateUnmet},
		{"changed source", func(r *NativeToolSmokeReport) { r.ArtifactChecks.Origin.Commit = strings.Repeat("b", 40) }, e.CodeGateUnmet},
		{"changed Plan", func(r *NativeToolSmokeReport) { r.ArtifactChecks.Inputs.PlanDigest = nativeSmokeTestDigest("9") }, e.CodeGateUnmet},
		{"failed report", func(r *NativeToolSmokeReport) { r.Status = "fail"; r.Code = string(e.CodeGateUnmet) }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := canonical.Marshal(report)
			var changed NativeToolSmokeReport
			if err := canonical.UnmarshalExact(raw, &changed); err != nil {
				t.Fatal(err)
			}
			tc.edit(&changed)
			receipt, err := authenticateHostedToolSmoke(t.Context(), checks, invocation, policy, hostedSmokeVerifier(func(_ context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
				return validSmokeVerification(t, req), nil
			}), func(context.Context, []string) ([]qualificationorigin.NativeSmokeArtifact, error) {
				return []qualificationorigin.NativeSmokeArtifact{smokeArtifact(t, changed)}, nil
			})
			if receipt != nil {
				t.Fatal("invalid scope minted receipt")
			}
			assertCode(t, err, tc.code)
		})
	}
}
func TestHostedSmokeAuthenticatesBeforeMintAndFreezesDescriptions(t *testing.T) {
	checks, invocation, policy, report := smokeAggregateFixture(t)
	artifact := smokeArtifact(t, report)
	calls := 0
	verifier := hostedSmokeVerifier(func(_ context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
		calls++
		return validSmokeVerification(t, req), nil
	})
	receipt, err := authenticateHostedToolSmoke(t.Context(), checks, invocation, policy, verifier, func(_ context.Context, platforms []string) ([]qualificationorigin.NativeSmokeArtifact, error) {
		if len(platforms) != 1 || platforms[0] != "linux/amd64" {
			t.Fatal("wrong selected workers")
		}
		return []qualificationorigin.NativeSmokeArtifact{artifact}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receipt.Close() }()
	if calls != 1 {
		t.Fatal("missing signature verification")
	}
	description, err := receipt.Description()
	if err != nil {
		t.Fatal(err)
	}
	if err := description.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := receipt.Require(report.ArtifactChecks, report.Invocation, report.PolicyDigest); err != nil {
		t.Fatal(err)
	}
	assertCode(t, receipt.Require(report.ArtifactChecks, report.Invocation, nativeSmokeTestDigest("9")), e.CodeGateUnmet)
	description.Workers[0].Members[0].ObservedVersion = "forged"
	description.Policy.Smoke.Tools[report.Members[0].Member] = "nvkectl-version-json"
	artifact.Document[0] = '!'
	policy.Smoke.Images["linux/amd64"] = "forged"
	fresh, err := receipt.Description()
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Validate(); err != nil {
		t.Fatalf("borrowed mutation changed receipt: %v", err)
	}
	copyReceipt := *receipt
	if err := receipt.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = copyReceipt.Description()
	assertCode(t, err, e.CodeGateUnmet)
}
func TestHostedSmokeVerifierFailureAndFinalRevocation(t *testing.T) {
	for _, mode := range []string{"signature", "wrong signer", "missing invocation claims", "wrong invocation claims", "relabeled earlier attempt", "cancel", "qualifier revoked"} {
		t.Run(mode, func(t *testing.T) {
			checks, invocation, policy, report := smokeAggregateFixture(t)
			invocation.description.Attempt = "12"
			invocation.description.InvocationID = "https://github.com/" + report.Invocation.Repository + "/actions/runs/" + report.Invocation.RunID + "/attempts/12"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			receipt, err := authenticateHostedToolSmoke(ctx, checks, invocation, policy, hostedSmokeVerifier(func(_ context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
				ev := validSmokeVerification(t, req)
				switch mode {
				case "signature":
					return trust.VerificationEvidence{}, e.New(e.CodeSignatureInvalid, "rejected fixture")
				case "wrong signer":
					ev.CertificateIdentities = []string{"https://github.com/evil/worker"}
				case "missing invocation claims":
					ev.GitHubInvocation = nil
				case "wrong invocation claims":
					ev.GitHubInvocation.BuildConfigDigest = strings.Repeat("f", 40)
				case "relabeled earlier attempt":
					ev.GitHubInvocation.RunInvocationURI = invocation.description.InvocationID
				case "cancel":
					cancel()
				case "qualifier revoked":
					invocation.closed.Store(true)
				}
				return ev, nil
			}), func(context.Context, []string) ([]qualificationorigin.NativeSmokeArtifact, error) {
				return []qualificationorigin.NativeSmokeArtifact{smokeArtifact(t, report)}, nil
			})
			if err == nil || receipt != nil {
				t.Fatalf("%s minted receipt", mode)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			var coded *e.Error
			if !errors.As(err, &coded) {
				t.Fatalf("uncoded failure: %v", err)
			}
		})
	}
}
func TestHostedSmokeProviderFailurePrecedesVerification(t *testing.T) {
	checks, invocation, policy, report := smokeAggregateFixture(t)
	for _, mode := range []string{"provider failure", "missing worker", "duplicate worker", "unknown platform", "empty signature"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			receipt, err := authenticateHostedToolSmoke(t.Context(), checks, invocation, policy, hostedSmokeVerifier(func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
				calls++
				return trust.VerificationEvidence{}, nil
			}), func(context.Context, []string) ([]qualificationorigin.NativeSmokeArtifact, error) {
				artifact := smokeArtifact(t, report)
				switch mode {
				case "provider failure":
					return nil, e.New(e.CodeFetchFailed, "provider failure")
				case "missing worker":
					return nil, nil
				case "duplicate worker":
					return []qualificationorigin.NativeSmokeArtifact{artifact, artifact}, nil
				case "unknown platform":
					artifact.Platform = "linux/unknown"
				case "empty signature":
					artifact.Signature = nil
				}
				return []qualificationorigin.NativeSmokeArtifact{artifact}, nil
			})
			if err == nil || receipt != nil || calls != 0 {
				t.Fatalf("invalid transport reached verification: %v %d", err, calls)
			}
		})
	}
}

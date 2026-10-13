// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package trust

import (
	"context"
	"testing"
)

func TestAuthenticateFreezesAndRequiresInvocationClaims(t *testing.T) {
	for _, mode := range []string{"exact", "caller mutation", "adapter mutation", "missing", "wrong"} {
		t.Run(mode, func(t *testing.T) {
			expected := GitHubInvocationConstraints{RunInvocationURI: "https://github.com/owner/repo/actions/runs/10/attempts/1", BuildConfigURI: "https://github.com/owner/repo/.github/workflows/qualify.yml@refs/heads/main", BuildConfigDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
			original := expected
			authenticator := testAuthenticator(t, documentVerifierFunc(func(_ context.Context, req DocumentVerificationRequest) (VerificationEvidence, error) {
				if req.GitHubInvocation == nil || *req.GitHubInvocation != original {
					t.Fatal("lost independent invocation policy")
				}
				ev := goodEvidence()
				claims := *req.GitHubInvocation
				ev.GitHubInvocation = &claims
				switch mode {
				case "caller mutation":
					expected.RunInvocationURI = "changed"
				case "adapter mutation":
					req.GitHubInvocation.RunInvocationURI = "changed"
					ev.GitHubInvocation = req.GitHubInvocation
				case "missing":
					ev.GitHubInvocation = nil
				case "wrong":
					ev.GitHubInvocation.BuildConfigDigest = "changed"
				}
				return ev, nil
			}))
			receipt, err := authenticator.Authenticate(t.Context(), AuthenticateRequest{SourcePath: writeTestDocument(t, canonicalTestDocument(t, "smoke")), Role: RoleToolSmokeWorker, Policy: testPolicy(t, RoleToolSmokeWorker), GitHubInvocation: &expected})
			success := mode == "exact" || mode == "caller mutation"
			if success != (err == nil) || success != receipt.Authenticated() {
				t.Fatalf("mode %s: receipt=%v error=%v", mode, receipt.Authenticated(), err)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSDKAuthenticateReleaseAdmitsClientAndContext(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		sdk  *client.Client
		ctx  context.Context
		want nvkrerr.Code
	}{
		{name: "unconfigured consumer", sdk: client.New(), ctx: t.Context(), want: nvkrerr.CodeEngineCapability},
		{name: "nil client", ctx: t.Context(), want: nvkrerr.CodeUsage},
		{name: "nil context", sdk: client.New(), want: nvkrerr.CodeTimeout},
		{name: "canceled context", sdk: client.New(), ctx: canceled, want: nvkrerr.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			authenticated, err := test.sdk.AuthenticateRelease(test.ctx,
				"registry.example.test/releases@sha256:"+strings.Repeat("a", 64),
				client.VerificationPolicy{Lane: "pipeline-attested", Scope: "authenticity", IdentityRegexp: "^builder$", Issuer: "issuer"})
			requireLifecycleCode(t, err, test.want)
			if authenticated != nil {
				t.Fatal("rejected SDK call returned an authenticated capability")
			}
		})
	}
}

func TestSDKAuthenticationAndVerificationScopesRemainSeparate(t *testing.T) {
	for _, test := range []struct {
		name         string
		authenticate bool
		lane         string
		scope        string
		want         nvkrerr.Code
	}{
		{name: "authenticate full payload scope", authenticate: true, lane: "pipeline-attested", scope: "artifact-publication", want: nvkrerr.CodeEngineCapability},
		{name: "authenticate full release scope", authenticate: true, lane: "pipeline-attested", scope: "release", want: nvkrerr.CodeEngineCapability},
		{name: "authenticate qualified scope", authenticate: true, lane: "qualified", scope: "authenticity", want: nvkrerr.CodeTrustPolicyMissing},
		{name: "verify authenticity scope", lane: "pipeline-attested", scope: "authenticity", want: nvkrerr.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "must-not-exist")
			sdk := client.New(client.WithReleaseConsumer(client.ReleaseConsumerOptions{WorkspaceRoot: workspace}))
			policy := client.VerificationPolicy{Lane: test.lane, Scope: test.scope, IdentityRegexp: "^builder$", Issuer: "issuer"}
			reference := "registry.example.test/releases@sha256:" + strings.Repeat("a", 64)
			var err error
			if test.authenticate {
				_, err = sdk.AuthenticateRelease(t.Context(), reference, policy)
			} else {
				_, err = sdk.VerifyRelease(t.Context(), reference, policy)
			}
			requireLifecycleCode(t, err, test.want)
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected scope touched workspace: %v", err)
			}
		})
	}
}

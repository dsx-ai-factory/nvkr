// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package sign

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

func TestProductionVerifierGitHubInvocationUsesHeldCertificate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		wrong  bool
		cancel bool
	}{
		{name: "exact"}, {name: "wrong commit", wrong: true}, {name: "cancel running verifier", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extensions := invocationExtensions(t)
			if tc.wrong {
				extensions[2] = utf8Extension(t, fulcioBuildConfigDigest, strings.Repeat("b", 40))
			}
			req := invocationRequest(t, invocationCertificate(t, extensions, invocationWorkerIdentity, invocationIssuer, false))
			binaryBytes := heldCosignHelperBytes(t)
			binary := filepath.Join(t.TempDir(), "cosign")
			if err := os.WriteFile(binary, binaryBytes, 0o700); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(binaryBytes)
			verifier, err := NewPinnedCosignVerifier(binary, "sha256:"+hex.EncodeToString(sum[:]))
			if err != nil {
				t.Fatal(err)
			}

			coordination := t.TempDir()
			ready := filepath.Join(coordination, "ready")
			continuePath := filepath.Join(coordination, "continue")
			finish := filepath.Join(coordination, "finish")
			t.Setenv("NVKR_COSIGN_HELD_HELPER", "1")
			t.Setenv("NVKR_COSIGN_READY", ready)
			t.Setenv("NVKR_COSIGN_CONTINUE", continuePath)
			t.Setenv("NVKR_COSIGN_CAPTURED", filepath.Join(coordination, "captured"))
			t.Setenv("NVKR_COSIGN_FINISH", finish)
			if !tc.cancel {
				for _, path := range []string{continuePath, finish} {
					if err := os.WriteFile(path, []byte("ready"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			joined := make(chan bool, 1)
			if tc.cancel {
				go func() {
					readyObserved := waitForTestPath(ready)
					cancel()
					joined <- readyObserved
				}()
			}
			evidence, err := verifier.VerifyDocument(ctx, req)
			want := e.Code("")
			if tc.cancel {
				want = e.CodeTimeout
				if !<-joined {
					t.Fatal("verifier never started")
				}
			} else if tc.wrong {
				want = e.CodeUntrustedSigner
			}
			if signCode(err) != want {
				t.Fatalf("code=%s want=%s err=%v", signCode(err), want, err)
			}
			if want != "" {
				if !reflect.DeepEqual(evidence, trust.VerificationEvidence{}) {
					t.Fatalf("failure returned claims: %+v", evidence)
				}
				return
			}
			if evidence.GitHubInvocation == nil || *evidence.GitHubInvocation != invocationConstraints() {
				t.Fatalf("evidence=%+v", evidence)
			}
		})
	}
}

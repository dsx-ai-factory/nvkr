// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func TestSBOMVerificationOwnsDeadlineAndJoinsRunner(t *testing.T) {
	for _, test := range []struct {
		name           string
		parentDeadline time.Duration
		cancelAfter    time.Duration
		cancelBefore   bool
		waitForCancel  bool
		returnSuccess  bool
		wantCause      error
		elapsed        time.Duration
	}{
		{name: "successful verification"},
		{name: "standalone deadline", waitForCancel: true, wantCause: context.DeadlineExceeded, elapsed: 3*time.Minute + time.Second},
		{name: "earlier parent deadline", parentDeadline: 50 * time.Millisecond, waitForCancel: true, wantCause: context.DeadlineExceeded, elapsed: 50*time.Millisecond + time.Second},
		{name: "already canceled", cancelBefore: true, wantCause: context.Canceled},
		{name: "caller cancellation", cancelAfter: 25 * time.Millisecond, waitForCancel: true, wantCause: context.Canceled, elapsed: 25*time.Millisecond + time.Second},
		{name: "successful runner after deadline", waitForCancel: true, returnSuccess: true, wantCause: context.DeadlineExceeded, elapsed: 3*time.Minute + time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				bom, err := sbom.NewSubject(ociTestDigest, "component", ociTestDigest)
				if err != nil {
					t.Fatal(err)
				}
				document, err := bom.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				bundle := testSBOMBundle(t, ociTestDigest, document)
				started := time.Now()
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				wantDeadline := started.Add(3 * time.Minute)
				if test.parentDeadline != 0 {
					var stop context.CancelFunc
					wantDeadline = started.Add(test.parentDeadline)
					ctx, stop = context.WithDeadline(ctx, wantDeadline)
					defer stop()
				}
				if test.cancelBefore {
					cancel()
				}
				calls, runnerJoined := 0, false
				workspace := ""
				runner := EnvRunnerFunc(func(runCtx context.Context, _ string, args, _ []string, _, _ io.Writer) error {
					calls++
					if deadline, ok := runCtx.Deadline(); !ok || !deadline.Equal(wantDeadline) {
						t.Fatalf("verification runner deadline=%v present=%v, want %v", deadline, ok, wantDeadline)
					}
					if args[0] != "verify-blob-attestation" {
						t.Fatalf("unexpected command: %s", args[0])
					}
					for i, arg := range args {
						if arg == "--bundle" && i+1 < len(args) {
							workspace = filepath.Dir(args[i+1])
						}
					}
					if workspace == "" {
						t.Fatal("verification omitted the exact bundle input")
					}
					if test.cancelAfter != 0 {
						time.AfterFunc(test.cancelAfter, cancel)
					}
					if test.waitForCancel {
						<-runCtx.Done()
						// Simulate bounded child stop-and-join after cancellation.
						// Verification must wait for the runner to finish cleanup.
						time.Sleep(time.Second)
					}
					runnerJoined = true
					if test.returnSuccess {
						return nil
					}
					return runCtx.Err()
				})
				signer, err := NewCosignOCISigner(CosignOCIOptions{Identity: ociTestIdentity, Issuer: ociTestIssuer, Runner: runner})
				if err != nil {
					t.Fatal(err)
				}
				err = signer.VerifySBOM(ctx, ociTestDigest, bundle)
				if test.wantCause != nil {
					requireSignCode(t, err, e.CodeTimeout)
					if !errors.Is(err, test.wantCause) {
						t.Fatalf("verification lost cancellation cause: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if test.cancelBefore {
					if calls != 0 {
						t.Fatal("canceled request started verification")
					}
				} else {
					if calls != 1 || !runnerJoined {
						t.Fatalf("verification did not join its single runner: calls=%d joined=%v", calls, runnerJoined)
					}
					if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("verification retained its private workspace: %v", err)
					}
				}
				if elapsed := time.Since(started); elapsed != test.elapsed {
					t.Fatalf("verification elapsed=%v, want %v", elapsed, test.elapsed)
				}
			})
		})
	}
}

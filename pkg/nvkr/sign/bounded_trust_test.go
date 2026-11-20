// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

type trustTransport func(*http.Request) (*http.Response, error)

func (f trustTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBoundedTrustResponseAccounting(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         int
		size           int
		allowance      int64
		max            int64
		wantHTTP       bool
		wantExhaustion bool
	}{
		{"successful-target", 200, 32, 128, 64, false, false},
		{"root-rotation-404-is-metered", 404, 32, 128, 64, true, false},
		{"failed-response-exhausts", 503, 128, 32, 256, false, true},
		{"target-exhausts", 200, 128, 32, 256, false, true},
		{"document-cap", 200, 128, 256, 32, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, budget, err := readbudget.WithLimit(t.Context(), tc.allowance)
			if err != nil {
				t.Fatal(err)
			}
			transport := trustTransport(func(request *http.Request) (*http.Response, error) {
				if request.Context() != ctx {
					t.Fatal("fetcher lost phase context")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", tc.size))), Header: http.Header{}}, nil
			})
			f := &boundedTrustFetcher{ctx: ctx, client: &http.Client{Transport: readbudget.Transport(ctx, transport)}}
			raw, err := f.DownloadFile(tufFixtureURL, tc.max, time.Hour)
			if tc.wantExhaustion {
				requireSignCode(t, err, e.CodeEngineCapability)
				if err == nil || len(raw) != 0 || budget.Check() == nil || budget.Used() != tc.allowance {
					t.Fatal("response escaped budget", err, budget.Used())
				}
				return
			}
			if tc.wantHTTP {
				var status *metadata.ErrDownloadHTTP
				if !errors.As(err, &status) || status.StatusCode != tc.status || budget.Used() != int64(tc.size) {
					t.Fatal("TUF status or metering lost", err, budget.Used())
				}
				return
			}
			if int64(tc.size) > tc.max {
				var length *metadata.ErrDownloadLengthMismatch
				if !errors.As(err, &length) || len(raw) != 0 {
					t.Fatal("oversized document accepted", err)
				}
				return
			}
			if err != nil || len(raw) != tc.size || budget.Used() != int64(tc.size) {
				t.Fatal("incorrect successful read", err, budget.Used())
			}
		})
	}
}

const tufFixtureURL = "https://tuf-repo-cdn.sigstore.dev/metadata/2.root.json"

func TestBoundedTrustAdmissionAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ctx, _, err := readbudget.WithLimit(ctx, 1024)
	if err != nil {
		t.Fatal(err)
	}
	f := &boundedTrustFetcher{ctx: ctx, client: &http.Client{Transport: trustTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}}
	for _, location := range []string{"http://tuf-repo-cdn.sigstore.dev/root.json", "https://other.invalid/root.json", tufFixtureURL + "?redirect=x"} {
		_, err := f.DownloadFile(location, 64, time.Second)
		requireSignCode(t, err, e.CodeTrustPolicyMissing)
	}
	cancel()
	if _, err := f.DownloadFile(tufFixtureURL, 64, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("fetch ignored phase cancellation", err)
	}
	err = WithBoundedVerification(t.Context(), func(context.Context) error { t.Fatal("unmetered callback"); return nil })
	requireSignCode(t, err, e.CodeEngineCapability)
}

func testBoundedTrustContext(t *testing.T) (context.Context, *boundedTrust, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	ctx, budget, err := readbudget.WithLimit(ctx, 1024)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic private state tests lifetime/argument binding, never TUF or
	// cryptographic acceptance. Production construction authenticates through TUF.
	owned := &boundedTrust{ctx: ctx, budget: budget, raw: []byte(`{"synthetic":"trust snapshot"}`)}
	return context.WithValue(ctx, boundedTrustKey{}, owned), owned, cancel
}

func TestBoundedTrustSnapshotLifetimeAndArguments(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   bool
		cancel   bool
		stripped bool
	}{
		{"exact-snapshot", false, false, false}, {"changed-file", true, false, false},
		{"closed-snapshot", false, true, false}, {"removed-deadline", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, owned, cancel := testBoundedTrustContext(t)
			if tc.cancel {
				cancel()
			}
			if tc.stripped {
				ctx = context.WithoutCancel(ctx)
			}
			called := false
			var path string
			err := withBoundedBundleVerification(ctx, []string{"verify-blob-attestation", "--bundle", "fixture.json"}, func(args []string) error {
				called = true
				i := slices.Index(args, "--trusted-root")
				if i < 0 || !slices.Contains(args, "--new-bundle-format=true") || slices.Contains(args, "--insecure-ignore-tlog") {
					t.Fatal("lost trust/format gate", args)
				}
				path = args[i+1]
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != string(owned.raw) {
					t.Fatal("substituted snapshot", err)
				}
				if tc.mutate {
					return os.WriteFile(path, []byte("changed"), 0600)
				}
				return nil
			})
			if tc.cancel || tc.stripped {
				if called {
					t.Fatal("expired context reached verifier", err)
				}
				code := e.CodeEngineCapability
				if tc.cancel {
					code = e.CodeTimeout
				}
				requireSignCode(t, err, code)
				return
			}
			if tc.mutate {
				requireSignCode(t, err, e.CodeImmutableViolation)
			}
			if tc.mutate && err == nil || !tc.mutate && err != nil || !called {
				t.Fatal("incorrect snapshot result", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("snapshot survived verification", err)
			}
			if owned.pending != 0 {
				t.Fatal("verification owner did not join")
			}
		})
	}
}

func TestBoundedTrustJoinsUnfinishedVerification(t *testing.T) {
	ctx, owned, _ := testBoundedTrustContext(t)
	started, finished := make(chan struct{}), make(chan error, 1)
	var joined atomic.Bool
	var escaped context.Context
	err := withBoundedTrustSnapshot(ctx, owned.budget, owned.raw, func(ctx context.Context) error {
		escaped = ctx
		go func() {
			finished <- withBoundedBundleVerification(ctx, []string{"verify-blob-attestation"}, func([]string) error {
				close(started)
				<-ctx.Done()
				joined.Store(true)
				return ctx.Err()
			})
		}()
		<-started
		return nil
	})
	requireSignCode(t, err, e.CodeGateUnmet)
	if !joined.Load() {
		t.Fatal("trust owner returned before joining verification")
	}
	if err := <-finished; err == nil {
		t.Fatal("unfinished verification succeeded")
	}
	_, err = admitBoundedTrust(escaped)
	requireSignCode(t, err, e.CodeTimeout)
}

func TestBoundedTrustVerificationRecordsAndFatalErrors(t *testing.T) {
	for _, scenario := range []string{"exact", "mutated-root", "missing-owner", "no-deadline", "unsupported-transport", "joined-cleanup", "coded-runner"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, _, _ := testBoundedTrustContext(t)
			stub := &cosignStub{stdout: map[string]string{"version": cosignVersionJSON}}
			calls := 0
			stub.onCall = func(call recordedCall) error {
				if call.args[0] != "verify-blob-attestation" {
					return nil
				}
				calls++
				index := slices.Index(call.args, "--trusted-root")
				if index < 0 {
					t.Fatal("bounded invocation omitted authenticated root")
				}
				if scenario == "joined-cleanup" {
					return errors.Join(errors.Join(nil, e.New(e.CodeSignatureInvalid, "rejected")), e.New(e.CodeImmutableViolation, "synthetic cleanup failure"))
				}
				if scenario == "coded-runner" {
					return e.New(e.CodeUnpinnedInput, "synthetic pinned-runner failure")
				}
				if scenario == "mutated-root" {
					return os.WriteFile(call.args[index+1], []byte("substituted"), 0600)
				}
				return nil
			}
			signer := newStubbedSigner(t, stub, Credential{})
			signature := []byte(testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer))
			provenance := []byte(testOCIBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer))
			if scenario == "missing-owner" || scenario == "no-deadline" || scenario == "unsupported-transport" {
				var err error
				ctx, _, err = readbudget.WithLimit(t.Context(), 1024)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "no-deadline" {
					ctx = context.WithoutCancel(ctx)
					err = WithBoundedVerification(ctx, func(context.Context) error { t.Fatal("unbounded trust callback"); return nil })
					requireSignCode(t, err, e.CodeEngineCapability)
					return
				}
				if scenario == "unsupported-transport" {
					ctx, cancel := context.WithTimeout(ctx, time.Minute)
					defer cancel()
					previous := http.DefaultTransport
					http.DefaultTransport = trustTransport(func(*http.Request) (*http.Response, error) { t.Fatal("unsupported transport used"); return nil, nil })
					defer func() { http.DefaultTransport = previous }()
					err = WithBoundedVerification(ctx, func(context.Context) error { t.Fatal("unsupported transport callback"); return nil })
					requireSignCode(t, err, e.CodeEngineCapability)
					return
				}
			}
			record, proof, err := signer.VerifyBundles(ctx, ociSubject(), VerifyPolicy{RequireAttestation: true}, signature, provenance)
			if scenario == "missing-owner" {
				requireSignCode(t, err, e.CodeEngineCapability)
				if calls != 0 || proof != nil {
					t.Fatal("unadmitted trust reached cosign")
				}
				return
			}
			if scenario == "mutated-root" || scenario == "joined-cleanup" || scenario == "coded-runner" {
				code := e.CodeImmutableViolation
				if scenario == "coded-runner" {
					code = e.CodeUnpinnedInput
				}
				requireSignCode(t, err, code)
				if calls != 1 || proof != nil {
					t.Fatal("trust mutation continued candidate/provenance work", calls)
				}
				return
			}
			if err != nil || proof == nil || calls != 2 {
				t.Fatal("bounded bundle verification failed", err, calls)
			}
			for _, args := range record.Ran {
				i := slices.Index(args, "--trusted-root")
				if i < 0 || args[i+1] != "${NVKR_TRUSTED_ROOT}" || !slices.Contains(args, "--new-bundle-format=true") {
					t.Fatal("record lost executed trust arguments", args)
				}
			}
		})
	}
}

func TestBoundedCandidateJoinedFailures(t *testing.T) {
	rejected := e.New(e.CodeSignatureInvalid, "candidate rejected")
	cleanup := e.New(e.CodeImmutableViolation, "cleanup failed")
	for _, tc := range []struct {
		name  string
		err   error
		fatal bool
	}{
		{"signature", rejected, false},
		{"signatures-only", errors.Join(rejected, rejected), false},
		{"cleanup-after-signature", errors.Join(rejected, cleanup), true},
		{"cleanup-before-signature", errors.Join(cleanup, rejected), true},
		{"nested-cleanup", errors.Join(errors.Join(nil, nil, nil, rejected), cleanup), true},
		{"wrapped-join", fmt.Errorf("verification: %w", errors.Join(rejected, cleanup)), true},
		{"coded-parent", e.Wrap(e.CodeSignatureInvalid, errors.Join(rejected, cleanup), "candidate"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, _ := testBoundedTrustContext(t)
			err := boundedVerificationCandidateFailure(ctx, tc.err)
			if !tc.fatal {
				if err != nil {
					t.Fatal("signature-only fallback lost", err)
				}
				return
			}
			requireSignCode(t, err, e.CodeImmutableViolation)
			if !errors.Is(err, rejected) || !errors.Is(err, cleanup) {
				t.Fatal("candidate or cleanup cause lost", err)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func authenticityPresentationFixture() lifecycle.AuthenticityDescription {
	release := releasePresentationFixtureResult(false).Release
	return lifecycle.AuthenticityDescription{
		SchemaVersion: lifecycle.AuthenticitySchemaVersion, Subject: release.Subject,
		Lane: lifecycle.LanePipeline, Scope: lifecycle.ScopeAuthenticity,
		Artifacts: release.Artifacts, ContentSubject: release.ContentSubject,
		PolicyDigest: release.PolicyDigest, PayloadIntegrityVerified: false,
	}
}

func TestReleaseAuthenticityTUIKeepsNarrowResultAndOwnerCode(t *testing.T) {
	for _, code := range []e.Code{"", e.CodeSignatureInvalid, e.CodeTimeout, e.CodeEngineCapability} {
		t.Run(string(code), func(t *testing.T) {
			calls, shows := 0, 0
			var owner error
			if code != "" {
				owner = e.New(code, "private-owner-error-sentinel")
			}
			authenticate := func(_ context.Context, _ string, policy lifecycle.VerificationPolicy, _ lifecycle.ConsumerOptions) (lifecycle.AuthenticityDescription, error) {
				calls++
				if policy.Scope != lifecycle.ScopeAuthenticity || policy.Lane != lifecycle.LanePipeline {
					t.Fatal("authentication policy changed")
				}
				return authenticityPresentationFixture(), owner
			}
			reporting := e.New(e.CodeUnavailable, "renderer failure")
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, opts runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				shows++
				if calls != 1 || work != nil || opts.Live {
					t.Fatal("result viewer repeated or retained owner work")
				}
				snapshot := source.Snapshot()
				assertReleasePresentationSafe(t, snapshot)
				if owner != nil {
					if snapshot.Status != "failed" || snapshot.FailureCode != string(code) || len(snapshot.Items) != 0 {
						t.Fatal("owner failure became an authenticated result")
					}
					return runpresenter.Result{PresentationErr: reporting}
				}
				if snapshot.Status != "complete" || !strings.Contains(snapshot.Note, "payload integrity was not checked") ||
					len(snapshot.Items) != 1 || snapshot.Items[0].State != "authenticated" {
					t.Fatalf("authenticity result overclaimed payload checks: %+v", snapshot)
				}
				fields := make(map[string]string)
				for _, field := range snapshot.Fields {
					fields[field.Label] = field.Value
				}
				if fields["Accepted scope"] != lifecycle.ScopeAuthenticity || fields["Accepted lane"] != lifecycle.LanePipeline ||
					fields["Payload integrity"] != "not checked" {
					t.Fatal("accepted scope or payload limit lost")
				}
				return runpresenter.Result{}
			}}
			var out bytes.Buffer
			app := &cli.App{Commands: []*cli.Command{cmdReleaseWithOperations(rejectFullVerification(t), authenticate)},
				Writer: &out, Metadata: map[string]interface{}{commandPresentationServicesKey: services}}
			raw, err := runCLIWithCapturedStdout(t, app, append(authenticityCLIArgs("unused"), "--ui=tui")...)
			if owner == nil && err != nil {
				t.Fatal(err)
			}
			if owner != nil {
				assertLifecycleCommandCode(t, err, code)
				if !errors.Is(err, owner) || !errors.Is(err, reporting) {
					t.Fatal("owning or reporting error lost")
				}
			}
			if calls != 1 || shows != 1 || out.Len() != 0 || len(raw) != 0 {
				t.Fatal("TUI operation count or output changed")
			}
		})
	}
}

func TestReleaseAuthenticityPresentationAdmissionBeforeOwner(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		eligible   bool
	}{
		{"unknown mode", "unknown", true},
		{"redirected TUI", "tui", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			authenticate := func(context.Context, string, lifecycle.VerificationPolicy, lifecycle.ConsumerOptions) (lifecycle.AuthenticityDescription, error) {
				t.Fatal("rejected presentation reached authentication")
				return lifecycle.AuthenticityDescription{}, nil
			}
			services := runPresentationServices{eligible: func(io.Writer) bool { return test.eligible }, show: func(context.Context, runpresenter.Options, runSnapshotSource, func(context.Context) error) runpresenter.Result {
				t.Fatal("rejected presentation opened a viewer")
				return runpresenter.Result{}
			}}
			app := &cli.App{Commands: []*cli.Command{cmdReleaseWithOperations(rejectFullVerification(t), authenticate)},
				Metadata: map[string]interface{}{commandPresentationServicesKey: services}}
			err := app.RunContext(t.Context(), append(authenticityCLIArgs("unused"), "--ui="+test.mode))
			assertLifecycleCommandCode(t, err, e.CodeUsage)
		})
	}
}

func TestReleaseAuthenticityPresentationPreservesPlainBytes(t *testing.T) {
	description := authenticityPresentationFixture()
	expected, err := canonical.Marshal(description)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", "plain"} {
		t.Run(mode, func(t *testing.T) {
			authenticate := func(context.Context, string, lifecycle.VerificationPolicy, lifecycle.ConsumerOptions) (lifecycle.AuthenticityDescription, error) {
				return description, nil
			}
			app := &cli.App{Commands: []*cli.Command{cmdReleaseWithOperations(rejectFullVerification(t), authenticate)}}
			args := authenticityCLIArgs("unused")
			if mode != "" {
				args = append(args, "--ui="+mode)
			}
			raw, err := runCLIWithCapturedStdout(t, app, args...)
			if err != nil || !bytes.Equal(raw, append(expected, '\n')) {
				t.Fatalf("plain authenticity JSON changed: error=%v output=%q", err, raw)
			}
		})
	}
}

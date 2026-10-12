// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package status

import (
	"context"
	"os"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

func diagnosticFixture(t *testing.T, action string) (DiagnosticCheckpointRequest, string, []byte) {
	t.Helper()
	policy, err := trust.NewRolePolicy(trust.RoleStatus, trust.Store{CLIIdentities: []string{checkpointSigner}}, checkpointIssuer)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := StatusCheckpoint{SchemaVersion: CheckpointSchemaVersion, Target: target, Authority: checkpointAuthority,
		Sequence: 1, PreviousCheckpoint: statusDigest('0'), DeclarationDigests: []string{}, IssuedEpoch: 100, ValidUntilEpoch: 300}
	request := DiagnosticCheckpointRequest{Policy: checkpointPolicy(t, checkpointSigner), AsOf: explicitAsOf(t, 200)}
	if action != "" {
		declaration := StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: action, EffectiveEpoch: 150}
		raw, marshalErr := declaration.Marshal()
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		checkpoint.DeclarationDigests = []string{declarationDigest(t, declaration)}
		request.Declarations = []trust.AuthenticateRequest{{
			SourcePath: writeStatusStateFile(t, "declaration.json", raw), Role: trust.RoleStatus, Policy: policy,
		}}
	}
	raw, err := checkpoint.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	request.Checkpoint = trust.AuthenticateRequest{SourcePath: writeStatusStateFile(t, "checkpoint.json", raw), Role: trust.RoleStatus, Policy: policy}
	initial, err := canonical.Marshal(highWaterFile{SchemaVersion: highWaterSchemaVersion, Target: target, Authority: checkpointAuthority,
		Sequence: 0, CheckpointDigest: checkpoint.PreviousCheckpoint})
	if err != nil {
		t.Fatal(err)
	}
	request.HighWaterPath = writeStatusStateFile(t, "high-water.json", initial)
	digest, err := checkpoint.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return request, digest, initial
}

func TestDiagnosticCheckpointCommitsBeforeDecision(t *testing.T) {
	for _, test := range []struct {
		action string
		code   e.Code
	}{
		{"", ""}, {ActionDeprecated, ""}, {ActionWithdrawn, e.CodeWithdrawn},
	} {
		t.Run("action="+test.action, func(t *testing.T) {
			request, digest, _ := diagnosticFixture(t, test.action)
			result, err := AcceptDiagnosticCheckpoint(context.Background(), request, statusDocumentVerifier{checkpointSigner, checkpointIssuer})
			if code(err) != test.code {
				t.Fatalf("decision error = %v; want %s", err, test.code)
			}
			var state highWaterFile
			if _, err := readProtectedHighWaterContext(context.Background(), request.HighWaterPath, &state); err != nil {
				t.Fatal(err)
			}
			if state.Sequence != 1 || state.CheckpointDigest != digest {
				t.Fatalf("decision returned before accepting checkpoint: %+v", state)
			}
			if test.code == "" && (result.CheckpointDigest != digest || result.Sequence != 1 || result.Decision.Action != test.action) {
				t.Fatalf("decision lost authenticated checkpoint binding: %+v", result)
			}
			// The accepted checkpoint can be evaluated again at the same minimum.
			_, err = AcceptDiagnosticCheckpoint(context.Background(), request, statusDocumentVerifier{checkpointSigner, checkpointIssuer})
			if code(err) != test.code {
				t.Fatalf("same-checkpoint replay changed decision: %v", err)
			}
		})
	}
}

func TestDiagnosticCheckpointRejectsWithoutChangingMinimum(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*DiagnosticCheckpointRequest)
		code e.Code
	}{
		{"missing policy", func(r *DiagnosticCheckpointRequest) { r.Policy = CheckpointPolicy{} }, e.CodeTrustPolicyMissing},
		{"missing epoch", func(r *DiagnosticCheckpointRequest) { r.AsOf = AsOf{} }, e.CodeStatusCheckpoint},
		{"missing declaration", func(r *DiagnosticCheckpointRequest) { r.Declarations = nil }, e.CodeStatusCheckpoint},
		{"foreign role", func(r *DiagnosticCheckpointRequest) { r.Checkpoint.Role = trust.RoleBuilderProfile }, e.CodeTrustPolicyMissing},
		{"wrong target", func(r *DiagnosticCheckpointRequest) {
			var err error
			r.Policy, err = NewCheckpointPolicy(replacement, checkpointAuthority, checkpointIssuer, []string{checkpointSigner})
			if err != nil {
				t.Fatal(err)
			}
		}, e.CodeStatusCheckpoint},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _, initial := diagnosticFixture(t, ActionWithdrawn)
			test.edit(&request)
			_, err := AcceptDiagnosticCheckpoint(context.Background(), request, statusDocumentVerifier{checkpointSigner, checkpointIssuer})
			if code(err) != test.code {
				t.Fatalf("rejection = %v; want %s", err, test.code)
			}
			assertStatusBytes(t, request.HighWaterPath, initial)
		})
	}
}

type diagnosticVerifierFunc func(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error)

func (f diagnosticVerifierFunc) VerifyDocument(ctx context.Context, request trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	return f(ctx, request)
}

func TestDiagnosticCheckpointOwnsVerificationAndCAS(t *testing.T) {
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{"signature failure", e.CodeSignatureInvalid},
		{"concurrent accepted state", e.CodeWriteConflict},
		{"canceled verification", e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, _, initial := diagnosticFixture(t, "")
			want := initial
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			verifier := diagnosticVerifierFunc(func(ctx context.Context, document trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
				switch test.name {
				case "signature failure":
					return trust.VerificationEvidence{}, e.New(e.CodeSignatureInvalid, "signature rejected")
				case "concurrent accepted state":
					var state highWaterFile
					if err := canonical.UnmarshalExact(initial, &state); err != nil {
						t.Fatal(err)
					}
					state.Sequence = 2
					state.CheckpointDigest = statusDigest('2')
					var err error
					want, err = canonical.Marshal(state)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(request.HighWaterPath, want, 0o600); err != nil {
						t.Fatal(err)
					}
				case "canceled verification":
					cancel()
				}
				return (statusDocumentVerifier{checkpointSigner, checkpointIssuer}).VerifyDocument(ctx, document)
			})
			_, err := AcceptDiagnosticCheckpoint(ctx, request, verifier)
			if code(err) != test.code {
				t.Fatalf("acceptance = %v; want %s", err, test.code)
			}
			assertStatusBytes(t, request.HighWaterPath, want)
		})
	}
}

func assertStatusBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("status minimum changed: %s (%v); want %s", got, err, want)
	}
}

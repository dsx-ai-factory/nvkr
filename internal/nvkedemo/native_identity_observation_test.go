// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/siteidentity"
)

func TestNativeIdentityResultObservationKeepsPrivateFieldsClosed(t *testing.T) {
	result := siteidentity.Result{SchemaVersion: siteidentity.SchemaVersion, Completion: siteidentity.Unknown, FailureCode: nvkrerr.CodeVerifyFailed, CallerID: "PRIVATE_CALLER", Operation: "PRIVATE_OPERATION", Reconfiguration: "PRIVATE_RESULT"}
	encode := func(result siteidentity.Result) string {
		t.Helper()
		raw, err := canonical.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	valid := encode(result)
	unknown := result
	unknown.Completion, unknown.FailureCode = "PRIVATE_COMPLETION", "PRIVATE_CODE"
	wrongSchema := result
	wrongSchema.SchemaVersion = "PRIVATE_SCHEMA"
	for _, tc := range []struct {
		name, raw, want string
		err             error
	}{
		{"source failure", valid, "command=failed exit=17 envelope=valid completion=unknown failure-code=NVKR-E-VERIFY-FAILED", &CommandError{Phase: "PRIVATE_PHASE", ExitCode: 17, Cause: errors.New("PRIVATE_CAUSE")}},
		{"joined local success", valid + "\n", "command=succeeded exit=0 envelope=valid completion=unknown", nil},
		{"unjoined child", valid, "command=unjoined exit=unavailable", errors.Join(ErrUnjoined, &CommandError{ExitCode: 17})},
		{"deadline", valid, "command=deadline exit=unavailable", context.DeadlineExceeded},
		{"canceled", valid, "command=canceled exit=unavailable", context.Canceled},
		{"unknown command", valid, "command=failed exit=unavailable", errors.New("PRIVATE_ERROR")},
		{"invalid exit", valid, "command=failed exit=unavailable", &CommandError{ExitCode: 1000}},
		{"missing", "", "envelope=missing completion=unavailable failure-code=unavailable", nil},
		{"oversized", strings.Repeat("x", (64<<10)+1), "envelope=oversized completion=unavailable", nil},
		{"invalid", "PRIVATE_OUTPUT", "envelope=invalid completion=unavailable", nil},
		{"duplicate", `{"completion":"unknown","completion":"PRIVATE"}`, "envelope=invalid", nil},
		{"trailing", valid + "{}", "envelope=invalid", nil},
		{"schema", encode(wrongSchema), "envelope=unsupported-schema completion=unavailable", nil},
		{"unknown fields", encode(unknown), "envelope=valid completion=unknown failure-code=unknown", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nativeIdentityResultObservation([]byte(tc.raw), tc.err)
			if !strings.Contains(got, tc.want) || strings.Contains(got, "PRIVATE") || len(got) > 512 {
				t.Fatalf("unsafe or incomplete observation: %q", got)
			}
		})
	}
}

func TestNativeIdentityHandoffObservationPreservesAdmissionAndRetention(t *testing.T) {
	for _, scenario := range []string{"verify", "recheck", "missing result", "remote unknown", "caller", "status", "success"} {
		t.Run(scenario, func(t *testing.T) {
			attempt, runner, root, _ := nativeIdentityReadinessFixture(t)
			runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_POLICY_UNREADY": ""})
			progress := &identityPolicyProgress{}
			runner.Progress = progress
			wantOperation, wantUnknown := "", false
			switch scenario {
			case "verify":
				runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_VERIFY_FAILURE": "1"})
				wantOperation = "verify-helper"
			case "recheck":
				runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_POLICY_UNREADY": "after-policy"})
				wantOperation = "runtime-recheck"
			case "missing result":
				writeTenantFixture(t, filepath.Join(root, "identity-result.json"), nil, 0600)
				wantOperation, wantUnknown = "helper-command", true
			case "remote unknown", "caller", "status":
				raw, err := os.ReadFile(filepath.Join(root, "identity-result.json"))
				if err != nil {
					t.Fatal(err)
				}
				var result siteidentity.Result
				if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte("\n")), &result); err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "remote unknown":
					result.Completion, result.FailureCode = siteidentity.Unknown, nvkrerr.CodeVerifyFailed
					runner.Env = SetEnv(runner.Env, map[string]string{"IDENTITY_HANDOFF_EXIT": "22"})
					wantOperation, wantUnknown = "helper-command", true
				case "caller":
					result.CallerID = "PRIVATE_OTHER_CALLER"
					wantOperation = "validate-caller"
				case "status":
					result.Status = nil
					wantOperation = "validate-status"
				}
				raw, err = canonical.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				writeTenantFixture(t, filepath.Join(root, "identity-result.json"), raw, 0600)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			receipt, err := attempt.completeNativeSiteIdentity(ctx, runner, "172.30.0.2", root)
			if (err == nil) != (scenario == "success") || (receipt != nil) != (scenario == "success") || errors.Is(err, ErrUnjoined) != wantUnknown || errors.Is(attempt.unjoinedWork, ErrUnjoined) != wantUnknown {
				t.Fatal("diagnostics changed handoff acceptance or retention", err)
			}
			observations := strings.Join(progress.observations, "")
			if strings.Contains(observations, "PRIVATE") || strings.Contains(observations, "command=unjoined") || wantOperation != "" && !strings.Contains(observations, "Site identity handoff stopped during "+wantOperation+".\n") {
				t.Fatal("missing fixed operation or private output leaked", observations)
			}
			if scenario == "remote unknown" && !strings.Contains(observations, "command=failed exit=22 envelope=valid completion=unknown failure-code=NVKR-E-VERIFY-FAILED") {
				t.Fatal("remote completion was conflated with local command exit", observations)
			}
			if scenario == "missing result" && !strings.Contains(observations, "command=succeeded exit=0 envelope=missing completion=unavailable") {
				t.Fatal("missing remote completion was conflated with local command exit", observations)
			}
			events, readErr := os.ReadFile(filepath.Join(root, "events"))
			if readErr != nil || strings.Count(string(events), "identity-handoff\n") > 1 || (scenario == "verify" || scenario == "recheck") && strings.Contains(string(events), "identity-handoff\n") {
				t.Fatal("diagnostics retried or bypassed pre-mutation checks", readErr)
			}
		})
	}
}

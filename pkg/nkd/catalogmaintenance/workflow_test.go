// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

func TestWorkflowIsDeterministicReadOnlyAndExplicitAboutExecution(t *testing.T) {
	cfg, _ := maintenanceFixture(t)
	for _, mode := range []string{"static", "local-run"} {
		t.Run(mode, func(t *testing.T) {
			cfg.Validation = mode
			cfg.Schedule = "0 6 * * 1"
			first, err := Workflow(t.Context(), cfg, "maintenance.yaml", "test (deadbeef)")
			if err != nil {
				t.Fatal(err)
			}
			second, err := Workflow(t.Context(), cfg, "maintenance.yaml", "test (deadbeef)")
			if err != nil || !bytes.Equal(first, second) {
				t.Fatalf("unstable workflow: %v", err)
			}
			var workflow map[string]any
			if err := yaml.UnmarshalStrict(first, &workflow); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"schedule:", "'0 6 * * 1'", "contents: read", "cancel-in-progress: false", "timeout-minutes:", "persist-credentials: false", "github.event.repository.default_branch", "ref: ${{ github.sha }}", "if-no-files-found: error", "make check", "--check", "catalog refresh", "candidate.patch", "-ldflags '-X main.version=test -X main.commit=deadbeef'"} {
				if !bytes.Contains(first, []byte(want)) {
					t.Fatalf("missing %s", want)
				}
			}
			for _, bad := range []string{"pull_request_target", "contents: write", "pull-requests: write", "id-token:", "git push", "--dry-run", "--publish", "self-hosted"} {
				if bytes.Contains(first, []byte(bad)) {
					t.Fatalf("unexpected %s", bad)
				}
			}
			hasRun := strings.Contains(string(first), "run local --definition")
			if hasRun != (mode == "local-run") {
				t.Fatal("execution mode not honored")
			}
		})
	}
	cfg.Schedule = ""
	raw, err := Workflow(t.Context(), cfg, "maintenance.yaml", "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "schedule:") {
		t.Fatal("schedule was implicitly enabled")
	}
	cfg.PullRequest = &PullRequest{ClientIDVariable: "BOT_ID", PrivateKeySecret: "BOT_KEY"}
	_, err = Workflow(t.Context(), cfg, "missing.yaml", "test (deadbeef)")
	assertCode(t, err, e.CodeEngineCapability)
}

func TestWorkflowRejectsMalformedGeneratorBeforeInput(t *testing.T) {
	for _, version := range []string{"missing stamp", "v1.0.0 (bad;command)", "$(touch marker) (deadbeef)"} {
		t.Run(version, func(t *testing.T) {
			raw, err := Workflow(t.Context(), validConfig(), "missing.yaml", version)
			assertCode(t, err, e.CodeInvalidCatalog)
			if len(raw) != 0 {
				t.Fatal("malformed generator emitted workflow bytes")
			}
		})
	}
}

func TestLocalValidationRejectsHostedEnvironmentBeforeMutationOrOutput(t *testing.T) {
	cfg, _ := maintenanceFixture(t)
	baseline := readFixture(t, cfg.Environment)
	beforeLock := readFixture(t, "catalog/pins.lock.yaml")
	beforePlan := readFixture(t, ".github/nkd-run/plan.json")
	for _, tc := range []struct{ name, extra string }{
		{"source authentication", "sourceAccess:\n  https://maintenance.example.invalid/source:\n    mirror: https://maintenance.example.invalid/source\n    credentialRef: github:SOURCE_TOKEN\n"},
		{"hosted dispatch", "githubDispatchBranches: [candidate]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFixture(t, cfg.Environment, append(append([]byte(nil), baseline...), tc.extra...))
			cfg.Validation = "static"
			if _, err := Workflow(t.Context(), cfg, "maintenance.yaml", "test (deadbeef)"); err != nil {
				t.Fatalf("static mode should remain supported: %v", err)
			}
			cfg.Validation = "local-run"
			raw, err := Workflow(t.Context(), cfg, "maintenance.yaml", "test (deadbeef)")
			assertCode(t, err, e.CodeEngineCapability)
			if len(raw) != 0 {
				t.Fatal("unsupported local capability emitted a workflow")
			}
			calls := 0
			_, err = refresh(t.Context(), cfg, "test (deadbeef)", false, func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
				calls++
				return catalog.SourcePinUpdate{}, nil
			})
			assertCode(t, err, e.CodeEngineCapability)
			if calls != 0 || !bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) || !bytes.Equal(beforePlan, readFixture(t, ".github/nkd-run/plan.json")) {
				t.Fatal("unsupported local capability reached mutation")
			}
		})
	}
}

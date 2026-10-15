// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

func profileFixture() Configuration {
	return Configuration{Source: Source{Kind: "published", Reference: "registry.example.test/release@sha256:" + strings.Repeat("a", 64), Policy: "policy.yaml"}, Checks: []Check{{Name: "kube-conformance", Suite: "kube-conformance", Harness: "hydrophone", Config: "checks/conformance.json"}}}
}
func saveProfile(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "validation.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func requireError(t *testing.T, err error, code e.Code, message string) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code || !strings.Contains(err.Error(), message) {
		t.Fatalf("error=%v want=%s containing %q", err, code, message)
	}
}
func TestProfileCapabilityRefusalPrecedesSourceAndHarnessWork(t *testing.T) {
	for _, tc := range []struct {
		name, selection string
		edit            func(*Configuration)
		code            e.Code
		message         string
	}{
		{"unknown selection", "ai-tests", func(*Configuration) {}, e.CodeUnsupported, "not configured"},
		{"AI check", "", func(p *Configuration) { p.Checks[0].Suite = "ai-tests" }, e.CodeUnsupported, "suite"},
		{"unsupported unselected check", "kube-conformance", func(p *Configuration) {
			p.Checks = append(p.Checks, Check{Name: "user-added", Suite: "ai-tests", Harness: "command", Config: "/must-not-open"})
		}, e.CodeUnsupported, "suite"},
		{"other harness", "", func(p *Configuration) { p.Checks[0].Harness = "sonobuoy" }, e.CodeUnsupported, "harness"},
		{"local source", "", func(p *Configuration) { p.Source.Kind = "local" }, e.CodeEngineCapability, "owning diagnostic capability"},
		{"wrong reference", "", func(p *Configuration) { p.Source.Reference = "registry.example.test/release:latest" }, e.CodeUsage, "source reference"},
		{"GitHub Run URL", "", func(p *Configuration) { p.Source.Reference = "https://github.com/example/nvkr/actions/runs/42" }, e.CodeUsage, "immutable OCI"},
		{"duplicate check", "", func(p *Configuration) { p.Checks = append(p.Checks, p.Checks[0]) }, e.CodeUsage, "unique"},
		{"missing checks", "", func(p *Configuration) { p.Checks = nil }, e.CodeUsage, "configured checks"},
		{"policy whitespace", "", func(p *Configuration) { p.Source.Policy = " policy.yaml" }, e.CodeUsage, "policy path"},
		{"config control", "", func(p *Configuration) { p.Checks[0].Config = "config\x1b" }, e.CodeUsage, "configuration path"},
		{"oversized config path", "", func(p *Configuration) { p.Checks[0].Config = strings.Repeat("x", 4097) }, e.CodeUsage, "configuration path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := profileFixture()
			profile.Source.Policy = "/must-not-open-policy"
			profile.Checks[0].Config = "/must-not-open-config"
			tc.edit(&profile)
			raw, err := json.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			outputs, err := RunFiles(t.Context(), tc.selection, saveProfile(t, raw), lifecycle.QualificationRuntimeOptions{WorkspaceRoot: "/must-not-create"})
			requireError(t, err, tc.code, tc.message)
			if len(outputs) != 0 {
				t.Fatal("capability refusal emitted reports")
			}
		})
	}
}
func TestProfileStrictJSONAndBounds(t *testing.T) {
	valid, err := json.Marshal(profileFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"duplicate keys", []byte(`{"source":{},"source":{},"checks":[]}`)},
		{"unknown field", append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"passed":true}`)...)},
		{"profile credential", []byte(strings.Replace(string(valid), `"source":{`, `"source":{"credential":"private",`, 1))},
		{"trailing document", append(append([]byte(nil), valid...), []byte(` {}`)...)},
		{"oversized", []byte(strings.Repeat("x", configurationByteLimit+1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := loadConfiguration(t.Context(), "", saveProfile(t, tc.raw))
			requireError(t, err, e.CodeUsage, "validation configuration")
		})
	}
}
func TestGenericDispatchRetainsPartialOutputAndError(t *testing.T) {
	source := profileFixture().Source
	checks := profileFixture().Checks
	original := e.New(e.CodeTimeout, "runner canceled")
	calls := 0
	outputs, err := runSelected(t.Context(), source, checks, lifecycle.QualificationRuntimeOptions{WorkspaceRoot: "selected"}, func(ctx context.Context, reference, policy, config string, options lifecycle.QualificationRuntimeOptions) (testresults.Output, error) {
		calls++
		if ctx != t.Context() || reference != source.Reference || policy != source.Policy || config != checks[0].Config || options.WorkspaceRoot != "selected" {
			t.Fatal("dispatch changed configured arguments")
		}
		return testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete}, original
	})
	if calls != 1 || !errors.Is(err, original) || len(outputs) != 1 || outputs[0].CaptureStatus != testresults.CapturePartial {
		t.Fatalf("partial output lost: %+v %v calls=%d", outputs, err, calls)
	}
	outputs, err = runSelected(t.Context(), source, checks, lifecycle.QualificationRuntimeOptions{}, func(context.Context, string, string, string, lifecycle.QualificationRuntimeOptions) (testresults.Output, error) {
		return testresults.Output{}, original
	})
	if !errors.Is(err, original) || len(outputs) != 0 {
		t.Fatal("admission error fabricated output")
	}
	outputs, err = runSelected(t.Context(), source, checks, lifecycle.QualificationRuntimeOptions{}, func(context.Context, string, string, string, lifecycle.QualificationRuntimeOptions) (testresults.Output, error) {
		return testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete}, nil
	})
	if err != nil || len(outputs) != 1 || outputs[0].CaptureStatus != testresults.CaptureComplete {
		t.Fatal("successful capture changed")
	}
	second := checks[0]
	second.Name, second.Config = "second", "checks/invalid.json"
	third := checks[0]
	third.Name, third.Config = "third", "checks/must-not-run.json"
	checks = append(checks, second, third)
	laterAdmission := e.New(e.CodeUsage, "second producer configuration refused")
	calls = 0
	outputs, err = runSelected(t.Context(), source, checks, lifecycle.QualificationRuntimeOptions{}, func(_ context.Context, _, _, config string, _ lifecycle.QualificationRuntimeOptions) (testresults.Output, error) {
		calls++
		if config == second.Config {
			return testresults.Output{}, laterAdmission
		}
		return testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete}, nil
	})
	if calls != 2 || !errors.Is(err, laterAdmission) || len(outputs) != 1 || outputs[0].CaptureStatus != testresults.CaptureComplete {
		t.Fatalf("later refusal lost earlier capture or continued execution: calls%d outputs%+v err%v", calls, outputs, err)
	}
}
func TestRunFilesContextAndPathAdmission(t *testing.T) {
	var missingContext context.Context
	_, err := RunFiles(missingContext, "", "/must-not-open", lifecycle.QualificationRuntimeOptions{WorkspaceRoot: "workspace"})
	requireError(t, err, e.CodeUsage, "context")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = RunFiles(ctx, "", "/must-not-open", lifecycle.QualificationRuntimeOptions{WorkspaceRoot: "workspace"})
	requireError(t, err, e.CodeTimeout, "canceled")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation cause lost")
	}
	for _, path := range []string{"", " path", strings.Repeat("x", 4097)} {
		_, err = RunFiles(t.Context(), "", path, lifecycle.QualificationRuntimeOptions{WorkspaceRoot: "workspace"})
		requireError(t, err, e.CodeUsage, "configuration and workspace")
	}
}

func TestConfiguredCheckInstancesSelectTheirExplicitSuite(t *testing.T) {
	profile := profileFixture()
	profile.Checks[0].Name = "my-customer-check"
	second := profile.Checks[0]
	second.Name = "another-check"
	second.Config = "checks/second.json"
	profile.Checks = append(profile.Checks, second)
	raw, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	path := saveProfile(t, raw)
	for _, tc := range []struct {
		selection string
		count     int
	}{{"", 2}, {"my-customer-check", 1}, {"another-check", 1}} {
		loaded, selected, err := loadConfiguration(t.Context(), tc.selection, path)
		if err != nil {
			t.Fatal(err)
		}
		if len(selected) != tc.count {
			t.Fatalf("selection %q: got %d checks", tc.selection, len(selected))
		}
		if loaded.Source.Policy != filepath.Join(filepath.Dir(path), profile.Source.Policy) {
			t.Fatal("policy path did not resolve beside configuration")
		}
		for _, check := range selected {
			config := profile.Checks[0].Config
			if check.Name == second.Name {
				config = second.Config
			}
			if check.Config != filepath.Join(filepath.Dir(path), config) {
				t.Fatal("check path did not resolve beside configuration")
			}
			if check.Suite != "kube-conformance" || check.Harness != "hydrophone" {
				t.Fatal("instance name changed explicit suite/harness")
			}
		}
	}
}

func TestProfileCheckCountBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		count   int
		allowed bool
	}{
		{"at limit", 64, true}, {"over limit", 65, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := profileFixture()
			for len(profile.Checks) < tc.count {
				check := profile.Checks[0]
				check.Name = strings.Repeat("x", len(profile.Checks)+1)
				profile.Checks = append(profile.Checks, check)
			}
			err := profile.validate()
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireError(t, err, e.CodeUsage, "1..64 configured checks")
		})
	}
}

func TestCompleteCaptureContinuesAfterNonzeroHarnessExit(t *testing.T) {
	profile := profileFixture()
	next := profile.Checks[0]
	next.Name, next.Config = "next", "next.json"
	profile.Checks = append(profile.Checks, next)
	calls := 0
	outputs, err := runSelected(t.Context(), profile.Source, profile.Checks, lifecycle.QualificationRuntimeOptions{}, func(context.Context, string, string, string, lifecycle.QualificationRuntimeOptions) (testresults.Output, error) {
		calls++
		return testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete,
			Environment: []testresults.EnvironmentFact{{Scope: "harness", Name: "process.exitCode", Phase: "after", Metadata: testresults.Metadata{Status: "observed", Value: "1"}}}}, nil
	})
	if err != nil || calls != 2 || len(outputs) != 2 {
		t.Fatalf("complete capture stopped later check: calls=%d outputs=%d err=%v", calls, len(outputs), err)
	}
	for _, output := range outputs {
		if output.CaptureStatus != testresults.CaptureComplete || output.Environment[0].Metadata.Value != "1" {
			t.Fatal("dispatch changed capture status or harness fact")
		}
	}
}

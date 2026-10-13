// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

const kubeFixture = `apiVersion: v1
kind: Config
current-context: exact
clusters:
- name: private-cluster
  cluster:
    server: https://private.invalid
    certificate-authority-data: Y2E=
users:
- name: private-user
  user:
    token: private-token
contexts:
- name: exact
  context:
    cluster: private-cluster
    user: private-user
preferences: {}
`

func optionsFixture() Options {
	pin := "sha256:" + strings.Repeat("a", 64)
	return Options{Hydrophone: Tool{Path: "/not-opened", Digest: pin}, Kubectl: Tool{Path: "/not-opened", Digest: pin}, Kubeconfig: Tool{Path: "/not-opened", Digest: pin}, Context: "exact", CNISelector: "app=cni", Image: "registry.k8s.io/conformance@" + pin, ImageVersion: "1.35.1", BusyboxImage: "registry.k8s.io/e2e-test-images/busybox@" + pin, RunTimeout: time.Minute, CommandTimeout: 2 * time.Second, CleanupTimeout: 5 * time.Second, Observation: testobservation.Observation{SchemaVersion: testobservation.SchemaVersion, Inputs: testobservation.Inputs{DefinitionDigest: pin, EnvironmentDigest: pin, PlanDigest: pin}, Origin: testobservation.Origin{Status: "unknown"}, Subjects: []testobservation.Subject{{Output: "cluster", Platform: "linux/amd64", BuildDigest: pin, ArtifactDigest: pin, PayloadDigest: pin}}, Suite: testobservation.Suite{Name: "kubernetes-conformance", HarnessRevision: HarnessRevision, ImageDigest: pin, PolicyDigest: pin, Invocation: "independent-invocation", Selection: []string{`\[Conformance\]`}, RequiredCases: []testobservation.CaseID{{Suite: "Kubernetes e2e suite", Class: "Kubernetes e2e suite", Name: "[It] required"}}}, Environment: testobservation.Environment{Role: "local", Facts: []testobservation.Fact{{Name: "role", Value: "local", Evidence: "declared", Phase: "before"}}}, Execution: testobservation.Outcome{Status: "not-run"}, Cleanup: testobservation.Outcome{Status: "unknown"}}}
}
func TestAdmissionBeforeWork(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Options)
	}{
		{"image tag", func(o *Options) { o.Image = "registry.k8s.io/conformance:v1.35.1" }},
		{"sidecar tag", func(o *Options) { o.BusyboxImage = "busybox:latest" }},
		{"missing tool pin", func(o *Options) { o.Hydrophone.Digest = "" }},
		{"version unknown", func(o *Options) { o.ImageVersion = "unknown" }},
		{"quick selection", func(o *Options) { o.Observation.Suite.Selection = []string{"quick"} }},
		{"no required cases", func(o *Options) { o.Observation.Suite.RequiredCases = nil }},
		{"wrong harness", func(o *Options) { o.Observation.Suite.HarnessRevision = strings.Repeat("b", 40) }},
		{"no context", func(o *Options) { o.Context = "" }},
		{"no CNI classification", func(o *Options) { o.CNISelector = "" }},
		{"unbounded timeout", func(o *Options) { o.RunTimeout = 0 }},
		{"missing independent pin", func(o *Options) { o.Observation.Inputs.PlanDigest = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := optionsFixture()
			tt.change(&o)
			r, err := Run(context.Background(), o)
			var coded *e.Error
			if r != nil || !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("admission result=%v error=%v", r, err)
			}
		})
	}
	if runtime.GOOS != "linux" {
		_, err := Run(context.Background(), optionsFixture())
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
			t.Fatalf("unsupported platform = %v", err)
		}
	}
}
func TestKubeconfigAdmission(t *testing.T) {
	tests := []struct {
		name, raw, context string
		valid              bool
	}{
		{"embedded token", kubeFixture, "exact", true},
		{"wrong context", kubeFixture, "other", false},
		{"external CA", strings.Replace(kubeFixture, "certificate-authority-data: Y2E=", "certificate-authority: /private/ca.pem", 1), "exact", false},
		{"exec plugin", strings.Replace(kubeFixture, "token: private-token", "exec: {command: unpinned}", 1), "exact", false},
		{"auth provider", strings.Replace(kubeFixture, "token: private-token", "auth-provider: {name: gcp}", 1), "exact", false},
		{"duplicate target", strings.Replace(kubeFixture, "current-context: exact", "current-context: exact\ncurrent-context: other", 1), "exact", false},
		{"TLS bypass", strings.Replace(kubeFixture, "certificate-authority-data: Y2E=", "insecure-skip-tls-verify: true", 1), "exact", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkKubeconfig([]byte(tt.raw), tt.context)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v error=%v", tt.valid, err)
			}
			if err != nil {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
					t.Fatal(err)
				}
			}
		})
	}
}
func buildFixture(t *testing.T, mode string) Options {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("held execution fixture requires Linux")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "tool")
	state := filepath.Join(dir, "state.json")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-X main.mode="+mode+" -X main.state="+state, "-o", bin, "./testdata/tool.go")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build process fixture: %v %s", err, raw)
	}
	binary, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	kube := filepath.Join(dir, "kubeconfig")
	if err = os.WriteFile(kube, []byte(kubeFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	o := optionsFixture()
	o.Hydrophone = Tool{Path: bin, Digest: blobDigest(binary)}
	o.Kubectl = o.Hydrophone
	o.Kubeconfig = Tool{Path: kube, Digest: blobDigest([]byte(kubeFixture))}
	return o
}
func TestPinnedProducerProcessContract(t *testing.T) {
	tests := []struct {
		mode, execution, cleanup string
		wantError                bool
		raw                      bool
	}{
		{"version-mismatch", "not-run", "completed", true, false},
		{"after-failure", "infrastructure-error", "completed", true, true},
		{"success", "completed", "completed", false, true}, {"failed", "failed", "completed", true, true},
		{"zero", "infrastructure-error", "completed", true, true}, {"missing", "infrastructure-error", "completed", true, false},
		{"deploy-failure", "infrastructure-error", "completed", true, false}, {"cleanup-failure", "completed", "failed", true, true},
		{"replaced", "completed", "failed", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			o := buildFixture(t, tt.mode)
			r, err := Run(context.Background(), o)
			if (err != nil) != tt.wantError {
				t.Fatalf("error = %v", err)
			}
			if r == nil {
				t.Fatalf("no result: %v", err)
			}
			if r.Observation.Execution.Status != tt.execution || r.Observation.Cleanup.Status != tt.cleanup {
				t.Fatalf("outcomes execution=%#v cleanup=%#v error=%v", r.Observation.Execution, r.Observation.Cleanup, err)
			}
			if (len(r.RawJUnit()) > 0) != tt.raw {
				t.Fatal("raw result preservation mismatch")
			}
			var start, finish int64
			for _, fact := range r.Observation.Environment.Facts {
				switch fact.Name {
				case "testStartedUnix":
					start, _ = strconv.ParseInt(fact.Value, 10, 64)
				case "testFinishedUnix":
					finish, _ = strconv.ParseInt(fact.Value, 10, 64)
				}
			}
			if start <= 0 || finish < start {
				t.Fatalf("invalid observed producer interval %d %d", start, finish)
			}
			raw, _ := canonical.Marshal(r.Observation.Environment)
			for _, private := range []string{"private-node", "private-registry", "private-user", "private-token", "private.invalid"} {
				if strings.Contains(string(raw), private) {
					t.Fatalf("private value leaked %q", private)
				}
			}
			if tt.mode == "success" {
				out := filepath.Join(t.TempDir(), "bundle")
				if err = r.Emit(context.Background(), out, blobDigest(r.rawJUnit)); err != nil {
					t.Fatal(err)
				}
				bundle, err := testobservation.Open(context.Background(), out)
				if err != nil {
					t.Fatal(err)
				}
				defer bundle.Close()
				if !bundle.Passing() {
					t.Fatal("expected passing descriptive bundle")
				}
			}
		})
	}
}
func TestCancellationStillCleans(t *testing.T) {
	o := buildFixture(t, "cancel")
	o.RunTimeout = 100 * time.Millisecond
	r, err := Run(context.Background(), o)
	if err == nil || r == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("result=%v error=%v", r, err)
	}
	if r.Observation.Execution.Status != "canceled" {
		t.Fatalf("canceled execution=%#v", r.Observation.Execution)
	}
	if r.Observation.Cleanup.Status != "completed" {
		t.Fatalf("cancellation cleanup=%#v", r.Observation.Cleanup)
	}
}
func TestAPIProjectionRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{`{"items":[],"items":[{}]}`, `{} {}`, `{"items":`} {
		var out any
		var coded *e.Error
		err := decodeAPI([]byte(raw), &out)
		if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
			t.Fatalf("ambiguous API = %v", err)
		}
	}
}

func TestEmitRejectsUnreviewedOrInvalidBeforeOutput(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Result)
		review string
	}{
		{"unreviewed JUnit", func(*Result) {}, ""},
		{"invalid input binding", func(r *Result) { r.Observation.Inputs.DefinitionDigest = "" }, "sha256:" + strings.Repeat("a", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Result{Observation: optionsFixture().Observation, rawJUnit: []byte(`<testsuite name="s" tests="0"/>`)}
			tt.change(r)
			out := filepath.Join(t.TempDir(), "must-not-exist")
			err := r.Emit(context.Background(), out, tt.review)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("admission error=%v", err)
			}
			if _, err = os.Stat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid admission created output")
			}
		})
	}
}

func TestExecutablePlatformAdmissionBeforeClusterWork(t *testing.T) {
	o := buildFixture(t, "success")
	raw, err := os.ReadFile(o.Hydrophone.Path)
	if err != nil {
		t.Fatal(err)
	}
	// The established debug/elf reader must reject an unsupported e_machine.
	raw[18], raw[19] = 0xff, 0xff
	if err = os.WriteFile(o.Hydrophone.Path, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	o.Hydrophone.Digest = blobDigest(raw)
	result, err := Run(context.Background(), o)
	var coded *e.Error
	if result != nil || !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("platform admission result=%v error=%v", result, err)
	}
}

func TestCleanupRejectsUnownedBindingBeforeAnyDelete(t *testing.T) {
	for _, mode := range []string{"empty-binding-subjects", "wrong-binding-role"} {
		t.Run(mode, func(t *testing.T) {
			o := buildFixture(t, mode)
			r, err := Run(context.Background(), o)
			var coded *e.Error
			if r == nil || !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed || r.Observation.Cleanup.Status != "failed" {
				t.Fatalf("result=%v error=%v", r, err)
			}
			marker := filepath.Join(filepath.Dir(o.Hydrophone.Path), "state.json.delete")
			if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unowned binding triggered DELETE: %v", err)
			}
		})
	}
}

func TestProperClusterExecutionRejectedBeforeAnyInputWork(t *testing.T) {
	for _, role := range []string{"site", "tenant", "unknown"} {
		t.Run(role, func(t *testing.T) {
			o := Options{Observation: testobservation.Observation{Environment: testobservation.Environment{Role: role}}}
			result, err := Run(context.Background(), o)
			var coded *e.Error
			if result != nil || !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("proper-cluster admission result=%v error=%v", result, err)
			}
		})
	}
}

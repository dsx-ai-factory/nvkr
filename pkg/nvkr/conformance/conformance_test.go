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
	return Options{DisposableCluster: true, Hydrophone: Tool{Path: "/not-opened", Digest: pin}, Kubectl: Tool{Path: "/not-opened", Digest: pin}, Kubeconfig: Tool{Path: "/not-opened", Digest: pin}, Context: "exact", CNISelector: "app=cni", Image: "registry.k8s.io/conformance@" + pin, ImageVersion: "1.35.1", BusyboxImage: "registry.k8s.io/e2e-test-images/busybox@" + pin, RunTimeout: time.Minute, CommandTimeout: 2 * time.Second, CleanupTimeout: 5 * time.Second, Observation: testobservation.Observation{SchemaVersion: testobservation.SchemaVersion, Inputs: testobservation.Inputs{DefinitionDigest: pin, EnvironmentDigest: pin, PlanDigest: pin}, Origin: testobservation.Origin{Status: "unknown"}, Subjects: []testobservation.Subject{{Output: "cluster", Platform: "linux/amd64", BuildDigest: pin, ArtifactDigest: pin, PayloadDigest: pin}}, Suite: testobservation.Suite{Name: "kubernetes-conformance", HarnessRevision: HarnessRevision, ImageDigest: pin, PolicyDigest: pin, Invocation: "independent-invocation", Selection: []string{`\[Conformance\]`}, RequiredCases: []testobservation.CaseID{{Suite: "Kubernetes e2e suite", Class: "Kubernetes e2e suite", Name: "[It] required"}}}, Environment: testobservation.Environment{Role: "local", Facts: []testobservation.Fact{{Name: "role", Value: "local", Evidence: "declared", Phase: "before"}}}, Execution: testobservation.Outcome{Status: "not-run"}, Cleanup: testobservation.Outcome{Status: "unknown"}}}
}
func TestAdmissionBeforeWork(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Options)
		code   e.Code
	}{
		{"image tag", func(o *Options) { o.Image = "registry.k8s.io/conformance:v1.35.1" }, e.CodeUnpinnedInput},
		{"sidecar tag", func(o *Options) { o.BusyboxImage = "busybox:latest" }, e.CodeUnpinnedInput},
		{"missing tool pin", func(o *Options) { o.Hydrophone.Digest = "" }, e.CodeUnpinnedInput},
		{"version unknown", func(o *Options) { o.ImageVersion = "unknown" }, e.CodeUsage},
		{"quick selection", func(o *Options) { o.Observation.Suite.Selection = []string{"quick"} }, e.CodeUsage},
		{"no required cases", func(o *Options) { o.Observation.Suite.RequiredCases = nil }, e.CodeUsage},
		{"wrong harness", func(o *Options) { o.Observation.Suite.HarnessRevision = strings.Repeat("b", 40) }, e.CodeUnpinnedInput},
		{"no context", func(o *Options) { o.Context = "" }, e.CodeUsage},
		{"no CNI classification", func(o *Options) { o.CNISelector = "" }, e.CodeUsage},
		{"unbounded timeout", func(o *Options) { o.RunTimeout = 0 }, e.CodeUsage},
		{"missing independent pin", func(o *Options) { o.Observation.Inputs.PlanDigest = "" }, e.CodeVerifyFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := optionsFixture()
			tt.change(&o)
			r, err := Run(context.Background(), o)
			var coded *e.Error
			if r != nil || !errors.As(err, &coded) || coded.Code != tt.code {
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
	cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-ldflags=-X main.mode="+mode+" -X main.state="+state, "-o", bin, "./testdata/tool.go")
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
		{"api-overflow", "not-run", "completed", true, false},
		{"after-failure", "infrastructure-error", "completed", true, true},
		{"success", "completed", "completed", false, true}, {"failed", "failed", "completed", true, true},
		{"zero", "infrastructure-error", "completed", true, true}, {"missing", "infrastructure-error", "completed", true, false},
		{"deploy-failure", "infrastructure-error", "completed", true, false}, {"cleanup-failure", "completed", "failed", true, true},
		{"replaced", "completed", "failed", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			o := buildFixture(t, tt.mode)
			for _, key := range []string{"ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_URL", "SIGSTORE_ID_TOKEN", "DOCKER_CONFIG", "KUBECONFIG"} {
				t.Setenv(key, "MUST-NOT-LEAK")
			}
			r, err := Run(context.Background(), o)
			if err != nil {
				var coded *e.Error
				want := e.CodeVerifyFailed
				if !errors.As(err, &coded) || coded.Code != want {
					t.Fatalf("coded error=%v want=%s", err, want)
				}
				messages := map[string]string{"version-mismatch": "major/minor versions differ", "api-overflow": "output byte budget exhausted", "after-failure": "pinned command failed", "failed": "exit 1", "zero": "zero cases", "missing": "original suite results unavailable", "deploy-failure": "exit 1", "cleanup-failure": "exit 8", "replaced": "namespace replaced"}
				if !strings.Contains(err.Error(), messages[tt.mode]) {
					t.Fatalf("wrong failure cause: %v", err)
				}
			}
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
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
		t.Fatal(err)
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
	for _, mode := range []string{"empty-binding-subjects", "wrong-binding-role", "foreign-subject-namespace", "extra-subject"} {
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

func TestDisposableAcknowledgementBeforeWork(t *testing.T) {
	o := optionsFixture()
	o.DisposableCluster = false
	r, err := Run(t.Context(), o)
	var coded *e.Error
	if r != nil || !errors.As(err, &coded) || coded.Code != e.CodeUsage {
		t.Fatalf("result=%v error=%v", r, err)
	}
}

func TestObservePrivateWorkspaceAndPhases(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			o := buildFixture(t, "success")
			o.Observation.Environment.Role = "site"
			cwd, _ := os.Getwd()
			before, err := os.ReadDir(cwd)
			if err != nil {
				t.Fatal(err)
			}
			env, err := Observe(t.Context(), o, phase)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadDir(cwd)
			if len(before) != len(after) {
				t.Fatal("observer changed caller cwd")
			}
			if env.Role != "site" || len(env.Facts) == 0 {
				t.Fatal("missing projection")
			}
			for _, f := range env.Facts {
				if f.Phase != phase {
					t.Fatal("wrong phase")
				}
			}
		})
	}
	_, err := Observe(t.Context(), Options{}, "invalid")
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
		t.Fatal(err)
	}
}

func TestRecoveryDiagnosticsAndDelayedCleanup(t *testing.T) {
	for _, mode := range []string{"interrupted-retrievable", "empty-retrievable", "truncated-retrievable", "large-retrievable", "verbose", "delayed-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			o := buildFixture(t, mode)
			o.CleanupTimeout = 15 * time.Second
			o.StartupTimeout = 7 * time.Minute
			r, err := Run(t.Context(), o)
			if err != nil {
				t.Fatal(err)
			}
			startup, readErr := os.ReadFile(filepath.Join(filepath.Dir(o.Hydrophone.Path), "state.json.startup"))
			if readErr != nil || string(startup) != "7m0s" {
				t.Fatal("startup timeout coupled to API request timeout", readErr, string(startup))
			}
			if r.Observation.Execution.Status != "completed" || r.Observation.Cleanup.Status != "completed" || len(r.RawJUnit()) == 0 {
				t.Fatalf("result=%#v", r)
			}
			for _, name := range []string{"hydrophone.stdout.log", "hydrophone.stderr.log"} {
				info, err := os.Stat(filepath.Join(r.PrivateWorkspace, name))
				if err != nil || info.Size() == 0 || info.Mode().Perm() != 0600 {
					t.Fatalf("diagnostic=%v %v", info, err)
				}
				limit := int64(maxAPIBytes)
				if name == "hydrophone.stderr.log" {
					limit = maxDiagnosticBytes
				}
				if info.Size() > limit {
					t.Fatal("diagnostic overflow")
				}
			}
			var found bool
			for _, f := range r.Observation.Environment.Facts {
				if f.Name == "tools.hydrophone.digest" && f.Value == o.Hydrophone.Digest {
					found = true
				}
			}
			if !found {
				t.Fatal("executed tool digest not recorded")
			}
		})
	}
}

func TestOwnershipBeforeDelete(t *testing.T) {
	for _, mode := range []string{"preexisting", "create-no-uid"} {
		t.Run(mode, func(t *testing.T) {
			o := buildFixture(t, mode)
			r, err := Run(t.Context(), o)
			var coded *e.Error
			if r == nil || !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("result=%v error=%v", r, err)
			}
			if _, err = os.Stat(filepath.Join(filepath.Dir(o.Hydrophone.Path), "state.json.delete")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("delete without ownership")
			}
		})
	}
}

func TestRunFilesRetainsFailedCandidate(t *testing.T) {
	o := buildFixture(t, "failed")
	c := FileConfiguration{SchemaVersion: ConfigurationSchema, DisposableCluster: true, Hydrophone: o.Hydrophone, Kubectl: o.Kubectl, Kubeconfig: o.Kubeconfig, Context: o.Context, CNISelector: o.CNISelector, Image: o.Image, ImageVersion: o.ImageVersion, BusyboxImage: o.BusyboxImage, Observation: o.Observation, RunTimeoutSeconds: 60, CommandTimeoutSeconds: 2, CleanupTimeoutSeconds: 5}
	raw, err := canonical.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := RunFiles(t.Context(), path)
	var coded *e.Error
	if r == nil || !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
		t.Fatalf("result=%v error=%v", r, err)
	}
	got, err := os.ReadFile(filepath.Join(r.PrivateWorkspace, "review-junit.xml"))
	if err != nil || string(got) != string(r.RawJUnit()) {
		t.Fatal("failed original not retained", err)
	}
	want, _ := canonical.Marshal(r.Observation)
	got, err = os.ReadFile(filepath.Join(r.PrivateWorkspace, "observation-description.json"))
	if err != nil || string(got) != string(want) {
		t.Fatal("failed description not retained", err)
	}
}

func TestProjectionBoundaries(t *testing.T) {
	for _, tt := range []struct {
		value string
		ok    bool
	}{{"containerd://2.1.0", true}, {"", false}, {"private token", false}, {"\x1b[2J", false}} {
		if publicToken(tt.value) != tt.ok {
			t.Fatalf("token %q", tt.value)
		}
	}
	got, err := aggregate(map[string]bool{"z": true, "a": true})
	if err != nil || got != "a,z" {
		t.Fatal(got, err)
	}
	for _, v := range []map[string]bool{{}, {strings.Repeat("x", 4097): true}} {
		_, err := aggregate(v)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
			t.Fatal(err)
		}
	}
}

func TestCancellationCausePrecedesCleanupFailure(t *testing.T) {
	o := buildFixture(t, "cancel-cleanup-failure")
	o.RunTimeout = 100 * time.Millisecond
	o.CleanupTimeout = time.Second
	r, err := Run(t.Context(), o)
	var coded *e.Error
	if r == nil || !errors.As(err, &coded) || coded.Code != e.CodeTimeout || !errors.Is(err, context.DeadlineExceeded) || r.Observation.Execution.Status != "canceled" || r.Observation.Cleanup.Status != "failed" {
		t.Fatalf("result=%v error=%v", r, err)
	}
	if !strings.Contains(coded.Error(), "command canceled") {
		t.Fatalf("cleanup masked cancellation: %v", err)
	}
}

func TestBoundedCopyCannotBypassWrite(t *testing.T) {
	b := &boundedBuffer{limit: 8}
	n, err := b.ReadFrom(strings.NewReader(strings.Repeat("x", 100)))
	if err != nil || n != 100 || b.Len() != 8 || !b.overflow {
		t.Fatalf("n=%d len=%d overflow=%t err=%v", n, b.Len(), b.overflow, err)
	}
}

func TestDeclaredClockIsProducerMetadata(t *testing.T) {
	o := buildFixture(t, "success")
	fixed := time.Unix(1700000000, 0)
	r, err := runWithClock(t.Context(), o, func() time.Time { return fixed })
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.Observation.Environment.Facts {
		if f.Name == "testStartedUnix" || f.Name == "testFinishedUnix" {
			if f.Value != "1700000000" || f.Evidence != "declared" {
				t.Fatal("clock promoted to authority", f)
			}
		}
	}
}

func TestCNIImageIdentifierForms(t *testing.T) {
	pin := "sha256:" + strings.Repeat("b", 64)
	for _, raw := range []string{pin, "containerd://" + pin, "docker-pullable://private.example/cni@" + pin} {
		got, err := projectImageIdentifier(raw)
		if err != nil || got != pin {
			t.Fatal(raw, got, err)
		}
	}
	for _, raw := range []string{"", "containerd://unknown", "sha512:" + strings.Repeat("a", 128)} {
		_, err := projectImageIdentifier(raw)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
			t.Fatal(raw, err)
		}
	}
}

func TestCanceledCaptureCodeAndCause(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("capture failed")} {
		got := captureFailure(ctx, err, e.CodeUnpinnedInput, "capture")
		var coded *e.Error
		if !errors.As(got, &coded) || coded.Code != e.CodeTimeout || !errors.Is(got, context.Canceled) {
			t.Fatal(got)
		}
	}
	got := captureFailure(t.Context(), context.DeadlineExceeded, e.CodeUnpinnedInput, "capture")
	var coded *e.Error
	if !errors.As(got, &coded) || coded.Code != e.CodeTimeout || !errors.Is(got, context.DeadlineExceeded) {
		t.Fatal(got)
	}
	_, err := openTool(ctx, optionsFixture().Hydrophone)
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCanceledCommandSnapshotCode(t *testing.T) {
	o := optionsFixture()
	a := &admitted{options: o, kubeBytes: []byte(kubeFixture)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err := a.command(ctx, nil, nil, t.TempDir(), nil, time.Second)
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout || !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestKubeconfigPrivateModeAdmission(t *testing.T) {
	o := buildFixture(t, "success")
	if err := os.Chmod(o.Kubeconfig.Path, 0644); err != nil {
		t.Fatal(err)
	}
	r, err := Run(t.Context(), o)
	var coded *e.Error
	if r != nil || !errors.As(err, &coded) || coded.Code != e.CodeUnpinnedInput || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("result=%v err=%v", r, err)
	}
}

func TestFallbackRejectsEmptyAndMalformedPodResults(t *testing.T) {
	for _, tt := range []struct{ mode, raw string }{{"empty-inpod", ""}, {"malformed-inpod", "<testsuite"}} {
		t.Run(tt.mode, func(t *testing.T) {
			r, err := Run(t.Context(), buildFixture(t, tt.mode))
			var coded *e.Error
			if r == nil || !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("result=%v error=%v", r, err)
			}
			if r.Observation.Execution.Status != "infrastructure-error" || r.Observation.Cleanup.Status != "completed" {
				t.Fatalf("execution=%v cleanup=%v", r.Observation.Execution, r.Observation.Cleanup)
			}
			if string(r.RawJUnit()) != tt.raw {
				t.Fatalf("original fallback changed: %q", r.RawJUnit())
			}
		})
	}
}

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
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
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
	return Options{Targets: []DeploymentTarget{{Output: "image", Namespace: "workload", LabelSelector: "app=nvke", Container: "nvke", RequiredReplicas: 1}}, DisposableCluster: true, Role: "local", SuiteName: "kubernetes-conformance", HarnessVersion: "fixture-declared-version", WorkspaceRoot: "/not-created", Hydrophone: Tool{Path: "/not-opened", Digest: pin}, Kubectl: Tool{Path: "/not-opened", Digest: pin}, Kubeconfig: Tool{Path: "/not-opened", Digest: pin}, Context: "exact", CNISelector: "app=cni", Image: "registry.k8s.io/conformance@" + pin, ImageVersion: "1.35.1", BusyboxImage: "registry.k8s.io/e2e-test-images/busybox@" + pin, RunTimeout: time.Minute, CommandTimeout: 2 * time.Second, CleanupTimeout: 5 * time.Second}
}
func requireCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error=%v want=%s", err, code)
	}
}
func TestConfigurationAdmissionBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Options)
		code e.Code
	}{
		{"image tag", func(o *Options) { o.Image = "registry.k8s.io/conformance:v1.35.1" }, e.CodeUnpinnedInput},
		{"sidecar tag", func(o *Options) { o.BusyboxImage = "busybox:latest" }, e.CodeUnpinnedInput},
		{"missing pin", func(o *Options) { o.Hydrophone.Digest = "" }, e.CodeUnpinnedInput},
		{"missing context", func(o *Options) { o.Context = "" }, e.CodeUsage},
		{"missing CNI classification", func(o *Options) { o.CNISelector = "" }, e.CodeUsage},
		{"missing version", func(o *Options) { o.ImageVersion = "unknown" }, e.CodeUsage},
		{"unbounded runtime", func(o *Options) { o.RunTimeout = 0 }, e.CodeUsage},
		{"nonlocal target", func(o *Options) { o.Role = "tenant" }, e.CodeEngineCapability},
		{"missing disposable acknowledgement", func(o *Options) { o.DisposableCluster = false }, e.CodeUsage},
		{"ambient override request", func(o *Options) { o.Environment = []string{"HOME=/foreign"} }, e.CodeUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := optionsFixture()
			tc.edit(&o)
			producer, err := New(o)
			requireCode(t, err, tc.code)
			if producer != nil {
				t.Fatal("invalid config created producer")
			}
		})
	}
	_, err := New(optionsFixture())
	requireCode(t, err, e.CodeGateUnmet)
	p := &Producer{options: optionsFixture()}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := p.Produce(ctx)
	requireCode(t, err, e.CodeTimeout)
	if out.SchemaVersion != "" || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled admission did work")
	}
	if runtime.GOOS != "linux" {
		_, err = p.Produce(t.Context())
		requireCode(t, err, e.CodeGateUnmet)
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
		t.Skip("held execution process fixture requires Linux")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "tool")
	state := filepath.Join(root, "state.json")
	command := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-ldflags=-X main.mode="+mode+" -X main.state="+state, "-o", bin, "./testdata/tool.go")
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("building process fixture: %v %s", err, raw)
	}
	binary, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	kube := filepath.Join(root, "kubeconfig")
	if err := os.WriteFile(kube, []byte(kubeFixture), 0600); err != nil {
		t.Fatal(err)
	}
	o := optionsFixture()
	o.WorkspaceRoot = root
	o.Hydrophone = Tool{Path: bin, Digest: blobDigest(binary)}
	o.Kubectl = o.Hydrophone
	o.Kubeconfig = Tool{Path: kube, Digest: blobDigest([]byte(kubeFixture))}
	return o
}
func produce(t *testing.T, o Options) (testresults.Output, error) {
	t.Helper()
	o, err := validateOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	p := &Producer{options: o}
	return p.produce(t.Context(), artifactDescriptionFixture())
}
func TestConfiguredProducerPreservesOriginalBytesAndFailureCauses(t *testing.T) {
	for _, tc := range []struct {
		mode, message string
		report        bool
	}{
		{"success", "", true},
		{"zero", "", true}, // Captured raw XML is not parsed or promoted to pass.
		{"malformed-inpod", "", true},
		{"interrupted-retrievable", "", true},
		{"empty-retrievable", "", true},
		{"large-retrievable", "", true},
		{"delayed-cleanup", "", true},
		{"failed", "exit 1", true},
		{"after-failure", "pinned command failed", true},
		{"missing", "original suite results unavailable", false},
		{"cleanup-failure", "exit 8", true},
		{"replaced", "namespace replaced", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			o := buildFixture(t, tc.mode)
			if tc.mode == "delayed-cleanup" {
				o.CleanupTimeout = 12 * time.Second
			}
			for _, key := range []string{"ACTIONS_ID_TOKEN_REQUEST_TOKEN", "SIGSTORE_ID_TOKEN", "DOCKER_CONFIG", "KUBECONFIG", "HTTP_PROXY"} {
				t.Setenv(key, "MUST-NOT-LEAK")
			}
			out, err := produce(t, o)
			if tc.message != "" {
				requireCode(t, err, e.CodeVerifyFailed)
				if !strings.Contains(err.Error(), tc.message) {
					t.Fatalf("wrong cause: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			expectedCapture := testresults.CaptureComplete
			if err != nil {
				expectedCapture = testresults.CapturePartial
			}
			if out.CaptureStatus != expectedCapture {
				t.Fatalf("capture status %q want %q error %v", out.CaptureStatus, expectedCapture, err)
			}
			if (len(out.Reports) > 0) != tc.report {
				t.Fatalf("report capture=%v error=%v", out.Reports, err)
			}
			if out.Binding == nil || len(out.Binding.Subjects) != 1 || out.Binding.Inputs == nil || out.Producer.Revision.Status != "unknown" || out.Suite.Name != o.SuiteName || out.Harness == nil || out.Harness.Name != "hydrophone" || out.Harness.Version.Value != o.HarnessVersion {
				t.Fatal("diagnostic adapter lost source binding or merged identities")
			}
			before, after := false, false
			for _, fact := range out.Environment {
				if fact.Name == "kubernetes.version" {
					before = before || fact.Phase == "before"
					after = after || fact.Phase == "after"
					if fact.Metadata.Status != "observed" {
						t.Fatal("API fact mislabeled")
					}
				}
			}
			if !before || tc.mode != "after-failure" && !after {
				t.Fatal("missing phased observation")
			}
			raw, err := canonical.Marshal(out.Environment)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"private-registry", "private-user", "private-token", "private.invalid"} {
				if strings.Contains(string(raw), private) {
					t.Fatal("private API identifier exposed")
				}
			}
			if !tc.report {
				return
			}
			report := out.Reports[0]
			original, err := os.ReadFile(report.Reference)
			if err != nil {
				t.Fatal(err)
			}
			if blobDigest(original) != report.Digest || int64(len(original)) != report.ByteLength || report.Format != testresults.FormatJUnit || report.Profile != JUnitProfile || report.DerivedFrom != "" {
				t.Fatal("original report changed or became derived output")
			}
			if err := out.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestCancellationStillOwnsCleanupAndPreservesCause(t *testing.T) {
	for _, mode := range []string{"cancel", "cancel-cleanup-failure"} {
		t.Run(mode, func(t *testing.T) {
			o := buildFixture(t, mode)
			o.RunTimeout = 100 * time.Millisecond
			out, err := produce(t, o)
			requireCode(t, err, e.CodeTimeout)
			if !errors.Is(err, context.DeadlineExceeded) || out.Binding == nil {
				t.Fatal("cancellation lost or runtime became qualification")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(o.Hydrophone.Path), "state.json.delete")); err != nil {
				t.Fatal("cancellation abandoned cleanup")
			}
			if mode == "cancel-cleanup-failure" && !strings.Contains(err.Error(), "exit 8") {
				t.Fatal("cleanup error lost")
			}
		})
	}
}
func TestCleanupNeverDeletesReplacedOrForeignScope(t *testing.T) {
	for _, mode := range []string{"preexisting", "create-no-uid", "empty-binding-subjects", "wrong-binding-role", "foreign-subject-namespace", "extra-subject", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			o := buildFixture(t, mode)
			_, err := produce(t, o)
			requireCode(t, err, e.CodeVerifyFailed)
			if _, err := os.Stat(filepath.Join(filepath.Dir(o.Hydrophone.Path), "state.json.delete")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("DELETE without exact ownership")
			}
		})
	}
}
func TestExecutableAndPrivateKubeconfigAdmission(t *testing.T) {
	for _, tc := range []string{"wrong architecture", "public kubeconfig", "script"} {
		t.Run(tc, func(t *testing.T) {
			o := buildFixture(t, "success")
			code := e.CodeEngineCapability
			if tc == "public kubeconfig" {
				if err := os.Chmod(o.Kubeconfig.Path, 0644); err != nil {
					t.Fatal(err)
				}
				code = e.CodeUnpinnedInput
			} else {
				raw, err := os.ReadFile(o.Hydrophone.Path)
				if err != nil {
					t.Fatal(err)
				}
				if tc == "script" {
					raw = []byte("#!/bin/sh\nexit 0\n")
				} else {
					raw[18], raw[19] = 255, 255
				}
				if err := os.WriteFile(o.Hydrophone.Path, raw, 0700); err != nil {
					t.Fatal(err)
				}
				o.Hydrophone.Digest = blobDigest(raw)
			}
			_, err := produce(t, o)
			requireCode(t, err, code)
		})
	}
}
func TestBoundedCopyCannotBypassWrite(t *testing.T) {
	b := &boundedBuffer{limit: 8}
	n, err := b.ReadFrom(strings.NewReader(strings.Repeat("x", 100)))
	if err != nil || n != 100 || b.Len() != 8 || !b.overflow {
		t.Fatalf("n=%d len=%d overflow=%t err=%v", n, b.Len(), b.overflow, err)
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

func TestReportRecoveryTimeoutDoesNotConsumeCleanupBudget(t *testing.T) {
	o := buildFixture(t, "slow-recovery")
	o.CommandTimeout = 150 * time.Millisecond
	o.CleanupTimeout = 2 * time.Second
	out, err := produce(t, o)
	requireCode(t, err, e.CodeTimeout)
	if !errors.Is(err, context.DeadlineExceeded) || len(out.Reports) != 0 {
		t.Fatalf("recovery refusal lost: %+v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(o.Hydrophone.Path), "state.json.delete")); err != nil {
		t.Fatal("report timeout abandoned UID cleanup")
	}
}
func TestReportReferenceRemainsAbsoluteWithRelativeWorkspace(t *testing.T) {
	o := buildFixture(t, "success")
	root := o.WorkspaceRoot
	t.Chdir(root)
	o.WorkspaceRoot = "."
	out, err := produce(t, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Reports) != 1 || !filepath.IsAbs(out.Reports[0].Reference) {
		t.Fatal("report reference inherits caller cwd")
	}
	t.Chdir(t.TempDir())
	raw, err := os.ReadFile(out.Reports[0].Reference)
	if err != nil || blobDigest(raw) != out.Reports[0].Digest {
		t.Fatal("changing cwd reinterpreted original report", err)
	}
}

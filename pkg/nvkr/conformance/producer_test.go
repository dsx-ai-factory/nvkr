// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
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
	return Options{Targets: []DeploymentTarget{{Output: "image", Namespace: "workload", LabelSelector: "app=nvke", Container: "nvke", RequiredReplicas: 1}}, DisposableCluster: true, Role: "local", SuiteName: "kubernetes-conformance", WorkspaceRoot: "/not-created", Hydrophone: ExecutableSelector{Output: "tools", Member: "hydrophone", Platform: "linux/" + runtime.GOARCH}, Kubectl: ExecutableSelector{Output: "tools", Member: "kubectl", Platform: "linux/" + runtime.GOARCH}, ConformanceImage: ImageSelector{Output: "conformance", Platform: "linux/amd64"}, Busybox: ImageSelector{Output: "busybox", Platform: "linux/amd64"}, Kubeconfig: Tool{Path: "/not-opened", Digest: pin}, Context: "exact", CNISelector: "app=cni", Image: "registry.k8s.io/conformance@" + pin, ImageVersion: "1.35.1", BusyboxImage: "registry.k8s.io/e2e-test-images/busybox@" + pin, RunTimeout: time.Minute, CommandTimeout: 2 * time.Second, CleanupTimeout: 5 * time.Second}
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
		{"image tag", func(o *Options) { o.ConformanceImage.Output = "" }, e.CodeUsage},
		{"sidecar tag", func(o *Options) { o.Busybox.Platform = "" }, e.CodeUsage},
		{"missing selector", func(o *Options) { o.Hydrophone.Member = "" }, e.CodeUsage},
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
	for _, tc := range []struct{ name, raw, context, message string }{
		{"embedded token", kubeFixture, "exact", ""},
		{"embedded client certificate", strings.Replace(kubeFixture, "token: private-token", "client-certificate-data: Y2VydA==\n    client-key-data: a2V5", 1), "exact", ""},
		{"wrong context", kubeFixture, "other", "single exact kubeconfig context"},
		{"external CA", strings.Replace(kubeFixture, "certificate-authority-data: Y2E=", "certificate-authority: /private/ca.pem", 1), "exact", "without external files or plugins"},
		{"exec plugin", strings.Replace(kubeFixture, "token: private-token", "exec: {command: unpinned}", 1), "exact", "without external files or plugins"},
		{"auth provider", strings.Replace(kubeFixture, "token: private-token", "auth-provider: {name: gcp}", 1), "exact", "without external files or plugins"},
		{"duplicate target", strings.Replace(kubeFixture, "current-context: exact", "current-context: exact\ncurrent-context: other", 1), "exact", "invalid private kubeconfig YAML"},
		{"TLS bypass", strings.Replace(kubeFixture, "certificate-authority-data: Y2E=", "insecure-skip-tls-verify: true", 1), "exact", "without external files or plugins"},
		{"plaintext server", strings.Replace(kubeFixture, "https://private.invalid", "http://private.invalid", 1), "exact", "exact TLS target and one embedded authentication method"},
		{"missing CA", strings.Replace(kubeFixture, "certificate-authority-data: Y2E=", "certificate-authority-data: \"\"", 1), "exact", "exact TLS target and one embedded authentication method"},
		{"token and certificate", strings.Replace(kubeFixture, "token: private-token", "token: private-token\n    client-certificate-data: Y2VydA==\n    client-key-data: a2V5", 1), "exact", "exact TLS target and one embedded authentication method"},
		{"token and partial certificate", strings.Replace(kubeFixture, "token: private-token", "token: private-token\n    client-certificate-data: Y2VydA==", 1), "exact", "multiple authentication methods"},
		{"no authentication", strings.Replace(kubeFixture, "token: private-token", "token: \"\"", 1), "exact", "exact TLS target and one embedded authentication method"},
		{"missing client key", strings.Replace(kubeFixture, "token: private-token", "client-certificate-data: Y2VydA==", 1), "exact", "exact TLS target and one embedded authentication method"},
		{"nonempty preferences", strings.Replace(kubeFixture, "preferences: {}", "preferences: {colors: true}", 1), "exact", "preferences unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkKubeconfig([]byte(tc.raw), tc.context)
			if tc.message == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireCode(t, err, e.CodeVerifyFailed)
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("wrong branch: %v, want %q", err, tc.message)
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
	kube := filepath.Join(root, "kubeconfig")
	if err := os.WriteFile(kube, []byte(kubeFixture), 0600); err != nil {
		t.Fatal(err)
	}
	o := optionsFixture()
	o.WorkspaceRoot = root
	o.Kubeconfig = Tool{Path: kube, Digest: blobDigest([]byte(kubeFixture))}
	return o
}
func produce(t *testing.T, o Options) (testresults.Output, error) {
	t.Helper()
	o, err := validateOptions(o)
	if err != nil {
		t.Fatal(err)
	}
	p := &Producer{options: o, captureTool: fixtureCapture(o)}
	return p.produce(t.Context(), artifactDescriptionFixture(), toolsDescriptionFixture())
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
		{"failed", "", true},
		{"signal", "exit -1", true},
		{"slow-after", "", true},
		{"delete-not-found", "", true},
		{"after-failure", "pinned command failed", true},
		{"missing", "original suite results unavailable", false},
		{"cleanup-failure", "exit 8", true},
		{"replaced", "namespace replaced", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			o := buildFixture(t, tc.mode)
			// Use the normal per-command budget for real process startup. The
			// slow-after fixture derives each observation delay from that budget;
			// its complete phase exceeds one command budget, without delaying cleanup.
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
			if tc.mode == "failed" || tc.mode == "signal" {
				var status testresults.Metadata
				for _, fact := range out.Environment {
					if fact.Scope == "harness" && fact.Name == "process.exitCode" {
						status = fact.Metadata
					}
				}
				if tc.mode == "failed" && (status.Status != "observed" || status.Value != "1") || tc.mode == "signal" && status.Status != "unavailable" {
					t.Fatalf("process status misclassified: %+v", status)
				}
			}
			if (len(out.Reports) > 0) != tc.report {
				t.Fatalf("report capture=%v error=%v", out.Reports, err)
			}
			if out.Binding == nil || len(out.Binding.Subjects) != 1 || out.Binding.Inputs == nil || out.Producer.Revision.Status != "unknown" || out.Suite.Name != o.SuiteName || out.Harness == nil || out.Harness.Name != "hydrophone" || out.Harness.Version.Status != "unknown" {
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
			if _, err := os.Stat(filepath.Join(filepath.Dir(o.Kubeconfig.Path), "state.json.delete")); err != nil {
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
			if _, err := os.Stat(filepath.Join(filepath.Dir(o.Kubeconfig.Path), "state.json.delete")); !errors.Is(err, os.ErrNotExist) {
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
				raw, err := os.ReadFile(fixtureToolPath(o))
				if err != nil {
					t.Fatal(err)
				}
				if tc == "script" {
					raw = []byte("#!/bin/sh\nexit 0\n")
				} else {
					raw[18], raw[19] = 255, 255
				}
				if err := os.WriteFile(fixtureToolPath(o), raw, 0700); err != nil {
					t.Fatal(err)
				}
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
	o.CleanupTimeout = 2 * time.Second
	out, err := produce(t, o)
	requireCode(t, err, e.CodeTimeout)
	if !errors.Is(err, context.DeadlineExceeded) || len(out.Reports) != 0 {
		t.Fatalf("recovery refusal lost: %+v %v", out, err)
	}
	if !strings.Contains(err.Error(), "original suite results unavailable") {
		t.Fatalf("timeout occurred before report recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(o.Kubeconfig.Path), "state.json.delete")); err != nil {
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

func fixtureToolPath(o Options) string { return filepath.Join(filepath.Dir(o.Kubeconfig.Path), "tool") }
func fixtureCapture(o Options) executableCapture {
	return func(ctx context.Context, selector ExecutableSelector) (*heldfile.File, error) {
		return captureExecutable(ctx, selector, func(ctx context.Context, output, member, platform string, limit int64, destination io.Writer) (lifecycle.ExecutableMemberDescription, error) {
			raw, err := os.ReadFile(fixtureToolPath(o))
			if err != nil {
				return lifecycle.ExecutableMemberDescription{}, err
			}
			_, err = destination.Write(raw)
			return lifecycle.ExecutableMemberDescription{Output: output, Member: member, Platform: platform, PayloadDigest: blobDigest(raw), Size: int64(len(raw))}, err
		})
	}
}

func toolsDescriptionFixture() lifecycle.ArtifactCheckDescription {
	d := artifactDescriptionFixture()
	d.Inputs.ReleaseSubject = "registry.example.test/tools@" + d.Inputs.ContentSubject
	d.Origin.InvocationID = "https://github.com/example/tools/actions/runs/2/attempts/1"
	image := d.Outputs[0]
	image.Name = "conformance"
	image.Reference = "mirror.example.test/conformance@" + image.Subject
	busybox := image
	busybox.Name = "busybox"
	busybox.Reference = "mirror.example.test/busybox@" + image.Subject
	d.Outputs = []lifecycle.ArtifactCheckOutput{{Name: "tools", Kind: lifecycle.OutputExecutable, Platform: "linux/" + runtime.GOARCH, BuildSubjects: []delivery.BuildSubject{{Component: "hydrophone"}, {Component: "kubectl"}}}, image, busybox}
	return d
}

func TestProducerUsesAuthenticatedImagesAndRetainsToolOrigins(t *testing.T) {
	o := buildFixture(t, "success")
	output, err := produce(t, o)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(o.Kubeconfig.Path), "state.json.argv"))
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	if err = json.Unmarshal(raw, &argv); err != nil {
		t.Fatal(err)
	}
	tools := toolsDescriptionFixture()
	for _, want := range []string{"--conformance-image=" + tools.Outputs[1].Reference, "--busybox-image=" + tools.Outputs[2].Reference} {
		found := false
		for _, arg := range argv {
			found = found || arg == want
		}
		if !found {
			t.Fatalf("authenticated argument %q missing: %v", want, argv)
		}
	}
	facts := map[string]testresults.Metadata{}
	for _, fact := range output.Environment {
		facts[fact.Scope+"/"+fact.Name] = fact.Metadata
	}
	for key, want := range map[string]string{"sourceRun/invocationID": artifactDescriptionFixture().Origin.InvocationID, "toolsSource/publication": tools.Inputs.ReleaseSubject, "toolsSource/invocationID": tools.Origin.InvocationID, "toolsSource/hydrophone.output": o.Hydrophone.Output, "toolsSource/hydrophone.member": o.Hydrophone.Member, "toolsSource/hydrophone.platform": o.Hydrophone.Platform, "toolsSource/kubectl.output": o.Kubectl.Output, "toolsSource/kubectl.member": o.Kubectl.Member, "toolsSource/kubectl.platform": o.Kubectl.Platform, "harness/process.exitCode": "0"} {
		if got := facts[key]; got.Status != "observed" || got.Value != want {
			t.Fatalf("fact %s: %+v, want %q", key, got, want)
		}
	}
}

func TestAfterObservationTimeoutBudget(t *testing.T) {
	for _, tc := range []struct {
		name           string
		targets        int
		commandTimeout time.Duration
	}{
		{"one target at former timing threshold", 1, 150 * time.Millisecond},
		{"two targets at former timing threshold", 2, 150 * time.Millisecond},
		{"maximum targets at former timing threshold", 64, 150 * time.Millisecond},
		{"one target at maximum command budget", 1, 5 * time.Minute},
		{"maximum targets at maximum command budget", 64, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := optionsFixture()
			o.Targets = make([]DeploymentTarget, tc.targets)
			o.CommandTimeout = tc.commandTimeout
			if got, want := afterObservationTimeout(o), time.Duration(tc.targets+5)*tc.commandTimeout; got != want {
				t.Fatalf("%d targets: %s want %s", tc.targets, got, want)
			}
		})
	}
}

func TestPrestartDiagnosticFailureDoesNotInventProcessExit(t *testing.T) {
	o := buildFixture(t, "diagnostic-create-failure")
	output, err := produce(t, o)
	requireCode(t, err, e.CodeFetchFailed)
	if !strings.Contains(err.Error(), "creating private Hydrophone diagnostics") || output.CaptureStatus != testresults.CapturePartial {
		t.Fatalf("prestart failure lost: %+v %v", output, err)
	}
	found := false
	for _, fact := range output.Environment {
		if fact.Scope == "harness" && fact.Name == "process.exitCode" {
			found = true
			if fact.Metadata.Status != "unavailable" || fact.Metadata.Value != "" {
				t.Fatalf("invented child exit: %+v", fact)
			}
		}
	}
	if !found {
		t.Fatal("missing unavailable process status")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(o.Kubeconfig.Path), "state.json.argv")); !os.IsNotExist(err) {
		t.Fatalf("prestart refusal ran child: %v", err)
	}
}

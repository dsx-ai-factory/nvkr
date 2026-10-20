// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

func nativeSmokeTestDigest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }
func nativeSmokeTestPolicy() QualificationPolicy {
	p := qualificationPolicyFixture()
	p.QualificationWorkflow = ".github/workflows/qualify.yml"
	p.Smoke = &QualificationSmokePolicy{Workflow: ".github/workflows/smoke.yml", Images: map[string]string{"linux/amd64": "registry.example.test/smoke@" + nativeSmokeTestDigest("1")}, Tools: map[string]string{"tool": "helm-version"}}
	return p
}

type nativeSmokeTestObservation struct {
	profile                   pipeline.ToolSmokeProfile
	digest                    string
	size                      int64
	platform, image, identity string
	stdout, stderr            []byte
}

func (o *nativeSmokeTestObservation) Profile() pipeline.ToolSmokeProfile { return o.profile }
func (o *nativeSmokeTestObservation) Subject() (string, int64)           { return o.digest, o.size }
func (o *nativeSmokeTestObservation) Platform() string                   { return o.platform }
func (o *nativeSmokeTestObservation) Image() string                      { return o.image }
func (o *nativeSmokeTestObservation) RuntimeIdentity() string            { return o.identity }
func (o *nativeSmokeTestObservation) Stdout() []byte                     { return o.stdout }
func (o *nativeSmokeTestObservation) Stderr() []byte                     { return o.stderr }

func TestNativeSmokeFixedVersionProfiles(t *testing.T) {
	for _, tc := range []struct{ name, profile, raw, commit string }{
		{"nvkectl", "nvkectl-version-json", `{"buildVersion":"v1.2.3","gitCommit":"aaaaaaaa","gitTreeState":"clean","platform":"linux/amd64","buildDate":"fixed"}`, "aaaaaaaa"},
		{"helm", "helm-version", "v1.2.3\n", ""},
		{"kubectl", "kubectl-client-version-json", `{"clientVersion":{"gitVersion":"v1.2.3","platform":"linux/amd64","gitCommit":"upstream"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, _ := nativeSmokeProfile(tc.profile)
			member := ToolSmokeMember{Profile: tc.profile, Platform: "linux/amd64", PayloadDigest: nativeSmokeTestDigest("2"), Size: 100, ExpectedVersion: "v1.2.3", ExpectedCommit: tc.commit}
			fresh := func() *nativeSmokeTestObservation {
				return &nativeSmokeTestObservation{profile: profile, digest: member.PayloadDigest, size: member.Size, platform: member.Platform, image: nativeSmokeTestDigest("3"), identity: nativeSmokeTestDigest("4"), stdout: []byte(tc.raw)}
			}
			observed, err := evaluateNativeSmokeMember(member, fresh())
			if err != nil || observed.ObservedVersion != member.ExpectedVersion || observed.ObservedCommit != member.ExpectedCommit {
				t.Fatalf("profile: %+v %v", observed, err)
			}
			for _, bad := range []struct {
				name string
				edit func(*nativeSmokeTestObservation)
			}{
				{"different member", func(o *nativeSmokeTestObservation) { o.digest = nativeSmokeTestDigest("5") }},
				{"different size", func(o *nativeSmokeTestObservation) { o.size++ }},
				{"different platform", func(o *nativeSmokeTestObservation) { o.platform = "linux/arm64" }},
				{"different profile", func(o *nativeSmokeTestObservation) { o.profile = "unexpected" }},
				{"wrong version", func(o *nativeSmokeTestObservation) { o.stdout = []byte(strings.ReplaceAll(tc.raw, "v1.2.3", "v9.9.9")) }},
				{"diagnostic stderr", func(o *nativeSmokeTestObservation) { o.stderr = []byte("warning") }},
				{"missing output", func(o *nativeSmokeTestObservation) { o.stdout = nil }},
				{"oversized output", func(o *nativeSmokeTestObservation) { o.stdout = make([]byte, pipeline.MaxToolSmokeOutputBytes+1) }},
			} {
				t.Run(bad.name, func(t *testing.T) {
					o := fresh()
					bad.edit(o)
					_, err := evaluateNativeSmokeMember(member, o)
					assertCode(t, err, e.CodeVerifyFailed)
				})
			}
			if tc.name != "helm" {
				for _, raw := range []string{strings.ReplaceAll(tc.raw, "linux/amd64", "linux/arm64"), tc.raw + tc.raw, `{"clientVersion":null,"clientVersion":null}`, `null`} {
					o := fresh()
					o.stdout = []byte(raw)
					_, err := evaluateNativeSmokeMember(member, o)
					assertCode(t, err, e.CodeVerifyFailed)
				}
			}
			if tc.name == "nvkectl" {
				for _, raw := range []string{strings.ReplaceAll(tc.raw, "aaaaaaaa", "bbbbbbbb"), strings.ReplaceAll(tc.raw, "clean", "dirty")} {
					o := fresh()
					o.stdout = []byte(raw)
					_, err := evaluateNativeSmokeMember(member, o)
					assertCode(t, err, e.CodeVerifyFailed)
				}
			}
		})
	}
}

type nativeSmokeTestBundles map[string]delivery.BinaryBundle

func (bundles nativeSmokeTestBundles) ExecutableBundle(output string) (delivery.BinaryBundle, error) {
	bundle, ok := bundles[output]
	if !ok {
		return delivery.BinaryBundle{}, e.New(e.CodeVerifyFailed, "missing bundle")
	}
	return bundle, nil
}
func nativeSmokeScopeFixture() (factory.Plan, ArtifactCheckDescription, nativeSmokeTestBundles, QualificationPolicy) {
	digest := nativeSmokeTestDigest("2")
	output := factory.LifecycleOutputPlan{Name: "cli", Kind: OutputExecutable, BuildDigest: digest, Components: []string{"tool"}}
	plan := factory.Plan{LifecycleOutputs: []factory.LifecycleOutputPlan{output}, Builds: []factory.BuildPlan{{Name: "cli", BuildDigest: digest, Platform: "linux/amd64", Graph: &ir.Graph{Nodes: []ir.Node{{Kind: ir.KindBuild, Component: "tool", Version: "1.2.3", Platform: "linux/amd64", OutputType: "host-package"}}}}}}
	checks := ArtifactCheckDescription{Outputs: []ArtifactCheckOutput{{Name: "cli", Kind: OutputExecutable, BuildDigest: digest, Subject: nativeSmokeTestDigest("3"), Platform: "linux/amd64", Overall: "pass"}}}
	bundles := nativeSmokeTestBundles{"cli": {SchemaVersion: delivery.BinaryBundleSchemaVersion, OS: "linux", Arch: "amd64", Purpose: "cli", Entries: []delivery.BinaryEntry{{Name: "tool", InstallPath: "/usr/local/bin/tool", Mode: 0o755, SHA256: nativeSmokeTestDigest("4"), Size: 100}}}}
	return plan, checks, bundles, nativeSmokeTestPolicy()
}

func TestNativeSmokeCompleteScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*factory.Plan, *ArtifactCheckDescription, nativeSmokeTestBundles, *QualificationPolicy)
		code e.Code
	}{
		{"complete", func(*factory.Plan, *ArtifactCheckDescription, nativeSmokeTestBundles, *QualificationPolicy) {}, ""},
		{"missing profile", func(_ *factory.Plan, _ *ArtifactCheckDescription, _ nativeSmokeTestBundles, p *QualificationPolicy) {
			p.Smoke.Tools = map[string]string{"other": "helm-version"}
		}, e.CodeGateUnmet},
		{"extra profile", func(_ *factory.Plan, _ *ArtifactCheckDescription, _ nativeSmokeTestBundles, p *QualificationPolicy) {
			p.Smoke.Tools["other"] = "helm-version"
		}, e.CodeReleaseIncomplete},
		{"extra image", func(_ *factory.Plan, _ *ArtifactCheckDescription, _ nativeSmokeTestBundles, p *QualificationPolicy) {
			p.Cosign.Digest = ""
			p.Cosign.PlatformDigests = map[string]string{"linux/amd64": nativeSmokeTestDigest("1"), "linux/arm64": nativeSmokeTestDigest("2")}
			p.Smoke.Images["linux/arm64"] = p.Smoke.Images["linux/amd64"]
		}, e.CodeReleaseIncomplete},
		{"missing output", func(_ *factory.Plan, c *ArtifactCheckDescription, _ nativeSmokeTestBundles, _ *QualificationPolicy) {
			c.Outputs = nil
		}, e.CodeReleaseIncomplete},
		{"wrong output subject build", func(_ *factory.Plan, c *ArtifactCheckDescription, _ nativeSmokeTestBundles, _ *QualificationPolicy) {
			c.Outputs[0].BuildDigest = nativeSmokeTestDigest("9")
		}, e.CodeReleaseIncomplete},
		{"extra output", func(_ *factory.Plan, c *ArtifactCheckDescription, _ nativeSmokeTestBundles, _ *QualificationPolicy) {
			c.Outputs = append(c.Outputs, c.Outputs[0])
		}, e.CodeReleaseIncomplete},
		{"wrong bundle platform", func(_ *factory.Plan, _ *ArtifactCheckDescription, b nativeSmokeTestBundles, _ *QualificationPolicy) {
			x := b["cli"]
			x.Arch = "arm64"
			b["cli"] = x
		}, e.CodeReleaseIncomplete},
		{"wrong member", func(_ *factory.Plan, _ *ArtifactCheckDescription, b nativeSmokeTestBundles, _ *QualificationPolicy) {
			b["cli"].Entries[0].Name = "other"
		}, e.CodeEngineCapability},
		{"missing member", func(_ *factory.Plan, _ *ArtifactCheckDescription, b nativeSmokeTestBundles, _ *QualificationPolicy) {
			x := b["cli"]
			x.Entries = nil
			b["cli"] = x
		}, e.CodeReleaseIncomplete},
		{"non-executable", func(_ *factory.Plan, _ *ArtifactCheckDescription, b nativeSmokeTestBundles, _ *QualificationPolicy) {
			b["cli"].Entries[0].Mode = 0o644
		}, e.CodeEngineCapability},
		{"unbounded payload", func(_ *factory.Plan, _ *ArtifactCheckDescription, b nativeSmokeTestBundles, _ *QualificationPolicy) {
			b["cli"].Entries[0].Size = pipeline.MaxToolSmokeExecutableBytes + 1
		}, e.CodeEngineCapability},
		{"ambiguous producer", func(p *factory.Plan, _ *ArtifactCheckDescription, _ nativeSmokeTestBundles, _ *QualificationPolicy) {
			p.Builds[0].Graph.Nodes = append(p.Builds[0].Graph.Nodes, p.Builds[0].Graph.Nodes[0])
		}, e.CodeReleaseIncomplete},
		{"extra exemption", func(_ *factory.Plan, _ *ArtifactCheckDescription, _ nativeSmokeTestBundles, p *QualificationPolicy) {
			p.Smoke.NonToolOutputs = []QualificationNonToolOutput{{Output: "other", Kind: OutputOCI, Components: []string{"image"}, Reason: "deployment-image"}}
		}, e.CodeReleaseIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, checks, bundles, policy := nativeSmokeScopeFixture()
			tc.edit(&plan, &checks, bundles, &policy)
			members, err := deriveQualificationSmokeMembers(&plan, checks, bundles, policy)
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil || len(members) != 1 || members[0].ExpectedVersion != "v1.2.3" {
				t.Fatalf("members=%+v err=%v", members, err)
			}
		})
	}
}

func TestNativeSmokeNonToolScopeIsExact(t *testing.T) {
	for _, kind := range []string{OutputOCI, OutputAPT, OutputHelm, OutputDiskImage} {
		for _, mode := range []string{"exact", "missing", "wrong components"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				plan, checks, bundles, policy := nativeSmokeScopeFixture()
				plan.LifecycleOutputs = append(plan.LifecycleOutputs, factory.LifecycleOutputPlan{Name: "non-tool", Kind: kind, Components: []string{"image"}, BuildDigest: nativeSmokeTestDigest("5")})
				checks.Outputs = append(checks.Outputs, ArtifactCheckOutput{Name: "non-tool", Kind: kind, BuildDigest: nativeSmokeTestDigest("5"), Overall: "pass"})
				if kind == OutputOCI || kind == OutputAPT {
					reason := "deployment-image"
					if kind == OutputAPT {
						reason = "package-repository"
					}
					if mode != "missing" {
						policy.Smoke.NonToolOutputs = []QualificationNonToolOutput{{Output: "non-tool", Kind: kind, Components: []string{"image"}, Reason: reason}}
						if mode == "wrong components" {
							policy.Smoke.NonToolOutputs[0].Components = []string{"other"}
						}
					}
				}
				_, err := deriveQualificationSmokeMembers(&plan, checks, bundles, policy)
				if (kind == OutputOCI || kind == OutputAPT) && mode != "exact" {
					assertCode(t, err, e.CodeGateUnmet)
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestNativeSmokeExpectedStampBindings(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ir.Node)
		code e.Code
	}{
		{"exact prefix", func(*ir.Node) {}, ""},
		{"current stamp family", func(n *ir.Node) { n.Env[0].Name, n.Env[1].Name = "NVKECTL_VERSION", "NVKECTL_COMMIT" }, ""},
		{"mixed stamp families", func(n *ir.Node) { n.Env[0].Name = "NVKECTL_VERSION" }, e.CodeVerifyFailed},
		{"both stamp families", func(n *ir.Node) {
			n.Env = append(n.Env, ir.EnvVar{Name: "NVKECTL_VERSION", Value: "v1.2.3"}, ir.EnvVar{Name: "NVKECTL_COMMIT", Value: n.SourceCommit})
		}, e.CodeVerifyFailed},
		{"current wrong version", func(n *ir.Node) {
			n.Env[0].Name, n.Env[1].Name = "NVKECTL_VERSION", "NVKECTL_COMMIT"
			n.Env[0].Value = "v1.2.4"
		}, e.CodeVerifyFailed},
		{"current wrong commit", func(n *ir.Node) {
			n.Env[0].Name, n.Env[1].Name = "NVKECTL_VERSION", "NVKECTL_COMMIT"
			n.Env[1].Value = "bbbbbbbb"
		}, e.CodeVerifyFailed},
		{"exact full commit", func(n *ir.Node) { n.Env[1].Value = n.SourceCommit }, ""},
		{"commit version", func(n *ir.Node) { n.Version = n.SourceCommit; n.Env[0].Value = n.SourceCommit }, ""},
		{"wrong commit version", func(n *ir.Node) { n.Version = strings.Repeat("b", 40); n.Env[0].Value = n.Version }, e.CodeVerifyFailed},
		{"prefixed commit version", func(n *ir.Node) { n.Version = n.SourceCommit; n.Env[0].Value = "v" + n.SourceCommit }, e.CodeVerifyFailed},
		{"wrong version", func(n *ir.Node) { n.Env[0].Value = "v1.2.4" }, e.CodeVerifyFailed},
		{"wrong commit", func(n *ir.Node) { n.Env[1].Value = "bbbbbbbb" }, e.CodeVerifyFailed},
		{"weak prefix", func(n *ir.Node) { n.Env[1].Value = "aaaa" }, e.CodeVerifyFailed},
		{"acquired cli", func(n *ir.Node) { n.Kind = ir.KindPull }, e.CodeVerifyFailed},
		{"duplicate stamp", func(n *ir.Node) { n.Env = append(n.Env, n.Env[0]) }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := ir.Node{Kind: ir.KindBuild, Version: "1.2.3", SourceCommit: strings.Repeat("a", 40), Env: []ir.EnvVar{{Name: "NKECTL_VERSION", Value: "v1.2.3"}, {Name: "NKECTL_COMMIT", Value: "aaaaaaaa"}}}
			tc.edit(&node)
			version, _, err := expectedSmokeVersion(node, "nvkectl-version-json")
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			} else if version != node.Env[0].Value {
				t.Fatalf("expected version %q differs from frozen stamp %q", version, node.Env[0].Value)
			}
		})
	}
}

type nativeSmokeTestInvocation struct {
	description qualificationorigin.Description
	source      qualificationorigin.Description
	closed      atomic.Bool
	requireHook func()
}

func (i *nativeSmokeTestInvocation) Description() (qualificationorigin.Description, error) {
	if i.closed.Load() {
		return qualificationorigin.Description{}, e.New(e.CodeProvenanceMissing, "closed invocation")
	}
	return i.description, nil
}
func (i *nativeSmokeTestInvocation) RequireSource(source qualificationorigin.Description) error {
	if _, err := i.Description(); err != nil {
		return err
	}
	if source != i.source {
		return e.New(e.CodeVerifyFailed, "wrong invocation source")
	}
	if i.requireHook != nil {
		i.requireHook()
	}
	return nil
}

type nativeSmokeTestRuntime struct {
	expected  []ToolSmokeMember
	count     int
	closed    bool
	runHook   func(context.Context) error
	closeHook func() error
}

func (r *nativeSmokeTestRuntime) Close() error {
	r.closed = true
	if r.closeHook != nil {
		return r.closeHook()
	}
	return nil
}
func (r *nativeSmokeTestRuntime) run(ctx context.Context, file *heldfile.File, profile pipeline.ToolSmokeProfile) (nativeSmokeObservation, error) {
	if r.runHook != nil {
		if err := r.runHook(ctx); err != nil {
			return nil, err
		}
	}
	expected := r.expected[r.count]
	r.count++
	if file.Digest() != expected.PayloadDigest || file.Size() != expected.Size {
		return nil, e.New(e.CodeVerifyFailed, "substituted held bytes")
	}
	if err := file.AssertUnchangedContext(ctx); err != nil {
		return nil, err
	}
	return &nativeSmokeTestObservation{profile: profile, digest: file.Digest(), size: file.Size(), platform: expected.Platform, image: nativeSmokeTestDigest("3"), identity: nativeSmokeTestDigest("4"), stdout: []byte(expected.ExpectedVersion)}, nil
}

func nativeSmokeLiveFixture(t *testing.T) (*ArtifactCheckReceipt, *nativeSmokeTestInvocation, QualificationPolicy, *nativeSmokeTestRuntime) {
	t.Helper()
	request, consumer, verifier := newQualificationFixture(t)
	checks, _, err := consumer.checkQualification(t.Context(), request, verifier.source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = checks.Close() })
	policy := nativeSmokeTestPolicy()
	policy.Smoke.Tools = map[string]string{}
	for _, output := range checks.lifetime.plan.LifecycleOutputs {
		for _, component := range output.Components {
			policy.Smoke.Tools[component] = "helm-version"
		}
	}
	expected, err := qualificationSmokeMembers(checks, policy)
	if err != nil {
		t.Fatal(err)
	}
	source := verifier.source.value
	current := source
	current.RunID = "50"
	current.Workflow = policy.QualificationWorkflow
	current.BuilderID = "https://github.com/" + source.Repository + "/" + current.Workflow + "@" + source.Ref
	current.InvocationID = "https://github.com/" + source.Repository + "/actions/runs/50/attempts/" + current.Attempt
	return checks, &nativeSmokeTestInvocation{source: source, description: current}, policy, &nativeSmokeTestRuntime{expected: expected}
}

func TestNativeSmokeOwnerIssuesOnlyAfterJoinedExecution(t *testing.T) {
	checks, invocation, policy, owner := nativeSmokeLiveFixture(t)
	receipt, report, err := runNativeToolSmoke(t.Context(), checks, invocation, policy, NativeToolSmokeOptions{}, "linux/amd64", func(_ context.Context, selection pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) {
		if selection.Image != policy.Smoke.Images[selection.Platform] || selection.RuntimeExecutable != "docker" {
			t.Fatal("wrong independent runtime selection")
		}
		return owner, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receipt.Close() }()
	if !owner.closed || owner.count != len(owner.expected) || report.Status != "pass" {
		t.Fatalf("incomplete operation: %+v %+v", owner, report)
	}
	raw, err := report.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	got, err := receipt.Digest()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := report.Digest()
	if got != want {
		t.Fatal("receipt/report digest mismatch")
	}
	report.Members[0].ObservedVersion = "forged"
	policy.Smoke.Tools[owner.expected[0].Member] = "nvkectl-version-json"
	description, err := receipt.Description()
	if err != nil {
		t.Fatal(err)
	}
	immutable, _ := description.Canonical()
	if string(immutable) != string(raw) {
		t.Fatal("caller mutation changed receipt")
	}
	copyReceipt := *receipt
	_ = copyReceipt.Close()
	_, err = receipt.Description()
	assertCode(t, err, e.CodeGateUnmet)
	if _, err := checks.Description(); err != nil {
		t.Fatal("closing smoke closed borrowed A")
	}
}

func TestNativeSmokeOwnerFailureAndRevocation(t *testing.T) {
	checks, baseInvocation, basePolicy, baseOwner := nativeSmokeLiveFixture(t)
	// Execution, version and cleanup failures have stronger member-by-member
	// assertions in TestNativeSmokeFailureDiagnosticsCoverKnownMembers.
	for _, mode := range []string{"invocation revoked", "final context canceled", "wrong invocation", "wrong policy", "unsupported platform"} {
		t.Run(mode, func(t *testing.T) {
			invocation := &nativeSmokeTestInvocation{source: baseInvocation.source, description: baseInvocation.description}
			policy := basePolicy
			owner := &nativeSmokeTestRuntime{expected: slices.Clone(baseOwner.expected)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			platform := "linux/amd64"
			want := e.CodeEngineCapability
			opened := false
			switch mode {
			case "invocation revoked":
				owner.closeHook = func() error { invocation.closed.Store(true); return nil }
				want = e.CodeProvenanceMissing
			case "final context canceled":
				owner.closeHook = func() error { invocation.requireHook = cancel; return nil }
				want = e.CodeTimeout
			case "wrong invocation":
				invocation.description.Workflow = ".github/workflows/other.yml"
				want = e.CodeUntrustedSigner
			case "wrong policy":
				policy.Source.Repository = "other/repository"
				want = e.CodeUntrustedSigner
			case "unsupported platform":
				platform = "darwin/arm64"
			}
			receipt, report, err := runNativeToolSmoke(ctx, checks, invocation, policy, NativeToolSmokeOptions{}, platform, func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) {
				opened = true
				return owner, nil
			})
			assertCode(t, err, want)
			if receipt != nil {
				t.Fatal("failure issued receipt")
			}
			if opened && (!owner.closed || report == nil || report.Status != "fail" || report.Code != string(want)) {
				t.Fatalf("failure lost cleanup/diagnostics: %+v %+v", owner, report)
			}
			if mode == "final context canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost context cause")
			}
		})
	}
}

func TestNativeSmokeClosingACancelsAndJoinsRuntime(t *testing.T) {
	checks, invocation, policy, owner := nativeSmokeLiveFixture(t)
	started := make(chan struct{})
	cleanup := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	owner.runHook = func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return e.Wrap(e.CodeTimeout, ctx.Err(), "runtime canceled")
	}
	owner.closeHook = func() error { close(cleanup); <-release; return nil }
	go func() {
		receipt, _, err := runNativeToolSmoke(t.Context(), checks, invocation, policy, NativeToolSmokeOptions{}, "linux/amd64", func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) { return owner, nil })
		if receipt != nil {
			done <- fmt.Errorf("cancellation issued receipt")
			return
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- checks.Close() }()
	select {
	case <-cleanup:
	case <-time.After(5 * time.Second):
		t.Fatal("A close did not cancel runtime")
	}
	select {
	case err := <-closed:
		t.Fatalf("A closed before runtime cleanup: %v", err)
	default:
	}
	if _, err := checks.lifetime.verified.Description(); err != nil {
		t.Fatal("payload released before runtime cleanup")
	}
	close(release)
	assertCode(t, <-done, e.CodeTimeout)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestNativeSmokeReportRejectsIncompleteOrSubstitutedFields(t *testing.T) {
	checks, invocation, policy, owner := nativeSmokeLiveFixture(t)
	receipt, report, err := runNativeToolSmoke(t.Context(), checks, invocation, policy, NativeToolSmokeOptions{}, "linux/amd64", func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) { return owner, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receipt.Close() }()
	raw, err := report.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"kind-version", "clusterctl-version-json", "oras-version"} {
		t.Run(profile+" commit shape", func(t *testing.T) {
			var changed NativeToolSmokeReport
			if err := canonical.UnmarshalExact(raw, &changed); err != nil {
				t.Fatal(err)
			}
			member := &changed.Members[0]
			member.Profile = profile
			for _, commit := range []string{"", "abcdef12", strings.Repeat("a", 40)} {
				member.ExpectedCommit, member.ObservedCommit = commit, commit
				err := changed.Validate()
				valid := (profile == "kind-version" && commit == "") || (profile != "kind-version" && len(commit) == 40)
				if valid {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					assertCode(t, err, e.CodeVerifyFailed)
				}
			}
		})
	}
	for _, tc := range []struct {
		name string
		edit func(*NativeToolSmokeReport)
	}{
		{"failed", func(r *NativeToolSmokeReport) { r.Status = "fail" }}, {"code on pass", func(r *NativeToolSmokeReport) { r.Code = "error" }},
		{"missing policy", func(r *NativeToolSmokeReport) { r.PolicyDigest = "" }}, {"missing runtime", func(r *NativeToolSmokeReport) { r.RuntimeIdentity = "" }},
		{"mutable image", func(r *NativeToolSmokeReport) { r.Image = "registry.example.test/image:latest" }},
		{"wrong invocation", func(r *NativeToolSmokeReport) { r.Invocation.InvocationID = r.ArtifactChecks.Origin.InvocationID }},
		{"empty members", func(r *NativeToolSmokeReport) { r.Members = nil }}, {"duplicate member", func(r *NativeToolSmokeReport) { r.Members = append(r.Members, r.Members[0]) }},
		{"wrong output", func(r *NativeToolSmokeReport) { r.Members[0].Output = "other" }}, {"wrong subject", func(r *NativeToolSmokeReport) { r.Members[0].Subject = nativeSmokeTestDigest("9") }},
		{"wrong observed version", func(r *NativeToolSmokeReport) { r.Members[0].ObservedVersion = "vwrong" }}, {"wrong platform", func(r *NativeToolSmokeReport) { r.Members[0].Platform = "linux/arm64" }},
		{"failed A", func(r *NativeToolSmokeReport) { r.ArtifactChecks.Outputs[0].Overall = "fail" }},
		{"duplicate build subject", func(r *NativeToolSmokeReport) {
			r.ArtifactChecks.Outputs[0].BuildSubjects = append(r.ArtifactChecks.Outputs[0].BuildSubjects, r.ArtifactChecks.Outputs[0].BuildSubjects[0])
		}},
		{"unsorted build subjects", func(r *NativeToolSmokeReport) {
			subject := r.ArtifactChecks.Outputs[0].BuildSubjects[0]
			r.ArtifactChecks.Outputs[0].BuildSubjects = []delivery.BuildSubject{{Component: subject.Component + "-tail", Digest: subject.Digest}, subject}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed NativeToolSmokeReport
			if err := canonical.UnmarshalExact(raw, &changed); err != nil {
				t.Fatal(err)
			}
			tc.edit(&changed)
			_, err := changed.Canonical()
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
	var decoded NativeToolSmokeReport
	if err := canonical.UnmarshalExact(append(slices.Clone(raw[:len(raw)-1]), []byte(`,"passed":true}`)...), &decoded); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestNativeSmokeScopeCoversEveryMemberAndPlatform(t *testing.T) {
	plan, checks, bundles, policy := nativeSmokeScopeFixture()
	plan.LifecycleOutputs[0].Components = append(plan.LifecycleOutputs[0].Components, "tool-two")
	node := plan.Builds[0].Graph.Nodes[0]
	node.Component = "tool-two"
	node.Version = "4.5.6"
	plan.Builds[0].Graph.Nodes = append(plan.Builds[0].Graph.Nodes, node)
	bundle := bundles["cli"]
	entry := bundle.Entries[0]
	entry.Name = "tool-two"
	entry.InstallPath = "/usr/local/bin/tool-two"
	entry.SHA256 = nativeSmokeTestDigest("8")
	bundle.Entries = append(bundle.Entries, entry)
	bundles["cli"] = bundle
	policy.Smoke.Tools["tool-two"] = "helm-version"
	second := plan.LifecycleOutputs[0]
	second.Name = "cli-arm"
	plan.LifecycleOutputs = append(plan.LifecycleOutputs, second)
	build := plan.Builds[0]
	build.Name = second.Name
	build.Platform = "linux/arm64"
	build.Graph = &ir.Graph{Nodes: slices.Clone(build.Graph.Nodes)}
	for i := range build.Graph.Nodes {
		build.Graph.Nodes[i].Platform = build.Platform
	}
	plan.Builds = append(plan.Builds, build)
	checked := checks.Outputs[0]
	checked.Name = second.Name
	checked.Platform = build.Platform
	checked.Subject = nativeSmokeTestDigest("7")
	checks.Outputs = append(checks.Outputs, checked)
	bundle.Arch = "arm64"
	bundles[second.Name] = bundle
	policy.Cosign.Digest = ""
	policy.Cosign.PlatformDigests = map[string]string{"linux/amd64": nativeSmokeTestDigest("1"), "linux/arm64": nativeSmokeTestDigest("2")}
	policy.Smoke.Images[build.Platform] = "registry.example.test/smoke-arm@" + nativeSmokeTestDigest("3")
	members, err := deriveQualificationSmokeMembers(&plan, checks, bundles, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 4 || members[0].Output != "cli" || members[0].Member != "tool" || members[1].Member != "tool-two" || members[1].ExpectedVersion != "v4.5.6" || members[2].Output != "cli-arm" || members[2].Platform != "linux/arm64" {
		t.Fatalf("incomplete sorted scope: %+v", members)
	}
	delete(policy.Smoke.Images, "linux/arm64")
	_, err = deriveQualificationSmokeMembers(&plan, checks, bundles, policy)
	assertCode(t, err, e.CodeReleaseIncomplete)
}

func TestNativeSmokeFreezesPolicyBeforeRuntimeAdmission(t *testing.T) {
	checks, invocation, policy, owner := nativeSmokeLiveFixture(t)
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	image := policy.Smoke.Images["linux/amd64"]
	receipt, report, err := runNativeToolSmoke(t.Context(), checks, invocation, policy, NativeToolSmokeOptions{}, "linux/amd64", func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) {
		policy.Smoke.Images["linux/amd64"] = "registry.example.test/replaced@" + nativeSmokeTestDigest("9")
		for component := range policy.Smoke.Tools {
			policy.Smoke.Tools[component] = "nvkectl-version-json"
		}
		return owner, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receipt.Close() }()
	if report.PolicyDigest != digest || report.Image != image {
		t.Fatal("caller mutation changed selected policy identity")
	}
	for _, member := range report.Members {
		if member.Profile != "helm-version" {
			t.Fatal("caller mutation changed profile during execution")
		}
	}
	invocation.closed.Store(true)
	_, err = receipt.Description()
	assertCode(t, err, e.CodeProvenanceMissing)
}

func TestNativeSmokePublicAdmissionAndBorrowedAClose(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{{"nil context", nil, e.CodeUsage}, {"canceled", canceled, e.CodeTimeout}, {"unissued A", t.Context(), e.CodeGateUnmet}} {
		t.Run(tc.name, func(t *testing.T) {
			receipt, report, err := RunNativeToolSmoke(tc.ctx, nil, nil, QualificationPolicy{}, NativeToolSmokeOptions{RuntimeExecutable: "/missing/docker"})
			assertCode(t, err, tc.code)
			if receipt != nil || report != nil {
				t.Fatal("refused admission returned partial result")
			}
		})
	}
	checks, invocation, policy, owner := nativeSmokeLiveFixture(t)
	receipt, _, err := runNativeToolSmoke(t.Context(), checks, invocation, policy, NativeToolSmokeOptions{}, "linux/amd64", func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) { return owner, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receipt.Close() }()
	if err := checks.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = receipt.Description()
	assertCode(t, err, e.CodeGateUnmet)
}

func TestNativeSmokeScopeBoundsBeforeRuntime(t *testing.T) {
	plan, checks, bundles, policy := nativeSmokeScopeFixture()
	nodes := make([]ir.Node, 512)
	components := make([]string, 512)
	entries := make([]delivery.BinaryEntry, 512)
	policy.Smoke.Tools = map[string]string{}
	for i := range nodes {
		name := fmt.Sprintf("tool-%03d", i)
		components[i] = name
		nodes[i] = ir.Node{Kind: ir.KindBuild, Component: name, Version: "1.2.3", OutputType: "host-package", Platform: "linux/amd64"}
		entries[i] = delivery.BinaryEntry{Name: name, InstallPath: "/usr/local/bin/" + name, Mode: 0o755, SHA256: nativeSmokeTestDigest("4"), Size: 100}
		policy.Smoke.Tools[name] = "helm-version"
	}
	templateOutput, templateBuild, templateCheck, templateBundle := plan.LifecycleOutputs[0], plan.Builds[0], checks.Outputs[0], bundles["cli"]
	plan.LifecycleOutputs = nil
	plan.Builds = nil
	checks.Outputs = nil
	for i := range 9 {
		name := fmt.Sprintf("cli-%03d", i)
		output := templateOutput
		output.Name = name
		output.Components = components
		plan.LifecycleOutputs = append(plan.LifecycleOutputs, output)
		build := templateBuild
		build.Name = name
		build.Graph = &ir.Graph{Nodes: nodes}
		plan.Builds = append(plan.Builds, build)
		check := templateCheck
		check.Name = name
		checks.Outputs = append(checks.Outputs, check)
		bundle := templateBundle
		bundle.Purpose = name
		bundle.Entries = entries
		bundles[name] = bundle
	}
	_, err := deriveQualificationSmokeMembers(&plan, checks, bundles, policy)
	assertCode(t, err, e.CodeEngineCapability)
	node := ir.Node{Version: strings.Repeat("a", 256)}
	_, _, err = expectedSmokeVersion(node, "helm-version")
	assertCode(t, err, e.CodeVerifyFailed)
}

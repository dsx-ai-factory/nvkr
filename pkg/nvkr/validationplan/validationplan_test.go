// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validationplan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

const reference = "registry.example.test/nvkr/releases@sha256:" +
	"1111111111111111111111111111111111111111111111111111111111111111"

func digest(c string) string { return "sha256:" + strings.Repeat(c, 64) }

func codeOf(t *testing.T, err error) e.Code {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) {
		t.Fatalf("error %v carries no code", err)
	}
	return coded.Code
}

func writePlanners(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const oneE2E = `apiVersion: nvkr.dev/validation-planners/v1
kind: ValidationPlanners
planners:
  - name: fake
    executor: nvke-e2e-gotest
    config: fake/planner.json
`

const twoPlanners = `apiVersion: nvkr.dev/validation-planners/v1
kind: ValidationPlanners
planners:
  - name: b-fake
    executor: nvke-e2e-gotest
    config: b/planner.json
  - name: a-fake
    executor: validators
    config: a/planner.json
`

// fakePlanner returns what it is given and records what it received.
type fakePlanner struct {
	name    string
	targets []Target
	plans   []PlannerPlan
	err     error
	got     Input
}

func (f *fakePlanner) Plan(_ context.Context, in Input) (Result, error) {
	f.got = in
	return Result{Targets: f.targets, Plans: f.plans}, f.err
}

func e2eTarget(planner, id string) Target {
	return Target{
		ID:     id,
		Inputs: Inputs{Suite: "nvke/e2e", TestEnv: "env", Components: map[string]Component{"cli": {Reference: "r/nvkectl@sha256:aa", Origin: OriginRelease}}},
		Source: Source{Planner: planner},
	}
}

func validatorTarget(planner, id string) Target {
	return Target{
		ID:     id,
		Inputs: Inputs{Suite: "nvke/nvkectl/cli", Platform: "linux/amd64", Components: map[string]Component{"executable": {Reference: "r/nvkectl@sha256:aa", Origin: OriginRelease}}},
		Source: Source{Planner: planner},
	}
}

func req(dir string) Request {
	return Request{Release: Release{Reference: reference}, PlannersDir: dir}
}

func TestBuildJoinsSortsAndAssignsExecutors(t *testing.T) {
	dir := writePlanners(t, twoPlanners)
	embedded, err := NewPlannerPlan("b-fake", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	b := &fakePlanner{targets: []Target{e2eTarget("b-fake", "e2e/two"), e2eTarget("b-fake", "e2e/one")}, plans: []PlannerPlan{embedded}}
	a := &fakePlanner{targets: []Target{validatorTarget("a-fake", "artifacts/cli")}}

	plan, err := Build(context.Background(), req(dir), map[string]Planner{"b-fake": b, "a-fake": a})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, target := range plan.Targets {
		got = append(got, target.ID+"="+target.Executor)
	}
	want := "artifacts/cli=validators,e2e/one=nvke-e2e-gotest,e2e/two=nvke-e2e-gotest"
	if strings.Join(got, ",") != want {
		t.Fatalf("targets = %v, want %s", got, want)
	}
	if plan.QualificationAuthority != AuthorityNone || plan.SchemaVersion != SchemaVersion || plan.Release.Reference != reference {
		t.Fatalf("header = %+v", plan)
	}
	if len(plan.PlannerPlans) != 1 || plan.PlannerPlans[0].Planner != "b-fake" {
		t.Fatalf("plans = %+v", plan.PlannerPlans)
	}
	if want := filepath.Join(dir, "b", "planner.json"); b.got.Config != want {
		t.Errorf("planner config = %q, want %q", b.got.Config, want)
	}
}

func TestBuildGivesEachPlannerOnlyItsOwnValues(t *testing.T) {
	dir := writePlanners(t, twoPlanners)
	a := &fakePlanner{name: "a-fake"}
	b := &fakePlanner{name: "b-fake"}
	r := req(dir)
	r.Vars = map[string]map[string]string{"a-fake": {"x": "1"}, "b-fake": {"y": "2"}}
	if _, err := Build(context.Background(), r, map[string]Planner{"a-fake": a, "b-fake": b}); err != nil {
		t.Fatal(err)
	}
	if len(a.got.Vars) != 1 || a.got.Vars["x"] != "1" || len(b.got.Vars) != 1 || b.got.Vars["y"] != "2" {
		t.Fatalf("a got %v, b got %v", a.got.Vars, b.got.Vars)
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	dir := writePlanners(t, twoPlanners)
	build := func() []byte {
		a := &fakePlanner{targets: []Target{validatorTarget("a-fake", "artifacts/cli")}}
		b := &fakePlanner{targets: []Target{e2eTarget("b-fake", "e2e/two"), e2eTarget("b-fake", "e2e/one")}}
		plan, err := Build(context.Background(), req(dir), map[string]Planner{"b-fake": b, "a-fake": a})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := plan.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	first, second := build(), build()
	if string(first) != string(second) {
		t.Fatal("two builds of the same inputs differ")
	}
}

func TestBuildFailsClosed(t *testing.T) {
	embedded, err := NewPlannerPlan("fake", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	wrongPlan := embedded
	wrongPlan.Planner = "other"

	tests := []struct {
		name    string
		config  string
		planner *fakePlanner
		set     map[string]map[string]string
		omit    bool
		want    e.Code
	}{
		{"a planner that is not built in", oneE2E, nil, nil, true, e.CodeUnsupported},
		{"values for an unlisted planner", oneE2E, &fakePlanner{}, map[string]map[string]string{"ghost": {"k": "v"}}, false, e.CodeUsage},
		{"an uncoded planner failure", oneE2E, &fakePlanner{err: errors.New("boom")}, nil, false, e.CodeInternal},
		{"a target attributed to another planner", oneE2E, &fakePlanner{targets: []Target{e2eTarget("other", "e2e/a")}}, nil, false, e.CodeInternal},
		{"an embedded plan attributed to another planner", oneE2E, &fakePlanner{targets: []Target{e2eTarget("fake", "e2e/a")}, plans: []PlannerPlan{wrongPlan}}, nil, false, e.CodeInternal},
		{"a target missing an input its executor requires", oneE2E, &fakePlanner{targets: []Target{{ID: "e2e/a", Source: Source{Planner: "fake"}, Inputs: Inputs{Suite: "nvke/e2e"}}}}, nil, false, e.CodeUsage},
		{"two targets with one id", oneE2E, &fakePlanner{targets: []Target{e2eTarget("fake", "e2e/a"), e2eTarget("fake", "e2e/a")}}, nil, false, e.CodeUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			planners := map[string]Planner{}
			if !tc.omit {
				planners["fake"] = tc.planner
			}
			r := req(writePlanners(t, tc.config))
			r.Vars = tc.set
			plan, err := Build(context.Background(), r, planners)
			if err == nil {
				t.Fatalf("a plan was built: %+v", plan)
			}
			if got := codeOf(t, err); got != tc.want {
				t.Fatalf("code = %s, want %s (%v)", got, tc.want, err)
			}
			if plan != nil {
				t.Fatal("a failure returned a plan")
			}
		})
	}
}

func TestBuildAdmitsBeforeWork(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	planner := &fakePlanner{}
	tests := []struct {
		name string
		ctx  context.Context
		req  Request
		want e.Code
	}{
		{"a canceled context", canceled, req(writePlanners(t, oneE2E)), e.CodeTimeout},
		{"no release reference", context.Background(), Request{PlannersDir: writePlanners(t, oneE2E)}, e.CodeUsage},
		{"no planners directory", context.Background(), Request{Release: Release{Reference: reference}}, e.CodeUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Build(tc.ctx, tc.req, map[string]Planner{"fake": planner})
			if err == nil || codeOf(t, err) != tc.want {
				t.Fatalf("err = %v, want code %s", err, tc.want)
			}
		})
	}
	if planner.got.Config != "" {
		t.Fatal("a planner ran although admission failed")
	}
}

func TestLoadConfigRejects(t *testing.T) {
	good := "apiVersion: nvkr.dev/validation-planners/v1\nkind: ValidationPlanners\n"
	tests := []struct {
		name string
		body string
		want e.Code
	}{
		{"another schema", "apiVersion: nvkr.dev/other/v1\nkind: ValidationPlanners\nplanners:\n  - {name: a, executor: validators, config: a.json}\n", e.CodeUnsupported},
		{"no planners", good + "planners: []\n", e.CodeUsage},
		{"an unknown field", good + "planners:\n  - {name: a, executor: validators, config: a.json, extra: x}\n", e.CodeUsage},
		{"an unknown executor", good + "planners:\n  - {name: a, executor: sudo, config: a.json}\n", e.CodeUnsupported},
		{"a repeated planner", good + "planners:\n  - {name: a, executor: validators, config: a.json}\n  - {name: a, executor: validators, config: b.json}\n", e.CodeUsage},
		{"a config that leaves the directory", good + "planners:\n  - {name: a, executor: validators, config: ../a.json}\n", e.CodeUsage},
		{"an absolute config", good + "planners:\n  - {name: a, executor: validators, config: /etc/passwd}\n", e.CodeUsage},
		{"an anchor", good + "planners:\n  - &x {name: a, executor: validators, config: a.json}\n", e.CodeUsage},
		{"two documents", good + "planners:\n  - {name: a, executor: validators, config: a.json}\n---\nx: y\n", e.CodeUsage},
		{"a repeated key", good + "planners:\n  - {name: a, name: b, executor: validators, config: a.json}\n", e.CodeUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(writePlanners(t, tc.body))
			if err == nil {
				t.Fatal("the configuration was accepted")
			}
			if got := codeOf(t, err); got != tc.want {
				t.Fatalf("code = %s, want %s (%v)", got, tc.want, err)
			}
		})
	}
	t.Run("a missing file", func(t *testing.T) {
		if _, err := LoadConfig(t.TempDir()); err == nil || codeOf(t, err) != e.CodeUsage {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestShippedPlannersConfigsLoad(t *testing.T) {
	for _, dir := range []string{"../../../inputs/testing/planners", "../../../demos/hello/planners"} {
		t.Run(dir, func(t *testing.T) {
			cfg, err := LoadConfig(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Planners) == 0 {
				t.Fatal("the shipped configuration lists no planner")
			}
		})
	}
}

func TestPlanValidate(t *testing.T) {
	embedded, err := NewPlannerPlan("p", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	valid := func() Plan {
		return Plan{
			SchemaVersion: SchemaVersion, QualificationAuthority: AuthorityNone,
			Release:      ReleaseRef{Reference: reference},
			Targets:      []Target{{ID: "e2e/a", Executor: "x", Source: Source{Planner: "p"}}},
			PlannerPlans: []PlannerPlan{embedded},
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Plan)
		want   e.Code
	}{
		{"another schema", func(p *Plan) { p.SchemaVersion = "x" }, e.CodeUnsupported},
		{"authority claimed", func(p *Plan) { p.QualificationAuthority = "qualified" }, e.CodeUsage},
		{"no release", func(p *Plan) { p.Release.Reference = "" }, e.CodeUsage},
		{"a target with no id", func(p *Plan) { p.Targets[0].ID = "" }, e.CodeUsage},
		{"a target with no executor", func(p *Plan) { p.Targets[0].Executor = "" }, e.CodeUsage},
		{"a target with no planner", func(p *Plan) { p.Targets[0].Source.Planner = "" }, e.CodeUsage},
		{"a component with no origin", func(p *Plan) {
			p.Targets[0].Inputs.Components = map[string]Component{"c": {Version: "1"}}
		}, e.CodeUsage},
		{"a component with an unknown origin", func(p *Plan) {
			p.Targets[0].Inputs.Components = map[string]Component{"c": {Version: "1", Origin: "guess"}}
		}, e.CodeUsage},
		{"a component that carries nothing", func(p *Plan) {
			p.Targets[0].Inputs.Components = map[string]Component{"c": {Origin: OriginRelease}}
		}, e.CodeUsage},
		{"a planner plan that is not a digest", func(p *Plan) { p.PlannerPlans[0].Digest = "sha256:short" }, e.CodeUsage},
		{"two plans from one planner", func(p *Plan) { p.PlannerPlans = append(p.PlannerPlans, embedded) }, e.CodeUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := valid()
			tc.mutate(&p)
			err := p.Validate()
			if err == nil || codeOf(t, err) != tc.want {
				t.Fatalf("err = %v, want code %s", err, tc.want)
			}
		})
	}
}

func TestNewPlannerPlanBindsItsDigest(t *testing.T) {
	value := map[string]string{"b": "2", "a": "1"}
	pp, err := NewPlannerPlan("p", value)
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical.Digest(canonical.KindPlannerPlan, value)
	if err != nil {
		t.Fatal(err)
	}
	if pp.Digest != want || string(pp.Plan) != `{"a":"1","b":"2"}` {
		t.Fatalf("plan = %+v", pp)
	}
}

func TestPlannerPlansAreNotPartOfThePlanEncoding(t *testing.T) {
	pp, err := NewPlannerPlan("p", map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		SchemaVersion: SchemaVersion, QualificationAuthority: AuthorityNone,
		Release:      ReleaseRef{Reference: reference},
		Targets:      []Target{{ID: "e2e/a", Executor: "x", Source: Source{Planner: "p", PlanDigest: pp.Digest}}},
		PlannerPlans: []PlannerPlan{pp},
	}
	raw, err := plan.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"k":"v"`) || strings.Contains(string(raw), "plans") {
		t.Fatalf("the plan carries the planner plan: %s", raw)
	}
	if !strings.Contains(string(raw), pp.Digest) {
		t.Fatalf("the target does not name the planner plan's digest: %s", raw)
	}
	if pp.FileName() != "p.json" || string(pp.Encode()) != `{"k":"v"}`+"\n" {
		t.Fatalf("file = %q %q", pp.FileName(), pp.Encode())
	}
}

// fakeReceipt stands in for the lifecycle receipt.
type fakeReceipt struct {
	description lifecycle.ArtifactCheckDescription
	descErr     error
	closeErr    error
	closed      int
}

func (f *fakeReceipt) Description() (lifecycle.ArtifactCheckDescription, error) {
	return f.description, f.descErr
}

func (f *fakeReceipt) Close() error {
	f.closed++
	return f.closeErr
}

func description(subject string, outputs ...lifecycle.ArtifactCheckOutput) lifecycle.ArtifactCheckDescription {
	d := lifecycle.ArtifactCheckDescription{Outputs: outputs}
	d.Inputs.ReleaseSubject = subject
	return d
}

func output(name, c string) lifecycle.ArtifactCheckOutput {
	return lifecycle.ArtifactCheckOutput{
		Name: name, Kind: lifecycle.OutputOCI,
		Reference: "registry.example.test/nvkr/" + name + "@" + digest(c), Subject: digest(c),
	}
}

func generateReq(dir string) GenerateRequest {
	return GenerateRequest{Reference: reference, Policy: "policy.yaml", PlannersDir: dir}
}

func TestGenerateAuthenticatesThenPlansFromTheDescription(t *testing.T) {
	receipt := &fakeReceipt{description: description(reference, output("nvkectl", "c"), output("nvke-controller", "a"))}
	var checked []string
	check := func(_ context.Context, ref, policy string, _ lifecycle.QualificationRuntimeOptions) (authenticated, error) {
		checked = append(checked, ref, policy)
		return receipt, nil
	}
	planner := &fakePlanner{targets: []Target{e2eTarget("fake", "e2e/a")}}
	plan, err := generate(context.Background(), generateReq(writePlanners(t, oneE2E)), map[string]Planner{"fake": planner}, lifecycle.QualificationRuntimeOptions{}, check)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(checked, ",") != reference+",policy.yaml" {
		t.Fatalf("checked %v", checked)
	}
	if len(planner.got.Release.Outputs) != 2 || planner.got.Release.Outputs[0].Name != "nvkectl" || planner.got.Release.Outputs[0].Digest != digest("c") {
		t.Fatalf("release outputs = %+v, want the authenticated ones", planner.got.Release.Outputs)
	}
	if plan.Release.Reference != reference {
		t.Fatalf("release = %s", plan.Release.Reference)
	}
	if receipt.closed != 1 {
		t.Fatalf("receipt closed %d times, want once", receipt.closed)
	}
}

func TestGenerateFailsClosed(t *testing.T) {
	other := "registry.example.test/nvkr/releases@" + digest("9")
	bad := output("nvkectl", "c")
	bad.Subject = "not-a-digest"

	tests := []struct {
		name    string
		req     func(t *testing.T) GenerateRequest
		receipt *fakeReceipt
		checkE  error
		want    e.Code
		reached bool
	}{
		{"the check fails", func(t *testing.T) GenerateRequest { return generateReq(writePlanners(t, oneE2E)) }, nil, e.New(e.CodeVerifyFailed, "no"), e.CodeVerifyFailed, true},
		{"a different release subject", func(t *testing.T) GenerateRequest { return generateReq(writePlanners(t, oneE2E)) }, &fakeReceipt{description: description(other, output("a", "a"))}, nil, e.CodeVerifyFailed, true},
		{"no release subject", func(t *testing.T) GenerateRequest { return generateReq(writePlanners(t, oneE2E)) }, &fakeReceipt{description: description("", output("a", "a"))}, nil, e.CodeProvenanceMissing, true},
		{"an output with no digest", func(t *testing.T) GenerateRequest { return generateReq(writePlanners(t, oneE2E)) }, &fakeReceipt{description: description(reference, bad)}, nil, e.CodeProvenanceMissing, true},
		{"the description fails", func(t *testing.T) GenerateRequest { return generateReq(writePlanners(t, oneE2E)) }, &fakeReceipt{descErr: e.New(e.CodeGateUnmet, "closed")}, nil, e.CodeGateUnmet, true},
		{"the receipt will not close", func(t *testing.T) GenerateRequest { return generateReq(writePlanners(t, oneE2E)) }, &fakeReceipt{description: description(reference, output("a", "a")), closeErr: errors.New("held")}, nil, e.CodeGateUnmet, true},
		{"a reference that is a URL", func(t *testing.T) GenerateRequest {
			r := generateReq(writePlanners(t, oneE2E))
			r.Reference = "https://registry.example.test/x@" + digest("1")
			return r
		}, nil, nil, e.CodeUnverifiedRef, false},
		{"no policy", func(t *testing.T) GenerateRequest {
			r := generateReq(writePlanners(t, oneE2E))
			r.Policy = ""
			return r
		}, nil, nil, e.CodeUsage, false},
		{"a planners directory with no configuration", func(t *testing.T) GenerateRequest { return generateReq(t.TempDir()) }, nil, nil, e.CodeUsage, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			check := func(context.Context, string, string, lifecycle.QualificationRuntimeOptions) (authenticated, error) {
				reached = true
				if tc.checkE != nil {
					return nil, tc.checkE
				}
				return tc.receipt, nil
			}
			planner := &fakePlanner{targets: []Target{e2eTarget("fake", "e2e/a")}}
			plan, err := generate(context.Background(), tc.req(t), map[string]Planner{"fake": planner}, lifecycle.QualificationRuntimeOptions{}, check)
			if err == nil {
				t.Fatalf("a plan was made: %+v", plan)
			}
			if got := codeOf(t, err); got != tc.want {
				t.Fatalf("code = %s, want %s (%v)", got, tc.want, err)
			}
			if plan != nil {
				t.Fatal("a failure returned a plan")
			}
			if reached != tc.reached {
				t.Fatalf("check reached = %v, want %v: admission must precede registry work", reached, tc.reached)
			}
			if tc.receipt != nil && tc.receipt.closed != 1 {
				t.Fatalf("receipt closed %d times, want once", tc.receipt.closed)
			}
		})
	}
}

// fakeLocal stands in for the lifecycle owner of a captured local Run.
type fakeLocal struct {
	description lifecycle.LocalDiagnosticDescription
	descErr     error
	closeErr    error
	closed      int
}

func (f *fakeLocal) Description() (lifecycle.LocalDiagnosticDescription, error) {
	return f.description, f.descErr
}

func (f *fakeLocal) Close() error {
	f.closed++
	return f.closeErr
}

func localOutput(name, c string) delivery.PublicationOutput {
	return delivery.PublicationOutput{Output: delivery.Output{
		Name: name, Kind: "oci", Digest: digest(c), OS: "linux", Arch: "amd64",
	}}
}

func localDescription(outputs ...delivery.PublicationOutput) lifecycle.LocalDiagnosticDescription {
	return lifecycle.LocalDiagnosticDescription{Attempt: "attempt-1", ContentSubject: digest("7"), Outputs: outputs}
}

func localReq(dir string) LocalRequest {
	return LocalRequest{AttemptDir: ".nvkr/runs/attempt-1", PlannersDir: dir}
}

func TestGenerateLocalPlansFromACapturedRunWithUnfetchableReferences(t *testing.T) {
	held := &fakeLocal{description: localDescription(localOutput("nvkectl", "c"), localOutput("nvke-controller", "a"))}
	var captured string
	capture := func(_ context.Context, dir string) (localCapture, error) {
		captured = dir
		return held, nil
	}
	planner := &fakePlanner{targets: []Target{e2eTarget("fake", "e2e/a")}}
	plan, err := generateLocal(context.Background(), localReq(writePlanners(t, oneE2E)), map[string]Planner{"fake": planner}, capture)
	if err != nil {
		t.Fatal(err)
	}
	if captured != ".nvkr/runs/attempt-1" {
		t.Fatalf("captured %q", captured)
	}
	if want := "local/attempt-1@" + digest("7"); plan.Release.Reference != want {
		t.Fatalf("release = %q, want %q", plan.Release.Reference, want)
	}
	got := planner.got.Release.Outputs
	if len(got) != 2 || got[0].Name != "nvkectl" || got[0].Digest != digest("c") || got[0].Platform != "linux/amd64" {
		t.Fatalf("outputs = %+v", got)
	}
	for _, o := range got {
		if !strings.HasPrefix(o.Reference, LocalPrefix+"attempt-1/"+o.Name+"@") {
			t.Errorf("%s reference = %q, want a local one that cannot be fetched", o.Name, o.Reference)
		}
	}
	if held.closed != 1 {
		t.Fatalf("the Run was closed %d times, want once", held.closed)
	}
}

func TestGenerateLocalFailsClosed(t *testing.T) {
	noAttempt := localDescription(localOutput("a", "a"))
	noAttempt.Attempt = ""
	noSubject := localDescription(localOutput("a", "a"))
	noSubject.ContentSubject = "nope"
	badOutput := localDescription(localOutput("a", "a"))
	badOutput.Outputs[0].Digest = "not-a-digest"

	good := func(t *testing.T) LocalRequest { return localReq(writePlanners(t, oneE2E)) }
	tests := []struct {
		name    string
		req     func(t *testing.T) LocalRequest
		held    *fakeLocal
		capErr  error
		want    e.Code
		reached bool
	}{
		{"the capture fails", good, nil, e.New(e.CodeVerifyFailed, "incomplete Run"), e.CodeVerifyFailed, true},
		{"the description fails", good, &fakeLocal{descErr: e.New(e.CodeGateUnmet, "closed")}, nil, e.CodeGateUnmet, true},
		{"no attempt", good, &fakeLocal{description: noAttempt}, nil, e.CodeProvenanceMissing, true},
		{"no content subject", good, &fakeLocal{description: noSubject}, nil, e.CodeProvenanceMissing, true},
		{"an output with no digest", good, &fakeLocal{description: badOutput}, nil, e.CodeProvenanceMissing, true},
		{"the Run will not close", good, &fakeLocal{description: localDescription(localOutput("a", "a")), closeErr: errors.New("held")}, nil, e.CodeGateUnmet, true},
		{"no Run directory", func(t *testing.T) LocalRequest {
			r := good(t)
			r.AttemptDir = ""
			return r
		}, nil, nil, e.CodeUsage, false},
		{"a planners directory with no configuration", func(t *testing.T) LocalRequest { return localReq(t.TempDir()) }, nil, nil, e.CodeUsage, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			capture := func(context.Context, string) (localCapture, error) {
				reached = true
				if tc.capErr != nil {
					return nil, tc.capErr
				}
				return tc.held, nil
			}
			planner := &fakePlanner{targets: []Target{e2eTarget("fake", "e2e/a")}}
			plan, err := generateLocal(context.Background(), tc.req(t), map[string]Planner{"fake": planner}, capture)
			if err == nil {
				t.Fatalf("a plan was made: %+v", plan)
			}
			if got := codeOf(t, err); got != tc.want {
				t.Fatalf("code = %s, want %s (%v)", got, tc.want, err)
			}
			if plan != nil {
				t.Fatal("a failure returned a plan")
			}
			if reached != tc.reached {
				t.Fatalf("capture reached = %v, want %v: admission must precede reading the Run", reached, tc.reached)
			}
			if tc.held != nil && tc.held.closed != 1 {
				t.Fatalf("the Run was closed %d times, want once", tc.held.closed)
			}
		})
	}
}

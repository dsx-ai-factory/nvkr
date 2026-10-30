// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const sourceOverrideRepository = "https://github.com/containerd/containerd"

type sourceOverrideFixture struct {
	before  *Plan
	git     func(...string) string
	commits map[string]string // tag or label -> commit
}

// newSourceOverrideFixture serves a local repository at the canonical HTTPS URL
// with release tags on main and one commit that no branch or tag contains.
func newSourceOverrideFixture(t *testing.T) sourceOverrideFixture {
	t.Helper()
	repository := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	run := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(context.Background(), "git", append([]string{"-C", repository, "-c", "user.name=Source Fixture",
			"-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	run("init", "--template=", "-q", "-b", "main")
	commits := map[string]string{}
	for _, tag := range []string{"v1.2.0", "v1.2.5", "v1.3.0", "v1.2.6-rc.1"} {
		run("commit", "--allow-empty", "-qm", tag)
		run("tag", tag)
		commits[tag] = run("rev-parse", "HEAD")
	}
	run("checkout", "-qb", "feature")
	run("commit", "--allow-empty", "-qm", "unmerged feature")
	commits["feature"] = run("rev-parse", "HEAD")
	run("checkout", "-q", "main")
	run("branch", "-D", "feature")
	run("config", "uploadpack.allowAnySHA1InWant", "true")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repository+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", sourceOverrideRepository)

	definition, environment := fixtureInputs("source-overrides", "cli-amd64", "ubuntu-24.04")
	var err error
	definition.Catalog.Base, err = filepath.Abs("../../../testdata/catalogs/kubernetes-layered")
	if err != nil {
		t.Fatal(err)
	}
	environment.RunsOn = ""
	environment.GitHubRunners = map[string]string{"linux/amd64": "ubuntu-24.04"}
	environment.GitHubDevelopment = true
	environment.Overridable = testOverridable(map[string]string{"containerd": sourceOverrideRepository, "kubernetes": "https://github.com/kubernetes/kubernetes"})
	environment.SourceAccess = map[string]authored.SourceAccess{sourceOverrideRepository: {Mirror: sourceOverrideRepository, CredentialRef: "github:SYNTHETIC_SOURCE_TOKEN"}}
	before, err := CompileDocuments(t.Context(), "definition.json", definition, "environment.json", environment, "v1.2.3 (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	return sourceOverrideFixture{before: before, git: run, commits: commits}
}

func (fixture sourceOverrideFixture) compile(t *testing.T, requests map[string]string) (*Plan, error) {
	t.Helper()
	before := fixture.before
	return CompileDevelopmentSourceOverrideDocuments(t.Context(), before.DefinitionPath, before.Definition, before.EnvironmentPath, before.Environment, before.GeneratorVersion, requests)
}

func (fixture sourceOverrideFixture) replay(t *testing.T, frozen map[string]SourceOverride) (*Plan, error) {
	t.Helper()
	before := fixture.before
	return ReplayDevelopmentSourceOverrideDocuments(t.Context(), before.DefinitionPath, before.Definition, before.EnvironmentPath, before.Environment, before.GeneratorVersion, frozen)
}

// testOverridable declares overridable sources from name to repository.
func testOverridable(sources map[string]string) *authored.Overridable {
	declared := make(map[string]authored.OverridableSource, len(sources))
	for name, repository := range sources {
		declared[name] = authored.OverridableSource{Repository: repository}
	}
	return &authored.Overridable{Sources: declared}
}

func TestDeclaredSourceOverridesFollowEnvironmentDeclarations(t *testing.T) {
	fixture := newSourceOverrideFixture(t)
	declared, err := DeclaredSourceOverrides(fixture.before)
	if err != nil {
		t.Fatal(err)
	}
	want := []DeclaredSourceOverride{
		{Name: "containerd", Repository: sourceOverrideRepository},
		{Name: "kubernetes", Repository: "https://github.com/kubernetes/kubernetes"},
	}
	if !reflect.DeepEqual(declared, want) {
		t.Fatalf("declared = %+v, want %+v", declared, want)
	}
	unopted := *fixture.before
	unopted.Environment = fixture.before.Environment.Clone()
	unopted.Environment.Overridable = nil
	if declared, err := DeclaredSourceOverrides(&unopted); err != nil || declared != nil {
		t.Fatalf("unopted Environment declared %+v %v", declared, err)
	}
	subset := *fixture.before
	subset.Environment = fixture.before.Environment.Clone()
	delete(subset.Environment.Overridable.Sources, "kubernetes")
	if declared, err := DeclaredSourceOverrides(&subset); err != nil || !reflect.DeepEqual(declared, want[:1]) {
		t.Fatalf("undeclared repository became overridable: %+v %v", declared, err)
	}
	unbuilt := *fixture.before
	unbuilt.Environment = fixture.before.Environment.Clone()
	unbuilt.Environment.Overridable.Sources["runc"] = authored.OverridableSource{Repository: "https://github.com/opencontainers/runc"}
	_, err = DeclaredSourceOverrides(&unbuilt)
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
}

func TestGitHubSourceOverrideRequestsFailClosed(t *testing.T) {
	fixture := newSourceOverrideFixture(t)
	commit := strings.Repeat("a", 40)
	for _, test := range []struct {
		name, raw string
		want      map[string]string
		code      e.Code
	}{
		{"absent", "", map[string]string{}, ""},
		{"null", "null", map[string]string{}, ""},
		{"all empty", `{"containerd":"","kubernetes":""}`, map[string]string{}, ""},
		{"constraint and commit", `{"containerd":"1.2.x","kubernetes":"` + commit + `"}`,
			map[string]string{sourceOverrideRepository: "1.2.x", "https://github.com/kubernetes/kubernetes": commit}, ""},
		{"unknown input", `{"source_commit":"` + commit + `"}`, nil, e.CodeUsage},
		{"non-string", `{"containerd":true}`, nil, e.CodeUsage},
		{"malformed request", `{"containerd":"main"}`, nil, e.CodeUsage},
		{"short commit", `{"containerd":"abc1234"}`, nil, e.CodeUsage},
		{"not an object", `["1.2.x"]`, nil, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := GitHubSourceOverrideRequests(fixture.before, test.raw)
			if test.code != "" {
				assertFactoryErrorCode(t, err, test.code)
				return
			}
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %v %v, want %v", got, err, test.want)
			}
		})
	}
	local := *fixture.before
	local.Environment = fixture.before.Environment.Clone()
	local.Environment.GitHubDevelopment = false
	_, err := GitHubSourceOverrideRequests(&local, `{"containerd":"1.2.x"}`)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
}

func TestAdmitSourceOverrideRequestsIsSharedIntake(t *testing.T) {
	const kubernetes = "https://github.com/kubernetes/kubernetes"
	commit := strings.Repeat("a", 40)
	declared := authored.Environment{Overridable: testOverridable(map[string]string{"containerd": sourceOverrideRepository, "kubernetes": kubernetes})}
	request := func(name, value string) SourceOverrideRequest {
		return SourceOverrideRequest{Name: name, Request: value}
	}
	for _, test := range []struct {
		name        string
		environment authored.Environment
		requests    []SourceOverrideRequest
		want        map[string]string
		code        e.Code
	}{
		{"none", declared, nil, map[string]string{}, ""},
		{"constraint and commit", declared, []SourceOverrideRequest{request("containerd", "1.2.x"), request("kubernetes", commit)},
			map[string]string{sourceOverrideRepository: "1.2.x", kubernetes: commit}, ""},
		{"empty request keeps pin", declared, []SourceOverrideRequest{request("containerd", "")}, map[string]string{}, ""},
		{"undeclared Environment", authored.Environment{}, []SourceOverrideRequest{request("containerd", "1.x")}, nil, e.CodeEngineCapability},
		{"undeclared name", declared, []SourceOverrideRequest{request("runc", "1.x")}, nil, e.CodeUsage},
		{"repository URL as name", declared, []SourceOverrideRequest{request(sourceOverrideRepository, "1.x")}, nil, e.CodeUsage},
		{"repeated name", declared, []SourceOverrideRequest{request("containerd", "1.x"), request("containerd", commit)}, nil, e.CodeUsage},
		{"malformed request", declared, []SourceOverrideRequest{request("containerd", "main")}, nil, e.CodeUsage},
		{"short commit", declared, []SourceOverrideRequest{request("containerd", "abc1234")}, nil, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := AdmitSourceOverrideRequests(test.environment, test.requests)
			if test.code != "" {
				assertFactoryErrorCode(t, err, test.code)
				return
			}
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %v %v, want %v", got, err, test.want)
			}
		})
	}
}

func TestSourceOverrideCompilationFreezesResolvedSelection(t *testing.T) {
	fixture := newSourceOverrideFixture(t)
	for _, test := range []struct {
		name, request, tag, commit string
	}{
		{"newest stable patch", "1.2.x", "v1.2.5", fixture.commits["v1.2.5"]},
		{"exact tag", "v1.2.0", "v1.2.0", fixture.commits["v1.2.0"]},
		{"newest stable", "1.x", "v1.3.0", fixture.commits["v1.3.0"]},
		{"unreferenced commit", fixture.commits["feature"], fixture.commits["feature"], fixture.commits["feature"]},
	} {
		t.Run(test.name, func(t *testing.T) {
			after, err := fixture.compile(t, map[string]string{sourceOverrideRepository: test.request})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]SourceOverride{sourceOverrideRepository: {Request: test.request, Tag: test.tag, Commit: test.commit}}
			if !reflect.DeepEqual(after.SourceOverrides, want) || after.DefinitionDigest != fixture.before.DefinitionDigest || after.EnvironmentDigest != fixture.before.EnvironmentDigest {
				t.Fatalf("frozen selection %+v, want %+v without authored identity changes", after.SourceOverrides, want)
			}
			matched := 0
			for index, build := range after.Builds {
				for name, got := range build.ResolvedComponents {
					original := fixture.before.Builds[index].ResolvedComponents[name]
					if strings.TrimSuffix(got.Repo, ".git") == sourceOverrideRepository {
						matched++
						if got.Commit != test.commit || got.Tag != test.tag || !reflect.DeepEqual(got.BuildCommand, original.BuildCommand) || got.ToolchainImage != original.ToolchainImage {
							t.Fatal("override changed more than the selected source revision")
						}
					} else if !reflect.DeepEqual(got, original) {
						t.Fatalf("unselected component %s changed", got.Name)
					}
				}
			}
			if matched == 0 {
				t.Fatal("override selected no producer")
			}
			replayed, err := fixture.replay(t, after.SourceOverrides)
			if err != nil {
				t.Fatal(err)
			}
			want1, _ := after.Digest()
			got1, _ := replayed.Digest()
			if want1 == "" || want1 != got1 {
				t.Fatal("replay did not reproduce the frozen Plan")
			}
			assertFactoryErrorCode(t, AdmitQualificationHandoff(after), e.CodeEngineCapability)
		})
	}
}

func TestSourceOverrideCompilationRejectsUnresolvableRequests(t *testing.T) {
	fixture := newSourceOverrideFixture(t)
	for _, test := range []struct {
		name     string
		requests map[string]string
		code     e.Code
	}{
		{"no requests", map[string]string{}, e.CodeEngineCapability},
		{"unselected repository", map[string]string{"https://github.com/opencontainers/runc": "1.x"}, e.CodeUsage},
		{"malformed request", map[string]string{sourceOverrideRepository: "latest"}, e.CodeUsage},
		{"no matching tag", map[string]string{sourceOverrideRepository: "2.x"}, e.CodeNoMatch},
		{"unknown commit", map[string]string{sourceOverrideRepository: strings.Repeat("e", 40)}, e.CodeFetchFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := fixture.compile(t, test.requests)
			assertFactoryErrorCode(t, err, test.code)
		})
	}
	undeclared := fixture.before.Environment.Clone()
	undeclared.Overridable = nil
	local := fixture.before.Environment.Clone()
	local.GitHubDevelopment = false
	for _, environment := range []authored.Environment{undeclared, local} {
		_, err := CompileDevelopmentSourceOverrideDocuments(t.Context(), fixture.before.DefinitionPath, fixture.before.Definition, fixture.before.EnvironmentPath, environment,
			fixture.before.GeneratorVersion, map[string]string{sourceOverrideRepository: "1.x"})
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	}
}

func TestSourceOverrideReplayRejectsMovedOrInconsistentSelection(t *testing.T) {
	fixture := newSourceOverrideFixture(t)
	after, err := fixture.compile(t, map[string]string{sourceOverrideRepository: "1.2.x"})
	if err != nil {
		t.Fatal(err)
	}
	frozen := after.SourceOverrides[sourceOverrideRepository]
	for _, test := range []struct {
		name string
		edit func(*SourceOverride)
		code e.Code
	}{
		{"tag outside constraint", func(o *SourceOverride) { o.Tag, o.Commit = "v1.3.0", fixture.commits["v1.3.0"] }, e.CodeUnpinnedSource},
		{"tag and commit disagree", func(o *SourceOverride) { o.Commit = fixture.commits["v1.2.0"] }, e.CodeUnverifiedRef},
		{"commit request with tag", func(o *SourceOverride) { o.Request = fixture.commits["v1.2.5"] }, e.CodeUnpinnedSource},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := frozen
			test.edit(&changed)
			_, err := fixture.replay(t, map[string]SourceOverride{sourceOverrideRepository: changed})
			assertFactoryErrorCode(t, err, test.code)
		})
	}
	t.Run("moved tag", func(t *testing.T) {
		fixture.git("tag", "-f", "v1.2.5", fixture.commits["v1.2.0"])
		_, err := fixture.replay(t, after.SourceOverrides)
		assertFactoryErrorCode(t, err, e.CodeUnverifiedRef)
	})
}

func TestSourceOverridePlanValidation(t *testing.T) {
	fixture := newSourceOverrideFixture(t)
	after, err := fixture.compile(t, map[string]string{sourceOverrideRepository: "1.2.x"})
	if err != nil {
		t.Fatal(err)
	}
	commit := fixture.commits["v1.2.5"]
	for _, test := range []struct {
		name string
		edit func(*Plan)
		code e.Code
	}{
		{"unopted Environment", func(p *Plan) { p.Environment.Overridable = nil }, e.CodeEngineCapability},
		{"with runtime source commit", func(p *Plan) { p.RuntimeSourceCommit = commit }, e.CodeEngineCapability},
		{"malformed request", func(p *Plan) {
			p.SourceOverrides[sourceOverrideRepository] = SourceOverride{Request: "main", Tag: "v1.2.5", Commit: commit}
		}, e.CodeUsage},
		{"short commit", func(p *Plan) {
			p.SourceOverrides[sourceOverrideRepository] = SourceOverride{Request: "1.2.x", Tag: "v1.2.5", Commit: commit[:12]}
		}, e.CodeUnpinnedSource},
		{"commit request with tag", func(p *Plan) {
			p.SourceOverrides[sourceOverrideRepository] = SourceOverride{Request: commit, Tag: "v1.2.5", Commit: commit}
		}, e.CodeUnpinnedSource},
		{"producer differs", func(p *Plan) {
			p.SourceOverrides[sourceOverrideRepository] = SourceOverride{Request: "1.2.x", Tag: "v1.2.0", Commit: fixture.commits["v1.2.0"]}
		}, e.CodeUnpinnedSource},
		{"unselected repository", func(p *Plan) {
			p.Environment.Overridable.Sources["runc"] = authored.OverridableSource{Repository: "https://github.com/opencontainers/runc"}
			p.SourceOverrides["https://github.com/opencontainers/runc"] = SourceOverride{Request: "1.x", Tag: "v1.2.5", Commit: commit}
		}, e.CodeUnpinnedSource},
		{"undeclared repository", func(p *Plan) { delete(p.Environment.Overridable.Sources, "containerd") }, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := *after
			copy.Environment = after.Environment.Clone()
			copy.SourceOverrides = map[string]SourceOverride{}
			for repository, override := range after.SourceOverrides {
				copy.SourceOverrides[repository] = override
			}
			test.edit(&copy)
			assertFactoryErrorCode(t, copy.Validate(), test.code)
		})
	}
}

func TestSourceOverridesKeepGeneratedWorkflowBytes(t *testing.T) {
	fixture := newSourceOverrideFixture(t)
	after, err := fixture.compile(t, map[string]string{sourceOverrideRepository: fixture.commits["feature"]})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := RenderGitHub(t.Context(), fixture.before)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := RenderGitHub(t.Context(), after)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedFileNames(baseline.Files), sortedFileNames(fresh.Files)) {
		t.Fatal("override changed generated member paths")
	}
	for _, member := range sortedFileNames(baseline.Files) {
		if member == baseline.PlanPath || member == githubActionDataPathFor(after) {
			continue
		}
		if !bytes.Equal(baseline.Files[member], fresh.Files[member]) {
			t.Fatalf("override changed executable member %s", member)
		}
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, fresh))
	found := false
	for id, job := range document.Jobs {
		if !strings.HasPrefix(id, "build-runner-") {
			continue
		}
		for _, step := range job.Steps {
			if step.ID != "validate-catalog" {
				continue
			}
			found = true
			if !strings.Contains(step.Run, `--source-overrides "$NVKR_SOURCE_OVERRIDES"`) || step.Env["NVKR_SOURCE_OVERRIDES"] != "${{ toJSON(github.event.inputs) }}" ||
				strings.Contains(step.Run, "${{") {
				t.Fatalf("bootstrap %s does not pass dispatch inputs as data: %+v", id, step)
			}
		}
	}
	if !found {
		t.Fatal("bootstrap lost catalog validation")
	}
	dispatch := string(baseline.Files[baseline.WorkflowPath])
	for _, input := range []string{"containerd", "kubernetes"} {
		if !strings.Contains(dispatch, "      "+input+":\n") {
			t.Fatalf("workflow omits dispatch input %s", input)
		}
	}
}

// localSourceOverrideEnvironmentFixture turns the fixture's development Environment
// into a local one with the same overridable sources: no generated dispatch
// interface and no hosted credentials.
func localSourceOverrideEnvironmentFixture(fixture sourceOverrideFixture) authored.Environment {
	environment := fixture.before.Environment.Clone()
	environment.GitHubDevelopment = false
	environment.GitHubRunners, environment.SourceAccess = nil, nil
	environment.RunsOn = "ubuntu-24.04"
	return environment
}

func TestLocalSourceOverrideCompilationUsesDeclaredSources(t *testing.T) {
	fixture := newSourceOverrideFixture(t)
	before := fixture.before
	environment := localSourceOverrideEnvironmentFixture(fixture)
	baseline, err := CompileDocuments(t.Context(), before.DefinitionPath, before.Definition, before.EnvironmentPath, environment, before.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, request, tag, commit string
	}{
		{"newest stable patch", "1.2.x", "v1.2.5", fixture.commits["v1.2.5"]},
		{"unreferenced commit", fixture.commits["feature"], fixture.commits["feature"], fixture.commits["feature"]},
	} {
		t.Run(test.name, func(t *testing.T) {
			after, err := CompileLocalSourceOverrideDocuments(t.Context(), before.DefinitionPath, before.Definition, before.EnvironmentPath, environment,
				before.GeneratorVersion, map[string]string{sourceOverrideRepository: test.request})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]SourceOverride{sourceOverrideRepository: {Request: test.request, Tag: test.tag, Commit: test.commit}}
			if !reflect.DeepEqual(after.SourceOverrides, want) || after.EnvironmentDigest != baseline.EnvironmentDigest {
				t.Fatalf("frozen selection %+v, want %+v without authored identity changes", after.SourceOverrides, want)
			}
			for index, build := range after.Builds {
				for name, got := range build.ResolvedComponents {
					original := baseline.Builds[index].ResolvedComponents[name]
					if strings.TrimSuffix(got.Repo, ".git") == sourceOverrideRepository {
						if got.Commit != test.commit || got.Tag != test.tag || !reflect.DeepEqual(got.BuildCommand, original.BuildCommand) {
							t.Fatal("local override changed more than the selected source revision")
						}
					} else if !reflect.DeepEqual(got, original) {
						t.Fatalf("unselected component %s changed", got.Name)
					}
				}
			}
			if err := AdmitLocalPlan(t.Context(), after); err != nil {
				t.Fatal("local override Plan failed local admission", err)
			}
			_, err = RenderGitHub(t.Context(), after)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			assertFactoryErrorCode(t, AdmitQualificationHandoff(after), e.CodeEngineCapability)
		})
	}
}

func TestLocalSourceOverrideCompilationFailsClosedBeforeFetch(t *testing.T) {
	fixture := newSourceOverrideFixture(t)
	before := fixture.before
	local := localSourceOverrideEnvironmentFixture(fixture)
	withAccess := local.Clone()
	withAccess.SourceAccess = before.Environment.SourceAccess
	undeclared := local.Clone()
	undeclared.Overridable = nil
	unknown := strings.Repeat("e", 40)
	for _, test := range []struct {
		name        string
		environment authored.Environment
		requests    map[string]string
		code        e.Code
	}{
		// An unknown commit would fail with CodeFetchFailed if it were fetched.
		{"development Environment", before.Environment, map[string]string{sourceOverrideRepository: unknown}, e.CodeEngineCapability},
		{"locally unsupported Environment", withAccess, map[string]string{sourceOverrideRepository: unknown}, e.CodeEngineCapability},
		{"undeclared Environment", undeclared, map[string]string{sourceOverrideRepository: unknown}, e.CodeEngineCapability},
		{"no requests", local, map[string]string{}, e.CodeEngineCapability},
		{"unselected repository", local, map[string]string{"https://github.com/opencontainers/runc": "1.x"}, e.CodeUsage},
		{"malformed request", local, map[string]string{sourceOverrideRepository: "latest"}, e.CodeUsage},
		{"unknown commit", local, map[string]string{sourceOverrideRepository: unknown}, e.CodeFetchFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := CompileLocalSourceOverrideDocuments(t.Context(), before.DefinitionPath, before.Definition, before.EnvironmentPath, test.environment,
				before.GeneratorVersion, test.requests)
			assertFactoryErrorCode(t, err, test.code)
		})
	}
}

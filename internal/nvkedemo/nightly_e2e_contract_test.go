// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func nightlyRuntimeRelease() lifecycle.Release {
	release := lifecycle.Release{}
	for _, role := range runtimeImages {
		digest := "sha256:" + strings.Repeat("a", 64)
		release.Artifacts = append(release.Artifacts, lifecycle.Artifact{Name: role + "-image-arm64", Kind: lifecycle.OutputOCI, Digest: digest, Reference: "registry.example:5000/team/" + role + "@" + digest})
	}
	return release
}

func TestNightlyRuntimeImageRolesPreserveExactRegistryAndDigest(t *testing.T) {
	release := nightlyRuntimeRelease()
	values, err := nightlyRuntimeValues(release)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	var walk func(map[string]any)
	walk = func(object map[string]any) {
		if repo, ok := object["repository"].(string); ok {
			combined := repo
			if registry, ok := object["registry"].(string); ok {
				combined = registry + "/" + repo
			}
			role := strings.TrimPrefix(combined, "registry.example:5000/team/")
			if role == combined {
				t.Fatalf("incorrect registry/repository projection: %s", combined)
			}
			seen[role]++
			digest := object["digest"]
			if tag, ok := object["tag"].(string); ok && tag != "" {
				digest = tag
			}
			if !strings.HasSuffix(digest.(string), "sha256:"+strings.Repeat("a", 64)) {
				t.Fatalf("unpinned %s: %v", role, object)
			}
		}
		for _, value := range object {
			if child, ok := value.(map[string]any); ok {
				walk(child)
			}
		}
	}
	for _, value := range values {
		walk(value.(map[string]any))
	}
	for _, role := range runtimeImages {
		if seen[role] == 0 {
			t.Fatalf("unmapped role %s", role)
		}
	}
	for _, test := range []struct {
		name   string
		change func(*lifecycle.Release)
	}{
		{"missing role", func(r *lifecycle.Release) { r.Artifacts = r.Artifacts[1:] }},
		{"duplicate role", func(r *lifecycle.Release) { r.Artifacts = append(r.Artifacts, r.Artifacts[0]) }},
		{"digest substitution", func(r *lifecycle.Release) { r.Artifacts[0].Digest = "sha256:" + strings.Repeat("b", 64) }},
		{"tag reference", func(r *lifecycle.Release) { r.Artifacts[0].Reference = "registry.example:5000/team/image:latest" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := nightlyRuntimeRelease()
			test.change(&changed)
			_, err := nightlyRuntimeValues(changed)
			assertNightlyCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func nightlyVersionPlan() *factory.Plan {
	return &factory.Plan{Builds: []factory.BuildPlan{{Name: "cwe", Graph: &ir.Graph{Nodes: []ir.Node{{Kind: ir.KindBuild, Component: "nvke-controller", Env: []ir.EnvVar{{Name: "BUILD_VERSION", Value: "producer-source-version"}}}}}}}}
}

func TestNightlyControllerVersionComesFromExactProducer(t *testing.T) {
	version, err := nightlyControllerVersion(nightlyVersionPlan(), "cwe")
	if err != nil || version != "producer-source-version" {
		t.Fatal(version, err)
	}
	for _, test := range []struct {
		name   string
		change func(*factory.Plan)
	}{
		{"missing build", func(p *factory.Plan) { p.Builds = nil }},
		{"duplicate build", func(p *factory.Plan) { p.Builds = append(p.Builds, p.Builds[0]) }},
		{"missing graph", func(p *factory.Plan) { p.Builds[0].Graph = nil }},
		{"wrong component", func(p *factory.Plan) { p.Builds[0].Graph.Nodes[0].Component = "other" }},
		{"duplicate node", func(p *factory.Plan) { g := p.Builds[0].Graph; g.Nodes = append(g.Nodes, g.Nodes[0]) }},
		{"missing binding", func(p *factory.Plan) { p.Builds[0].Graph.Nodes[0].Env = nil }},
		{"duplicate binding", func(p *factory.Plan) { n := &p.Builds[0].Graph.Nodes[0]; n.Env = append(n.Env, n.Env[0]) }},
		{"empty binding", func(p *factory.Plan) { p.Builds[0].Graph.Nodes[0].Env[0].Value = "" }},
		{"unresolved stamp", func(p *factory.Plan) { p.Builds[0].Graph.Nodes[0].Env[0].Value = "{{.Commit}}" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := nightlyVersionPlan()
			test.change(plan)
			_, err := nightlyControllerVersion(plan, "cwe")
			assertNightlyCode(t, err, e.CodeVerifyFailed)
		})
	}
	_, err = nightlyControllerVersion(nil, "cwe")
	assertNightlyCode(t, err, e.CodeVerifyFailed)
	plan := nightlyVersionPlan()
	plan.Builds[0].Name = "project-controller"
	if _, err := nightlyControllerVersion(plan, "project-controller"); err != nil {
		t.Fatal(err)
	}
}

func TestNightlyDeadlineReservesEveryCleanupStage(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name     string
		deadline time.Time
		work     time.Duration
		invalid  bool
	}{
		{"workflow330minutes", now.Add(330 * time.Minute), 295 * time.Minute, false},
		{"longer enclosing timeout caps work", now.Add(360 * time.Minute), nightlyMaximumWork, false},
		{"shorter enclosing timeout", now.Add(40 * time.Minute), 5 * time.Minute, false},
		{"no test timeout", time.Time{}, nightlyMaximumWork, false},
		{"cleanup would consume entire deadline", now.Add(nightlyCleanupReserve), 0, true},
		{"expired deadline", now.Add(-time.Second), 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget, err := nightlyTimeBudget(now, test.deadline)
			if test.invalid {
				assertNightlyCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil || budget.WorkDeadline.Sub(now) != test.work {
				t.Fatal(budget, err)
			}
			cleanup := nightlySuiteGrace + nightlyVerdictGrace + nightlyDiskGrace + nightlyNetworkGrace + nightlyReportGrace
			if budget.WorkDeadline.Add(cleanup).After(budget.CleanupDeadline) || !test.deadline.IsZero() && !budget.CleanupDeadline.Before(test.deadline) {
				t.Fatal("work, process join and cleanup exceed enclosing deadline")
			}
			parent, cancel := context.WithCancel(t.Context())
			cancel()
			bounded, stop := budget.cleanupContext(parent, time.Minute)
			defer stop()
			deadline, ok := bounded.Deadline()
			if !ok || deadline.After(budget.CleanupDeadline) || bounded.Err() != nil {
				t.Fatal("cleanup lost its explicit budget or inherited work cancellation")
			}
		})
	}
}

func TestNightlyOptInCannotSilentlySkipRuntimeJob(t *testing.T) {
	for _, test := range []struct {
		enable, actions, profile string
		enabled, refused         bool
	}{
		{"", "", "", false, false},
		{"", "true", "", false, false},
		{"", "true", "selected-profile", false, true},
		{"0", "true", "selected-profile", false, true},
		{"1", "true", "selected-profile", true, false},
	} {
		enabled, err := nightlyTestAdmission(test.enable, test.actions, test.profile)
		if enabled != test.enabled {
			t.Fatal(test, enabled)
		}
		if test.refused {
			assertNightlyCode(t, err, e.CodeUsage)
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

func TestNightlySiteImagesRequireUniqueImmutableOCIOutputs(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	good := lifecycle.Artifact{Name: "project-controller", Kind: lifecycle.OutputOCI, Digest: digest, Reference: "registry.example/team/controller@" + digest}
	for _, test := range []struct {
		name      string
		artifacts []lifecycle.Artifact
		output    string
		valid     bool
	}{
		{"exact", []lifecycle.Artifact{good}, good.Name, true},
		{"missing", nil, good.Name, false},
		{"empty output", []lifecycle.Artifact{good}, "", false},
		{"duplicate", []lifecycle.Artifact{good, good}, good.Name, false},
		{"wrong kind", []lifecycle.Artifact{{Name: good.Name, Kind: lifecycle.OutputHelm, Digest: digest, Reference: good.Reference}}, good.Name, false},
		{"digest substitution", []lifecycle.Artifact{{Name: good.Name, Kind: good.Kind, Digest: "sha256:" + strings.Repeat("b", 64), Reference: good.Reference}}, good.Name, false},
		{"tag locator", []lifecycle.Artifact{{Name: good.Name, Kind: good.Kind, Digest: digest, Reference: "registry.example/team/controller:latest"}}, good.Name, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			image, err := nightlyNamedImage(lifecycle.Release{Artifacts: test.artifacts}, test.output)
			if test.valid {
				if err != nil || image.Name != good.Name || image.Reference != good.Reference || image.Digest != good.Digest {
					t.Fatal(image, err)
				}
				return
			}
			assertNightlyCode(t, err, e.CodeVerifyFailed)
		})
	}
	if nightlyControllerOutput(NightlyProfile{}) != "cwe" || nightlyControllerOutput(NightlyProfile{ControllerOutput: "project-controller"}) != "project-controller" {
		t.Fatal("explicit controller selection or legacy default lost")
	}
}

func TestNightlySuiteUsesAdmittedInterpreterAndActualEntrypoint(t *testing.T) {
	source, admitted, ambient := t.TempDir(), t.TempDir(), t.TempDir()
	for _, directory := range []string{admitted, ambient} {
		if err := os.WriteFile(filepath.Join(directory, "bash"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", ambient)
	runner := Runner{Env: []string{"PATH=" + admitted}}
	entry := filepath.Join(source, "controller/hack/ci/nico-vms-e2e.sh")
	if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(entry), "nico-vms-candidate.sh"), []byte("candidate only"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := nightlySuiteCommand(t.Context(), runner, source)
	assertNightlyCode(t, err, e.CodeVerifyFailed)
	if err := os.WriteFile(entry, []byte("#!/bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command, err := nightlySuiteCommand(t.Context(), runner, source)
	if err != nil || command.Path != filepath.Join(admitted, "bash") || command.Args[1] != entry {
		t.Fatal(command, err)
	}
	_, err = nightlySuiteCommand(t.Context(), Runner{Env: []string{"PATH=" + t.TempDir()}}, source)
	assertNightlyCode(t, err, e.CodeEngineCapability)
	var missingContext context.Context
	_, err = nightlySuiteCommand(missingContext, runner, source)
	assertNightlyCode(t, err, e.CodeUsage)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = nightlySuiteCommand(ctx, runner, source)
	assertNightlyCode(t, err, e.CodeTimeout)
}

func TestNightlySourceAdmitsBothReviewedEntrypoints(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			source, work := privateTestDir(t), privateTestDir(t)
			git := func(args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, "git", append([]string{"-C", source}, args...)...)
				command.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test"}
				raw, err := command.CombinedOutput()
				if err != nil {
					t.Fatal(err, string(raw))
				}
				return strings.TrimSpace(string(raw))
			}
			git("init", "-q")
			directory := filepath.Join(source, "controller/hack/ci")
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "nico-vms-candidate.sh"), []byte("candidate"), 0600); err != nil {
				t.Fatal(err)
			}
			if present {
				if err := os.WriteFile(filepath.Join(directory, "nico-vms-e2e.sh"), []byte("e2e"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("add", ".")
			git("-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
			err := nightlySource(t.Context(), Runner{Work: work, Env: NativeEnvironment()}, source, git("rev-parse", "HEAD"))
			if present {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertNightlyCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestNightlySignalsCancelTheOwnedTestContext(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range []os.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestNightlySignalContextHelper$", "-test.timeout=8s")
			command.Env = []string{"NVKR_NIGHTLY_TEST_SIGNAL=1"}
			if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
				t.Fatal(err)
			}
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); _ = ownedcmd.KillDescendants(command) }()
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "nightly-signal-ready" {
				cancel()
				_ = command.Wait()
				t.Fatal("signal helper did not start", scanner.Err())
			}
			if err := command.Process.Signal(signal); err != nil {
				cancel()
				_ = command.Wait()
				t.Fatal(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatal("signal did not reach orderly context cancellation", err)
			}
		})
	}
}

func TestNightlySignalContextHelper(t *testing.T) {
	if os.Getenv("NVKR_NIGHTLY_TEST_SIGNAL") != "1" {
		t.Skip("owned signal subprocess fixture")
	}
	ctx, cancel := nightlySignalContext(t.Context(), time.Now().Add(6*time.Second))
	defer cancel()
	fmt.Println("nightly-signal-ready")
	<-ctx.Done()
	if ctx.Err() != context.Canceled {
		t.Fatal("signal was not received", ctx.Err())
	}
}

func TestNightlyCandidateVerdictSeparatesTestsFromJoinedCleanup(t *testing.T) {
	profile := NightlyProfile{OSProfile: "os-profile", KubernetesVersion: "1.34.2"}
	for _, test := range []struct {
		name, raw   string
		clean, pass bool
		code        e.Code
	}{
		{"passed", `{"cleanup_exit":0,"test_exit":0,"entry_exit":0,"profile":"os-profile","kubernetes":"1.34.2"}`, true, true, ""},
		{"test failed cleanup joined", `{"cleanup_exit":0,"test_exit":1,"entry_exit":1,"profile":"os-profile","kubernetes":"1.34.2"}`, true, false, e.CodeGateUnmet},
		{"cleanup failed", `{"cleanup_exit":1,"test_exit":0,"entry_exit":0,"profile":"os-profile","kubernetes":"1.34.2"}`, false, false, e.CodeGateUnmet},
		{"cleanup missing", `{"test_exit":0,"entry_exit":0,"profile":"os-profile","kubernetes":"1.34.2"}`, false, false, e.CodeGateUnmet},
		{"null cleanup", `{"cleanup_exit":null,"test_exit":0,"entry_exit":0,"profile":"os-profile","kubernetes":"1.34.2"}`, false, false, e.CodeGateUnmet},
		{"wrong profile", `{"cleanup_exit":0,"test_exit":0,"entry_exit":0,"profile":"other","kubernetes":"1.34.2"}`, false, false, e.CodeGateUnmet},
		{"wrong kubernetes", `{"cleanup_exit":0,"test_exit":0,"entry_exit":0,"profile":"os-profile","kubernetes":"1.35.0"}`, false, false, e.CodeGateUnmet},
		{"duplicate verdict", `{"cleanup_exit":0,"cleanup_exit":1}`, false, false, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			clean, err := nightlyCandidateVerdict([]byte(test.raw), profile)
			if clean != test.clean {
				t.Fatalf("cleanup=%v; want %v", clean, test.clean)
			}
			if test.pass {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertNightlyCode(t, err, test.code)
			}
		})
	}
}

func TestNightlyPreparationCachesArePrivateAndCredentialsExcluded(t *testing.T) {
	t.Setenv("GH_TOKEN", "must-not-propagate")
	t.Setenv("GITHUB_TOKEN", "must-not-propagate")
	work := t.TempDir()
	binding := map[string]string{"HOME": "/home/runtime", "XDG_RUNTIME_DIR": "/run/user/123"}
	env, err := nightlyPreparationEnvironment(NativeEnvironment(), binding, work)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"HOME": binding["HOME"], "XDG_RUNTIME_DIR": binding["XDG_RUNTIME_DIR"], "GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off", "GOFLAGS": "-mod=readonly", "GH_TOKEN": "", "GITHUB_TOKEN": ""} {
		if got := environmentValue(env, key); got != want {
			t.Fatalf("%s=%q; want %q", key, got, want)
		}
	}
	for _, name := range []string{"go-cache", "go-modules", "go-path"} {
		info, err := os.Stat(filepath.Join(work, name))
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal(name, info, err)
		}
	}
	_, err = nightlyPreparationEnvironment(NativeEnvironment(), binding, work)
	assertNightlyCode(t, err, e.CodeImmutableViolation)
	_, err = nightlyPreparationEnvironment(nil, map[string]string{}, t.TempDir())
	assertNightlyCode(t, err, e.CodeEngineCapability)
}

func TestNightlyPublicDiagnosticsRequireMarkerAndAllowlist(t *testing.T) {
	for _, mode := range []string{"absent", "valid", "marker symlink", "report symlink"} {
		t.Run(mode, func(t *testing.T) {
			suite, public := t.TempDir(), t.TempDir()
			for name, data := range map[string]string{"junit.xml": "<testsuite/>", "candidate-outcome.json": "{}", "private-log": "secret"} {
				if err := os.WriteFile(filepath.Join(suite, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(suite, "SAFE_TO_UPLOAD")
			if mode == "marker symlink" {
				if err := os.Symlink(filepath.Join(suite, "private-log"), marker); err != nil {
					t.Fatal(err)
				}
			} else if mode != "absent" {
				if err := os.WriteFile(marker, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "report symlink" {
				if err := os.Rename(filepath.Join(suite, "junit.xml"), filepath.Join(suite, "real.xml")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(suite, "real.xml"), filepath.Join(suite, "junit.xml")); err != nil {
					t.Fatal(err)
				}
			}
			err := nightlyPublicDiagnostics(t.Context(), suite, public)
			if strings.Contains(mode, "symlink") {
				assertNightlyCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(public)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "absent" {
				if len(entries) != 0 {
					t.Fatal(entries)
				}
				return
			}
			if len(entries) != 2 {
				t.Fatal(entries)
			}
			for _, entry := range entries {
				if entry.Name() != "junit.xml" && entry.Name() != "candidate-outcome.json" {
					t.Fatal(entry.Name())
				}
			}
			var verdict map[string]any
			raw, err := os.ReadFile(filepath.Join(public, "candidate-outcome.json"))
			if err != nil || json.Unmarshal(raw, &verdict) != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNightlyBundleRolesFailClosed(t *testing.T) {
	bundle := delivery.BinaryBundle{Entries: []delivery.BinaryEntry{{Name: "first", InstallPath: "/a/kind"}, {Name: "second", InstallPath: "/b/kind"}}}
	_, err := nightlyBundleEntries(bundle, t.TempDir())
	assertNightlyCode(t, err, e.CodeVerifyFailed)
	bundle.Entries = []delivery.BinaryEntry{{Name: "../kind", InstallPath: "/bin/kind"}}
	_, err = nightlyBundleEntries(bundle, t.TempDir())
	assertNightlyCode(t, err, e.CodeVerifyFailed)
}

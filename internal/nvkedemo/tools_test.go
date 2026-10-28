// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/releaseasset"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

func TestNativeToolSelectionKeepsExplicitOSBundles(t *testing.T) {
	for _, tc := range []struct{ platform, bundle string }{
		{"darwin/arm64", "native-tools-darwin-arm64"}, {"linux/arm64", "native-tools-linux-arm64"},
		{"linux/amd64", ""}, {"windows/arm64", ""}, {"", ""},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			definition, environment, bundle, members, err := nativeToolSelection(tc.platform)
			if tc.bundle == "" {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability || definition != "" || environment != "" || members != nil {
					t.Fatalf("unsupported host selected outputs: %v", err)
				}
				return
			}
			if err != nil || bundle != tc.bundle || !strings.HasSuffix(definition, ".json") || environment == "" || len(members) == 0 {
				t.Fatalf("selection %q, %q, %v", definition, bundle, err)
			}
			if tc.platform == "darwin/arm64" {
				if definition != "inputs/definitions/demo/native-mac/definition-tools.json" || len(members) != 12 || !slices.Contains(members, "nvke-installer-sources") || !slices.Contains(members, "nerdctl") {
					t.Fatalf("Mac dependency preparation omitted source tools: %s %v", definition, members)
				}
			}
		})
	}
}

func TestNativeMacToolsSelectCompatibleVMNetClient(t *testing.T) {
	_, _, _, members, err := nativeToolSelection("darwin/arm64")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(members, "vmnet-client") || slices.Contains(members, "vmnet-run") {
		t.Fatal("NICoSim requires the upstream vmnet-client command and its --unprivileged option")
	}
	directory := filepath.Join("..", "..", "inputs", "catalogs", "tools", "native")
	lock, err := catalog.LoadLockContext(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vmnet-client", "vmnet-helper"} {
		t.Run(name, func(t *testing.T) {
			pin := lock.Pins[name]
			if pin.Tag != "v0.10.0" || pin.Commit != "c593b89a2e93e92cfe7c5b1189e1eeb2c7977554" || pin.Tool == nil {
				t.Fatal("the helper pair must retain the reviewed pre-v0.11 command contract")
			}
			if pin.Tool.ExecutablePath != "opt/vmnet-helper/bin/"+name || pin.Tool.ArchiveDigest != "sha256:bcf37a2631878a24fd02e2bf812b7387d4c4ff0bcee27c3f4be410c040896f8b" {
				t.Fatal("vmnet tools must select their genuine upstream members from one release archive")
			}
		})
	}
}

// An explicit local release archive exercises the actual upstream command parser
// without starting vmnet, launching a guest or installing anything on the host.
func TestNativeMacVMNetReleaseCommandContract(t *testing.T) {
	archive := os.Getenv("NVKR_VMNET_RELEASE_ARCHIVE")
	if archive == "" {
		t.Skip("requires the reviewed local upstream vmnet release archive")
	}
	lock, err := catalog.LoadLockContext(t.Context(), filepath.Join("..", "..", "inputs", "catalogs", "tools", "native"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := boundedRegularRead(archive, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vmnet-client", "vmnet-helper"} {
		t.Run(name, func(t *testing.T) {
			pin := lock.Pins[name].Tool
			if pin == nil || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != pin.ArchiveDigest {
				t.Fatal("supplied archive differs from the selected release")
			}
			member, err := releaseasset.Select(t.Context(), bytes.NewReader(raw), int64(len(raw)), pin.Format, pin.ExecutablePath, releaseasset.ToolLimits())
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("sha256:%x", sha256.Sum256(member)) != pin.ExecutableDigest {
				t.Fatal("selected executable differs from its locked bytes")
			}
			path := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(path, member, 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			help, err := exec.CommandContext(ctx, path, "--help").CombinedOutput()
			if err != nil {
				t.Fatalf("actual upstream help: %v: %s", err, help)
			}
			for _, flag := range []string{"--operation-mode", "--help"} {
				if !bytes.Contains(help, []byte(flag)) {
					t.Fatalf("upstream %s omits %s", name, flag)
				}
			}
			if name == "vmnet-client" && !bytes.Contains(help, []byte("--unprivileged")) {
				t.Fatal("selected NICoSim requires the upstream --unprivileged option")
			}
		})
	}
}

func TestNativeToolMembershipIncludesInstallerArchive(t *testing.T) {
	_, _, _, members, err := nativeToolSelection("darwin/arm64")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"complete", "missing installer", "unexpected file", "executable installer", "nonexecutable command", "symlink installer"} {
		t.Run(name, func(t *testing.T) {
			directory := privateTestDir(t)
			for _, member := range members {
				if name == "missing installer" && member == "nvke-installer-sources" {
					continue
				}
				mode := os.FileMode(0700)
				if member == "nvke-installer-sources" {
					mode = 0644
					if name == "executable installer" {
						mode = 0755
					}
					if name == "symlink installer" {
						if err := os.Symlink("nvkectl", filepath.Join(directory, member)); err != nil {
							t.Fatal(err)
						}
						continue
					}
				} else if name == "nonexecutable command" && member == "nvkectl" {
					mode = 0644
				}
				if err := os.WriteFile(filepath.Join(directory, member), []byte("fixture"), mode); err != nil {
					t.Fatal(err)
				}
			}
			if name == "unexpected file" {
				if err := os.WriteFile(filepath.Join(directory, "unexpected"), []byte("fixture"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			err := verifyToolMembers(directory, members)
			if (err == nil) != (name == "complete") {
				t.Fatalf("membership check: %v", err)
			}
		})
	}
}

func TestToolPreparationRejectsBeforeWorkspaceWork(t *testing.T) {
	for _, tc := range []struct {
		name, platform, attempt string
		jobs                    int
		code                    nvkrerr.Code
	}{
		{"unsupported", "windows/arm64", "test", 1, nvkrerr.CodeEngineCapability},
		{"empty attempt", "darwin/arm64", "", 1, nvkrerr.CodeUsage},
		{"unsafe attempt", "darwin/arm64", "../test", 1, nvkrerr.CodeUsage},
		{"jobs", "darwin/arm64", "test", 0, nvkrerr.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := prepareTools(t.Context(), ToolConfig{Attempt: tc.attempt, Jobs: tc.jobs, Repository: "absent", Out: "absent"}, tc.platform)
			var coded *nvkrerr.Error
			if out != "" || !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("admission %q, %v", out, err)
			}
		})
	}
}

func TestNativeToolBuildRetainsRunWithoutExtracting(t *testing.T) {
	buildDockerFixture(t, "")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	for _, tc := range []struct {
		name, corrupt                                    string
		planExit, runExit                                int
		failed, missing, recordOnly, consume, uncaptured bool
		code                                             nvkrerr.Code
	}{
		{name: "completed Run"},
		{name: "graph rejected", planExit: 23},
		{name: "producer failed", runExit: 24},
		{name: "failed Run record", failed: true, code: nvkrerr.CodeVerifyFailed},
		{name: "missing companion", missing: true, code: nvkrerr.CodeVerifyFailed},
		{name: "record without payloads", recordOnly: true},
		{name: "uncaptured Run", uncaptured: true, code: nvkrerr.CodeVerifyFailed},
		{name: "corrupted host payload", corrupt: "native-tools-linux-arm64"},
		{name: "corrupted companion payload", corrupt: "site-startup-tools-arm64"},
		{name: "standalone still extracts", consume: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := privateTestDir(t)
			definition, environment, bundle, _, err := nativeToolSelection("linux/arm64")
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{definition, environment} {
				path = filepath.Join(repo, path)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				writeTenantFixture(t, path, []byte("{}"), 0600)
			}
			plan, inputs, err := factory.CaptureCompileFiles(t.Context(), filepath.Join("..", ".."), definition, environment, "native-tool-fixture")
			if err != nil {
				t.Fatal(err)
			}
			observations := make(map[string]lifecycle.Artifact)
			packages := filepath.Join(repo, "bundle-fixture")
			if !tc.recordOnly {
				if err := os.Mkdir(packages, 0700); err != nil {
					t.Fatal(err)
				}
				source, _, _ := hostToolViewFixture(t, "linux/arm64", true)
				if err := os.Rename(source, filepath.Join(packages, bundle)); err != nil {
					t.Fatal(err)
				}
				member := filepath.Join(repo, "site-tool")
				writeTenantFixture(t, member, []byte("exact site tool"), 0700)
				_, err := delivery.BuildBinaryBundle(t.Context(), filepath.Join(packages, "site-startup-tools-arm64"), delivery.BinaryBundleRequest{
					OS: "linux", Arch: "arm64", Purpose: "site-startup-tools-arm64", Epoch: 1,
					Files: []delivery.BinaryFileInput{
						{Name: "nerdctl", Path: member, InstallPath: "/usr/local/bin/nerdctl"},
						{Name: nativeIdentityHelper, Path: member, InstallPath: "/usr/local/bin/" + nativeIdentityHelper},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{bundle, "site-startup-tools-arm64"} {
				if !tc.recordOnly {
					root := filepath.Join(packages, name)
					raw, err := os.ReadFile(filepath.Join(root, "index.json"))
					if err != nil {
						t.Fatal(err)
					}
					var index v1.IndexManifest
					if err := json.Unmarshal(raw, &index); err != nil || len(index.Manifests) != 1 {
						t.Fatalf("bundle fixture index: %v", err)
					}
					manifest := index.Manifests[0].Digest
					observations[name] = lifecycle.Artifact{Digest: manifest.String()}
					if tc.corrupt == name {
						blob := filepath.Join(root, "blobs", manifest.Algorithm, manifest.Hex)
						if err := os.Chmod(blob, 0600); err != nil {
							t.Fatal(err)
						}
						writeTenantFixture(t, blob, []byte("corrupted payload"), 0600)
					}
				}
			}
			fixture, calls := filepath.Join(repo, "run-fixture", "fresh"), filepath.Join(repo, "calls")
			record := writeCapturedRunFixture(t, fixture, plan, inputs, observations)
			if tc.failed {
				record.Status = lifecycle.RunFailed
			}
			if tc.missing {
				record.Observed = record.Observed[:1]
			}
			writeCanonicalRunFixture(t, filepath.Join(fixture, "lifecycle.json"), record)
			if tc.uncaptured {
				path := filepath.Join(fixture, "compile-inputs.json")
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
			}
			nvkr := filepath.Join(repo, "nvkr")
			endpoint := "unix:///var/run/docker.sock"
			if runtime.GOOS == "darwin" {
				endpoint = "unix:///private/tmp/nvkr-build-fixture.sock"
			}
			writeTenantFixture(t, nvkr, []byte(fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$1:$2" >> %s
test "$DOCKER_HOST" = %s
test -z "${DOCKER_CONTEXT-}${DOCKER_CONFIG-}"
case "$1:$2" in
 run:plan) test %d = 0 || exit %d; cat %s ;;
 run:local)
   captured=false
   for arg do if test "$arg" = --capture-inputs-root=.; then captured=true; fi; done
   "$captured"
   test %d = 0 || exit %d
   mkdir -p .nvkr/native-tools/runs
   cp -R %s .nvkr/native-tools/runs/fresh
   if test -d %s; then cp -R %s .nvkr/native-tools/runs/fresh/packages; fi ;;
 *) exit 25 ;;
esac
`, operatorQuote(calls), operatorQuote(endpoint), tc.planExit, tc.planExit, operatorQuote(filepath.Join(fixture, ".runner/plan.json")), tc.runExit, tc.runExit, operatorQuote(fixture), operatorQuote(packages), operatorQuote(packages))), 0700)
			progress := &recordedProgress{}
			cfg := ToolConfig{Repository: repo, NVKR: nvkr, Go: "/usr/bin/true", Attempt: "fresh", Jobs: 2, Out: filepath.Join(repo, "work"), Progress: progress}
			var got string
			if tc.consume {
				got, err = prepareTools(t.Context(), cfg, "linux/arm64")
				// This fixture has real bundles and captured compiler inputs,
				// but its installer archive contains synthetic payload bytes.
				// Standalone preparation must still publish and extract them before
				// rejecting that invalid source archive.
				if got != "" || err == nil {
					t.Fatalf("standalone accepted synthetic installer: %q %v", got, err)
				}
				for _, path := range []string{"publication/index.json", "bin/kubectl", "site/bin/" + nativeIdentityHelper} {
					if _, err := os.Stat(filepath.Join(cfg.Out, path)); err != nil {
						t.Fatalf("standalone no longer materializes verified tools: %s %v", path, err)
					}
				}
			} else {
				got, err = buildTools(t.Context(), cfg, "linux/arm64")
				success := tc.planExit == 0 && tc.runExit == 0 && !tc.failed && !tc.missing && !tc.recordOnly && !tc.uncaptured && tc.corrupt == ""
				wantLast := LaneFailed
				if success {
					wantLast = LaneFinished
					if want := filepath.Join(repo, ".nvkr/native-tools/runs/fresh"); err != nil || got != want {
						t.Fatalf("completed Run=%q, want %q: %v", got, want, err)
					}
				} else {
					var coded *nvkrerr.Error
					wantCode := tc.code
					if wantCode == "" {
						wantCode = nvkrerr.CodePreflightFailed
					}
					if got != "" || !errors.As(err, &coded) || coded.Code != wantCode {
						t.Fatalf("failed producer yielded a Run: %q %v", got, err)
					}
				}
				last := progress.events[len(progress.events)-1]
				if last.Name != "artifacts" || last.Kind != wantLast {
					t.Fatalf("producer outcome was not reported: %+v", progress.events)
				}
				for _, name := range []string{"publication", "bin", "site", "installer"} {
					if _, err := os.Stat(filepath.Join(cfg.Out, name)); !os.IsNotExist(err) {
						t.Fatalf("tool producer extracted unused %s: %v", name, err)
					}
				}
			}
			wantCalls := "run:plan\n"
			if tc.planExit == 0 {
				wantCalls += "run:local\n"
			}
			if raw, err := os.ReadFile(calls); err != nil || string(raw) != wantCalls {
				t.Fatalf("producer order=%q, want %q: %v", raw, wantCalls, err)
			}
			selected := slices.IndexFunc(progress.events, func(event ProgressEvent) bool { return event.Kind == BuildPlanSelected })
			started := slices.IndexFunc(progress.events, func(event ProgressEvent) bool {
				return event.Kind == ProcessStarted && event.Name == "native-tools-run"
			})
			if tc.planExit == 0 {
				if selected < 0 || started <= selected {
					t.Fatal("native tools started before presenting their own selected Plan")
				}
				event := progress.events[selected]
				if event.Name != "native-tools" || event.BuildPlan == nil || event.BuildGraph == nil ||
					event.BuildPlan.PlanDigest != record.PlanDigest || event.BuildGraph.PlanDigest != record.PlanDigest ||
					len(event.BuildPlan.Outputs) != len(plan.LifecycleOutputs) {
					t.Fatal("native tool display inherited a different Run's Plan")
				}
			} else if selected >= 0 || started >= 0 {
				t.Fatal("refused native tool admission presented or started a Run")
			}
			if tc.planExit != 0 {
				if _, err := os.Stat(cfg.Out); !os.IsNotExist(err) {
					t.Fatalf("rejected graph created consumer workspace: %v", err)
				}
			}
		})
	}
}

func TestSiteStartupToolsAreSeparateFromHostExecutables(t *testing.T) {
	for _, platform := range []string{"darwin/arm64", "linux/arm64", "windows/arm64"} {
		t.Run(platform, func(t *testing.T) {
			bundles := siteStartupToolBundles(platform)
			if platform != "darwin/arm64" && platform != "linux/arm64" {
				if len(bundles) != 0 {
					t.Fatal("other hosts unexpectedly selected Mac site-startup companions")
				}
				return
			}
			if len(bundles) != 1 || bundles[0].name != "site-startup-tools-arm64" || bundles[0].directory != "bin" {
				t.Fatal("guest preparation must select only its runtime bundle")
			}
			if !slices.Contains(bundles[0].members, "nerdctl") || slices.Contains(bundles[0].members, "nvkectl") || slices.Contains(bundles[0].members, "kind") {
				t.Fatal("guest dependencies must contain its runtime and exclude the host CLI")
			}
		})
	}
}

// The complete selection is the authored owner of both tool bundles.
func TestNativeToolDefinitionsMatchHostAndSiteSelections(t *testing.T) {
	for _, platform := range []string{"linux/arm64", "darwin/arm64"} {
		t.Run(platform, func(t *testing.T) {
			path, _, hostBundle, members, err := nativeToolSelection(platform)
			if err != nil {
				t.Fatal(err)
			}
			complete, err := definition.LoadDefinition(t.Context(), filepath.Join("..", "..", path))
			if err != nil {
				t.Fatal(err)
			}
			if len(complete.Outputs) != 2 {
				t.Fatalf("unexpected dependency bundles: %v", complete.Outputs)
			}
			if complete.Catalog.Base != "../../../catalogs/tools/native" {
				t.Fatal("tools lost their shared catalog owner")
			}
			siteBundles := siteStartupToolBundles(platform)
			if len(siteBundles) != 1 {
				t.Fatal("native site requires one owner-selected companion bundle")
			}
			expected := map[string]definition.DefinitionOutput{
				hostBundle:                 {Name: hostBundle, Platform: platform, Format: definition.OutputExecutable, Components: slices.Clone(members)},
				"site-startup-tools-arm64": {Name: "site-startup-tools-arm64", Platform: "linux/arm64", Format: definition.OutputExecutable, Components: slices.Clone(siteBundles[0].members)},
			}
			if platform == "linux/arm64" {
				for i, member := range expected[hostBundle].Components {
					if slices.Contains([]string{"clusterctl", "helm", "kind", "kubectl", "oras"}, member) {
						expected[hostBundle].Components[i] += "-linux"
					}
				}
			}
			for _, output := range complete.Outputs {
				if !reflect.DeepEqual(output, expected[output.Name]) {
					t.Fatal("tool bundle differs from selected host or site members", output.Name)
				}
				delete(expected, output.Name)
			}
			if len(expected) != 0 {
				t.Fatal("complete selection omitted required bundles", expected)
			}
		})
	}
}

func TestNativeToolsRejectDockerEndpointBeforeOutput(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Mac build endpoint admission")
	}
	for _, tc := range []struct {
		name, body string
		timeout    bool
		code       nvkrerr.Code
	}{
		{"remote", `case "$2" in show) echo fixture-selected;; inspect) echo tcp://foreign:2375;; esac`, false, nvkrerr.CodeEngineCapability},
		{"empty", `case "$2" in show) echo fixture-selected;; inspect) exit 0;; esac`, false, nvkrerr.CodeEngineCapability},
		{"failed", "exit 19", false, nvkrerr.CodeEngineCapability},
		{"deadline", `case "$2" in show) echo fixture-selected;; inspect) exec /bin/sleep 30;; esac`, true, nvkrerr.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_COUNT", "0")
			buildDockerFixture(t, tc.body)
			repo := privateTestDir(t)
			definition, environment, _, _, err := nativeToolSelection("darwin/arm64")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{definition, environment} {
				path := filepath.Join(repo, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				writeTenantFixture(t, path, []byte("{}"), 0600)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.timeout {
				ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
			}
			out := filepath.Join(repo, "work")
			got, err := buildTools(ctx, ToolConfig{Repository: repo, NVKR: "/usr/bin/true", Go: "/usr/bin/true", Attempt: "fresh", Jobs: 1, Out: out}, "darwin/arm64")
			var coded *nvkrerr.Error
			if got != "" || !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("rejected endpoint yielded Run %q: %v", got, err)
			}
			for _, path := range []string{out, filepath.Join(repo, ".nvkr")} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("endpoint refusal wrote %s: %v", path, err)
				}
			}
		})
	}
}

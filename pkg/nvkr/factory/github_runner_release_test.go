// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// useGitHubGeneratorBuildInfo makes the generator report the given build for
// the rest of the test.
func useGitHubGeneratorBuildInfo(t *testing.T, info *debug.BuildInfo) {
	t.Helper()
	previous := readGitHubGeneratorBuildInfo
	readGitHubGeneratorBuildInfo = func() (*debug.BuildInfo, bool) { return info, info != nil }
	t.Cleanup(func() { readGitHubGeneratorBuildInfo = previous })
}

func releasedGeneratorBuildInfo(version, sum, goVersion string) *debug.BuildInfo {
	return &debug.BuildInfo{GoVersion: goVersion, Main: debug.Module{Path: githubRunnerModule, Version: version, Sum: sum}}
}

// Only a module-mode install of an exact release or commit pins the runner to
// a module version; checkout builds and other modules build from source.
func TestGitHubRunnerReleaseForGenerator(t *testing.T) {
	sum := testRunnerRelease.Sum
	for _, test := range []struct {
		name string
		info *debug.BuildInfo
		want githubRunnerRelease
		code e.Code
	}{
		{name: "no build information"},
		{name: "development build", info: releasedGeneratorBuildInfo("(devel)", "", "go1.27.3")},
		{name: "tagged local checkout", info: releasedGeneratorBuildInfo("v1.2.3", "", "go1.27.3")},
		{name: "dirty local checkout", info: releasedGeneratorBuildInfo("v1.2.3+dirty", sum, "go1.27.3")},
		{name: "commit install", info: releasedGeneratorBuildInfo("v1.2.4-0.20261028120000-abcdefabcdef", sum, "go1.27.3"),
			want: githubRunnerRelease{Version: "v1.2.4-0.20261028120000-abcdefabcdef", Sum: sum, GoVersion: "1.27.3"}},
		{name: "commit install before any tag", info: releasedGeneratorBuildInfo("v0.0.0-20261028120000-abcdefabcdef", sum, "go1.27.3"),
			want: githubRunnerRelease{Version: "v0.0.0-20261028120000-abcdefabcdef", Sum: sum, GoVersion: "1.27.3"}},
		{name: "pseudo-version checkout build", info: releasedGeneratorBuildInfo("v1.2.4-0.20261028120000-abcdefabcdef+dirty", "", "go1.27.3")},
		{name: "commit install with unsupported checksum", info: releasedGeneratorBuildInfo("v1.2.4-0.20261028120000-abcdefabcdef", "h2:abc", "go1.27.3"),
			code: e.CodeEngineCapability},
		{name: "other module", info: &debug.BuildInfo{GoVersion: "go1.27.3", Main: debug.Module{Path: "example.com/fork", Version: "v1.2.3", Sum: sum}}},
		{name: "release", info: releasedGeneratorBuildInfo("v1.2.3", sum, "go1.27.3"), want: testRunnerRelease},
		{name: "prerelease", info: releasedGeneratorBuildInfo("v1.3.0-rc.1", sum, "go1.27.3"),
			want: githubRunnerRelease{Version: "v1.3.0-rc.1", Sum: sum, GoVersion: "1.27.3"}},
		{name: "unreleased toolchain", info: releasedGeneratorBuildInfo("v1.2.3", sum, "devel go1.28-abcdef"), code: e.CodeEngineCapability},
		{name: "unsupported checksum", info: releasedGeneratorBuildInfo("v1.2.3", "h2:abc", "go1.27.3"), code: e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			useGitHubGeneratorBuildInfo(t, test.info)
			release, err := githubRunnerReleaseForGenerator()
			if test.code != "" {
				assertFactoryErrorCode(t, err, test.code)
				return
			}
			if err != nil || release != test.want {
				t.Fatalf("release = %+v, %v; want %+v", release, err, test.want)
			}
		})
	}
}

// A released generator pins every runner and transport helper bootstrap to
// its own module version and checksum; nothing builds the checkout's source.
func TestRenderGitHubPinsReleasedGeneratorRunner(t *testing.T) {
	useGitHubGeneratorBuildInfo(t, releasedGeneratorBuildInfo(testRunnerRelease.Version, testRunnerRelease.Sum, "go1.27.3"))
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	workflow := expandedGitHubWorkflow(t, bundle)
	for _, required := range []string{
		"go-version: '1.27.3'",
		"go install -trimpath -ldflags '-X main.version=v1.2.3 -X main.commit=deadbeef' " + nvkrRunnerPackage + "@v1.2.3\n",
		"test \"$installed\" = '" + githubRunnerModule + " v1.2.3 " + testRunnerRelease.Sum + "'\n",
	} {
		if !bytes.Contains(workflow, []byte(required)) {
			t.Errorf("released generator bundle omits %q", required)
		}
	}
	for _, forbidden := range []string{"go-version-file", " ./cmd/nvkr\n", "@latest"} {
		if bytes.Contains(workflow, []byte(forbidden)) {
			t.Errorf("released generator bundle contains %q", forbidden)
		}
	}
	helper, found := bundle.Files[githubCITransferPath(plan, "bootstrap-download")+"/action.yml"]
	if !found || !strings.Contains(string(helper), githubRunnerModule+"@v1.2.3") || !strings.Contains(string(helper), testRunnerRelease.Sum) ||
		!regexp.MustCompile(`go-version: ['"]?1\.27\.3['"]?\n`).Match(helper) || strings.Contains(string(helper), "go-version-file") {
		t.Fatal("transport helper bootstrap is not pinned to the released generator")
	}

	// The runner source is part of the bundle: a released generator's bundle
	// does not validate under a development build. The transport template check
	// refuses it before the workflow comparison.
	useGitHubGeneratorBuildInfo(t, nil)
	assertFactoryErrorCode(t, ValidateGitHubBundle(context.Background(), plan, bundle), e.CodeEngineCapability)
}

// Linker stamps win; unstamped release and commit installs are labeled from
// build information, and every label round-trips through linker flags.
func TestGeneratorVersion(t *testing.T) {
	sum := testRunnerRelease.Sum
	for _, test := range []struct {
		name            string
		version, commit string
		info            *debug.BuildInfo
		want            string
	}{
		{name: "no build information", version: UnstampedGeneratorVersion, commit: UnstampedGeneratorCommit, want: "dev (unknown)"},
		{name: "unstamped checkout build", version: UnstampedGeneratorVersion, commit: UnstampedGeneratorCommit,
			info: releasedGeneratorBuildInfo("v1.2.4-0.20261028120000-abcdefabcdef+dirty", "", "go1.27.3"), want: "dev (unknown)"},
		{name: "unstamped tagged checkout build", version: UnstampedGeneratorVersion, commit: UnstampedGeneratorCommit,
			info: releasedGeneratorBuildInfo("v1.2.3", "", "go1.27.3"), want: "dev (unknown)"},
		{name: "unstamped release install", version: UnstampedGeneratorVersion, commit: UnstampedGeneratorCommit,
			info: releasedGeneratorBuildInfo("v1.2.3", sum, "go1.27.3"), want: "v1.2.3 (unknown)"},
		{name: "unstamped prerelease install", version: UnstampedGeneratorVersion, commit: UnstampedGeneratorCommit,
			info: releasedGeneratorBuildInfo("v1.3.0-rc.1", sum, "go1.27.3"), want: "v1.3.0-rc.1 (unknown)"},
		{name: "unstamped commit install", version: UnstampedGeneratorVersion, commit: UnstampedGeneratorCommit,
			info: releasedGeneratorBuildInfo("v1.2.4-0.20261028120000-abcdefabcdef", sum, "go1.27.3"), want: "dev (abcdefabcdef)"},
		{name: "unstamped commit install before any tag", version: UnstampedGeneratorVersion, commit: UnstampedGeneratorCommit,
			info: releasedGeneratorBuildInfo("v0.0.0-20261028120000-abcdefabcdef", sum, "go1.27.3"), want: "dev (abcdefabcdef)"},
		{name: "unstamped other module", version: UnstampedGeneratorVersion, commit: UnstampedGeneratorCommit,
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.com/fork", Version: "v1.2.3", Sum: sum}}, want: "dev (unknown)"},
		{name: "unstamped other module commit install", version: UnstampedGeneratorVersion, commit: UnstampedGeneratorCommit,
			info: &debug.BuildInfo{Main: debug.Module{Path: "example.com/fork", Version: "v1.2.4-0.20261028120000-abcdefabcdef", Sum: sum}}, want: "dev (unknown)"},
		{name: "stamped release install", version: "v9.9.9", commit: "cafef00d",
			info: releasedGeneratorBuildInfo("v1.2.3", sum, "go1.27.3"), want: "v9.9.9 (cafef00d)"},
		{name: "stamped commit install", version: "v9.9.9", commit: "cafef00d",
			info: releasedGeneratorBuildInfo("v1.2.4-0.20261028120000-abcdefabcdef", sum, "go1.27.3"), want: "v9.9.9 (cafef00d)"},
		{name: "stamped commit only", version: UnstampedGeneratorVersion, commit: "cafef00d",
			info: releasedGeneratorBuildInfo("v1.2.3", sum, "go1.27.3"), want: "dev (cafef00d)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			useGitHubGeneratorBuildInfo(t, test.info)
			if got := GeneratorVersion(test.version, test.commit); got != test.want {
				t.Fatalf("GeneratorVersion(%q, %q) = %q; want %q", test.version, test.commit, got, test.want)
			}
			if _, err := GeneratorLinkerFlags(test.want); err != nil {
				t.Fatalf("label %q does not round-trip through linker flags: %v", test.want, err)
			}
		})
	}
}

// An unstamped `go install …@vX.Y.Z` generator labels its Plan with the
// release, and the pinned runner install restamps that same label so the
// runner's committed-generation check reproduces the generator's bytes.
func TestRenderGitHubLabelsUnstampedReleasedGenerator(t *testing.T) {
	useGitHubGeneratorBuildInfo(t, releasedGeneratorBuildInfo(testRunnerRelease.Version, testRunnerRelease.Sum, "go1.27.3"))
	plan := githubTestPlan(t)
	plan.GeneratorVersion = GeneratorVersion(UnstampedGeneratorVersion, UnstampedGeneratorCommit)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	required := "go install -trimpath -ldflags '-X main.version=v1.2.3 -X main.commit=unknown' " + nvkrRunnerPackage + "@v1.2.3\n"
	if !bytes.Contains(expandedGitHubWorkflow(t, bundle), []byte(required)) {
		t.Fatalf("released generator bundle omits %q", required)
	}
	if got := GeneratorVersion("v1.2.3", UnstampedGeneratorCommit); got != plan.GeneratorVersion {
		t.Fatalf("restamped runner label = %q; want %q", got, plan.GeneratorVersion)
	}
}

// An unstamped `go install …@<commit>` generator records the commit its
// pseudo-version encodes, and every bootstrap installs that exact module version
// from its published source, never the checkout, and restamps the same label.
func TestRenderGitHubPinsUnstampedCommitGenerator(t *testing.T) {
	const version = "v1.2.4-0.20261028120000-abcdefabcdef"
	useGitHubGeneratorBuildInfo(t, releasedGeneratorBuildInfo(version, testRunnerRelease.Sum, "go1.27.3"))
	plan := githubTestPlan(t)
	plan.GeneratorVersion = GeneratorVersion(UnstampedGeneratorVersion, UnstampedGeneratorCommit)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	workflow := expandedGitHubWorkflow(t, bundle)
	for _, required := range []string{
		"go-version: '1.27.3'",
		"go install -trimpath -ldflags '-X main.version=dev -X main.commit=abcdefabcdef' " + nvkrRunnerPackage + "@" + version + "\n",
		"test \"$installed\" = '" + githubRunnerModule + " " + version + " " + testRunnerRelease.Sum + "'\n",
	} {
		if !bytes.Contains(workflow, []byte(required)) {
			t.Errorf("commit generator bundle omits %q", required)
		}
	}
	for _, forbidden := range []string{"go-version-file", " ./cmd/nvkr\n"} {
		if bytes.Contains(workflow, []byte(forbidden)) {
			t.Errorf("commit generator bundle contains %q", forbidden)
		}
	}
	helper, found := bundle.Files[githubCITransferPath(plan, "bootstrap-download")+"/action.yml"]
	if !found || !strings.Contains(string(helper), githubRunnerModule+"@"+version) || !strings.Contains(string(helper), testRunnerRelease.Sum) {
		t.Fatal("transport helper bootstrap is not pinned to the commit generator's module version")
	}
	if got := GeneratorVersion(UnstampedGeneratorVersion, "abcdefabcdef"); got != plan.GeneratorVersion {
		t.Fatalf("restamped runner label = %q; want %q", got, plan.GeneratorVersion)
	}
	if err := ValidateGitHubBundle(context.Background(), plan, bundle); err != nil {
		t.Fatalf("commit generator bundle does not validate under the same build: %v", err)
	}
}

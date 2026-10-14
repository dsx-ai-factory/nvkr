// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	version, err := nightlyControllerVersion(nightlyVersionPlan())
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
			_, err := nightlyControllerVersion(plan)
			assertNightlyCode(t, err, e.CodeVerifyFailed)
		})
	}
	_, err = nightlyControllerVersion(nil)
	assertNightlyCode(t, err, e.CodeVerifyFailed)
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

func nightlyTar(t *testing.T, headers []*tar.Header) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := io.CopyN(writer, nightlyZeroReader{}, header.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

type nightlyZeroReader struct{}

func (nightlyZeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func TestNightlyProfileProjectionBoundsAndRejectsUnsafeMembers(t *testing.T) {
	valid := []*tar.Header{{Name: "profiles", Typeflag: tar.TypeDir, Mode: 0700}, {Name: "profiles/osProfiles/metadata.yaml", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}, {Name: "runbooks/setup.yaml", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}, {Name: "unrelated/private", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}}
	var projected bytes.Buffer
	if err := nightlyProfileArchive(t.Context(), bytes.NewReader(nightlyTar(t, valid)), &projected); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(&projected)
	members := []string{}
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, h.Name)
	}
	if strings.Join(members, ",") != "profiles,profiles/osProfiles/metadata.yaml,runbooks/setup.yaml" {
		t.Fatal(members)
	}
	for _, test := range []struct {
		name    string
		headers []*tar.Header
	}{
		{"traversal", []*tar.Header{{Name: "../profiles/x", Typeflag: tar.TypeReg}}},
		{"symlink", []*tar.Header{{Name: "profiles/link", Typeflag: tar.TypeSymlink, Linkname: "outside"}}},
		{"hardlink", []*tar.Header{{Name: "runbooks/link", Typeflag: tar.TypeLink, Linkname: "outside"}}},
		{"privileged mode", []*tar.Header{{Name: "profiles/x", Typeflag: tar.TypeReg, Mode: 04700}}},
		{"duplicate", []*tar.Header{{Name: "profiles/x", Typeflag: tar.TypeReg}, {Name: "profiles/x", Typeflag: tar.TypeReg}}},
		{"selected size", []*tar.Header{{Name: "profiles/large", Typeflag: tar.TypeReg, Size: 16<<20 + 1}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertNightlyCode(t, nightlyProfileArchive(t.Context(), bytes.NewReader(nightlyTar(t, test.headers)), io.Discard), e.CodeVerifyFailed)
		})
	}
	headers := make([]*tar.Header, 4097)
	for i := range headers {
		headers[i] = &tar.Header{Name: "ignored", Typeflag: tar.TypeReg}
	}
	assertNightlyCode(t, nightlyProfileArchive(t.Context(), bytes.NewReader(nightlyTar(t, headers)), io.Discard), e.CodeVerifyFailed)
	// Stream a large ignored member: ignored data must still respect total bytes.
	var header bytes.Buffer
	writer := tar.NewWriter(&header)
	if err := writer.WriteHeader(&tar.Header{Name: "ignored", Typeflag: tar.TypeReg, Size: 129 << 20}); err != nil {
		t.Fatal(err)
	}
	stream := io.MultiReader(bytes.NewReader(header.Bytes()), io.LimitReader(nightlyZeroReader{}, 129<<20))
	assertNightlyCode(t, nightlyProfileArchive(t.Context(), stream, io.Discard), e.CodeVerifyFailed)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	assertNightlyCode(t, nightlyProfileArchive(canceled, bytes.NewReader(nil), io.Discard), e.CodeTimeout)
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

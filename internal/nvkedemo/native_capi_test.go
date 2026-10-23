// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

const nativeInstallerSiteSpecFixture = "cluster:\n  k8sVersion: \"1.34.2\"\n"

// Runtime fixtures carry an explicit selected Run binding. They do not claim
// factory execution; separate tests exercise actual captured-input replay.
func nativeInstallerFixture(t *testing.T, installer, commit, version string) *nativeInstallerSelection {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(installer, "make"), 0700); err != nil {
		t.Fatal(err)
	}
	versions := []byte("CAPI_VERSION ?= " + version + "\n")
	writeTenantFixture(t, filepath.Join(installer, "SOURCE_COMMIT"), []byte(commit+"\n"), 0600)
	writeTenantFixture(t, filepath.Join(installer, "CAPI_VERSION"), []byte(version+"\n"), 0600)
	writeTenantFixture(t, filepath.Join(installer, "make/tool-versions.mk"), versions, 0600)
	if err := os.MkdirAll(filepath.Join(installer, filepath.Dir(nativeSiteSpecPath)), 0700); err != nil {
		t.Fatal(err)
	}
	spec := []byte(nativeInstallerSiteSpecFixture)
	writeTenantFixture(t, filepath.Join(installer, nativeSiteSpecPath), spec, 0600)
	return &nativeInstallerSelection{commit: commit, capiVersion: version, versionsDigest: plainSHA(versions), siteSpecDigest: plainSHA(spec)}
}

func TestNativeCAPIRechecksSelectedMetadataBeforeCommands(t *testing.T) {
	for _, revision := range []struct{ commit, version string }{{strings.Repeat("a", 40), "v1.13.6"}, {strings.Repeat("b", 40), "v2.4.0"}} {
		for _, name := range []string{"selected", "missing selection", "changed source", "changed provider", "changed versions", "symlink versions", "directory versions", "oversize versions", "canceled"} {
			t.Run(revision.version+"/"+name, func(t *testing.T) {
				installer, out := privateTestDir(t), privateTestDir(t)
				selected := nativeInstallerFixture(t, installer, revision.commit, revision.version)
				assets := NativeAssets{Installer: installer, installerSelection: selected}
				switch name {
				case "missing selection":
					assets.installerSelection = nil
				case "changed source":
					writeTenantFixture(t, filepath.Join(installer, "SOURCE_COMMIT"), []byte(strings.Repeat("c", 40)+"\n"), 0600)
				case "changed provider":
					writeTenantFixture(t, filepath.Join(installer, "CAPI_VERSION"), []byte("v9.99.0\n"), 0600)
				case "changed versions":
					writeTenantFixture(t, filepath.Join(installer, "make/tool-versions.mk"), []byte("CAPI_VERSION ?= v9.99.0\n"), 0600)
				case "symlink versions", "directory versions":
					p := filepath.Join(installer, "make/tool-versions.mk")
					if err := os.Rename(p, p+".held"); err != nil {
						t.Fatal(err)
					}
					var err error
					if name == "symlink versions" {
						err = os.Symlink(p+".held", p)
					} else {
						err = os.Mkdir(p, 0700)
					}
					if err != nil {
						t.Fatal(err)
					}
				case "oversize versions":
					writeTenantFixture(t, filepath.Join(installer, "make/tool-versions.mk"), make([]byte, (64<<10)+1), 0600)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if name == "canceled" {
					cancel()
				}
				version, err := nativeCAPIProviderVersion(ctx, assets)
				if name == "selected" {
					if err != nil || version != revision.version {
						t.Fatalf("version=%q err=%v", version, err)
					}
					return
				}
				if err == nil || version != "" {
					t.Fatalf("substituted metadata accepted: %q %v", version, err)
				}
				attempt := nativeSiteAttempt{assets: assets}
				if err := attempt.deployFromHost(ctx, Runner{Work: out, Env: NativeEnvironment()}, "/absent/nvkectl", "/absent/tools", out, "/absent/spec"); err == nil {
					t.Fatal("failed metadata reached deployment")
				}
				entries, err := os.ReadDir(out)
				if err != nil || len(entries) != 0 {
					t.Fatalf("metadata failure created command output: %v %v", entries, err)
				}
			})
		}
	}
}

func TestNativeCAPIInitSelectsAllThreeProvidersAndWaits(t *testing.T) {
	got := nativeCAPIInitArgs("/selected tools", "/private site.kubeconfig", "v2.4.0")
	want := []string{"/selected tools/clusterctl", "init", "--kubeconfig", "/private site.kubeconfig", "--core", "cluster-api:v2.4.0", "--bootstrap", "kubeadm:v2.4.0", "--control-plane", "kubeadm:v2.4.0", "--wait-providers"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func nativeCapturedToolsFixture(t *testing.T) (string, string, *factory.Plan) {
	t.Helper()
	plan, inputs, err := factory.CaptureCompileFiles(t.Context(), filepath.Join("..", ".."), "inputs/definitions/demo/native-linux/definition-tools.json", "inputs/environments/local-native-linux.json", "native-selection-test")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(privateTestDir(t), "native-selection-test")
	writeCapturedRunFixture(t, root, plan, inputs, nil)
	return root, "native-tools-linux-arm64", plan
}

func TestNativeInstallerSelectionReplaysCapturedRun(t *testing.T) {
	root, bundle, plan := nativeCapturedToolsFixture(t)
	selection, err := nativeInstallerSelectionFromRun(t.Context(), root, bundle)
	if err != nil {
		t.Fatal(err)
	}
	var buildDigest string
	for _, build := range plan.Builds {
		if build.Name == bundle {
			buildDigest = build.BuildDigest
		}
	}
	for _, name := range []string{"wrong build", "different executable source", "missing installer", "ambiguous clusterctl", "unstable clusterctl"} {
		t.Run(name, func(t *testing.T) {
			raw, err := canonical.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			var changed factory.Plan
			if err := canonical.UnmarshalExact(raw, &changed); err != nil {
				t.Fatal(err)
			}
			digest := buildDigest
			for i := range changed.Builds {
				b := &changed.Builds[i]
				if b.Name != bundle {
					continue
				}
				for j := range b.ResolvedComponents {
					c := &b.ResolvedComponents[j]
					switch {
					case name == "different executable source" && c.Name == "nvkectl":
						c.Commit = strings.Repeat("2", 40)
					case name == "missing installer" && c.Name == "nvke-installer-sources":
						c.Name = "unselected"
					case name == "unstable clusterctl" && c.Name == "clusterctl-linux":
						c.Tag = "v1.13.6-beta.1"
					}
				}
				if name == "ambiguous clusterctl" {
					b.ResolvedComponents = append(b.ResolvedComponents, catalog.NamedComponent{Name: "clusterctl", Component: catalog.Component{Tag: selection.capiVersion}})
				}
			}
			if name == "wrong build" {
				digest = "sha256:" + strings.Repeat("3", 64)
			}
			if _, err := nativeInstallerSelectionFromPlan(&changed, bundle, digest); err == nil {
				t.Fatal("substituted source binding accepted")
			}
		})
	}
	t.Run("different Run identity", func(t *testing.T) {
		raw, err := readRunFile(t.Context(), root, "lifecycle.json", diagnosticLimit)
		if err != nil {
			t.Fatal(err)
		}
		var run lifecycle.Run
		if err := canonical.UnmarshalExact(raw, &run); err != nil {
			t.Fatal(err)
		}
		run.PlanDigest = "sha256:" + strings.Repeat("9", 64)
		raw, err = canonical.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		writeTenantFixture(t, filepath.Join(root, "lifecycle.json"), raw, 0600)
		if _, err := nativeInstallerSelectionFromRun(t.Context(), root, bundle); err == nil {
			t.Fatal("mismatched Run accepted")
		}
	})
}

// Executes the actual catalog metadata-producing prefix using the selected Git
// source and GNU Make. It changes only a private test workspace, never NVKE.
func TestNativeInstallerSelectedSourceMakeContract(t *testing.T) {
	source := os.Getenv("NVKR_NVKE_SOURCE_CONTRACT")
	if source == "" {
		t.Skip("requires the selected NVKE Git source")
	}
	loaded, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "inputs/catalogs/tools/native")))
	if err != nil {
		t.Fatal(err)
	}
	producer := loaded.Components["nvke-installer-sources"]
	out := privateTestDir(t)
	runner := Runner{Work: out, Env: NativeEnvironment()}
	raw, err := runner.Capture(t.Context(), []string{"git", "-C", source, "show", producer.Commit + ":make/tool-versions.mk"}, "selected-source-versions", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(out, "make"), 0700); err != nil {
		t.Fatal(err)
	}
	writeTenantFixture(t, filepath.Join(out, "make/tool-versions.mk"), raw, 0600)
	if err := os.Mkdir(filepath.Join(out, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	makeTool, err := commandPath("gmake", runner.Env)
	if err != nil {
		makeTool, err = commandPath("make", runner.Env)
	}
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(out, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(makeTool, filepath.Join(tools, "make")); err != nil {
		t.Fatal(err)
	}
	script := producer.BuildCommand[2]
	end := strings.Index(script, "tar --sort=name")
	if end < 0 {
		t.Fatal("source archive command missing")
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"PATH": tools + ":" + environmentValue(runner.Env, "PATH"), "NKECTL_COMMIT": producer.Commit, "CAPI_VERSION": "v99.99.99"})
	if err := runner.Run(t.Context(), []string{"bash", "-euc", script[:end]}, "source-owned-provider-selection", time.Minute); err != nil {
		t.Fatal(err)
	}
	selected, err := readRunFile(t.Context(), out, "bin/CAPI_VERSION", 128)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"clusterctl", "clusterctl-linux"} {
		if string(selected) != loaded.Components[name].Tag+"\n" {
			t.Fatalf("source provider %q differs from %s %s", selected, name, loaded.Components[name].Tag)
		}
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDefaultLocalEnvironmentPolicy(t *testing.T) {
	for _, host := range []struct {
		os, arch string
		admitted bool
	}{
		{"linux", "amd64", true}, {"linux", "arm64", true}, {"darwin", "arm64", true}, {"darwin", "amd64", true},
		{"windows", "amd64", false}, {"linux", "riscv64", false},
	} {
		t.Run(host.os+"-"+host.arch, func(t *testing.T) {
			env, err := defaultLocalEnvironment(host.os, host.arch)
			if !host.admitted {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if env.Signing != nil || env.Publication != nil || len(env.ExecutionClasses) != 0 || env.GitHubARC != "" || env.Tekton != nil || len(env.Runtime.NativeToolchains) != 0 || env.Runtime.BuildNetworkPolicy != authored.BuildNetworkOfflineOnly || env.UtilityPlatform != "linux/"+host.arch {
				t.Fatalf("unsafe default: %+v", env)
			}
			if _, err := env.ToolchainAccess.ClassifyImage("ghcr.io/private/tool@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
				t.Fatal("private image admitted")
			}
		})
	}
}

func TestDefaultLocalCaptureFreezesAndReplaysPolicy(t *testing.T) {
	root, definition, _ := snapshotFixture(t)
	definition.Outputs = definition.Outputs[1:]
	writeSnapshotFixtureDocument(t, root, snapshotDefinition, definition)
	// A checkout file at the logical name cannot replace the built-in policy.
	writeSnapshotFixtureFile(t, root, DefaultLocalEnvironmentPath, []byte("malicious override"))
	plan, snapshot, err := CaptureCompileDefaultLocal(t.Context(), root, snapshotDefinition, "test")
	if err != nil {
		t.Fatal(err)
	}
	if plan.EnvironmentPath != DefaultLocalEnvironmentPath || snapshot.EnvironmentPath != DefaultLocalEnvironmentPath {
		t.Fatal("default policy path not recorded")
	}
	raw, err := canonical.Marshal(plan.Environment)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range snapshot.Files {
		if file.Path == DefaultLocalEnvironmentPath {
			found = true
			if !bytes.Equal(file.Data, raw) {
				t.Fatal("policy bytes differ")
			}
		}
	}
	if !found {
		t.Fatal("missing policy bytes")
	}
	writeSnapshotFixtureDocument(t, root, snapshotEnvironment, plan.Environment)
	explicit, _, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "test")
	if err != nil {
		t.Fatal(err)
	}
	if explicit.EnvironmentDigest != plan.EnvironmentDigest || len(explicit.Builds) != len(plan.Builds) {
		t.Fatal("same policy changed selected inputs")
	}
	for i := range plan.Builds {
		if explicit.Builds[i].BuildDigest != plan.Builds[i].BuildDigest {
			t.Fatal("logical Build identity changed with the policy source path")
		}
	}
	// Explicit selection always means the authored bytes, even at the logical
	// default location. Paths describe inputs; they do not attest policy origin.
	selected := plan.Environment.Clone()
	selected.Metadata.Name = "explicit-policy"
	writeSnapshotFixtureDocument(t, root, DefaultLocalEnvironmentPath, selected)
	explicitAtDefaultPath, _, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, DefaultLocalEnvironmentPath, "test")
	if err != nil {
		t.Fatal(err)
	}
	if explicitAtDefaultPath.EnvironmentDigest == plan.EnvironmentDigest || explicitAtDefaultPath.Environment.Metadata.Name != "explicit-policy" {
		t.Fatal("explicit policy selection was replaced by the default")
	}
	replay, err := CompileSnapshot(t.Context(), snapshot, "test")
	if err != nil {
		t.Fatal(err)
	}
	want, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	got, err := replay.Digest()
	if err != nil || got != want {
		t.Fatalf("replay identity differs: %s / %s: %v", got, want, err)
	}
}

func TestDefaultLocalCaptureRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		canceled   bool
		code       e.Code
	}{
		{"reserved path", DefaultLocalEnvironmentPath, false, e.CodeUsage},
		{"outside root", "../definition.yaml", false, e.CodeInvalidCatalog},
		{"canceled", "definition.yaml", true, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			plan, snapshot, err := CaptureCompileDefaultLocal(ctx, t.TempDir(), tc.path, "test")
			assertFactoryErrorCode(t, err, tc.code)
			if plan != nil || len(snapshot.Files) != 0 {
				t.Fatal("refusal returned partial inputs")
			}
		})
	}
}

func TestDefaultLocalPolicyVersionAndMaintainedPin(t *testing.T) {
	maintained, err := authored.LoadEnvironment(t.Context(), "../../../inputs/environments/local-amd64-kvm.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ arch, digest string }{
		{"amd64", "sha256:1c98c468746b4e67db02f4201b748ed38e60e8059c23b285598cf6fb2bf4f7fc"},
		{"arm64", "sha256:baac6bbffb14fbfcf8335a599ef2ce46c5c09fce78de4b8fec6624d14808a600"},
	} {
		t.Run(tc.arch, func(t *testing.T) {
			env, err := defaultLocalEnvironment("linux", tc.arch)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := env.Digest()
			if err != nil || digest != tc.digest {
				t.Fatalf("v1 policy changed; a policy change needs an explicit version migration: %s, %v", digest, err)
			}
			if env.UtilityContainer != maintained.UtilityContainer {
				t.Fatal("built-in utility index differs from maintained local pin; review and version the policy update")
			}
		})
	}
}

func TestCompileStarterRefusesReservedDestination(t *testing.T) {
	_, err := CompileStarter(t.Context(), t.TempDir(), DefaultLocalEnvironmentPath, authored.Definition{}, authored.Environment{})
	assertFactoryErrorCode(t, err, e.CodeUsage)
}

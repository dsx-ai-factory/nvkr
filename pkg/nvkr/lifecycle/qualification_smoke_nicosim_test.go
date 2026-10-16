// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestNICoSimSmokeSourceState(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, profile := range []string{"nicosim-version-json", "nicosim-patched-version-json"} {
		t.Run(profile, func(t *testing.T) {
			tree := "clean"
			if profile == "nicosim-patched-version-json" {
				tree = "patched"
			}
			fresh := func() ir.Node {
				node := ir.Node{Kind: ir.KindBuild, Version: "0.21.0", SourceCommit: commit,
					Env: []ir.EnvVar{{Name: "NICO_SIM_VERSION", Value: "v0.21.0"}, {Name: "NICO_SIM_COMMIT", Value: commit}, {Name: "NICO_SIM_TREE", Value: tree}}}
				if tree == "patched" {
					node.Patches = []ir.PatchRef{{Name: "version.patch", SHA256: nativeSmokeTestDigest("1"), Content: "fixture"}}
				}
				return node
			}
			version, source, err := expectedSmokeVersion(fresh(), profile)
			if err != nil || version != "v0.21.0" || source != commit {
				t.Fatalf("version=%q source=%q error=%v", version, source, err)
			}
			for _, mode := range []string{"missing stamps", "wrong version", "wrong commit", "dirty", "patch mismatch", "acquired"} {
				t.Run(mode, func(t *testing.T) {
					node := fresh()
					switch mode {
					case "missing stamps":
						node.Env = nil
					case "wrong version":
						node.Env[0].Value = "v9.0.0"
					case "wrong commit":
						node.Env[1].Value = strings.Repeat("b", 40)
					case "dirty":
						node.Env[2].Value = "dirty"
					case "acquired":
						node.Kind = ir.KindPull
					case "patch mismatch":
						if tree == "patched" {
							node.Patches = nil
						} else {
							node.Patches = []ir.PatchRef{{Name: "unexpected.patch"}}
						}
					}
					_, _, err := expectedSmokeVersion(node, profile)
					assertCode(t, err, e.CodeVerifyFailed)
				})
			}
			native, err := nativeSmokeProfile(profile)
			if err != nil {
				t.Fatal(err)
			}
			member := ToolSmokeMember{Profile: profile, Platform: "linux/arm64", PayloadDigest: nativeSmokeTestDigest("2"), Size: 100, ExpectedVersion: version, ExpectedCommit: commit}
			for _, observedTree := range []string{"clean", "patched", "dirty", "unknown", ""} {
				raw := `{"buildVersion":"v0.21.0","gitCommit":"` + commit + `","gitTreeState":"` + observedTree + `","platform":"linux/arm64"}`
				_, err := evaluateNativeSmokeMember(member, &nativeSmokeTestObservation{profile: native, digest: member.PayloadDigest, size: member.Size, platform: member.Platform, image: nativeSmokeTestDigest("3"), identity: nativeSmokeTestDigest("4"), stdout: []byte(raw)})
				if observedTree == tree {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					assertCode(t, err, e.CodeVerifyFailed)
				}
			}
		})
	}
}

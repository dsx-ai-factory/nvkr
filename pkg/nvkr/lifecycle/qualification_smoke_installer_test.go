// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestInstallerSmokeVersionsAndSourceStamps(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, tc := range []struct{ profile, version, stamp, wantCommit, raw string }{
		{"kind-version", "1.2.3", "1.2.3", "", "1.2.3\n"},
		{"clusterctl-version-json", "v1.2.3", "v1.2.3", commit, `{"clusterctl":{"gitVersion":"v1.2.3","gitCommit":"` + commit + `","gitTreeState":"clean","platform":"linux/amd64"}}`},
		{"oras-version", "1.2.3", "", commit, "Version:        1.2.3\nGo version:     go1.27.0\nOS/Arch:        linux/amd64\nGit commit:     " + commit + "\nGit tree state: clean\n"},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			freshNode := func() ir.Node {
				return ir.Node{Kind: ir.KindBuild, Version: "1.2.3", SourceCommit: commit, Env: []ir.EnvVar{{Name: "GIT_VERSION", Value: commit}, {Name: "BUILD_VERSION", Value: tc.stamp}}}
			}
			version, source, err := expectedSmokeVersion(freshNode(), tc.profile)
			if err != nil || version != tc.version || source != tc.wantCommit {
				t.Fatalf("expected version=%q commit=%q err=%v", version, source, err)
			}
			for _, mode := range []string{"changed commit", "missing stamp", "duplicate stamp", "acquired tool", "changed version stamp"} {
				if mode == "changed version stamp" && tc.profile == "oras-version" {
					continue
				}
				t.Run(mode, func(t *testing.T) {
					node := freshNode()
					switch mode {
					case "changed commit":
						node.SourceCommit = strings.Repeat("b", 40)
					case "missing stamp":
						node.Env = nil
					case "duplicate stamp":
						node.Env = append(node.Env, node.Env[0])
					case "acquired tool":
						node.Kind = ir.KindPull
					case "changed version stamp":
						node.Env[1].Value = "9.9.9"
					}
					_, _, err := expectedSmokeVersion(node, tc.profile)
					assertCode(t, err, e.CodeVerifyFailed)
				})
			}
			profile, err := nativeSmokeProfile(tc.profile)
			if err != nil {
				t.Fatal(err)
			}
			member := ToolSmokeMember{Profile: tc.profile, Platform: "linux/amd64", PayloadDigest: nativeSmokeTestDigest("2"), Size: 100, ExpectedVersion: version, ExpectedCommit: source}
			observe := func(raw string) (ToolSmokeMember, error) {
				return evaluateNativeSmokeMember(member, &nativeSmokeTestObservation{profile: profile, digest: member.PayloadDigest, size: member.Size, platform: member.Platform, image: nativeSmokeTestDigest("3"), identity: nativeSmokeTestDigest("4"), stdout: []byte(raw)})
			}
			got, err := observe(tc.raw)
			if err != nil || got.ObservedVersion != version || got.ObservedCommit != source {
				t.Fatalf("observed=%+v err=%v", got, err)
			}
			bad := []string{"", tc.raw + tc.raw, strings.ReplaceAll(tc.raw, "1.2.3", "9.9.9")}
			if tc.wantCommit != "" {
				bad = append(bad, strings.ReplaceAll(tc.raw, commit, strings.Repeat("b", 40)), strings.ReplaceAll(tc.raw, "clean", "dirty"), strings.ReplaceAll(tc.raw, "linux/amd64", "linux/arm64"))
			}
			if tc.profile == "oras-version" {
				bad = append(bad, tc.raw+"Other: value\n", strings.ReplaceAll(tc.raw, "Git commit:", "Other:"), strings.ReplaceAll(tc.raw, "1.2.3", ""))
			}
			if tc.profile == "clusterctl-version-json" {
				bad = append(bad, `{"clusterctl":null}`, `{"clusterctl":{},"clusterctl":{}}`)
			}
			for _, raw := range bad {
				_, err := observe(raw)
				assertCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

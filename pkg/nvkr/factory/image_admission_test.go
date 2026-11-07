// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestPrivateUtilityUsesPreparedImageWithoutRepull(t *testing.T) {
	image := "ghcr.io/new-owner/selected@sha256:" + strings.Repeat("a", 64)
	plan := recompileGitHubStabilityPlan(t, arcTestPlan(t, "linux/amd64"), func(in *githubStabilityInputs) {
		in.environment.ToolchainAccess.PrivateRoots = []string{"ghcr.io/new-owner/selected"}
		in.environment.UtilityContainer = image
	})
	var node Node
	for _, candidate := range plan.Graph.Nodes {
		if candidate.IRNode == "" && candidate.Container == image {
			node = candidate
			break
		}
	}
	if node.ID == "" {
		t.Fatal("no private utility")
	}
	access, err := plan.runtimeImageAccess(node, "github")
	if err != nil || access != pipeline.ImageScopedReader {
		t.Fatalf("private route: %q %v", access, err)
	}
	for _, tc := range []struct {
		name         string
		missing      bool
		arch, digest string
		want         e.Code
	}{
		{"prepared", false, "amd64", strings.Repeat("a", 64), ""},
		{"missing", true, "amd64", strings.Repeat("a", 64), e.CodeEngineCapability},
		{"wrong digest", false, "amd64", strings.Repeat("c", 64), e.CodePreflightFailed},
		{"wrong platform", false, "arm64", strings.Repeat("a", 64), e.CodePreflightFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			metadata, calls := filepath.Join(root, "image"), filepath.Join(root, "calls")
			configuration := ""
			err := pullGitHubImages(t.Context(), []string{image}, node.Platform, "synthetic-reader", "synthetic-token", func(_ context.Context, config, platform, selected string) error {
				configuration = config
				if _, err := os.Stat(filepath.Join(config, "config.json")); err != nil {
					return err
				}
				if selected != image || platform != node.Platform {
					t.Fatal("preparation changed selection")
				}
				raw := fmt.Sprintf(`{"Id":"sha256:%s","Os":"linux","Architecture":%q,"RepoDigests":["ghcr.io/new-owner/selected@sha256:%s"]}`, strings.Repeat("b", 64), tc.arch, tc.digest)
				return os.WriteFile(metadata, []byte(raw), 0o600)
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(configuration); !os.IsNotExist(err) {
				t.Fatal("preparation credentials survived")
			}
			if tc.missing {
				if err := os.Remove(metadata); err != nil {
					t.Fatal(err)
				}
			}
			script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$1" in
info) printf '%%s\n' '{"ID":"daemon-a","OSType":"linux","Architecture":"amd64"}';;
version) printf '%%s\n' '{}';;
image) cat %q;;
run) case " $* " in *' --pull=never '*) exit 0;; *) exit 93;; esac;;
rm) exit 0;;
*) exit 94;;
esac
`, calls, metadata)
			runtime := filepath.Join(root, "docker")
			if err := os.WriteFile(runtime, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			err = executeUtilityNode(t.Context(), node, utilityRunnerLocation{runtime: runtime, imageAccess: access}, io.Discard, io.Discard)
			if tc.want != "" {
				assertFactoryErrorCode(t, err, tc.want)
			} else if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "pull --platform") || strings.Contains(string(raw), "run --pull=never") != (tc.want == "") {
				t.Fatalf("unadmitted runtime calls: %s", raw)
			}
		})
	}
}

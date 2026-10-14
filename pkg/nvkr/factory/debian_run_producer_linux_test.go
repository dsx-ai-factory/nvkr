// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package factory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func debianBridgeGit(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git fixture failed: %v %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// Actual source fetch/materialization uses a controlled local Git repository;
// the ordinary developer-fetch path and a test-only Git insteadOf redirect.
// Runtime observations and emitted archive bytes are fixtures, not a daemon/DEB.
// The hosted isolated-source path is not exercised by this local fixture.
func TestDebianRunProducerBridgeNativeCompletion(t *testing.T) {
	for _, mode := range []string{"success", "cleanup-failure", "source-substitution", "closed-run"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			repo := t.TempDir()
			debianBridgeGit(t, ctx, repo, "init", "-q")
			if err := os.WriteFile(filepath.Join(repo, "input.txt"), []byte("controlled pinned source\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			debianBridgeGit(t, ctx, repo, "add", "input.txt")
			debianBridgeGit(t, ctx, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "controlled source fixture")
			commit := debianBridgeGit(t, ctx, repo, "rev-parse", "HEAD")
			tree := debianBridgeGit(t, ctx, repo, "rev-parse", "HEAD^{tree}")
			d, env, stack := debianFactoryInputs(t)
			component := stack.Catalog.Components["agent"]
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "url."+filepath.ToSlash(repo)+".insteadOf")
			t.Setenv("GIT_CONFIG_VALUE_0", component.Repo)
			component.Commit = commit
			component.TreeHash = tree
			stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"agent": component}})
			if err != nil {
				t.Fatal(err)
			}
			d.Outputs[0].Platform = "linux/" + runtime.GOARCH
			env.UtilityPlatform = d.Outputs[0].Platform
			plan, err := Compile(ctx, d, env, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			var fetch Node
			for _, node := range plan.Graph.Nodes {
				if node.Kind == NodeFetch {
					fetch = node
					break
				}
			}
			if err := executeProducer(ctx, plan, fetch, plan.Environment.RuntimeBinary(), 1, io.Discard, io.Discard, (pipeline.Local{}).ExecNode, nil); err != nil {
				t.Fatal("actual controlled fetch", err)
			}
			digest, err := plan.Digest()
			if err != nil {
				t.Fatal(err)
			}
			result := &LocalRunResult{SchemaVersion: LocalRunSchemaVersion, PlanDigest: digest, Qualification: TierLocalExecution}
			session, err := pipeline.NewNodeSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(session.Close)
			t.Cleanup(func() { _ = result.Close() })
			if err := bindDebianRunSession(ctx, plan, result, session); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "docker")
			script := fmt.Sprintf(`#!/bin/sh
set -eu
case "$1" in
 version) printf '{"Client":{"Version":"1"},"Server":{"Version":"1"}}'; exit 0;;
 info) printf '"controlled-daemon"'; exit 0;;
 image) printf '{"Id":"sha256:%s","RepoDigests":["example.test/toolchain@sha256:%s"],"Os":"linux","Architecture":"%s","Config":{"Env":[],"Volumes":null}}'; exit 0;;
 rm) if [ %q = cleanup-failure ]; then exit 7; fi; exit 0;;
esac
out=
while [ "$#" -gt 0 ]; do
 case "$1" in
 -v) case "$2" in *:/src/out) out=${2%%:/src/out};; esac; shift 2;;
 *) shift;;
 esac
done
[ -n "$out" ]
printf 'controlled final archive' > "$out/agent-%s.deb"
`, strings.Repeat("a", 64), strings.Repeat("c", 64), runtime.GOARCH, mode, runtime.GOARCH)
			if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			if mode == "source-substitution" {
				for _, artifact := range plan.Graph.Artifacts {
					if artifact.Kind == "source-closure" {
						if err := os.WriteFile(filepath.Join(artifact.Path, "input.txt"), []byte("substituted after fetch\n"), 0o644); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			if mode == "closed-run" {
				if err := result.Close(); err != nil {
					t.Fatal(err)
				}
			}
			err = result.debian.executeBoundProducer(ctx, result, plan, plan.LifecycleOutputs[0], path, io.Discard, io.Discard)
			if mode != "success" {
				want := e.CodeBuildFailed
				if mode == "closed-run" {
					want = e.CodeEngineCapability
				}
				if factoryErrorCode(err) != want || len(result.debian.completions) != 0 {
					t.Fatal("failed producer retained completion or wrong code", err)
				}
				return
			}
			if err != nil {
				t.Fatal("original Run producer transfer", err)
			}
			completion, ok := result.debian.completions[plan.LifecycleOutputs[0].ArtifactID]
			if !ok {
				t.Fatal("missing original completion")
			}
			var retained bytes.Buffer
			if _, err := completion.archive.WriteToContext(ctx, &retained); err != nil || retained.String() != "controlled final archive" {
				t.Fatal("retained bytes changed", err)
			}
			if completion.description.BuildDigest != plan.LifecycleOutputs[0].BuildDigest {
				t.Fatal("Build join changed")
			}
			if err := result.debian.executeBoundProducer(ctx, result, plan, plan.LifecycleOutputs[0], path, io.Discard, io.Discard); factoryErrorCode(err) != e.CodeVerifyFailed || len(result.debian.completions) != 1 {
				t.Fatal("duplicate named completion admitted or original completion changed", err)
			}
			if err := finishLocalRun(result, session, true, nil); err != nil {
				t.Fatal(err)
			}
			if err := session.RequireOpenContext(ctx); err != nil {
				t.Fatal("session closed before retained observation", err)
			}
			if err := result.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := completion.archive.WriteToContext(ctx, io.Discard); factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatal("closed original Run retained producer bytes", err)
			}
		})
	}
}

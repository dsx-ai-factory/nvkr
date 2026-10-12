// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Real bounded command captures, synthetic Kubernetes answers. This composes
// the actual owner projection with the CLI presenter, not NVKE E2E acceptance.
func cliOperatorFixture(t *testing.T) (nvkedemo.OperatorAccess, nvkedemo.Runner) {
	t.Helper()
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(work, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kubeconfig", "tenant-kubeconfig"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte("private-config-sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
set -eu
case "$*" in
 *--raw=/readyz*) printf 'ok\n' ;;
 *'get services -o json'*) printf '{"items":[]}\n' ;;
 *'get nodes -o json'*) printf '%s\n' '{"items":[{"metadata":{"name":"private-node-name","labels":{"node-role.kubernetes.io/control-plane":""}},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"architecture":"arm64","kubeletVersion":"v1.34.2"},"addresses":[{"address":"private-address"}]}}]}' ;;
 *) exit 99 ;;
esac
`
	if err := os.WriteFile(filepath.Join(tools, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := nvkedemo.Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin"}}
	access, err := nvkedemo.InspectOperatorAccess(t.Context(), nvkedemo.ManagementSession{Work: work, Tools: tools, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	return access, runner
}

func assertCLIClusterOutput(t *testing.T, text string) {
	t.Helper()
	for _, wanted := range []string{"Live Kubernetes checks", "Site / Kind management", "Tenant / QCOW2", "API: ok", "Nodes: 1/1 Ready", "ROLE", "control-plane", "arm64", "v1.34.2"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("cluster result lost %q: %s", wanted, text)
		}
	}
	if strings.Count(text, "Nodes: 1/1 Ready") != 2 || strings.Contains(text, "private-") || strings.ContainsRune(text, '\x1b') {
		t.Fatal("cluster result duplicated, leaked or exposed controls", text)
	}
}

func TestCleanupCommandsKeepActualClusterOutput(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, command := range []string{"run", "e2e"} {
		for _, mode := range []string{"concise", "plain", "verbose"} {
			t.Run(command+"/"+mode, func(t *testing.T) {
				access, runner := cliOperatorFixture(t)
				var out, diagnostics bytes.Buffer
				actions := demoActions{
					load: func(context.Context, string) (nvkedemo.DemoFile, error) {
						file := configuredFixture()
						if command == "e2e" {
							file.Run.Runs, file.Run.NVKR = nvkedemo.RunFileInputs{}, ""
						}
						return file, nil
					},
					prepare: func(_ context.Context, _ nvkedemo.BuildConfig, cfg nvkedemo.Config) (nvkedemo.Config, error) {
						return cfg, nil
					},
					build: func(context.Context, nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
						return nvkedemo.BuiltInputs{}, nil
					},
					run: func(ctx context.Context, cfg nvkedemo.Config) error {
						if !cfg.Cleanup {
							t.Fatal("explicit cleanup flag lost")
						}
						if err := access.WriteLiveStatus(ctx, runner, cfg.Progress); err != nil {
							return err
						}
						// Observe output before returning from the cleanup callback.
						assertCLIClusterOutput(t, out.String())
						return nil
					},
				}
				args := []string{command, "--config", "fixture.yaml", "--cleanup"}
				if mode != "concise" {
					args = append(args, "--"+mode)
				}
				assertCode(t, executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), args), "")
				assertCLIClusterOutput(t, out.String())
			})
		}
	}
}

func TestDashboardKeepsClusterSnapshotAfterCleanupAndActivityEviction(t *testing.T) {
	for _, cleanupFailed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cleanup-failed"}[cleanupFailed], func(t *testing.T) {
			access, runner := cliOperatorFixture(t)
			_, cancel := context.WithCancel(t.Context())
			defer cancel()
			view := newDashboard()
			writer := &checkedWriter{out: view, observer: view, cancel: cancel, started: time.Now(), phases: make(map[string]time.Time)}
			if err := access.WriteLiveStatus(t.Context(), runner, writer); err != nil {
				t.Fatal(err)
			}
			assertCLIClusterOutput(t, strings.Join(view.snapshot().activity, "\n"))
			for range dashboardLines + 1 {
				if _, err := io.WriteString(writer, "[done] cleanup activity\n"); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(strings.Join(view.snapshot().activity, "\n"), "Live Kubernetes checks") {
				t.Fatal("fixture did not evict activity")
			}
			var cleanupErr error
			if cleanupFailed {
				cleanupErr = nvkrerr.New(nvkrerr.CodeTimeout, "cleanup did not complete")
			}
			var restored bytes.Buffer
			err := finishDashboardProgress(writer, &restored, "", cleanupErr, nil)
			if !errors.Is(err, cleanupErr) {
				t.Fatal("cleanup result lost", err)
			}
			assertCLIClusterOutput(t, restored.String())
			if !strings.Contains(restored.String(), "historical, not a claim that services remain running") {
				t.Fatal("replay presented a deleted cluster as currently running")
			}
		})
	}
}

func TestProgressRejectsUnissuedOperatorStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var out bytes.Buffer
	writer := &checkedWriter{out: &out, cancel: cancel}
	assertCode(t, writer.WriteOperatorStatus(nvkedemo.OperatorStatus{}), nvkrerr.CodePreflightFailed)
	if out.Len() != 0 || ctx.Err() == nil {
		t.Fatal("unissued status produced output or failed to cancel")
	}
}

func TestOperatorStatusOutputFailureCancelsAndPreservesCodedFailure(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "short"}[short], func(t *testing.T) {
			access, runner := cliOperatorFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			writer := &checkedWriter{out: failedProgressWriter{short: short}, cancel: cancel}
			err := access.WriteLiveStatus(ctx, runner, writer)
			assertCode(t, err, nvkrerr.CodePreflightFailed)
			if ctx.Err() == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("cluster output failure did not cancel or exposed private data", err)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Exercise the real source-fetch owner with modeled daemon information. No Git
// fetch, image materialization, or source output may follow failed admission.
func TestSourceFetchRejectsForeignVendorDaemonBeforeCheckout(t *testing.T) {
	for _, tc := range []struct {
		name, osName, arch string
		want               e.Code
	}{
		{"native", "linux", runtime.GOARCH, e.CodeFetchFailed},
		{"foreign architecture", "linux", "other", e.CodeEngineCapability},
		{"foreign OS", "other", runtime.GOARCH, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, _, _ := pristineClosureFixture(t)
			node.Kind, node.Repo, node.Network = ir.KindFetch, "https://example.invalid/source", ir.NetworkDefault
			node.VendorCmd = []string{"vendor"}
			node.VendorImage = "example.invalid/tool@sha256:" + strings.Repeat("a", 64)
			node.VendorSpecDigest, _ = ir.VendorSpecIdentity(node.VendorCmd, node.VendorImage)
			work := t.TempDir()
			t.Chdir(work)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			workspace, err := HoldSourceWorkspace(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			base := sourceVendorRuntimeFixture(t, "linux/"+runtime.GOARCH)
			adapter := &sourceVendorIntegrityRuntime{sourceVendorTestRuntime: base, intercept: func(_ io.Reader, out io.Writer, args []string) (bool, error) {
				if args[0] != "info" {
					t.Fatalf("unexpected runtime work: %v", args)
				}
				_, err := fmt.Fprintf(out, `{"OSType":%q,"Architecture":%q}`, tc.osName, tc.arch)
				return true, err
			}}
			err = (Local{}).fetchWithSourceVendorAdmission(node, withDefaults(RunOptions{Ctx: ctx, SourceRoot: filepath.Join(work, "src"), SourceWorkspace: workspace, Runtime: "docker", Launch: true, Stdout: io.Discard, Stderr: io.Discard}), func(ctx context.Context, _ ir.Node, _ string) (*sourceVendor, error) {
				if err := checkSourceVendorPlatform(ctx, adapter, "linux/"+runtime.GOARCH); err != nil {
					return nil, err
				}
				// A sentinel after successful admission prevents this contract fixture
				// from contacting a registry or remote Git repository.
				return nil, e.New(e.CodeFetchFailed, "native admission reached next boundary")
			})
			if code(err) != tc.want {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if len(base.commands) != 1 || base.commands[0][0] != "info" {
				t.Fatalf("commands: %v", base.commands)
			}
			entries, err := os.ReadDir(work)
			if err != nil || len(entries) != 0 {
				t.Fatalf("source admission created output: %v / %v", entries, err)
			}
		})
	}
}

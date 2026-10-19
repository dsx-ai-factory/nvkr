// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The original public Packer owner retains the manifest and controlled ELF
// diagnostics. This tests the real coordinator and sealed sink together without
// claiming that the ELF protocol is Docker, Packer or a real NVKE bake.
func TestGitHubDiskCoordinatorSealedDiagnostics(t *testing.T) {
	for _, mode := range []string{"deadline", "ordinary failure", "successful completion"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			fixture := newPublicDiskEvidenceFixture(t, ctx, "coordinator diagnostics")
			_, _, diagnostics := fixture.produceWithDiagnostics(t, ctx)
			if diagnostics.RetainedPath() != "" {
				t.Fatal("open original sink exposed a transport route")
			}
			output := filepath.Join(fixture.root, "github-output")
			t.Setenv("GITHUB_OUTPUT", output)
			primary := e.New(e.CodeBuildFailed, "controlled dispatcher failure")
			err := coordinateGitHubDiskExecution(ctx, diagnostics, func(ctx context.Context) error {
				if mode == "successful completion" {
					return nil
				}
				if mode == "ordinary failure" {
					return primary
				}
				bounded, stop := context.WithTimeout(ctx, time.Millisecond)
				defer stop()
				<-bounded.Done()
				return bounded.Err()
			})
			switch mode {
			case "successful completion":
				if err != nil {
					t.Fatal(err)
				}
			case "ordinary failure":
				if err != primary {
					t.Fatalf("diagnostic transport replaced the primary failure: %v", err)
				}
			case "deadline":
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeTimeout || coded.Msg != "ARC disk execution exhausted its bounded deadline" || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("dispatcher deadline classification=%v", err)
				}
			}
			route := diagnostics.RetainedPath()
			if !strings.HasPrefix(route, "diagnostics/packer/run-") {
				t.Fatalf("original concrete sink did not seal one route: %q", route)
			}
			manifest, err := os.Stat(filepath.Join(fixture.root, route, "material-manifest.json"))
			if err != nil || !manifest.Mode().IsRegular() || manifest.Mode().Perm() != 0o444 {
				t.Fatalf("route lacks the original sealed manifest: %v", err)
			}
			raw, err := os.ReadFile(output)
			if mode == "successful completion" {
				if !os.IsNotExist(err) {
					t.Fatalf("successful execution published failure-only diagnostics: %s %v", raw, err)
				}
			} else if err != nil || string(raw) != "packer_diagnostics="+route+"\n" {
				t.Fatalf("failed execution did not publish the exact sealed route: %s %v", raw, err)
			}
		})
	}
}

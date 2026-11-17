// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTemporaryForkDeployDebugRemainsPrivate(t *testing.T) {
	for _, tc := range []struct{ name, repository, ref, event, flag string }{
		{"upstream", "dsx-ai-factory/nvkr", "refs/heads/fix/nvkr-nktp-chart-source", "workflow_dispatch", "1"},
		{"other branch", "njhensley/nvkr", "refs/heads/main", "workflow_dispatch", "1"},
		{"scheduled", "njhensley/nvkr", "refs/heads/fix/nvkr-nktp-chart-source", "schedule", "1"},
		{"disabled", "njhensley/nvkr", "refs/heads/fix/nvkr-nktp-chart-source", "workflow_dispatch", "0"},
		{"capture", "njhensley/nvkr", "refs/heads/fix/nvkr-nktp-chart-source", "workflow_dispatch", "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RUNNER_TEMP", root)
			t.Setenv("NVKR_NKTP_DEBUG", tc.flag)
			t.Setenv("GITHUB_REPOSITORY", tc.repository)
			t.Setenv("GITHUB_REF", tc.ref)
			t.Setenv("GITHUB_EVENT_NAME", tc.event)
			environment := filepath.Join(root, "environment")
			if err := os.WriteFile(environment, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GITHUB_ENV", environment)
			var public bytes.Buffer
			r, enabled, err := temporaryForkDeployDebug(Runner{Env: []string{"PATH=/usr/bin:/bin"}, output: &public})
			if err != nil || enabled != (tc.name == "capture") {
				t.Fatalf("fork admission: %v %v", enabled, err)
			}
			if !enabled {
				if _, err := os.Stat(filepath.Join(root, "nktp-deploy-debug-plain")); !os.IsNotExist(err) {
					t.Fatal("inadmissible context created diagnostic storage")
				}
				return
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			_, err = r.Capture(ctx, []string{"/bin/sh", "-c", "printf synthetic-private-stdout; printf synthetic-private-stderr >&2; exit 1"}, "host-nvke-deploy", 0)
			if CommandExitCode(err) != 1 {
				t.Fatalf("lost deployment failure: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, "kubectl"), []byte("#!/bin/sh\nprintf synthetic-private-kubectl\nprintf synthetic-private-kubectl-error >&2\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			temporaryForkDeployFailure(ctx, r, root, filepath.Join(root, "unused-kubeconfig"))
			if public.Len() != 0 {
				t.Fatal("diagnostic bytes entered public output")
			}
			raw, err := os.ReadFile(filepath.Join(r.Work, "host-nvke-deploy.log"))
			if err != nil || !bytes.Contains(raw, []byte("synthetic-private-stdout")) || !bytes.Contains(raw, []byte("synthetic-private-stderr")) {
				t.Fatal("combined private deployment output was not retained")
			}
			raw, err = os.ReadFile(environment)
			if err != nil || string(raw) != "NVKR_HOST_DEPLOY_FAILED=1\n" {
				t.Fatal("failure did not enable the ciphertext step with a fixed boolean")
			}
			files, err := os.ReadDir(r.Work)
			if err != nil || len(files) != 10 {
				t.Fatal("missing private deployment or Kubernetes capture")
			}
			for _, file := range files {
				info, err := file.Info()
				if err != nil || info.Mode().Perm() != 0o600 || !strings.HasSuffix(file.Name(), ".log") {
					t.Fatal("private diagnostic permissions or inventory changed")
				}
			}
		})
	}
}

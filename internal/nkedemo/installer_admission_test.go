// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestInstallerPlatformAdmissionBeforeHostWork(t *testing.T) {
	for _, row := range []struct{ system, arch string }{
		{"darwin", "arm64"}, {"linux", "amd64"}, {"windows", "arm64"},
	} {
		for _, network := range []string{"ovn", "upstream-ovn", "kind-calico"} {
			t.Run(row.system+"/"+row.arch+"/"+network, func(t *testing.T) {
				// Missing PATH and repository would fail differently if any
				// environment, filesystem, Docker or kernel probe came first.
				t.Setenv("PATH", "")
				var progress bytes.Buffer
				result, err := prepareInstallerOnPlatform(t.Context(), BuildConfig{}, Config{Network: network, Progress: &progress}, row.system, row.arch)
				var coded *nkderr.Error
				if result != (Config{}) || !errors.As(err, &coded) || coded.Code != nkderr.CodeEngineCapability || progress.Len() != 0 {
					t.Fatalf("unsupported host produced work or wrong classification: %+v %v %q", result, err, progress.String())
				}
			})
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := prepareInstallerOnPlatform(ctx, BuildConfig{}, Config{}, "darwin", "arm64")
	var coded *nkderr.Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &coded) || coded.Code != nkderr.CodeTimeout {
		t.Fatalf("early capability check hid cancellation: %v", err)
	}
}

func TestInstallerDockerAdmissionDoesNotSuggestPullForMissingDaemon(t *testing.T) {
	for _, scenario := range []string{"missing CLI", "daemon unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			work := privateTestDir(t)
			marker := filepath.Join(work, "image-inspection")
			if scenario == "daemon unavailable" {
				body := "#!/bin/sh\nif [ \"$1\" = version ]; then exit 1; fi\n: > \"" + marker + "\"\nexit 99\n"
				if err := os.WriteFile(filepath.Join(work, "docker"), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := admitInstallerImages(t.Context(), Runner{Work: work, Env: []string{"PATH=" + work}})
			if err == nil || strings.Contains(err.Error(), "docker pull") {
				t.Fatalf("misleading Docker recovery: %v", err)
			}
			if scenario == "missing CLI" && !strings.Contains(err.Error(), "Docker CLI") || scenario == "daemon unavailable" && !strings.Contains(err.Error(), "Docker daemon is unavailable") {
				t.Fatalf("missing actionable diagnostic: %v", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed Docker capability proceeded to image inspection")
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDemoNarrationQualificationSelection(t *testing.T) {
	repo := narrationRepository(t)
	for _, row := range []struct {
		name    string
		cfg     nvkedemo.BuildConfig
		enabled bool
	}{
		{"disabled", nvkedemo.BuildConfig{}, false},
		{"automatic", nvkedemo.BuildConfig{QualifyLocal: true}, true},
		{"explicit", nvkedemo.BuildConfig{Qualification: nvkedemo.QualificationFile{BuilderKey: "/private/not-displayed"}}, true},
		{"policy admission only", nvkedemo.BuildConfig{QualificationPolicy: "/private/not-displayed"}, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			row.cfg.Repository = repo
			var out bytes.Buffer
			if err := presentDemoNarration(t.Context(), &out, row.cfg); err != nil {
				t.Fatal(err)
			}
			for _, description := range []string{"build → qualification →", "Qualification verifies every output", "independently verifies registry contents"} {
				if strings.Contains(out.String(), description) != row.enabled {
					t.Fatalf("qualification selection described incorrectly: %s", out.String())
				}
			}
			if strings.Contains(out.String(), "/private/not-displayed") {
				t.Fatal("qualification locator appeared in narration")
			}
		})
	}
}

func TestDemoNarrationMissingDefaultAndExplicitInputs(t *testing.T) {
	selected, environment, err := nvkedemo.LocalArtifactInputsForPlatform("ovn", false, runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{selected, environment} {
		for _, explicit := range []bool{false, true} {
			t.Run(missing+map[bool]string{false: "/default", true: "/explicit"}[explicit], func(t *testing.T) {
				repo := narrationRepository(t)
				if err := os.Remove(filepath.Join(repo, missing)); err != nil {
					t.Fatal(err)
				}
				cfg := nvkedemo.BuildConfig{Repository: repo}
				want := nvkrerr.CodeEngineCapability
				if explicit {
					cfg.Definition, cfg.Environment = selected, environment
					want = nvkrerr.CodeInvalidCatalog
				}
				if !explicit {
					assertCode(t, nvkedemo.AdmitDemoBuild(t.Context(), cfg), nvkrerr.CodeEngineCapability)
				}
				var out bytes.Buffer
				err := presentDemoNarration(t.Context(), &out, cfg)
				assertCode(t, err, want)
				if !explicit && nvkrerr.ExitCode(err) != 29 {
					t.Fatal("missing default inputs did not preserve the capability exit code", err)
				}
				if out.Len() != 0 {
					t.Fatal("missing inputs emitted narration")
				}
				if _, err := os.Lstat(filepath.Join(repo, ".nvkr")); !os.IsNotExist(err) {
					t.Fatal("narration created attempt state", err)
				}
			})
		}
	}
}

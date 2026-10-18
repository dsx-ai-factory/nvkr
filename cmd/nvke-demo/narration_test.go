// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	"strings"
	"testing"
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

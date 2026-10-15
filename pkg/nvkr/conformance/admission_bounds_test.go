// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"fmt"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestOptionAdmissionBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Options)
		code e.Code
	}{
		{"startup default", func(o *Options) { o.StartupTimeout = 0 }, ""},
		{"startup maximum", func(o *Options) { o.StartupTimeout = 12 * time.Hour }, ""},
		{"startup over maximum", func(o *Options) { o.StartupTimeout = 12*time.Hour + 1 }, e.CodeUsage},
		{"startup negative", func(o *Options) { o.StartupTimeout = -1 }, e.CodeUsage},
		{"command maximum", func(o *Options) { o.CommandTimeout = 5 * time.Minute }, ""},
		{"command over maximum", func(o *Options) { o.CommandTimeout = 5*time.Minute + 1 }, e.CodeUsage},
		{"command zero", func(o *Options) { o.CommandTimeout = 0 }, e.CodeUsage},
		{"cleanup maximum", func(o *Options) { o.CleanupTimeout = 10 * time.Minute }, ""},
		{"cleanup over maximum", func(o *Options) { o.CleanupTimeout = 10*time.Minute + 1 }, e.CodeUsage},
		{"cleanup zero", func(o *Options) { o.CleanupTimeout = 0 }, e.CodeUsage},
		{"run maximum", func(o *Options) { o.RunTimeout = 12 * time.Hour }, ""},
		{"run over maximum", func(o *Options) { o.RunTimeout = 12*time.Hour + 1 }, e.CodeUsage},
		{"suite maximum", func(o *Options) { o.SuiteName = strings.Repeat("s", 256) }, ""},
		{"suite too long", func(o *Options) { o.SuiteName = strings.Repeat("s", 257) }, e.CodeUsage},
		{"suite whitespace", func(o *Options) { o.SuiteName = " suite" }, e.CodeUsage},
		{"workspace maximum", func(o *Options) { o.WorkspaceRoot = strings.Repeat("w", 4096) }, ""},
		{"workspace too long", func(o *Options) { o.WorkspaceRoot = strings.Repeat("w", 4097) }, e.CodeUsage},
		{"workspace control", func(o *Options) { o.WorkspaceRoot = "/private\nroot" }, e.CodeUsage},
		{"missing kubeconfig pin", func(o *Options) { o.Kubeconfig.Digest = "" }, e.CodeUnpinnedInput},
		{"prerelease image", func(o *Options) { o.ImageVersion = "1.35.1-rc.1" }, e.CodeUsage},
		{"metadata image", func(o *Options) { o.ImageVersion = "1.35.1+custom" }, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := optionsFixture()
			tc.edit(&o)
			_, err := validateOptions(o)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireCode(t, err, tc.code)
			}
		})
	}
}
func TestDeploymentTargetBounds(t *testing.T) {
	for _, count := range []int{0, 1, 64, 65} {
		t.Run(fmt.Sprintf("targets %d", count), func(t *testing.T) {
			targets := make([]DeploymentTarget, count)
			for i := range targets {
				targets[i] = optionsFixture().Targets[0]
				targets[i].LabelSelector = fmt.Sprintf("app=target-%d", i)
			}
			err := validateTargetShape(targets)
			if count == 0 || count > 64 {
				requireCode(t, err, e.CodeUsage)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, count := range []int{0, 1, 10000, 10001} {
		t.Run(fmt.Sprintf("replicas %d", count), func(t *testing.T) {
			targets := optionsFixture().Targets
			targets[0].RequiredReplicas = count
			err := validateTargetShape(targets)
			if count == 0 || count > 10000 {
				requireCode(t, err, e.CodeUsage)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

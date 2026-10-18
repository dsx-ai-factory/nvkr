// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

func TestWalkthroughFlagOverridesConfigurationAndHelpDoesNotExecute(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		root := newCommand(&bytes.Buffer{}, &bytes.Buffer{}, demoActions{})
		command, _, err := root.Find([]string{"e2e"})
		if err != nil {
			t.Fatal(err)
		}
		flag := "--walkthrough=false"
		if enabled {
			flag = "--walkthrough"
		}
		if err := command.ParseFlags([]string{flag}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := command.Help(); err != nil {
			t.Fatal(err)
		}
		root.SetOut(&out)
		if err := command.Help(); err != nil || !strings.Contains(out.String(), "--walkthrough") {
			t.Fatal("flag absent from help", err)
		}
		selected, err := configuredFile(command, "/fixture", nvkedemo.DemoFile{Run: nvkedemo.RunFile{Walkthrough: enabled}}, demoActions{load: func(_ context.Context, _ string) (nvkedemo.DemoFile, error) {
			return nvkedemo.DemoFile{Version: 1,
				Build: nvkedemo.BuildFile{Repo: "/fixture/repo", Attempt: "fresh", Jobs: 1},
				Run:   nvkedemo.RunFile{Walkthrough: !enabled, Out: "/fixture/out", TimeoutSeconds: nvkedemo.DefaultTenantTimeoutSeconds}}, nil
		}})
		if err != nil || selected.Run.Walkthrough != enabled {
			t.Fatal("explicit flag did not override YAML", err)
		}
		_, run, err := selected.E2EConfig(io.Discard)
		if err != nil || run.Walkthrough != enabled {
			t.Fatal("explicit flag was lost before deployment selection", err)
		}
	}
}

func TestPlainWalkthroughPreservesSudoAuthenticationCallback(t *testing.T) {
	var out bytes.Buffer
	calls := 0
	refresh := func(context.Context) error { calls++; return nil }
	err := presentProgress(t.Context(), &out, nil, false, "", true, func(ctx context.Context, progress io.Writer) error {
		auth := progress.(nvkedemo.SudoAuthorizer).SudoAuthentication()
		if auth == nil {
			t.Fatal("plain presentation lost the selected terminal authentication callback")
		}
		return auth(ctx)
	}, refresh)
	if err != nil || calls != 1 {
		t.Fatal("plain presentation changed authentication", err, calls)
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"errors"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestResolveGitHubAuthAction(t *testing.T) {
	const sha = "7c6bc770dae815cd3e89ee6cdf493a5fab2cc093"
	for _, test := range []struct {
		name string
		pins map[string]catalog.ActionPin
		want string
		code e.Code
	}{
		{name: "major 3", pins: map[string]catalog.ActionPin{"google-auth": {Repo: "google-github-actions/auth", SHA: sha, Version: "v3.0.0"}},
			want: "google-github-actions/auth@" + sha},
		{name: "missing", pins: map[string]catalog.ActionPin{}, code: e.CodeUnpinnedSource},
		{name: "another repository", pins: map[string]catalog.ActionPin{"google-auth": {Repo: "example/auth", SHA: sha, Version: "v3.0.0"}},
			code: e.CodeEngineCapability},
		{name: "another major", pins: map[string]catalog.ActionPin{"google-auth": {Repo: "google-github-actions/auth", SHA: sha, Version: "v2.1.13"}},
			code: e.CodeEngineCapability},
		{name: "short commit", pins: map[string]catalog.ActionPin{"google-auth": {Repo: "google-github-actions/auth", SHA: sha[:12], Version: "v3.0.0"}},
			code: e.CodeUnpinnedSource},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveGitHubAuthAction(test.pins)
			if test.code == "" {
				if err != nil || got != test.want {
					t.Fatalf("got %q, %v; want %q", got, err, test.want)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("got %v; want %s", err, test.code)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package containerruntime

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSelect(t *testing.T) {
	for _, tc := range []struct {
		name, environment, override, want string
		invalid                           bool
	}{
		{name: "default", want: "docker"},
		{name: "Docker", environment: "docker", want: "docker"},
		{name: "Podman", environment: "podman", want: "podman"},
		{name: "path", environment: "podman", override: "/opt/bin/podman", want: "/opt/bin/podman"},
		{name: "default path", override: "/opt/bin/docker", want: "/opt/bin/docker"},
		{name: "conflict", environment: "podman", override: "docker", invalid: true},
		{name: "default conflict", override: "podman", invalid: true},
		{name: "unknown override", override: "nerdctl", invalid: true},
		{name: "unknown Environment", environment: "containerd", invalid: true},
		{name: "Environment path", environment: "/opt/bin/docker", invalid: true},
		{name: "whitespace", environment: " docker ", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Select(tc.environment, tc.override)
			if tc.invalid {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability || got != "" {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

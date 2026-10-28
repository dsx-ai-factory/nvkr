// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRunRuntimeAdmissionPrecedesCatalogResolution(t *testing.T) {
	root := t.TempDir()
	environment := filepath.Join(root, "environment.json")
	raw := `{"apiVersion":"nvkr.dev/environment/v1","kind":"Environment","metadata":{"name":"test"},"utilityPlatform":"linux/amd64","utilityContainer":"busybox@sha256:` + strings.Repeat("a", 64) + `"}`
	if err := os.WriteFile(environment, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		extra []string
		code  e.Code
	}{
		{"unsupported Environment", nil, e.CodeEngineCapability},
		{"independent qualifier", []string{"--qualification-policy", "missing-policy.yaml"}, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"run", "generate", "--definition", "missing-definition.yaml", "--environment", environment, "--runtime"}
			_, err := invokeLifecycleCLI(t, append(args, test.extra...)...)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("got %v, want admission code %s before missing Definition/catalog work", err, test.code)
			}
		})
	}
}

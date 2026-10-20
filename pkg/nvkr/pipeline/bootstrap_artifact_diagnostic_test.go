// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBootstrapHostAdmission(t *testing.T) {
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" && os.Geteuid() == 0 {
		t.Skip("host may provide the real native route")
	}
	err := AdmitArtifactDiagnostic(t.Context(), ArtifactDiagnostic{Suite: "bootstrap", Platform: "linux/amd64", ControllerImage: "support:fixture"})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("unsupported host admitted: %v", err)
	}
}

func TestBootstrapExecutionUsesOriginalTargetAndSuppliedPath(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("owned process-group runtime")
	}
	for _, tc := range []struct {
		name, body string
		timeout    time.Duration
		code       e.Code
	}{
		{name: "success", body: "exit 0\n"},
		{name: "suite failure", body: "exit 7\n", code: e.CodeBuildFailed},
		{name: "timeout", body: "exec sleep 30\n", timeout: 100 * time.Millisecond, code: e.CodeTimeout},
		{name: "failure before cleanup deadline", body: "trap '' TERM\nsleep 0.5\nexit 7\n", timeout: 200 * time.Millisecond, code: e.CodeBuildFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cli := filepath.Join(root, "nvke", "cli")
			if err := os.MkdirAll(cli, 0o700); err != nil {
				t.Fatal(err)
			}
			tools := filepath.Join(root, "tools")
			if err := os.Mkdir(tools, 0o700); err != nil {
				t.Fatal(err)
			}
			makeScript := `#!/bin/sh
printf '%s\n' "$PWD" "$*" "$NVKECTL" "$NVKECTL_EXPECTED_VERSION" "$NVKECTL_CONTROLLER_IMAGE" "$NVKECTL_E2E_REPORT"
if [ -n "${MAKEFILES:-}" ]; then exit 91; fi
` + tc.body
			if err := os.MkdirAll(filepath.Join(cli, "e2e", "bootstrap"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cli, "e2e", "bootstrap", "run.sh"), []byte(makeScript), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("NVKECTL", "wrong-default")
			t.Setenv("MAKEFILES", "must-not-run.mk")
			var output bytes.Buffer
			input := ArtifactDiagnostic{Workspace: root, Artifact: filepath.Join(root, "staged supplied cli"), Suite: "bootstrap", Version: "candidate", ControllerImage: "support:fixture", Timeout: tc.timeout}
			err := executeBootstrapArtifactDiagnostic(t.Context(), input, &output, &output)
			if tc.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code {
					t.Fatalf("wrong primary outcome: %v %s", err, output.String())
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.code != e.CodeTimeout {
				for _, want := range []string{input.Artifact, input.Version, input.ControllerImage, filepath.Join(root, "bootstrap.json")} {
					if !strings.Contains(output.String(), want) {
						t.Fatalf("missing %q: %s", want, output.String())
					}
				}

			}
			if strings.Contains(output.String(), "wrong-default") {
				t.Fatal("substituted supplied subject")
			}
			raw, err := os.ReadFile(filepath.Join(root, "bootstrap.log"))
			if err != nil || string(raw) != output.String() {
				t.Fatalf("raw capture lost: %v", err)
			}
		})
	}
}

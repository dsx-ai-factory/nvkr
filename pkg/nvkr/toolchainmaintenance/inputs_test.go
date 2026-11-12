// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPreparationInputs(t *testing.T) {
	pin, err := os.ReadFile("../../../.go-version")
	if err != nil {
		t.Fatal(err)
	}
	goVersion := strings.TrimSpace(string(pin))
	for _, tc := range []struct {
		name, path, content string
		allowed             bool
	}{
		{"archive transformation", ".gitattributes", "pkg/helper/helper.go export-ignore\n", false},
		{"root checkout attributes", ".gitattributes", "*.txt text eol=crlf\n", false},
		{"ancestor checkout attributes", "pkg/.gitattributes", "*.txt text eol=crlf\n", false},
		{"unchanged", "", "", true},
		{"unrelated package", "pkg/unrelated/file.go", "package unrelated\nconst Value = 2\n", true},
		{"closure package", "pkg/helper/helper.go", "package helper\nconst Value = 2\n", false},
		{"embedded asset", "pkg/helper/data/value.txt", "changed", false},
		{"new package input", "pkg/helper/new.go", "package helper\nconst New = 1\n", false},
		{"arm64 closure", "internal/arm/arm.go", "package arm\nconst Value = 2\n", false},
		{"removed dependency", "cmd/nvkr-disk-inspect/main.go", "package main\nfunc main() {}\n", false},
		{"added dependency", "cmd/nvkr-disk-inspect/main.go", "package main\nimport _ \"example.test/inputs/pkg/unrelated\"\nfunc main() {}\n", false},
		{"module", "go.mod", "module example.test/inputs\n\ngo 1.27.0\n", false},
		{"workspace", "go.work", "go " + goVersion + "\nuse .\n", false},
		{"vendor", "vendor/modules.txt", "changed", false},
		{"sums", "go.sum", "changed", false},
		{"compiler", ".go-version", "1.0.0\n", false},
		{"packer", "toolchains/nvkr-packer/Dockerfile", "changed", false},
		{"inspector", "toolchains/nvkr-disk-inspect/Dockerfile", "changed", false},
		{"preparation", "scripts/prepare-native-toolchains.sh", "changed", false},
		{"list failure", "pkg/helper/helper.go", "not valid Go", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				out, err := command(t.Context(), root, "git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(string(out))
			}
			write := func(path, content string) {
				t.Helper()
				full := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("init", "-q")
			git("config", "user.name", "Fixture")
			git("config", "user.email", "fixture@example.test")
			for path, content := range map[string]string{
				"go.mod": "module example.test/inputs\n\ngo " + goVersion + "\n", "go.sum": "", ".go-version": goVersion + "\n",
				"cmd/nvkr-disk-inspect/main.go":         "package main\nimport _ \"example.test/inputs/pkg/helper\"\nfunc main() {}\n",
				"cmd/nvkr-disk-inspect/native_arm64.go": "package main\nimport _ \"example.test/inputs/internal/arm\"\n",
				"internal/arm/arm.go":                   "package arm\nconst Value = 1\n",
				"pkg/helper/helper.go":                  "package helper\nimport _ \"embed\"\n//go:embed data/value.txt\nvar Value string\n",
				"pkg/helper/data/value.txt":             "original",
				"pkg/unrelated/file.go":                 "package unrelated\nconst Value = 1\n",
				"toolchains/nvkr-packer/Dockerfile":     "original", "toolchains/nvkr-disk-inspect/Dockerfile": "original", "scripts/prepare-native-toolchains.sh": "original",
			} {
				write(path, content)
			}
			git("add", ".")
			git("-c", "commit.gpgsign=false", "commit", "-qm", "source")
			source := git("rev-parse", "HEAD")
			if tc.path != "" {
				write(tc.path, tc.content)
				git("add", ".")
				git("-c", "commit.gpgsign=false", "commit", "-qm", "head")
			}
			err := checkPreparationInputs(t.Context(), root, source)
			if tc.allowed {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, e.CodeGateUnmet)
			}
			// A missing historical commit can never be interpreted as no changes.
			assertCode(t, checkPreparationInputs(t.Context(), root, strings.Repeat("f", 40)), e.CodeGateUnmet)
		})
	}
}

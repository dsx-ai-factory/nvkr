// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPrivateCIUploadCommandAdmitsBeforePathsOrTransfer(t *testing.T) {
	for _, scenario := range []string{"missing token", "missing username", "invalid repository", "invalid artifact", "missing output", "symlink output", "symlink directory", "canceled", "empty optional upload"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			output := filepath.Join(root, "provider-output")
			if err := os.WriteFile(output, []byte("retained=value\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			index := filepath.Join(root, "index")
			if err := os.Mkdir(index, 0o700); err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com", "GITHUB_REPOSITORY": "Example/Source", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2", "NVKR_CI_STORAGE_TOKEN": "private-read-write", "NVKR_CI_STORAGE_USER": "Example", "GITHUB_OUTPUT": output} {
				t.Setenv(key, value)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			name := "carrier"
			code := e.CodeImmutableViolation
			switch scenario {
			case "missing token":
				t.Setenv("NVKR_CI_STORAGE_TOKEN", "")
				code = e.CodeTrustPolicyMissing
			case "missing username":
				t.Setenv("NVKR_CI_STORAGE_USER", "")
				code = e.CodeTrustPolicyMissing
			case "invalid repository":
				t.Setenv("GITHUB_REPOSITORY", "../invalid")
				code = e.CodeUsage
			case "invalid artifact":
				name, code = "../escape", e.CodeUsage
			case "missing output":
				t.Setenv("GITHUB_OUTPUT", "")
				code = e.CodeUsage
			case "symlink output":
				link := filepath.Join(root, "output-link")
				if err := os.Symlink(output, link); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GITHUB_OUTPUT", link)
			case "symlink directory":
				link := index + "-link"
				if err := os.Symlink(index, link); err != nil {
					t.Fatal(err)
				}
				index = link
			case "canceled":
				cancel()
				code = e.CodeTimeout
			}
			app := newApp()
			var stdout, stderr bytes.Buffer
			app.Writer, app.ErrWriter = &stdout, &stderr
			err := app.RunContext(ctx, []string{"nvkr", "factory", "github", "ci-upload", "--name", name, "--path", "not-present", "--out", index, "--if-no-files-found", "ignore"})
			if scenario == "empty optional upload" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != code {
					t.Fatalf("command = %v, want %s", err, code)
				}
			}
			want := "retained=value\n"
			if scenario == "empty optional upload" {
				want += "locator_path=\n"
			}
			raw, readErr := os.ReadFile(output)
			if readErr != nil || string(raw) != want {
				t.Fatalf("rejected command changed provider output: %q, %v", raw, readErr)
			}
			entries, readErr := os.ReadDir(index)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("command created a locator without payload: %v, %v", entries, readErr)
			}
		})
	}
}

func TestPrivateCIDownloadCommandRequiresExactCrossRunAttempt(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "Example/Source")
	t.Setenv("GITHUB_RUN_ID", "123")
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")
	t.Setenv("NVKR_CI_STORAGE_TOKEN", "private-read")
	t.Setenv("NVKR_CI_STORAGE_USER", "storage-reader")
	for _, flags := range [][]string{{"--run-id", "456"}, {"--attempt", "3"}, {"--run-id", "00", "--attempt", "3"}} {
		_, err := invokeLifecycleCLI(t, append([]string{"factory", "github", "ci-download", "--index-dir", "/missing", "--destination", "/must-not-create"}, flags...)...)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
			t.Fatalf("inexact cross-Run request = %v", err)
		}
	}
}

func TestPrivateCILocatorCommandAdmitsBeforeFilesystemOrNetwork(t *testing.T) {
	for _, scenario := range []struct {
		name, server, token string
		flags               []string
		code                e.Code
	}{
		{"wrong server", "https://example.com", "repository-reader", nil, e.CodeEngineCapability},
		{"unpaired run", "https://github.com", "repository-reader", []string{"--run-id", "456"}, e.CodeUsage},
		{"unpaired attempt", "https://github.com", "repository-reader", []string{"--attempt", "2"}, e.CodeUsage},
		{"wildcard", "https://github.com", "repository-reader", []string{"--pattern", "*"}, e.CodeUsage},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("GITHUB_ACTIONS", "true")
			t.Setenv("GITHUB_SERVER_URL", scenario.server)
			t.Setenv("GITHUB_REPOSITORY", "Example/Source")
			t.Setenv("GITHUB_RUN_ID", "123")
			t.Setenv("GITHUB_RUN_ATTEMPT", "1")
			t.Setenv("NVKR_CI_REPOSITORY_TOKEN", scenario.token)
			_, err := invokeLifecycleCLI(t, append([]string{"factory", "github", "ci-fetch-locators", "--index-dir", "/must-not-create"}, scenario.flags...)...)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != scenario.code {
				t.Fatalf("locator admission = %v, want %s", err, scenario.code)
			}
		})
	}
}

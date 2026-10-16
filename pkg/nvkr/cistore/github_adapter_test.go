// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package cistore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func githubUploadFixture(t *testing.T) GitHubUpload {
	t.Helper()
	root := t.TempDir()
	writePayload(t, root, "payload", "private payload", 0o600)
	out := filepath.Join(root, "index")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	return GitHubUpload{Request: testRequest, Options: testOptions, Root: root,
		Paths: "payload", OutputDirectory: out, IfNoFilesFound: "error"}
}

func githubAdapterLocator(request Request) Locator {
	return Locator{SchemaVersion: SchemaVersion, Request: request,
		Reference:     "ghcr.io/" + strings.ToLower(request.Repository) + "/ci-artifacts@sha256:" + strings.Repeat("a", 64),
		ArchiveDigest: "sha256:" + strings.Repeat("b", 64), ArchiveSize: 10240, Paths: []string{"payload"}}
}

func TestGitHubUploadAdmissionBeforePathsAndTransfer(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*GitHubUpload)
		code e.Code
	}{
		{"empty token", func(i *GitHubUpload) { i.Options.Token = "" }, e.CodeTrustPolicyMissing},
		{"invalid repository", func(i *GitHubUpload) { i.Request.Repository = "../invalid" }, e.CodeUsage},
		{"invalid attempt", func(i *GitHubUpload) { i.Request.Attempt = "02" }, e.CodeUsage},
		{"invalid name", func(i *GitHubUpload) { i.Request.Name = "../escape" }, e.CodeUsage},
		{"invalid username", func(i *GitHubUpload) { i.Options.Username = "bad:name" }, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := githubUploadFixture(t)
			input.Root, input.Paths, input.OutputDirectory = "/not-present", "missing", "/not-present-index"
			input.IfNoFilesFound = "ignore"
			tc.edit(&input)
			path, err := uploadGitHub(t.Context(), input, func(context.Context, Request, string, []string, Options) (Locator, error) {
				t.Fatal("invalid transfer reached payload storage")
				return Locator{}, nil
			})
			assertCode(t, err, tc.code)
			if path != "" {
				t.Fatal("invalid transfer exposed a locator path")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := UploadGitHub(ctx, GitHubUpload{})
	assertCode(t, err, e.CodeTimeout)
}

func TestGitHubUploadHoldsDirectoryAndPublishesWithoutReplacement(t *testing.T) {
	for _, scenario := range []string{"success", "symlink directory", "existing locator", "directory replacement", "racing locator", "canceled transfer"} {
		t.Run(scenario, func(t *testing.T) {
			input := githubUploadFixture(t)
			locatorPath := filepath.Join(input.OutputDirectory, input.Request.Name+locatorSuffix)
			if scenario == "symlink directory" {
				target := input.OutputDirectory
				input.OutputDirectory += "-link"
				if err := os.Symlink(target, input.OutputDirectory); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "existing locator" {
				writePayload(t, input.OutputDirectory, input.Request.Name+locatorSuffix, "retained", 0o600)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			path, err := uploadGitHub(ctx, input, func(ctx context.Context, request Request, root string, paths []string, options Options) (Locator, error) {
				called = true
				deadline, bounded := ctx.Deadline()
				if !bounded || time.Until(deadline) > 45*time.Minute || request != input.Request || root != input.Root || len(paths) != 1 || paths[0] != "payload" || options.Token != testOptions.Token {
					t.Fatal("transfer lost its exact paths, identity, credentials or timeout")
				}
				switch scenario {
				case "directory replacement":
					if err := os.Rename(input.OutputDirectory, input.OutputDirectory+"-old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(input.OutputDirectory, 0o700); err != nil {
						t.Fatal(err)
					}
				case "racing locator":
					writePayload(t, input.OutputDirectory, input.Request.Name+locatorSuffix, "retained", 0o600)
				case "canceled transfer":
					cancel()
				}
				return githubAdapterLocator(request), nil
			})
			if scenario == "success" {
				if err != nil || path != locatorPath || !called {
					t.Fatalf("locator publication = %s, %v", path, err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				locator, err := DecodeLocator(raw)
				if err != nil || locator.Request != input.Request {
					t.Fatalf("published locator = %v, %v", locator, err)
				}
			} else {
				code := e.CodeImmutableViolation
				if scenario == "canceled transfer" {
					code = e.CodeTimeout
				}
				assertCode(t, err, code)
				if path != "" || ((scenario == "symlink directory" || scenario == "existing locator") && called) {
					t.Fatal("rejected destination reached storage or exposed a locator")
				}
			}
			entries, err := os.ReadDir(input.OutputDirectory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".nvkr-download-") {
					t.Fatal("locator staging leaked after publication or failure")
				}
			}
			if scenario == "existing locator" || scenario == "racing locator" {
				raw, err := os.ReadFile(locatorPath)
				if err != nil || string(raw) != "retained" {
					t.Fatal("locator publication replaced an existing file")
				}
			}
		})
	}
}

func TestGitHubDownloadRejectsInvalidContextAndMalformedIndexBeforeNetwork(t *testing.T) {
	for _, scenario := range []string{"empty token", "invalid run", "canceled", "non-locator", "malformed", "wrong attempt", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			index := t.TempDir()
			request, options := testRequest, testOptions
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			code := e.CodeVerifyFailed
			switch scenario {
			case "empty token":
				options.Token, code = "", e.CodeTrustPolicyMissing
				// Metadata needs no token; a validated private locator must still
				// reject missing credentials before any network or output work.
				raw, err := canonical.Marshal(githubAdapterLocator(request))
				if err != nil {
					t.Fatal(err)
				}
				writePayload(t, index, request.Name+locatorSuffix, string(raw), 0o600)
			case "invalid run":
				request.RunID, index, code = "00", "/missing-index", e.CodeUsage
			case "canceled":
				cancel()
				index, code = "/missing-index", e.CodeTimeout
			case "non-locator":
				writePayload(t, index, "binary", "payload", 0o600)
			case "malformed":
				writePayload(t, index, request.Name+locatorSuffix, "{}", 0o600)
			case "wrong attempt":
				locator := githubAdapterLocator(request)
				locator.Request.Attempt = "1"
				raw, err := canonical.Marshal(locator)
				if err != nil {
					t.Fatal(err)
				}
				writePayload(t, index, request.Name+locatorSuffix, string(raw), 0o600)
			case "empty":
				code = e.CodeReleaseIncomplete
			}
			destination := filepath.Join(t.TempDir(), "not-created")
			err := DownloadGitHub(ctx, request, index, destination, options)
			assertCode(t, err, code)
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("rejected private index created a payload destination")
			}
		})
	}
}

func TestGitHubOutputAdmissionRejectsSymlinkFIFOAndCancellation(t *testing.T) {
	for _, scenario := range []string{"append", "symlink", "fifo", "directory", "missing", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "output")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "append", "canceled":
				writePayload(t, root, "output", "retained\n", 0o600)
			case "symlink":
				writePayload(t, root, "target", "retained\n", 0o600)
				if err := os.Symlink(filepath.Join(root, "target"), path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "canceled" {
				cancel()
			}
			start := time.Now()
			file, err := OpenGitHubOutput(ctx, path)
			if time.Since(start) > time.Second {
				t.Fatal("provider output admission blocked on a nonregular file")
			}
			if scenario != "append" {
				code := e.CodeImmutableViolation
				if scenario == "canceled" {
					code = e.CodeTimeout
				}
				assertCode(t, err, code)
				if file != nil {
					t.Fatal("invalid output returned a writable descriptor")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := WriteLocatorOutput(file, "/private/locator.json"); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != "retained\nlocator_path=/private/locator.json\n" {
				t.Fatalf("provider output append changed existing bytes: %q %v", raw, err)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestImageAdmissionRoutes(t *testing.T) {
	subject := "ghcr.io/example/private@sha256:" + strings.Repeat("a", 64)
	id := "sha256:" + strings.Repeat("b", 64)
	valid := fmt.Sprintf(`{"Id":%q,"RepoDigests":[%q],"Os":"linux","Architecture":"amd64"}`, id, subject)
	for _, tc := range []struct {
		name     string
		access   ImageAccess
		missing  bool
		document string
		want     e.Code
		pulls    int
	}{
		{"private prepared", ImageScopedReader, false, valid, "", 0},
		{"private absent", ImageScopedReader, true, valid, e.CodeEngineCapability, 0},
		{"default absent", ImagePreparedOnly, true, valid, e.CodeEngineCapability, 0},
		{"anonymous absent", ImageAnonymous, true, valid, "", 1},
		{"anonymous prepared", ImageAnonymous, false, valid, "", 0},
		{"wrong platform", ImageScopedReader, false, strings.Replace(valid, "amd64", "arm64", 1), e.CodePreflightFailed, 0},
		{"sibling repository", ImageScopedReader, false, strings.Replace(valid, "example/private", "other/private", 1), e.CodePreflightFailed, 0},
		{"wrong manifest", ImageAnonymous, false, strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("c", 64), 1), e.CodePreflightFailed, 0},
		{"missing config ID", ImageScopedReader, false, strings.Replace(valid, id, "", 1), e.CodePreflightFailed, 0},
		{"unknown route", "guess", false, valid, e.CodeEngineCapability, 0},
		{"empty route", "", false, valid, e.CodeEngineCapability, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, pulls := 0, 0
			receipt, err := admitContainerImage(t.Context(), subject, "linux/amd64", tc.access, "observed-runtime", func(ctx context.Context, args ...string) ([]byte, error) {
				if _, bounded := ctx.Deadline(); !bounded {
					t.Fatal("unbounded materialization")
				}
				calls++
				if args[0] == "pull" {
					pulls++
					return nil, nil
				}
				if calls == 1 && tc.missing {
					return nil, errors.New("absent")
				}
				return []byte(tc.document), nil
			})
			if pulls != tc.pulls {
				t.Fatalf("pulls=%d want %d", pulls, tc.pulls)
			}
			if tc.want != "" {
				if receipt != nil || code(err) != tc.want {
					t.Fatalf("receipt=%v err=%v", receipt, err)
				}
				return
			}
			if err != nil || receipt.digest == receipt.configID || receipt.configID != id {
				t.Fatalf("manifest/config identities conflated: %v %v", receipt, err)
			}
		})
	}
}

func imageAdmissionRuntimeFixture(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	subject := "ghcr.io/example/private@sha256:" + strings.Repeat("a", 64)
	id := "sha256:" + strings.Repeat("b", 64)
	files := map[string]string{
		"daemon": `{"ID":"daemon-a","OSType":"linux","Architecture":"amd64"}`,
		"image":  fmt.Sprintf(`{"Id":%q,"RepoDigests":[%q],"Os":"linux","Architecture":"amd64"}`, id, subject),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$1" in
info) cat %q;;
version) printf '%%s\n' '{}';;
image) cat %q;;
run|create) case " $* " in *' --pull=never '*) exit 0;; *) exit 93;; esac;;
*) exit 94;;
esac
`, filepath.Join(dir, "calls"), filepath.Join(dir, "daemon"), filepath.Join(dir, "image"))
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, subject, dir
}

func TestPreparedImageReceiptReauthenticatesBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
		want   e.Code
	}{
		{"prepared after credentials destroyed", func(t *testing.T, dir string) {
			credential := filepath.Join(dir, "config.json")
			if err := os.WriteFile(credential, []byte(`{"auths":{"ghcr.io":{"auth":"synthetic"}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(credential); err != nil {
				t.Fatal(err)
			}
		}, ""},
		{"wrong runtime", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "daemon"), []byte(`{"ID":"daemon-b","OSType":"linux","Architecture":"amd64"}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, e.CodePreflightFailed},
		{"evicted image", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "image")); err != nil {
				t.Fatal(err)
			}
		}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, subject, dir := imageAdmissionRuntimeFixture(t)
			receipt, err := PrepareContainerImage(t.Context(), path, subject, "linux/amd64", ImageScopedReader)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, dir)
			err = runContainerRuntime(RunOptions{Ctx: t.Context(), Runtime: path, preparedImage: receipt, Stdout: io.Discard, Stderr: io.Discard}, "run", subject, "true")
			if tc.want == "" && err != nil || tc.want != "" && code(err) != tc.want {
				t.Fatalf("execution: %v", err)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "pull --") {
				t.Fatal("prepared consumer pulled")
			}
			if strings.Contains(string(calls), "run --pull=never") != (tc.want == "") {
				t.Fatalf("execution before admission: %s", calls)
			}
			if code((&PreparedImage{}).RequireFor(t.Context(), path, subject, "linux/amd64")) != e.CodePreflightFailed {
				t.Fatal("forged receipt accepted")
			}
			if code(receipt.RequireFor(t.Context(), path, subject, "linux/arm64")) != e.CodePreflightFailed {
				t.Fatal("receipt reused for another platform")
			}
		})
	}
}

func TestPreparedImageRepositorySpelling(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		subject, observed string
		match             bool
	}{
		{"busybox", "docker.io/library/busybox", true},
		{"docker.io/library/busybox", "busybox", true},
		{"ghcr.io/owner/tool", "ghcr.io/other/tool", false},
		{"docker.io/library/busybox", "ghcr.io/library/busybox", false},
	} {
		t.Run(tc.subject+"/"+tc.observed, func(t *testing.T) {
			if got := sameImageSubject(tc.subject+digest, tc.observed+digest); got != tc.match {
				t.Fatalf("subject match=%t", got)
			}
		})
	}
}

func TestFetchImageAdmissionRequiresVendorCommand(t *testing.T) {
	for _, vendoring := range []bool{false, true} {
		t.Run(fmt.Sprint("vendoring=", vendoring), func(t *testing.T) {
			base, _, opts := pristineClosureFixture(t)
			runtimePath, subject, root := imageAdmissionRuntimeFixture(t)
			daemon := fmt.Sprintf(`{"ID":"daemon-a","OSType":"linux","Architecture":%q}`, runtime.GOARCH)
			if err := os.WriteFile(filepath.Join(root, "daemon"), []byte(daemon), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(root, "image")); err != nil {
				t.Fatal(err)
			}
			node := testFetchNode(base.ID, "https://example.test/source", base.Commit, base.TreeHash, base.Dest, subject, "")
			if vendoring {
				node.VendorCmd = []string{"vendor"}
				var err error
				node.VendorSpecDigest, err = ir.VendorSpecIdentity(node.VendorCmd, node.VendorImage)
				if err != nil {
					t.Fatal(err)
				}
			}
			opts.Launch, opts.Runtime, opts.ImageAccess = true, runtimePath, ImageScopedReader
			opts.Stdout, opts.Stderr = io.Discard, io.Discard
			_, err := (Local{}).ExecNode(node, opts)
			if vendoring {
				if code(err) != e.CodeEngineCapability {
					t.Fatalf("missing vendor image accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "calls")); !os.IsNotExist(err) {
				t.Fatal("fetch without vendoring invoked image runtime")
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"go.yaml.in/yaml/v3"
)

func TestOSAgentAcquisitionUsesGoTransportFallback(t *testing.T) {
	root := filepath.Dir(filepath.Dir(demoScript(t, "nke-os-local.sh")))
	raw, err := os.ReadFile(filepath.Join(root, "definitions/nke-os/catalog/registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Sources map[string]struct {
			VendorCommand []string `yaml:"vendorCommand"`
		}
	}
	if err := yaml.Unmarshal(raw, &registry); err != nil {
		t.Fatal(err)
	}
	command := registry.Sources["nke-os"].VendorCommand
	if len(command) != 3 || command[0] != "bash" || command[1] != "-ec" {
		t.Fatalf("unexpected agent preparation command: %v", command)
	}
	overlay, err := catalog.LoadOverlayContext(t.Context(), filepath.Join(root, "definitions/nke-os/arm64-kvm.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	producer := overlay.Components["nke-node-image"].Component
	if producer == nil || !slices.Equal(producer.VendorCommand, command) {
		t.Fatal("ARM64 KVM producer must retain the same tested acquisition command")
	}
	const module = "example.com/material"
	const version = "v1.0.0"
	const mod = "module " + module + "\n\ngo 1.27.0\n"
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	for _, file := range []struct{ name, body string }{
		{"go.mod", mod}, {"material.go", "package material\nconst Value = 1\n"},
	} {
		f, err := w.Create(module + "@" + version + "/" + file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var failures, zipRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + module + "/@v/" + version + ".info":
			_, _ = fmt.Fprint(w, `{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`)
		case "/" + module + "/@v/" + version + ".mod":
			_, _ = fmt.Fprint(w, mod)
		case "/" + module + "/@v/" + version + ".zip":
			mu.Lock()
			zipRequests++
			fail := zipRequests <= failures
			mu.Unlock()
			if fail {
				http.Error(w, "temporary fixture failure", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	// Only the fixture disables sumdb: Go computes the known fixture's hashes,
	// and every subsequent download must match those pinned go.sum entries.
	baseEnv := append(os.Environ(), "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local",
		"GOFLAGS=-mod=readonly", "GOPROXY="+server.URL, "GOSUMDB=off",
		"GOPRIVATE=", "GONOPROXY=", "GONOSUMDB=", "GOVCS=*:off")
	cacheEnv := func(t *testing.T, dir string) []string {
		t.Helper()
		env := append(slices.Clone(baseEnv), "GOMODCACHE="+filepath.Join(dir, "cache"))
		// Go's module cache has read-only directories. Let its own cleaner
		// release this exact fixture cache before TempDir cleanup.
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "clean", "-modcache")
			cmd.Dir, cmd.Env = dir, env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("clean fixture cache: %v: %s", err, out)
			}
		})
		return env
	}
	bootstrap := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "go", "mod", "download", "-json", module+"@"+version)
	cmd.Dir = bootstrap
	cmd.Env = cacheEnv(t, bootstrap)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap known module checksums: %v: %s", err, out)
	}
	var sums struct{ Sum, GoModSum string }
	if err := json.Unmarshal(out, &sums); err != nil || sums.Sum == "" || sums.GoModSum == "" {
		t.Fatalf("known fixture checksum output: %v: %s", err, out)
	}
	for _, test := range []struct {
		name         string
		failures     int
		wrongSum     bool
		wantRequests int
		wantError    string
	}{
		{name: "initial success", wantRequests: 1},
		{name: "two transient failures recover", failures: 2, wantRequests: 3},
		{name: "transport retries exhaust", failures: 3, wantRequests: 3, wantError: "503"},
		{name: "checksum failure is not retried", wrongSum: true, wantRequests: 1, wantError: "checksum mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mu.Lock()
			failures, zipRequests = test.failures, 0
			mu.Unlock()
			dir := t.TempDir()
			sum := sums.Sum
			if test.wrongSum {
				sum = "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
			}
			pinned := fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", module, version, sum, module, version, sums.GoModSum)
			for name, body := range map[string]string{
				"agent/go.mod":  "module example.com/agent\n\ngo 1.27.0\n\nrequire " + module + " " + version + "\n",
				"agent/go.sum":  pinned,
				"agent/main.go": "package agent\nimport \"" + module + "\"\nvar Value = material.Value\n",
				// The host may not have timeout (macOS). Assert its exact argv
				// here; bound all real fixture Go commands with the test context.
				"bin/timeout": "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$TIMEOUT_CALLS\"\nshift 2\nexec \"$@\"\n",
			} {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			env := append(cacheEnv(t, dir),
				"PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"), "TIMEOUT_CALLS="+filepath.Join(dir, "timeouts"),
				"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
			for _, args := range [][]string{{"init", "-q"}, {"add", "agent"},
				{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "pinned fixture"}} {
				cmd := exec.CommandContext(t.Context(), "git", args...)
				cmd.Dir, cmd.Env = dir, env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fixture git: %v: %s", err, out)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			// Substitute only the public proxy URL with the fixture, retaining
			// the exact authored fallback syntax, verification and vendoring.
			cmd := exec.CommandContext(ctx, command[0], command[1], strings.ReplaceAll(command[2], "https://proxy.golang.org", server.URL))
			cmd.Dir, cmd.Env = dir, env
			out, err := cmd.CombinedOutput()
			if (err != nil) != (test.wantError != "") || (test.wantError != "" && !strings.Contains(string(out), test.wantError)) {
				t.Fatalf("preparation error=%v, want %q: %s", err, test.wantError, out)
			}
			mu.Lock()
			gotRequests := zipRequests
			mu.Unlock()
			if gotRequests != test.wantRequests {
				t.Fatalf("zip requests=%d, want %d", gotRequests, test.wantRequests)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "timeouts"))
			if err != nil {
				t.Fatal(err)
			}
			want := "--kill-after=10s 540s go mod download\n"
			if test.wantError == "" {
				want += "--kill-after=10s 60s go mod verify\n--kill-after=10s 180s go mod vendor\n"
				if _, err := os.Stat(filepath.Join(dir, "agent/vendor/example.com/material/material.go")); err != nil {
					t.Fatalf("offline vendoring did not produce fixture module: %v", err)
				}
			}
			if string(calls) != want {
				t.Fatalf("unexpected execution/retry boundaries: %s", calls)
			}
			actual, err := os.ReadFile(filepath.Join(dir, "agent/go.sum"))
			if err != nil || string(actual) != pinned {
				t.Fatalf("pinned sums changed: %v: %s", err, actual)
			}
		})
	}
}

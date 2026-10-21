// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nativeUIFixture(t *testing.T) (installer, work string) {
	t.Helper()
	root := privateTestDir(t)
	installer, work = filepath.Join(root, "selected-installer"), filepath.Join(root, "runtime")
	ui := filepath.Join(installer, "controller", "nico-sim", "ui")
	for _, dir := range []string{ui, work, filepath.Join(ui, "vendor")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"index.html":       "<!doctype html><title>selected UI fixture</title>",
		"app.js":           "/* exact selected app fixture */",
		"vendor/style.css": "/* exact nested asset fixture */",
	} {
		if err := os.WriteFile(filepath.Join(ui, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return installer, work
}

func TestNativeSimulatorUIRelativeHTTPBytes(t *testing.T) {
	installer, work := nativeUIFixture(t)
	if err := prepareNativeSimulatorUI(t.Context(), installer, work); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(work, "ui"))
	if err != nil || filepath.IsAbs(link) {
		t.Fatalf("require a relative selected-asset view: %v", err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// A real child cwd exercises NICoSim's relative ServeFile/FileServer layout
	// without changing the test runner's global cwd or starting any VM.
	runner := Runner{Work: work, Env: append(NativeEnvironment(), "NVKR_TEST_NATIVE_UI_HTTP=1")}
	raw, err := runner.Capture(t.Context(), []string{program, "-test.run=^TestNativeSimulatorUIHTTPFixture$"}, "ui-http", 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("HTTP fixture result: %v", err)
	}
	for url, file := range map[string]string{
		"/ui/": "index.html", "/ui/app.js": "app.js", "/ui/vendor/style.css": "vendor/style.css",
	} {
		want, err := os.ReadFile(filepath.Join(installer, "controller", "nico-sim", "ui", file))
		if err != nil || got[url] != string(want) {
			t.Fatalf("relative HTTP asset %s differs from selection: %v", url, err)
		}
	}
}

func TestNativeSimulatorUIHTTPFixture(t *testing.T) {
	if os.Getenv("NVKR_TEST_NATIVE_UI_HTTP") != "1" {
		return
	}
	result := func() map[string]string {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, "ui/index.html")
		})
		mux.Handle("GET /ui/", http.StripPrefix("/ui/", http.FileServer(http.Dir("ui"))))
		server := httptest.NewServer(mux)
		defer server.Close()
		client := &http.Client{Timeout: 3 * time.Second}
		result := make(map[string]string)
		for _, path := range []string{"/ui/", "/ui/app.js", "/ui/vendor/style.css"} {
			response, err := client.Get(server.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			closeErr := response.Body.Close()
			if response.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
				t.Fatalf("HTTP fixture %s: status %d, read %v, close %v", path, response.StatusCode, readErr, closeErr)
			}
			result[path] = string(body)
		}
		return result
	}()
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestNativeSimulatorUIRefusesUnsafeOrExistingViews(t *testing.T) {
	for _, name := range []string{
		"canceled", "nil context", "missing index", "empty index", "symlink index", "symlink UI", "symlink installer",
		"public runtime", "symlink runtime", "existing file", "existing directory", "dangling link", "existing correct link",
	} {
		t.Run(name, func(t *testing.T) {
			installer, work := nativeUIFixture(t)
			ui := filepath.Join(installer, "controller", "nico-sim", "ui")
			entry := filepath.Join(work, "ui")
			ctx := t.Context()
			var err error
			switch name {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil context":
				ctx = nil
			case "missing index":
				err = os.Remove(filepath.Join(ui, "index.html"))
			case "empty index":
				err = os.WriteFile(filepath.Join(ui, "index.html"), nil, 0o600)
			case "symlink index":
				index := filepath.Join(ui, "index.html")
				if err = os.Rename(index, index+".original"); err == nil {
					err = os.Symlink(index+".original", index)
				}
			case "symlink UI", "symlink installer", "symlink runtime":
				path := ui
				switch name {
				case "symlink installer":
					path = installer
				case "symlink runtime":
					path = work
				}
				if err = os.Rename(path, path+".original"); err == nil {
					err = os.Symlink(path+".original", path)
				}
			case "public runtime":
				err = os.Chmod(work, 0o755)
			case "existing file":
				err = os.WriteFile(entry, []byte("do not replace"), 0o600)
			case "existing directory":
				err = os.Mkdir(entry, 0o700)
			case "dangling link":
				err = os.Symlink("missing-original", entry)
			case "existing correct link":
				err = os.Symlink(ui, entry)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, beforeErr := os.Lstat(entry)
			err = prepareNativeSimulatorUI(ctx, installer, work)
			if err == nil || name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("unsafe UI view accepted or cancellation lost: %v", err)
			}
			after, afterErr := os.Lstat(entry)
			if errors.Is(beforeErr, os.ErrNotExist) {
				if !errors.Is(afterErr, os.ErrNotExist) {
					t.Fatalf("refused request created a UI view: %v", afterErr)
				}
			} else if beforeErr != nil || afterErr != nil || !os.SameFile(before, after) {
				t.Fatalf("refused request replaced its destination: before %v, after %v", beforeErr, afterErr)
			}
		})
	}
}

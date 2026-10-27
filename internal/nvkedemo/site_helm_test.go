// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sys/unix"
)

func TestPrepareSiteHelmRejectsUnavailableBudgetBeforeFiles(t *testing.T) {
	canceled, cancel := context.WithTimeout(t.Context(), time.Minute)
	cancel()
	expired, stopExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stopExpired()
	tooLong, stopLong := context.WithTimeout(context.Background(), (time.Duration(math.MaxInt32)+2)*time.Second)
	defer stopLong()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		code  nvkrerr.Code
		cause error
	}{
		{"nil", nil, nvkrerr.CodeUsage, nil},
		{"unbounded", context.Background(), nvkrerr.CodeUsage, nil},
		{"canceled", canceled, nvkrerr.CodeTimeout, context.Canceled},
		{"expired", expired, nvkrerr.CodeTimeout, context.DeadlineExceeded},
		{"Kubernetes deadline overflow", tooLong, nvkrerr.CodeUsage, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := privateTestDir(t)
			base := Runner{Env: []string{"SYNTHETIC_CALLER=unchanged"}}
			got, args, err := prepareSiteHelm(tc.ctx, base, "/unavailable-tools", out)
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != tc.code || tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("budget admission returned %v; want %s/%v", err, tc.code, tc.cause)
			}
			entries, readErr := os.ReadDir(out)
			if readErr != nil || len(entries) != 0 || args != nil || !slices.Equal(got.Env, base.Env) {
				t.Fatal("rejected budget changed files or environment", entries, readErr)
			}
		})
	}
}

func TestPrepareSiteHelmPrivateIndependentConfiguration(t *testing.T) {
	out := privateTestDir(t)
	tools := filepath.Join(out, "selected tools")
	base := Runner{Env: []string{"PATH=/usr/bin:/bin", "HELM_PLUGINS=/synthetic/caller-plugins", "SYNTHETIC_CALLER=preserved"}}
	beforeEnv := slices.Clone(base.Env)
	t.Setenv("HELM_PLUGINS", "/synthetic/ambient-plugins")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second+500*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	before := time.Until(deadline)
	got, args, err := prepareSiteHelm(ctx, base, tools, out)
	after := time.Until(deadline)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 5 || args[0] != "--timeout" || !slices.Equal(args[2:4], []string{"--", "--post-renderer"}) {
		t.Fatal("source installer lost its Helm passthrough", args)
	}
	budget, err := time.ParseDuration(args[1])
	if err != nil || budget < after || budget > before {
		t.Fatalf("timeout=%v err=%v, caller remaining=[%v,%v]", budget, err, after, before)
	}
	if !slices.Equal(base.Env, beforeEnv) || environmentValue(got.Env, "SYNTHETIC_CALLER") != "preserved" || environmentValue(got.Env, "NVKR_SITE_KUBECTL") != filepath.Join(tools, "kubectl") {
		t.Fatal("Helm preparation changed caller state or lost the selected kubectl")
	}
	home := filepath.Join(out, "site-helm")
	for _, key := range []string{"HELM_PLUGINS", "HELM_CACHE_HOME", "HELM_CONFIG_HOME", "HELM_DATA_HOME"} {
		relative, err := filepath.Rel(home, environmentValue(got.Env, key))
		if err != nil || !filepath.IsLocal(relative) || relative == "." {
			t.Fatalf("%s escaped private Helm storage", key)
		}
	}
	if err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := fs.FileMode(0600)
		if entry.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want || !entry.IsDir() && !info.Mode().IsRegular() {
			t.Errorf("private Helm entry %s has mode %s", path, info.Mode())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareSiteHelmRefusesExistingDestination(t *testing.T) {
	for _, kind := range []string{"directory", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			out, protected := privateTestDir(t), privateTestDir(t)
			path := filepath.Join(out, "site-helm")
			writeTenantFixture(t, filepath.Join(protected, "sentinel"), []byte("unchanged"), 0600)
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "file":
				err = os.WriteFile(path, []byte("existing"), 0600)
			case "symlink":
				err = os.Symlink(protected, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			_, args, err := prepareSiteHelm(ctx, Runner{}, "/selected-tools", out)
			if !errors.Is(err, fs.ErrExist) || args != nil {
				t.Fatal("existing Helm destination was admitted", args, err)
			}
			entries, err := os.ReadDir(protected)
			if err != nil || len(entries) != 1 {
				t.Fatal("existing destination changed unrelated storage", entries, err)
			}
		})
	}
}

func TestSiteHelmRendererOwnsScratchAndSelectedKubectl(t *testing.T) {
	for _, tc := range []struct {
		name   string
		exit   int
		cancel bool
	}{
		{"success", 0, false},
		{"kubectl failure", 37, false},
		{"cancellation", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := privateTestDir(t)
			tools, record := filepath.Join(out, "selected tools"), filepath.Join(out, "record")
			for _, directory := range []string{tools, record} {
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			kubectl := filepath.Join(tools, "kubectl")
			writeTenantFixture(t, kubectl, []byte(`#!/bin/sh
set -eu
test "$#" = 2 && test "$1" = kustomize
test -r "$2/kustomization.yaml"
printf '%s\n' "$0" > "$RECORD/program"
printf '%s\n' "$2" > "$RECORD/scratch"
cp "$2/rendered.yaml" "$RECORD/input.yaml"
printf '%s\n' "$$" > "$RECORD/pid"
if test "$BLOCK" = true; then exec /bin/sleep 30; fi
cat "$2/rendered.yaml"
exit "$RESULT"
`), 0700)
			input := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: synthetic-input}\n")
			inputPath := filepath.Join(out, "input.yaml")
			writeTenantFixture(t, inputPath, input, 0600)
			file, err := os.Open(inputPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			runner, args, err := prepareSiteHelm(ctx, Runner{Work: out, Env: []string{"PATH=/usr/bin:/bin"}, input: file}, tools, out)
			if err != nil {
				t.Fatal(err)
			}
			plugin := filepath.Join(environmentValue(runner.Env, "HELM_PLUGINS"), args[len(args)-1])
			runner.Env = SetEnv(runner.Env, map[string]string{"HELM_PLUGIN_DIR": plugin, "RECORD": record, "RESULT": strconv.Itoa(tc.exit), "BLOCK": strconv.FormatBool(tc.cancel)})
			done := make(chan error, 1)
			go func() {
				raw, captureErr := runner.Capture(ctx, []string{"/bin/sh", filepath.Join(plugin, "render.sh")}, "render", 0)
				if !tc.cancel && !bytes.Equal(raw, input) {
					t.Error("renderer lost kubectl's actual stdout")
				}
				done <- captureErr
			}()
			var pid int
			until := time.Now().Add(5 * time.Second)
			for time.Now().Before(until) {
				if raw, readErr := os.ReadFile(filepath.Join(record, "pid")); readErr == nil {
					pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if tc.cancel || pid == 0 {
				cancel()
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("renderer did not stop and join")
			}
			if pid == 0 || errors.Is(err, ErrUnjoined) || tc.cancel && !errors.Is(err, context.Canceled) || !tc.cancel && tc.exit == 0 && err != nil || !tc.cancel && tc.exit != 0 && CommandExitCode(err) != tc.exit {
				t.Fatal("renderer lost command status or joining", pid, err)
			}
			for name, want := range map[string][]byte{"input.yaml": input, "program": []byte(kubectl + "\n")} {
				raw, readErr := os.ReadFile(filepath.Join(record, name))
				if readErr != nil || !bytes.Equal(raw, want) {
					t.Fatal("renderer changed input or selected tool", name, readErr)
				}
			}
			scratch, err := os.ReadFile(filepath.Join(record, "scratch"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(strings.TrimSpace(string(scratch))); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("renderer retained private scratch after return", err)
			}
			if tc.cancel {
				if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
					t.Fatal("selected kubectl process survived cancellation", err)
				}
			}
		})
	}
}

func TestSiteHelmRendererStockManifest(t *testing.T) {
	tools, fixture := os.Getenv("NVKR_TEST_SITE_HELM_TOOLS"), os.Getenv("NVKR_TEST_SITE_HELM_MANIFEST")
	if tools == "" && fixture == "" {
		t.Skip("requires explicitly selected Helm/kubectl directory and saved stock manifest")
	}
	if !filepath.IsAbs(tools) || !filepath.IsAbs(fixture) {
		t.Fatal("both selected tools and stock manifest must be absolute paths")
	}
	original, err := os.ReadFile(fixture)
	if err != nil || len(bytes.TrimSpace(original)) == 0 {
		t.Fatal("stock manifest must be nonempty", err)
	}
	out := privateTestDir(t)
	chart := filepath.Join(out, "chart")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"Chart.yaml":            []byte("apiVersion: v2\nname: replay\nversion: 0.0.0\n"),
		"stock.yaml":            original,
		"templates/replay.yaml": []byte(`{{ .Files.Get "stock.yaml" }}`),
	} {
		writeTenantFixture(t, filepath.Join(chart, name), raw, 0600)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	runner, args, err := prepareSiteHelm(ctx, Runner{Work: out, Env: []string{"PATH=/usr/bin:/bin", "HOME=" + out}}, tools, out)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := time.ParseDuration(args[1])
	if err != nil {
		t.Fatal(err)
	}
	command := append([]string{filepath.Join(tools, "helm"), "template", "replay", chart, "--namespace", "nke-controller"}, args[3:]...)
	patched, err := runner.Capture(ctx, command, "helm-stock-replay", 0)
	if err != nil {
		t.Fatal("selected Helm post-renderer failed", err)
	}
	var objects [2]map[string]map[string]any
	for index, raw := range [][]byte{original, patched} {
		objects[index] = make(map[string]map[string]any)
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		targets := 0
		for {
			var object map[string]any
			if err := decoder.Decode(&object); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				t.Fatal("decode stock resource", err)
			}
			if object == nil {
				continue
			}
			metadata := object["metadata"].(map[string]any)
			namespace, _ := metadata["namespace"].(string)
			if annotations, ok := metadata["annotations"].(map[string]any); ok {
				delete(annotations, "postrenderer.helm.sh/postrender-filename")
				if len(annotations) == 0 {
					delete(metadata, "annotations")
				}
			}
			if object["apiVersion"] == "apps/v1" && object["kind"] == "Deployment" && metadata["name"] == "nke-controller" && namespace == "nke-controller" {
				targets++
				spec := object["spec"].(map[string]any)
				containers := spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
				for _, rawContainer := range containers {
					container := rawContainer.(map[string]any)
					if container["name"] != "nke-controller" {
						continue
					}
					if index == 1 {
						probe := container["startupProbe"].(map[string]any)
						httpGet := probe["httpGet"].(map[string]any)
						period, threshold := probe["periodSeconds"].(int), probe["failureThreshold"].(int)
						progress := time.Duration(spec["progressDeadlineSeconds"].(int)) * time.Second
						window := time.Duration(period*threshold) * time.Second
						if httpGet["path"] != "/healthz" || httpGet["port"] != "http" || period <= 0 || threshold <= 0 || progress < budget || progress >= budget+time.Second || window < progress || window >= progress+time.Duration(period)*time.Second {
							t.Fatal("startup/progress windows differ from the original work budget")
						}
					}
					delete(container, "startupProbe")
				}
				delete(spec, "progressDeadlineSeconds")
			}
			identity := fmt.Sprintf("%s/%s/%s/%s", object["apiVersion"], object["kind"], namespace, metadata["name"])
			if _, duplicate := objects[index][identity]; duplicate {
				t.Fatal("duplicate rendered resource", identity)
			}
			objects[index][identity] = object
		}
		if targets != 1 {
			t.Fatalf("rendered set contains %d controller Deployments", targets)
		}
	}
	if len(objects[0]) < 2 || len(objects[0]) != len(objects[1]) {
		t.Fatal("stock replay must preserve controller and unrelated resources")
	}
	for identity, original := range objects[0] {
		if !reflect.DeepEqual(original, objects[1][identity]) {
			t.Fatal("post-renderer changed unrelated resource semantics", identity)
		}
	}
}

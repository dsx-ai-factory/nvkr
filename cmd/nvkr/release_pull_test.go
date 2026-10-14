// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/urfave/cli/v2"
)

func TestReleasePullDownloadsOnlySelectedOutput(t *testing.T) {
	for _, selected := range []struct {
		arch, output, path, payload string
		mode                        os.FileMode
	}{
		{"amd64", "tools", "cli", "opaque cli fixture bytes", 0o755},
		{"arm64", "tools", "cli", "opaque cli fixture bytes", 0o755},
		{"amd64", "node-image", "node-image.qcow2", "opaque disk fixture bytes", 0o644},
		{"arm64", "node-image", "node-image.qcow2", "opaque disk fixture bytes", 0o644},
	} {
		t.Run(selected.arch+"/"+selected.output, func(t *testing.T) {
			arch := selected.arch
			fixture := releaseBrowsePayloadFixture(t, arch)
			var selectedGets, otherGets atomic.Int64
			var corruptMetadata atomic.Bool
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if fixture.payloads[filepath.Base(r.URL.Path)] && strings.Contains(r.URL.Path, "/blobs/") {
						if strings.Contains(r.URL.Path, "/product/"+selected.output+"/") {
							selectedGets.Add(1)
						} else {
							otherGets.Add(1)
							http.Error(w, "unselected payload must not be downloaded", http.StatusForbidden)
							return
						}
					}
					if corruptMetadata.Load() && strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.diskConfig) {
						raw := append([]byte(nil), fixture.diskConfigBytes...)
						raw[len(raw)-1] ^= 1
						_, _ = w.Write(raw)
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			transport := delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}
			reference, err := delivery.Push(t.Context(), fixture.layout, strings.TrimPrefix(server.URL, "http://")+"/product/releases", transport)
			if err != nil {
				t.Fatal(err)
			}
			selectedGets.Store(0)
			otherGets.Store(0)
			root := t.TempDir()
			out, workspace := filepath.Join(root, selected.output), filepath.Join(root, "workspace")
			args := []string{"--reference", reference, "--output", selected.output, "--out", out, "--workspace", workspace, "--anonymous", "--plain-http"}
			raw, err := runReleasePull(t.Context(), args...)
			if err != nil {
				t.Fatal(err)
			}
			if selectedGets.Load() == 0 || otherGets.Load() != 0 {
				t.Fatalf("wrong payloads fetched: selected=%d other=%d", selectedGets.Load(), otherGets.Load())
			}
			payload, err := os.ReadFile(filepath.Join(out, selected.path))
			if err != nil || string(payload) != selected.payload {
				t.Fatalf("wrong extracted bytes: %q, %v", payload, err)
			}
			info, err := os.Stat(filepath.Join(out, selected.path))
			if err != nil || info.Mode().Perm() != selected.mode {
				t.Fatalf("payload mode not retained: %v, %v", info, err)
			}
			for _, want := range []string{filepath.Join(out, selected.path), "linux/" + arch, "payload integrity checked", "signatures and policy not verified"} {
				if !strings.Contains(raw, want) {
					t.Errorf("pull result missing %q: %s", want, raw)
				}
			}
			assertPullWorkspaceEmpty(t, workspace)
			for _, test := range []struct {
				name, output string
				corrupt      bool
				code         e.Code
			}{
				{"unknown", "missing", false, e.CodeUsage},
				{"invalid metadata", selected.output, true, e.CodeFetchFailed},
			} {
				t.Run(test.name, func(t *testing.T) {
					corruptMetadata.Store(test.corrupt)
					defer corruptMetadata.Store(false)
					selectedGets.Store(0)
					otherGets.Store(0)
					destination := filepath.Join(root, test.name)
					raw, err := runReleasePull(t.Context(), "--reference", reference, "--output", test.output, "--out", destination, "--workspace", workspace, "--anonymous", "--plain-http")
					if cliErrorCode(err) != test.code || raw != "" {
						t.Fatalf("failed selection returned output or wrong code: %q, %v", raw, err)
					}
					if selectedGets.Load() != 0 || otherGets.Load() != 0 {
						t.Fatal("failed selection downloaded payloads")
					}
					if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("failed selection wrote destination: %v", err)
					}
					assertPullWorkspaceEmpty(t, workspace)
				})
			}
		})
	}
}

func TestReleasePullRejectsInvalidUsageBeforeAcquisition(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags []string
		code  e.Code
	}{
		{"missing output", nil, e.CodeUsage},
		{"empty output", []string{"--output", ""}, e.CodeUsage},
		{"timeout", []string{"--output", "tools", "--timeout", "0s"}, e.CodeUsage},
		{"positional", []string{"--output", "tools", "unexpected"}, e.CodeUsage},
		{"tag", []string{"--output", "tools", "--reference", "registry.example.test/releases:latest"}, e.CodeUnverifiedRef},
		{"empty destination", []string{"--output", "tools", "--out", ""}, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			args := []string{"--reference", "127.0.0.1:1/product/releases@sha256:" + strings.Repeat("a", 64), "--workspace", filepath.Join(root, "workspace"), "--out", filepath.Join(root, "out")}
			raw, err := runReleasePull(t.Context(), append(args, test.flags...)...)
			if cliErrorCode(err) != test.code {
				t.Fatalf("invalid usage returned wrong code: %v; output=%q", err, raw)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed admission touched filesystem: %v, %v", entries, err)
			}
		})
	}
}

func TestReleasePullEscapesRegistryLabels(t *testing.T) {
	for _, control := range []string{"\x1b[2J", "\u202e", "\t"} {
		t.Run(control, func(t *testing.T) {
			result := delivery.PulledOutput{Output: delivery.PublicationOutput{Output: delivery.Output{Kind: "executable" + control, OS: "linux" + control, Arch: "arm64" + control}},
				Files: []delivery.PulledFile{{Path: "cli" + control, SHA256: "sha256:" + strings.Repeat("a", 64)}}}
			raw := releasePullMessage("tools"+control, "out"+control, result)
			if strings.Contains(raw, control) {
				t.Fatalf("pull printed a terminal control character: %q", raw)
			}
		})
	}
}

func runReleasePull(ctx context.Context, flags ...string) (string, error) {
	var out bytes.Buffer
	app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
	err := app.RunContext(ctx, append([]string{"nvkr", "release", "pull"}, flags...))
	return out.String(), err
}

func assertPullWorkspaceEmpty(t *testing.T, workspace string) {
	t.Helper()
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("pull leaked workspace: %v, %v", entries, err)
	}
}

func TestReleasePullPresentationSurroundsOneOwnedOperation(t *testing.T) {
	for _, test := range []struct {
		name                 string
		eligible             bool
		ownerCode            e.Code
		wantCode             e.Code
		wantCalls, wantViews int
	}{
		{"success", true, "", "", 1, 1},
		{"failure", true, e.CodeFetchFailed, e.CodeFetchFailed, 1, 1},
		{"ineligible terminal", false, "", e.CodeUsage, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, views := 0, 0
			destination := filepath.Join(t.TempDir(), "output")
			var out bytes.Buffer
			operation := func(ctx context.Context, reference, output, path string, options lifecycle.PullOptions) (delivery.PulledOutput, error) {
				calls++
				if ctx == nil || reference != "exact-reference" || output != "tools" || path != destination || options.Timeout <= 0 {
					t.Fatal("command lost selected operation inputs")
				}
				if test.ownerCode != "" {
					return delivery.PulledOutput{}, e.New(test.ownerCode, "private-error-sentinel")
				}
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				return delivery.PulledOutput{Output: delivery.PublicationOutput{Output: delivery.Output{Kind: "executable", OS: "linux", Arch: "amd64"}}, Files: []delivery.PulledFile{{Path: "cli", SHA256: "sha256:" + strings.Repeat("a", 64), Size: 17}}}, nil
			}
			app := &cli.App{Writer: &out, Commands: []*cli.Command{{Name: "release", Subcommands: []*cli.Command{releasePullCmdWithOperation(operation)}}}, Metadata: map[string]interface{}{commandPresentationServicesKey: runPresentationServices{
				eligible: func(io.Writer) bool { return test.eligible },
				show: func(_ context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
					views++
					if calls != 1 || options.Live || work != nil {
						t.Fatal("viewer started before operation returned or offered live work")
					}
					snapshot := source.Snapshot()
					if snapshot.FailureCode != string(test.ownerCode) {
						t.Fatal("viewer lost operation error")
					}
					if test.ownerCode == "" {
						if _, err := os.Stat(destination); err != nil {
							t.Fatal("viewer preceded destination publication", err)
						}
						if snapshot.Status != "complete" || len(snapshot.Items) != 1 || snapshot.Items[0].Name != "cli" {
							t.Fatal("viewer lost written files")
						}
					}
					return runpresenter.Result{}
				},
			}}}
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "pull", "--reference", "exact-reference", "--output", "tools", "--out", destination, "--ui=tui"})
			if cliErrorCode(err) != test.wantCode || calls != test.wantCalls || views != test.wantViews || out.Len() != 0 {
				t.Fatalf("unexpected result: calls=%d views=%d output=%q err=%v", calls, views, out.String(), err)
			}
		})
	}
}

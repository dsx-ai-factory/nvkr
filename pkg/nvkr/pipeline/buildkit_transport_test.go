// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func buildKitPrivateFixture(t *testing.T) (string, ir.Node, RunOptions) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(source, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"private/payload": "private bytes\n", "Dockerfile": "FROM scratch\nCOPY private/payload /payload\n"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n := ir.Node{ID: "private-buildkit", Component: "private", Platform: "linux/arm64", Toolchain: "moby/buildkit@sha256:" + repeatHex("1"), Workdir: "/src", WriteDir: "out", ArtifactPath: "out/image.tar", SourceReadOnly: true, BuilderType: ir.BuilderBuildKit}
	opts := RunOptions{Ctx: t.Context(), Runtime: "docker", Stdout: io.Discard, Stderr: io.Discard, SourceRoot: filepath.Join(t.TempDir(), "src")}
	return source, n, opts
}

func TestBuildKitPrivateTransport(t *testing.T) {
	for _, runtimeName := range []string{"docker", "podman"} {
		t.Run(runtimeName, func(t *testing.T) {
			source, n, opts := buildKitPrivateFixture(t)
			before, err := sourceTreeDigestContext(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			var inner []byte
			runtime := &recordingPackerRuntime{name: runtimeName, run: func(in io.Reader, out, _ io.Writer, args []string) error {
				if args[0] == "wait" {
					_, err := io.WriteString(out, "0\n")
					return err
				}
				if args[0] == "volume" {
					_, err := io.WriteString(out, args[len(args)-1]+"\n")
					return err
				}
				if args[0] == "cp" && args[1] == "-" {
					tr := tar.NewReader(in)
					h, err := tr.Next()
					if err != nil {
						return err
					}
					if h.Name != "input.tar" {
						return fmt.Errorf("unexpected carrier %s", h.Name)
					}
					inner, err = io.ReadAll(tr)
					if err != nil {
						return err
					}
					_, err = io.Copy(io.Discard, in)
					return err
				}
				return nil
			}}
			transport, err := prepareBuildKitTransport(t.Context(), n, opts, source, runtime)
			if err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(inner))
			tr := tar.NewReader(bytes.NewReader(inner))
			seen := map[string]int64{}
			for {
				h, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				seen[h.Name] = h.Mode
			}
			for name, mode := range map[string]int64{"source/private": 0o700, "source/private/payload": 0o600, "source/Dockerfile": 0o600} {
				if seen[name] != mode {
					t.Fatalf("%s mode=%o want %o", name, seen[name], mode)
				}
			}
			create := strings.Join(runtime.calls[2], " ")
			for _, want := range []string{"--network=none", "--read-only", "--user=0:0", "--cap-drop=ALL", digest, "--strip-components=1"} {
				if !strings.Contains(create, want) {
					t.Fatalf("missing %s in %s", want, create)
				}
			}
			if strings.Contains(create, source) || strings.Contains(create, "--privileged") {
				t.Fatal("input preparation gained host mount or broad privilege")
			}
			if err := transport.close(); err != nil {
				t.Fatal(err)
			}
			if len(runtime.cleanups) != 2 {
				t.Fatalf("volume cleanup=%v", runtime.cleanups)
			}
			if _, err := os.Stat(transport.root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("scratch remains: %v", err)
			}
			after, err := sourceTreeDigestContext(t.Context(), source)
			if err != nil || after != before {
				t.Fatalf("source changed: %v", err)
			}
		})
	}
}

func TestBuildKitTransportFailureCleanup(t *testing.T) {
	for _, phase := range []string{"create", "cp", "start", "cancel"} {
		t.Run(phase, func(t *testing.T) {
			source, n, opts := buildKitPrivateFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			runtime := &recordingPackerRuntime{runContext: func(ctx context.Context, in io.Reader, out, _ io.Writer, args []string) error {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded transport phase")
				}
				if args[0] == "volume" {
					_, err := io.WriteString(out, args[len(args)-1]+"\n")
					return err
				}
				if args[0] == phase {
					return errors.New("injected failure")
				}
				if args[0] == "wait" {
					_, err := io.WriteString(out, "0\n")
					return err
				}
				if args[0] == "start" && phase == "cancel" {
					cancel()
					return ctx.Err()
				}
				if in != nil {
					_, err := io.Copy(io.Discard, in)
					return err
				}
				return nil
			}}
			transport, err := prepareBuildKitTransport(ctx, n, opts, source, runtime)
			if transport != nil || err == nil {
				t.Fatalf("failure returned transport=%v error=%v", transport, err)
			}
			wantCode := e.CodeBuildFailed
			if phase == "cancel" {
				wantCode = e.CodeTimeout
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != wantCode {
				t.Fatalf("failure=%v want %s", err, wantCode)
			}
			if len(runtime.cleanups) != 3 {
				t.Fatalf("container/volumes not reaped: %v", runtime.cleanups)
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(source), ".nvkr-buildkit-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("scratch remains %v: %v", matches, err)
			}
		})
	}
}

func TestBuildKitDeclaredExport(t *testing.T) {
	for _, kind := range []string{"exact", "foreign", "oversized", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			_, n, opts := buildKitPrivateFixture(t)
			root := t.TempDir()
			member := "image.tar"
			if kind == "foreign" {
				member = "other.tar"
			}
			if err := os.WriteFile(filepath.Join(root, member), []byte("exact OCI export"), 0o600); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(root, "carrier.tar")
			if err := transportarchive.Pack(t.Context(), archive, root, []string{member}); err != nil {
				t.Fatal(err)
			}
			runtime := &recordingPackerRuntime{run: func(_ io.Reader, out, _ io.Writer, args []string) error {
				if strings.Join(args, " ") != "cp producer:/src/out/image.tar -" {
					t.Fatalf("unexpected collection %v", args)
				}
				f, err := os.Open(archive)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(out, f)
				return err
			}}
			transport := &buildKitTransport{root: t.TempDir(), resources: &packerContainerResources{runtime: runtime}, limit: 1 << 20}
			if kind == "oversized" {
				transport.limit = 1
			}
			ctx := t.Context()
			if kind == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := transport.collect(ctx, opts, n, "producer")
			if kind == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(artifactHostPath(opts, n))
				if err != nil || string(raw) != "exact OCI export" {
					t.Fatalf("export=%q %v", raw, err)
				}
			} else {
				var coded *e.Error
				if !errors.As(err, &coded) {
					t.Fatalf("missing coded failure: %v", err)
				}
				if _, err := os.Stat(artifactHostPath(opts, n)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("partial artifact exposed: %v", err)
				}
			}
		})
	}
}

// Opt in on the actual Linux execution host; this exercises UID-independent
// input/output transport, not qualification or a complete release.
func TestBuildKitPrivateTransportLive(t *testing.T) {
	image := os.Getenv("NVKR_BUILDKIT_TEST_IMAGE")
	if image == "" {
		t.Skip("set pinned NVKR_BUILDKIT_TEST_IMAGE")
	}
	source, n, opts := buildKitPrivateFixture(t)
	n.Toolchain = image
	if value := os.Getenv("NVKR_BUILDKIT_TEST_RUNTIME"); value != "" {
		opts.Runtime = value
	}
	if value := os.Getenv("NVKR_BUILDKIT_TEST_PLATFORM"); value != "" {
		n.Platform = value
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	opts.Ctx = ctx
	runtime := buildKitCommandRuntime{opts: opts}
	transport, err := prepareBuildKitTransport(ctx, n, opts, source, runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := transport.close(); err != nil {
			t.Error(err)
		}
	}()
	name := containerName(n)
	transport.resources.containers = append(transport.resources.containers, name)
	args := []string{"create", "--name", name, "--platform", n.Platform, "--network=none"}
	args = append(args, buildKitRuntimeArgs(opts.Runtime)...)
	args = append(args, buildKitVolumeArgs(opts.Runtime, transport.source, "/src", true)...)
	args = append(args, buildKitVolumeArgs(opts.Runtime, transport.output, "/src/out", false)...)
	args = append(args, "-w", "/src", image, "buildctl-daemonless.sh", "build", "--frontend", "dockerfile.v0", "--local", "context=/src", "--local", "dockerfile=/src", "--output", "type=oci,dest=/src/out/image.tar")
	var log bytes.Buffer
	if err := runtime.Run(ctx, nil, &log, &log, args...); err != nil {
		t.Fatalf("create: %v %s", err, log.String())
	}
	if err := runtime.Run(ctx, nil, &log, &log, "start", "--attach", name); err != nil {
		t.Fatalf("build: %v %s", err, log.String())
	}
	if err := transport.collect(ctx, opts, n, name); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(artifactHostPath(opts, n))
	if err != nil || info.Size() == 0 {
		t.Fatalf("missing export: %v", err)
	}
	t.Logf("non-root BuildKit exported %d bytes from private host source uid=%d", info.Size(), os.Getuid())
}

// Preparation must authenticate the container exit, not just the start command.
// In particular, Docker start without attachment returns success for failed jobs.
func TestBuildKitPreparationObservedExit(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		failPhase    string
		want         e.Code
	}{
		{name: "success", status: "0\n"},
		{name: "container failure", status: "42\n", want: e.CodeBuildFailed},
		{name: "empty status", want: e.CodeBuildFailed},
		{name: "multiple statuses", status: "0\n0\n", want: e.CodeBuildFailed},
		{name: "non numeric status", status: "success\n", want: e.CodeBuildFailed},
		{name: "negative status", status: "-1\n", want: e.CodeBuildFailed},
		{name: "out of range status", status: "256\n", want: e.CodeBuildFailed},
		{name: "oversized status", status: strings.Repeat("0", 64), want: e.CodeBuildFailed},
		{name: "wait command failure", failPhase: "wait", want: e.CodeBuildFailed},
		{name: "logs command failure", status: "0\n", failPhase: "logs", want: e.CodeBuildFailed},
		{name: "canceled wait", failPhase: "cancel", want: e.CodeTimeout},
		{name: "canceled logs", status: "0\n", failPhase: "cancel-logs", want: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, n, opts := buildKitPrivateFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			waited, logs := false, false
			runtime := &recordingPackerRuntime{runContext: func(ctx context.Context, in io.Reader, out, stderr io.Writer, args []string) error {
				if args[0] == "volume" {
					_, err := io.WriteString(out, args[len(args)-1]+"\n")
					return err
				}
				if args[0] == "start" && len(args) != 2 {
					return errors.New("preparation must not attach to process output")
				}
				if args[0] == tc.failPhase {
					return errors.New("injected command failure")
				}
				if args[0] == "wait" {
					waited = true
					if tc.failPhase == "cancel" {
						cancel()
						return ctx.Err()
					}
					_, err := io.WriteString(out, tc.status)
					return err
				}
				if args[0] == "logs" {
					if tc.failPhase == "cancel-logs" {
						cancel()
						return ctx.Err()
					}
					logs = true
					_, err := io.WriteString(stderr, "retained helper diagnostic\n")
					return err
				}
				if in != nil {
					_, err := io.Copy(io.Discard, in)
					return err
				}
				return nil
			}}
			var diagnostic bytes.Buffer
			opts.Stderr = &diagnostic
			transport, err := prepareBuildKitTransport(ctx, n, opts, source, runtime)
			if code(err) != tc.want {
				t.Fatalf("error=%v want %s", err, tc.want)
			}
			if (tc.status == "42\n" || tc.failPhase == "wait" || tc.failPhase == "cancel") && !strings.Contains(diagnostic.String(), "retained helper diagnostic") {
				t.Fatal("failed helper diagnostics lost before cleanup")
			}
			if tc.want == "" {
				if !waited || !logs || !strings.Contains(diagnostic.String(), "retained helper diagnostic") {
					t.Fatal("missing exit observation or helper log")
				}
				if err := transport.close(); err != nil {
					t.Fatal(err)
				}
			} else {
				if transport != nil {
					t.Fatal("failed preparation exposed transport")
				}
				if len(runtime.cleanups) != 3 {
					t.Fatalf("unjoined cleanup: %v", runtime.cleanups)
				}
			}
		})
	}
}

// Exercise transport alone with a real source tree on its actual execution host.
// The source is copied through the production snapshot owner into test-owned
// scratch; this never writes beside, or otherwise modifies, the supplied tree.
func TestBuildKitPrivatePreparationLive(t *testing.T) {
	image, input := os.Getenv("NVKR_BUILDKIT_TEST_IMAGE"), os.Getenv("NVKR_BUILDKIT_TEST_SOURCE")
	if image == "" || input == "" {
		t.Skip("set pinned NVKR_BUILDKIT_TEST_IMAGE and NVKR_BUILDKIT_TEST_SOURCE")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	_, n, opts := buildKitPrivateFixture(t)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copySourceTreeContext(ctx, input, source, 0); err != nil {
		t.Fatal(err)
	}
	before, err := sourceTreeDigestContext(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	n.Toolchain = image
	opts.Ctx = ctx
	if value := os.Getenv("NVKR_BUILDKIT_TEST_RUNTIME"); value != "" {
		opts.Runtime = value
	}
	if value := os.Getenv("NVKR_BUILDKIT_TEST_PLATFORM"); value != "" {
		n.Platform = value
	}
	opts.Stdout, opts.Stderr = os.Stdout, os.Stderr
	opts.progress = &progressSink{w: os.Stdout}
	runtime := buildKitCommandRuntime{opts: opts}
	transport, err := prepareBuildKitTransport(ctx, n, opts, source, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.close(); err != nil {
		t.Fatal(err)
	}
	after, err := sourceTreeDigestContext(ctx, source)
	if err != nil || after != before {
		t.Fatalf("source changed: %v", err)
	}
	t.Log("private preparation exited zero, source unchanged, owned transport cleanup joined")
}

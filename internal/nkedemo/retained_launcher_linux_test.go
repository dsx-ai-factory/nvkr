// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nkedemo

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
)

// These tests exercise the actual Retain/serveRetained launch, progress relay,
// handoff and cleanup machinery with real HTTP and sleep processes. The Run
// callback and kubectl answers are synthetic. They are NOT NKE readiness,
// authenticated artifact acceptance, privileged recovery or full E2E evidence.
type retainedLauncherFixture struct {
	Root, Endpoint string
}

func TestRetainedLauncherOwnerFixture(t *testing.T) {
	arguments := flag.Args()
	if len(arguments) != 2 {
		return
	}
	root, mode := arguments[0], arguments[1]
	if mode != "ready" && mode != "partial" {
		t.Fatal("unknown isolated fixture selection")
	}
	launch, err := readRetainedJSON[retainedLaunch](t.Context(), filepath.Join(root, retainedRequestFile))
	if err != nil {
		t.Fatal(err)
	}
	err = serveRetained(t.Context(), root, launch, func(ctx context.Context, cfg Config) (result error) {
		if err := os.Mkdir(cfg.Out, 0o700); err != nil {
			return err
		}
		runner := Runner{Work: cfg.Out, Env: NativeEnvironment()}
		service, err := runner.Start(ctx, []string{"/bin/sleep", "90"}, "retained-launcher-service")
		if err != nil {
			return err
		}
		var disk *diskService
		defer func() {
			if err := waitRetainedCleanup(ctx, result); err != nil {
				result = errors.Join(result, err)
				return
			}
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			cleanupErr := service.Stop(cleanup)
			if disk != nil {
				cleanupErr = errors.Join(cleanupErr, disk.Stop(cleanup))
			}
			recordRetainedCleanup(ctx, cleanupErr)
			result = errors.Join(result, cleanupErr)
		}()
		image := filepath.Join(cfg.Out, "fixture.qcow2")
		if err := privateWrite(image, []byte("retained launcher exact HTTP fixture")); err != nil {
			return err
		}
		disk, err = startDiskService(ctx, image, "127.0.0.1:0")
		if err != nil {
			return err
		}
		if err := privateJSON(filepath.Join(root, "fixture-http.json"), retainedLauncherFixture{Root: root, Endpoint: disk.url}); err != nil {
			return err
		}
		if _, err := io.WriteString(cfg.Progress, "public actual launcher fixture progress\n"); err != nil {
			return err
		}
		if mode == "partial" {
			return errors.New("private-launcher-operation-failure")
		}
		tools := filepath.Join(cfg.Out, "tools")
		if err := os.Mkdir(tools, 0o700); err != nil {
			return err
		}
		for _, name := range []string{"kubeconfig", "tenant-kubeconfig"} {
			if err := privateWrite(filepath.Join(cfg.Out, name), []byte("private-config-fixture")); err != nil {
				return err
			}
		}
		script := "#!/bin/sh\nset -eu\ncase \"$*\" in\n *--raw=/readyz*) printf 'ok\\n' ;;\n *'get nodes -o json'*) printf '%s\\n' " + operatorQuote(operatorNodesFixture) + " ;;\n *'get services -o json'*) printf '{\"items\":[]}\\n' ;;\n *) exit 99 ;;\nesac\n"
		if err := os.WriteFile(filepath.Join(tools, "kubectl"), []byte(script), 0o700); err != nil {
			return err
		}
		access, err := InspectOperatorAccess(ctx, ManagementSession{Work: cfg.Out, Tools: tools, Runner: runner})
		if err != nil {
			return err
		}
		return runtimeRetention(ctx).markReady(ctx, access)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetainedLauncherForegroundFixture(t *testing.T) {
	arguments := flag.Args()
	if len(arguments) != 2 {
		return
	}
	root, mode := arguments[0], arguments[1]
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var progress bytes.Buffer
	session, err := Retain(ctx, Config{Network: "upstream-ovn", DemoExecutable: filepath.Join(root, "launcher"), Out: filepath.Join(root, "demo"), Timeout: 300 * time.Second, Progress: &progress})
	if session == nil {
		t.Fatal("actual launcher did not return its selected session", err)
	}
	handedOff := false
	defer func() {
		if !handedOff {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 20*time.Second)
			defer cancel()
			if err := CleanupRetained(cleanup, session.root, nil); err != nil {
				t.Error("exact fixture owner cleanup failed", err)
			}
		}
	}()
	var coded *nkderr.Error
	if mode == "ready" && err != nil || mode == "partial" && (!errors.As(err, &coded) || coded.Code != nkderr.CodePreflightFailed) {
		t.Fatal("actual launcher outcome was misclassified", err)
	}
	if session.child == nil || !session.child.released || !strings.Contains(progress.String(), "public actual launcher fixture progress") {
		t.Fatal("actual child was not handed off or public progress was lost")
	}
	var instructions bytes.Buffer
	if err := session.WriteInstructions(ctx, os.Args[0], &instructions); err != nil {
		t.Fatal(err)
	}
	if mode == "ready" && (!strings.Contains(instructions.String(), "Nodes: 2/2 Ready") || !strings.Contains(instructions.String(), "TENANT_CONFIG=")) {
		t.Fatal("actual handoff omitted live kubectl/access projection", &instructions)
	}
	if strings.Contains(instructions.String()+progress.String(), "private-launcher-operation-failure") || strings.Contains(instructions.String(), "private-config-fixture") {
		t.Fatal("private operation/config payload entered public handoff")
	}
	if err := privateJSON(filepath.Join(root, "fixture-handoff.json"), retainedLauncherFixture{Root: session.root}); err != nil {
		t.Fatal(err)
	}
	handedOff = true // This foreground now exits naturally with its sole handle released.
}

func TestRetainedLauncherReadyAndPartialSurviveForegroundExit(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("actual demo launcher declares the Linux ARM64 route")
	}
	for _, mode := range []string{"ready", "partial"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestDir(t)
			cleaned := false
			// Even a failed/killed fixture foreground must leave no known test
			// owner behind. Select only this isolated root's original private
			// record and authenticate its exact workspace before requesting work.
			t.Cleanup(func() {
				if cleaned {
					return
				}
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 20*time.Second)
				defer cancel()
				roots, err := filepath.Glob(filepath.Join(root, ".nke-retained-*"))
				if err != nil || len(roots) > 1 {
					t.Error("isolated fixture owner selection was ambiguous", err)
					return
				}
				for _, selected := range roots {
					owner, err := openRetainedSession(cleanup, selected)
					if err != nil {
						// An RPC timeout/refusal or bad record is not proof of
						// termination. Fail the fixture and retain its evidence;
						// never report unknown owned services as joined.
						t.Error("isolated fixture cleanup remains unjoined", errors.Join(ErrUnjoined, err))
						continue
					}
					if owner.description.Work != filepath.Join(root, "demo") {
						t.Error("refused a different fixture workspace")
						return
					}
					if err := CleanupRetained(cleanup, selected, nil); err != nil {
						t.Error("isolated launcher fixture did not clean up", err)
					}
				}
			})
			// Test-only fixed executable adapter preserves exec PID. Production
			// source uses only its own CLI entry; no callback injection is exposed.
			script := fmt.Sprintf("#!/bin/sh\nset -eu\ntest \"$#\" = 3\ntest \"$1\" = retained-owner\ntest \"$2\" = --session\nexec %s -test.run=^TestRetainedLauncherOwnerFixture$ -test.count=1 -test.timeout=90s -- \"$3\" %s\n", operatorQuote(os.Args[0]), operatorQuote(mode))
			if err := os.WriteFile(filepath.Join(root, "launcher"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			log, err := os.OpenFile(filepath.Join(root, "foreground.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 70*time.Second)
			defer cancel()
			foreground := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRetainedLauncherForegroundFixture$", "-test.count=1", "-test.timeout=60s", "--", root, mode)
			foreground.Stdout, foreground.Stderr = log, log
			if err := foreground.Run(); err != nil {
				t.Fatal("fixture foreground did not return naturally", err)
			}
			handoff, err := readRetainedJSON[retainedLauncherFixture](ctx, filepath.Join(root, "fixture-handoff.json"))
			if err != nil {
				t.Fatal(err)
			}
			session, err := openRetainedSession(ctx, handoff.Root)
			if err != nil {
				t.Fatal("selected actual owner did not survive foreground exit", err)
			}
			state, err := session.request(ctx, http.MethodGet, "/state")
			want := retainedReady
			if mode == "partial" {
				want = retainedPartial
			}
			if err != nil || state.State != want || !state.Detached || state.Joined {
				t.Fatal("launcher invented retention/readiness/cleanup", state.State, err)
			}
			fixture, err := readRetainedJSON[retainedLauncherFixture](ctx, filepath.Join(handoff.Root, "fixture-http.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := session.request(ctx, http.MethodPost, "/abort"); err != nil {
				t.Fatal(err)
			}
			raw := []byte("retained launcher exact HTTP fixture")
			image := lifecycle.Artifact{Digest: "sha256:" + plainSHA(raw), Disk: &delivery.DiskOutput{Size: int64(len(raw))}}
			if err := verifyHTTPDisk(ctx, fixture.Endpoint, image); err != nil {
				t.Fatal("foreground exit/abort destroyed owned HTTP service", err)
			}
			if mode == "partial" {
				raw, err := os.ReadFile(filepath.Join(handoff.Root, "owner-error.log"))
				if err != nil || !bytes.Contains(raw, []byte("private-launcher-operation-failure")) {
					t.Fatal("partial failure cause unavailable before cleanup", err)
				}
			}
			if err := CleanupRetained(ctx, handoff.Root, nil); err != nil {
				t.Fatal("actual launcher owner did not join explicit cleanup", err)
			}
			cleaned = true
			if _, err := openRetainedSession(ctx, handoff.Root); err == nil {
				t.Fatal("exited owner record was reused as a live owner")
			}
		})
	}
}

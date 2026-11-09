// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// A copied ELF test binary supplies the held-runtime protocol. It proves the
// real descriptor/subprocess boundary, not Docker or registry availability.
func init() {
	root := os.Getenv("NVKR_IMAGE_ADMISSION_FIXTURE")
	if root == "" {
		return
	}
	if len(os.Args) < 2 {
		os.Exit(92)
	}
	log, err := os.OpenFile(filepath.Join(root, "held-calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(93)
	}
	_, _ = fmt.Fprintln(log, os.Args[1])
	_ = log.Close()
	switch os.Args[1] {
	case "version":
		fmt.Print(`{}`)
	case "info":
		raw, err := os.ReadFile(filepath.Join(root, "daemon"))
		if err != nil {
			os.Exit(94)
		}
		var info struct{ ID string }
		if json.Unmarshal(raw, &info) != nil {
			os.Exit(95)
		}
		_ = json.NewEncoder(os.Stdout).Encode(info.ID)
	case "image":
		if _, err := os.Stat(filepath.Join(root, "wait")); err == nil {
			_ = os.WriteFile(filepath.Join(root, "started"), []byte(strconv.Itoa(os.Getpid())), 0o600)
			time.Sleep(time.Hour)
		}
		raw, err := os.ReadFile(filepath.Join(root, "image"))
		if err != nil {
			os.Exit(96)
		}
		_, _ = os.Stdout.Write(raw)
	default:
		os.Exit(97) // Pull is never permitted by this prepared-private fixture.
	}
	os.Exit(0)
}

func heldImageAdmissionFixture(t *testing.T) (*heldRuntimeExecutable, ir.Node, string) {
	t.Helper()
	path, subject, root := imageAdmissionRuntimeFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := snapshotRuntimeExecutableContext(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := held.close(); err != nil {
			t.Error(err)
		}
	})
	held.environment = append(held.environment, "NVKR_IMAGE_ADMISSION_FIXTURE="+root)
	held.identity, err = observeHeldRuntimeIdentity(t.Context(), held)
	if err != nil {
		t.Fatal(err)
	}
	return held, ir.Node{ID: "private", Toolchain: subject, Platform: "linux/amd64"}, root
}

func TestHeldPrivateImageAdmission(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		missing, runtimeChanged bool
		want                    e.Code
	}{
		{"prepared", false, false, ""},
		{"missing", true, false, e.CodeEngineCapability},
		{"changed runtime", false, true, e.CodePreflightFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held, node, root := heldImageAdmissionFixture(t)
			if tc.missing {
				if err := os.Remove(filepath.Join(root, "image")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.runtimeChanged {
				if err := os.WriteFile(filepath.Join(root, "daemon"), []byte(`{"ID":"another-daemon"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := observeHeldContainerImage(t.Context(), held, node, ImageScopedReader)
			if tc.want == "" {
				if err != nil || got != "sha256:"+strings.Repeat("a", 64) {
					t.Fatalf("identity=%q err=%v", got, err)
				}
			} else if code(err) != tc.want || got != "" {
				t.Fatalf("identity=%q err=%v", got, err)
			}
			calls, err := os.ReadFile(filepath.Join(root, "held-calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "pull") {
				t.Fatal("private consumer attempted a pull")
			}
		})
	}
}

func TestHeldImageAdmissionCancellationJoins(t *testing.T) {
	held, node, root := heldImageAdmissionFixture(t)
	if err := os.WriteFile(filepath.Join(root, "wait"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := observeHeldContainerImage(ctx, held, node, ImageScopedReader); done <- err }()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var pid int
	waiting := true
	for waiting {
		select {
		case err := <-done:
			t.Fatalf("observation ended before cancellation: %v", err)
		case <-timer.C:
			cancel()
			<-done
			t.Fatal("held image observation did not start")
		case <-ticker.C:
			raw, err := os.ReadFile(filepath.Join(root, "started"))
			if err == nil {
				pid, err = strconv.Atoi(string(raw))
				if err != nil {
					t.Fatal(err)
				}
				waiting = false
			}
		}
	}
	cancel()
	select {
	case err := <-done:
		if code(err) != e.CodeTimeout {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("held admission did not join after cancellation")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("held child remains after admission returned: %v", err)
	}
}

func TestPrivateProducerUsesPreparedImageBeforeOutput(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint("missing=", missing), func(t *testing.T) {
			runtimePath, subject, root := imageAdmissionRuntimeFixture(t)
			for _, member := range []string{"image", "daemon"} {
				path := filepath.Join(root, member)
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), "amd64", runtime.GOARCH)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := os.ReadFile(runtimePath)
			if err != nil {
				t.Fatal(err)
			}
			script := strings.Replace(string(raw), `run|create) case " $* " in *' --pull=never '*) exit 0;; *) exit 93;; esac;;`, `run)
case " $* " in *' --pull=never '*) ;; *) exit 93;; esac
out=
for arg in "$@"; do case "$arg" in *:/nvkr/output) out=${arg%:/nvkr/output};; esac; done
[ -n "$out" ] || exit 95
printf recipe > "$out/recipe.yaml";;
rm) exit 0;;`, 1)
			if err := os.WriteFile(runtimePath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			executable := []byte("synthetic pinned executable")
			tool := filepath.Join(root, "tool")
			if err := os.WriteFile(tool, executable, 0o700); err != nil {
				t.Fatal(err)
			}
			credential := filepath.Join(root, "config.json")
			if err := os.WriteFile(credential, []byte(`{"auths":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(credential); err != nil {
				t.Fatal(err)
			}
			if missing {
				if err := os.Remove(filepath.Join(root, "image")); err != nil {
					t.Fatal(err)
				}
			}
			node := pinnedToolNode(executable)
			node.Toolchain, node.Platform = subject, "linux/"+runtime.GOARCH
			result, err := (Local{}).ExecNode(node, RunOptions{Ctx: t.Context(), Launch: true, Runtime: runtimePath, ImageAccess: ImageScopedReader,
				SourceRoot: filepath.Join(root, "work", "src"), ToolExecutable: tool, Stdout: io.Discard, Stderr: io.Discard})
			if missing {
				if code(err) != e.CodeEngineCapability || result != nil {
					t.Fatalf("missing private image: %v %v", result, err)
				}
				if _, err := os.Stat(filepath.Join(root, "work")); !os.IsNotExist(err) {
					t.Fatal("missing image created producer output")
				}
			} else if err != nil || result == nil || result.SHA256 != FileDigest([]byte("recipe")) {
				t.Fatalf("prepared private producer: %v %v", result, err)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "pull --platform") {
				t.Fatal("private producer pulled")
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package nvkedemo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func TestDarwinRetainedPeerHelper(t *testing.T) {
	address := os.Getenv("NVKR_TEST_DARWIN_PEER")
	if address == "" {
		return
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: address})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	generation, err := retainedPeerGeneration(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		PID        int
		Generation string
	}{os.Getpid(), generation}); err != nil {
		t.Fatal(err)
	}
	connection, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var action [1]byte
	if _, err := io.ReadFull(connection, action[:]); err != nil {
		t.Fatal(err)
	}
	if action[0] == 'D' {
		if err := detachRetainedProcess(); err != nil {
			t.Fatal(err)
		}
		after, err := retainedPeerGeneration(t.Context())
		if err != nil || after != generation {
			t.Fatal("POSIX detach changed the original audit generation", err)
		}
		fmt.Fprintln(connection, "detached")
		if _, err := io.ReadFull(connection, action[:]); err != nil || action[0] != 'Q' {
			t.Fatal("missing natural exit request", err)
		}
		return
	}
	if action[0] != 'C' {
		t.Fatal("unexpected fixture request")
	}
	// Transport EOF is deliberately earlier than process exit.
	_ = connection.Close()
	if _, err := io.ReadFull(os.Stdin, action[:]); err != nil || action[0] != 'Q' {
		t.Fatal("missing post-EOF natural exit request", err)
	}
}

func TestDarwinRetainedOriginalGenerationExitAndTransport(t *testing.T) {
	if err := checkRetainedPeerCapability(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"detach and exit", "starts independent", "transport closes first", "close observation"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("/private/tmp", "nvkr-peer-fixture-")
			if err != nil {
				t.Fatal(err)
			}
			address := filepath.Join(dir, "peer.sock")
			defer func() { _ = os.Remove(address); _ = os.Remove(dir) }()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDarwinRetainedPeerHelper$", "-test.count=1")
			isolateRetainedCommand(cmd, mode != "starts independent")
			cmd.Env = append(os.Environ(), "NVKR_TEST_DARWIN_PEER="+address)
			cmd.WaitDelay = time.Second
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if t.Failed() {
					cancel()
				}
				waitErr := cmd.Wait()
				if waitErr != nil && !t.Failed() {
					t.Fatalf("real fixture did not join naturally: %v %s", waitErr, &diagnostic)
				}
			}()
			var description struct {
				PID        int
				Generation string
			}
			if err := json.NewDecoder(bufio.NewReader(stdout)).Decode(&description); err != nil || description.PID != cmd.Process.Pid {
				t.Fatal("original selector differs from the actual unreaped child", err)
			}
			sid, err := unix.Getsid(description.PID)
			if err != nil || (sid == description.PID) != (mode == "starts independent") {
				t.Fatal("owner session does not match terminal-authentication requirement", sid, err)
			}
			dialed, err := (&net.Dialer{}).DialContext(ctx, "unix", address)
			if err != nil {
				t.Fatal(err)
			}
			connection := dialed.(*net.UnixConn)
			defer connection.Close()
			if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			replacement := "ff"
			if description.Generation[len(description.Generation)-2:] == replacement {
				replacement = "00"
			}
			for _, selected := range []struct {
				pid        int
				generation string
			}{{description.PID + 1, description.Generation}, {description.PID, description.Generation[:len(description.Generation)-2] + replacement}} {
				peer, err := observeRetainedPeer(ctx, connection, selected.pid, selected.generation)
				var coded *nvkrerr.Error
				if peer != nil || !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
					t.Fatalf("changed original PID/generation was admitted: %v", err)
				}
			}
			peer, err := observeRetainedPeer(ctx, connection, description.PID, description.Generation)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			if peer.PID() != description.PID || peer.UID() != uint32(os.Getuid()) {
				t.Fatal("observation differs from the exact child")
			}
			short, stop := context.WithTimeout(ctx, 10*time.Millisecond)
			err = peer.WaitExit(short)
			stop()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("live owner fabricated exit", err)
			}
			if mode == "transport closes first" {
				if _, err := connection.Write([]byte("C")); err != nil {
					t.Fatal(err)
				}
				if _, err := connection.Read(make([]byte, 1)); err != io.EOF {
					t.Fatal("fixture transport did not close", err)
				}
				short, stop := context.WithTimeout(ctx, 10*time.Millisecond)
				err = peer.WaitExit(short)
				stop()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("transport closure fabricated process exit", err)
				}
				_, err = input.Write([]byte("Q"))
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := connection.Write([]byte("D")); err != nil {
					t.Fatal(err)
				}
				if line, err := bufio.NewReader(connection).ReadString('\n'); err != nil || line != "detached\n" {
					t.Fatal("original child did not detach", err)
				}
				if sid, err := unix.Getsid(description.PID); err != nil || sid != description.PID {
					t.Fatal("child does not own its actual new session", err)
				}
				if mode == "close observation" {
					if err := peer.Close(); err != nil {
						t.Fatal(err)
					}
					var coded *nvkrerr.Error
					if err := peer.WaitExit(ctx); !errors.As(err, &coded) || coded.Code != nvkrerr.CodeUsage {
						t.Fatal("closed watch remained issued", err)
					}
				}
				if _, err := connection.Write([]byte("Q")); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "close observation" {
				if err := peer.WaitExit(ctx); err != nil {
					t.Fatal(err)
				}
				if err := peer.WaitExit(ctx); err != nil {
					t.Fatal("consumed kernel exit event lost its observation", err)
				}
				var coded *nvkrerr.Error
				if reopened, err := observeRetainedPeer(ctx, connection, description.PID, description.Generation); reopened != nil ||
					!errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
					t.Fatal("dead original generation reconstructed a live watch", err)
				}
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nkedemo

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

// The helper creates its own listener after exec. An inherited parent-created
// socketpair/listener would describe the parent's credentials, not this child.
func TestSupervisorPeerNativeHelper(t *testing.T) {
	address := os.Getenv("NKE_TEST_SUPERVISOR_PEER")
	if address == "" {
		return
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: address, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "peer-listener-ready")
	connection, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var action [1]byte
	if _, err = io.ReadFull(connection, action[:]); err != nil || action[0] != 'D' {
		t.Fatalf("missing native detach fixture request: %v", err)
	}
	// The fixture deliberately inherits its parent's process group. It is not
	// an OwnedProcess; no pre-detach group-finalizer or Wait goroutine exists.
	sid, err := unix.Setsid()
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(connection).Encode(struct{ PID, SID, Group int }{os.Getpid(), sid, unix.Getpgrp()}); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(connection, action[:]); err != nil || action[0] != 'Q' {
		t.Fatalf("missing natural fixture exit request: %v", err)
	}
}

func TestSupervisorPeerNativeIdentityExitAndClose(t *testing.T) {
	if err := CheckSupervisorPeerCapability(t.Context()); err != nil {
		// Supported native paths do not skip a missing required kernel primitive.
		t.Fatal(err)
	}
	for _, mode := range []string{"natural exit", "close observation"} {
		t.Run(mode, func(t *testing.T) {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				t.Fatal(err)
			}
			address := "@nke-peer-fixture-" + hex.EncodeToString(random[:])
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSupervisorPeerNativeHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "NKE_TEST_SUPERVISOR_PEER="+address)
			cmd.WaitDelay = time.Second
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// This short-lived fixture creates no descendants. Context cancellation
			// bounds failure; the sole Wait below always joins it before returning.
			defer func() {
				if t.Failed() {
					cancel()
				}
				waitErr := cmd.Wait()
				cancel()
				if waitErr != nil && !t.Failed() {
					t.Fatalf("native fixture did not exit naturally with zero: %v %s", waitErr, &diagnostic)
				}
			}()
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || strings.TrimSpace(line) != "peer-listener-ready" {
				t.Fatalf("native listener failed: %v %s", err, &diagnostic)
			}
			dialed, err := (&net.Dialer{}).DialContext(ctx, "unix", address)
			if err != nil {
				t.Fatal(err)
			}
			connection := dialed.(*net.UnixConn)
			defer connection.Close()
			if err = connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			peer, err := ObserveSupervisorPeer(ctx, connection, cmd.Process.Pid+1)
			var coded *nkderr.Error
			if peer != nil || !errors.As(err, &coded) || coded.Code != nkderr.CodePreflightFailed {
				t.Fatalf("wrong selected child was admitted: %v", err)
			}
			peer, err = ObserveSupervisorPeer(ctx, connection, cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			if peer.PID() != cmd.Process.Pid || peer.UID() != uint32(os.Getuid()) {
				t.Fatal("peer did not describe the actual native child")
			}
			if _, err = connection.Write([]byte("D")); err != nil {
				t.Fatal(err)
			}
			var result struct{ PID, SID, Group int }
			if err = json.NewDecoder(connection).Decode(&result); err != nil {
				t.Fatal(err)
			}
			actualSID, err := unix.Getsid(cmd.Process.Pid)
			if err != nil || actualSID != cmd.Process.Pid || result.PID != result.SID || result.PID != result.Group {
				t.Fatalf("native child did not detach: %+v %v", result, err)
			}
			short, stop := context.WithTimeout(ctx, 10*time.Millisecond)
			err = peer.WaitExit(short)
			stop()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("live peer fabricated exit: %v", err)
			}
			if mode == "close observation" {
				if err = peer.Close(); err != nil {
					t.Fatal(err)
				}
				if err = peer.WaitExit(ctx); !errors.As(err, &coded) || coded.Code != nkderr.CodeUsage {
					t.Fatal("closed descriptor remained an issued observation", err)
				}
			}
			// Closing/timing out observation did not signal or delete the child:
			// it still receives and completes this natural application request.
			if _, err = connection.Write([]byte("Q")); err != nil {
				t.Fatal(err)
			}
			if mode == "natural exit" {
				if err = peer.WaitExit(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				// Read EOF before the owner Wait, avoiding cancellation racing exit.
				if _, err = connection.Read(make([]byte, 1)); err != io.EOF {
					t.Fatalf("closed-observation child did not finish: %v", err)
				}
			}
		})
	}
}

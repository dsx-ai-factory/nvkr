// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCaptureHelper(t *testing.T) {
	mode := os.Getenv("NVKR_CAPTURE_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "text":
		fmt.Print(os.Getenv("NVKR_CAPTURE_TEXT"))
	case "fail":
		fmt.Println("failure output")
		os.Exit(3)
	case "descendant":
		f, err := os.OpenFile(os.Getenv("NVKR_CAPTURE_FIFO"), os.O_WRONLY, 0)
		if err != nil {
			os.Exit(80)
		}
		defer f.Close()
		if err := os.WriteFile(os.Getenv("NVKR_CAPTURE_READY"), []byte("ready"), 0o600); err != nil {
			os.Exit(81)
		}
		time.Sleep(30 * time.Second)
	case "parent", "overflow", "exit":
		cmd := exec.Command(os.Args[0], "-test.run=^TestCaptureHelper$")
		cmd.Env = append(os.Environ(), "NVKR_CAPTURE_HELPER=descendant")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(82)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv("NVKR_CAPTURE_READY")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(83)
			}
			time.Sleep(time.Millisecond)
		}
		if mode == "overflow" {
			fmt.Print("first diagnostic\n" + strings.Repeat("x", 65536))
		} else {
			fmt.Println("running")
		}
		if mode != "exit" {
			time.Sleep(30 * time.Second)
		}
	}
	os.Exit(0)
}
func helperArgs(t *testing.T, mode string) ([]string, []string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{exe, "-test.run=^TestCaptureHelper$"}, append(os.Environ(), "NVKR_CAPTURE_HELPER="+mode)
}
func TestCaptureBoundary(t *testing.T) {
	for _, n := range []int{16, 17} {
		for _, emit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", n, emit), func(t *testing.T) {
				text := strings.Repeat("é", n)
				argv, env := helperArgs(t, "text")
				env = append(env, "NVKR_CAPTURE_TEXT="+text)
				var out bytes.Buffer
				log, err := captured(t.Context(), argv, "", env, 5*time.Second, emit, 32, &out, io.Discard)
				if n == 16 {
					if err != nil || log != text {
						t.Fatalf("%q %v", log, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "stdout exceeded 32-byte capture limit") {
					t.Fatalf("%v", err)
				}
				want := ""
				if emit || n == 17 {
					want = text
				}
				if out.String() != want {
					t.Fatalf("output %q want %q", out.String(), want)
				}
			})
		}
	}
	argv, env := helperArgs(t, "fail")
	var out bytes.Buffer
	_, err := captured(t.Context(), argv, "", env, 5*time.Second, true, 1024, &out, io.Discard)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(out.String(), "failure output") {
		t.Fatalf("%s %v", out.String(), err)
	}
}
func TestCaptureStopsDescendants(t *testing.T) {
	for _, mode := range []string{"parent", "overflow", "exit"} {
		for _, emit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", mode, emit), func(t *testing.T) {
				dir := t.TempDir()
				fifo := filepath.Join(dir, "pipe")
				ready := filepath.Join(dir, "ready")
				if err := syscall.Mkfifo(fifo, 0o600); err != nil {
					t.Fatal(err)
				}
				fd, err := syscall.Open(fifo, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer syscall.Close(fd)
				argv, env := helperArgs(t, mode)
				env = append(env, "NVKR_CAPTURE_FIFO="+fifo, "NVKR_CAPTURE_READY="+ready)
				var out bytes.Buffer
				timeout := 2 * time.Second
				if mode == "exit" {
					timeout = 10 * time.Second
				}
				_, err = captured(t.Context(), argv, "", env, timeout, emit, 65536, &out, io.Discard)
				if err == nil {
					t.Fatal("expected timeout, overflow or inherited-pipe error")
				}
				if _, err := os.Stat(ready); err != nil {
					t.Fatal("descendant never started", err)
				}
				if mode == "overflow" && (!strings.HasPrefix(out.String(), "first diagnostic\n") || out.Len() <= 65536) {
					t.Fatal("lost overflow diagnostics")
				}
				deadline := time.Now().Add(time.Second)
				for {
					var b [1]byte
					n, err := syscall.Read(fd, b[:])
					if n == 0 && err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("descendant retained FIFO: %d %v", n, err)
					}
					time.Sleep(time.Millisecond)
				}
			})
		}
	}
}
func TestCaptureCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	argv, env := helperArgs(t, "text")
	if _, err := captured(ctx, argv, "", env, time.Second, false, 1024, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/siteidentity"
	"golang.org/x/sys/unix"
)

func TestTransferredPipeOwnership(t *testing.T) {
	for _, access := range []int{unix.O_RDONLY, unix.O_WRONLY} {
		t.Run(fmt.Sprint(access), func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			original := reader
			if access == unix.O_WRONLY {
				original = writer
			}
			owned, err := adoptTransferredPipe(t.Context(), original, access, "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			defer owned.Close()
			if _, err := original.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("original descriptor was not consumed: %v", err)
			}
			raw, err := owned.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			if err := raw.Control(func(fd uintptr) {
				flags, flagErr := unix.FcntlInt(fd, unix.F_GETFL, 0)
				descriptor, descriptorErr := unix.FcntlInt(fd, unix.F_GETFD, 0)
				if fd < 3 || flagErr != nil || descriptorErr != nil || flags&unix.O_NONBLOCK == 0 ||
					flags&unix.O_ACCMODE != access || descriptor&unix.FD_CLOEXEC == 0 {
					t.Errorf("private pollable ownership mismatch: fd=%d flags=%d descriptor=%d errors=%v/%v", fd, flags, descriptor, flagErr, descriptorErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err := owned.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatalf("owned pipe has no deadlines: %v", err)
			}
		})
	}
}

func TestTransferredPipeRejectsOtherDescriptors(t *testing.T) {
	for _, name := range []string{"nil", "regular", "device", "closed", "wrong read", "wrong write"} {
		t.Run(name, func(t *testing.T) {
			var original *os.File
			access := unix.O_WRONLY
			switch name {
			case "regular", "closed":
				file, err := os.CreateTemp(t.TempDir(), "synthetic")
				if err != nil {
					t.Fatal(err)
				}
				original = file
				if name == "closed" {
					_ = file.Close()
				}
			case "device":
				file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				original = file
			case "wrong read", "wrong write":
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				defer writer.Close()
				original = reader
				if name == "wrong write" {
					original, access = writer, unix.O_RDONLY
				}
			}
			owned, err := adoptTransferredPipe(t.Context(), original, access, "synthetic")
			var coded *e.Error
			if owned != nil || !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("descriptor refusal differs: owned=%v error=%v", owned, err)
			}
			if original != nil {
				if _, err := original.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("refused transfer remained open: %v", err)
				}
			}
		})
	}
}

func TestTransferredPipeContextRefusalConsumesOriginal(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		code  e.Code
		cause error
	}{{"nil", nil, e.CodeUsage, nil}, {"canceled", canceled, e.CodeTimeout, context.Canceled}} {
		t.Run(tc.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			owned, err := adoptTransferredPipe(tc.ctx, writer, unix.O_WRONLY, "synthetic")
			var coded *e.Error
			if owned != nil || !errors.As(err, &coded) || coded.Code != tc.code ||
				(tc.cause != nil && !errors.Is(err, tc.cause)) {
				t.Fatalf("context code/cause differs: owned=%v err=%v", owned, err)
			}
			if _, err := writer.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("context refusal lost transferred ownership: %v", err)
			}
		})
	}
}

func stdioChild(t *testing.T, mode string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	if runtime.GOARCH != "arm64" {
		t.Skip("actual identity helper execution requires Linux ARM64; pipe ownership tests run on every Linux architecture")
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, program, "-test.run=^TestHelperProcessStdio$")
	cmd.Env = []string{"NVKR_SITE_IDENTITY_STDIO_TEST=" + mode}
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return cmd, &stdout, &stderr
}

func assertChildExit(t *testing.T, err error, want e.Code, stderr *bytes.Buffer) {
	t.Helper()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != e.ExitCode(e.New(want, "synthetic")) {
		t.Fatalf("child exit differs: %v; stderr=%s", err, stderr.String())
	}
}

func assertNotStarted(t *testing.T, raw []byte, code e.Code) {
	t.Helper()
	if bytes.Count(raw, []byte{'\n'}) != 1 || !bytes.HasSuffix(raw, []byte{'\n'}) {
		t.Fatalf("not one original helper result: %q", raw)
	}
	var result siteidentity.Result
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != siteidentity.SchemaVersion || result.Completion != siteidentity.NotStarted ||
		result.FailureCode != code || result.CallerID != "" || result.Status != nil || result.Reconfiguration != "" || result.After != nil {
		t.Fatalf("invalid input reached identity or changed completion: %+v", result)
	}
}

func TestCommandInheritedBlockingStdio(t *testing.T) {
	cmd, stdout, stderr := stdioChild(t, "invalid")
	cmd.Stdin = bytes.NewReader([]byte("{}\n"))
	assertChildExit(t, cmd.Run(), e.CodeUsage, stderr)
	assertNotStarted(t, stdout.Bytes(), e.CodeUsage)
}

func TestCommandContextAdmissionBeforeStdio(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{{"nil", nil, e.CodeUsage}, {"canceled", canceled, e.CodeTimeout}} {
		t.Run(tc.name, func(t *testing.T) {
			err := run(tc.ctx, nil)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("runtime owner admission differs: %v", err)
			}
			for _, file := range []*os.File{os.Stdin, os.Stdout} {
				if _, err := file.Stat(); err != nil {
					t.Fatalf("pre-admission stdio was consumed: %v", err)
				}
			}
		})
	}
}

func TestUnsupportedArchitectureRefusesBeforeStdio(t *testing.T) {
	if runtime.GOARCH == "arm64" {
		t.Skip("Linux ARM64 is the supported helper runtime")
	}
	err := run(t.Context(), nil)
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("unsupported runtime was admitted: %v", err)
	}
	for _, file := range []*os.File{os.Stdin, os.Stdout} {
		if _, err := file.Stat(); err != nil {
			t.Fatalf("unsupported runtime consumed stdio: %v", err)
		}
	}
}

func TestCommandHeldOpenInputCancelsAndJoins(t *testing.T) {
	cmd, stdout, stderr := stdioChild(t, "cancel")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	assertChildExit(t, cmd.Wait(), e.CodeTimeout, stderr)
	assertNotStarted(t, stdout.Bytes(), e.CodeTimeout)
}

func TestCommandOutputAdmissionBeforeInput(t *testing.T) {
	cmd, _, stderr := stdioChild(t, "invalid")
	output, err := os.CreateTemp(t.TempDir(), "refused-output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd.Stdout = output
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	assertChildExit(t, cmd.Wait(), e.CodeEngineCapability, stderr)
	info, err := output.Stat()
	if err != nil || info.Size() != 0 {
		t.Fatalf("refused output was written: %v %v", info, err)
	}
}

func TestCommandBackpressuredOrClosedOutputJoins(t *testing.T) {
	for _, name := range []string{"full", "closed"} {
		t.Run(name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			if name == "closed" {
				_ = reader.Close()
			} else {
				raw, err := writer.SyscallConn()
				if err != nil {
					t.Fatal(err)
				}
				if err := raw.Control(func(fd uintptr) {
					for {
						if _, err := unix.Write(int(fd), make([]byte, 4096)); err != nil {
							if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
								t.Errorf("filling synthetic pipe: %v", err)
							}
							break
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			cmd, _, stderr := stdioChild(t, "invalid")
			cmd.Stdin, cmd.Stdout = bytes.NewReader([]byte("{}\n")), writer
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = writer.Close()
			assertChildExit(t, cmd.Wait(), e.CodeUnavailable, stderr)
		})
	}
}

func TestHelperProcessStdio(t *testing.T) {
	mode := os.Getenv("NVKR_SITE_IDENTITY_STDIO_TEST")
	if mode == "" {
		return
	}
	ctx := context.Background()
	if mode == "cancel" {
		bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		ctx = bounded
	}
	// Exercise the actual command entry, not a parallel implementation. The only
	// input is an invalid schema or a held-open empty pipe; neither can reach RPC.
	err := run(ctx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(e.ExitCode(err))
	}
	os.Exit(0)
}

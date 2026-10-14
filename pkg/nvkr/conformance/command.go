// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
	sink     io.Writer
}

// Keep the backing buffer private: every copy must pass through bounded Write.
func (b *boundedBuffer) Len() int      { return b.buffer.Len() }
func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }
func (b *boundedBuffer) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{b}, r)
}

func (b *boundedBuffer) Write(raw []byte) (int, error) {
	n := min(len(raw), b.limit-b.Len())
	if n < len(raw) {
		b.overflow = true
	}
	if n > 0 {
		if b.sink != nil {
			if _, err := b.sink.Write(raw[:n]); err != nil {
				return 0, err
			}
		}
		_, _ = b.buffer.Write(raw[:n])
	}
	return len(raw), nil
}
func (a *admitted) command(ctx context.Context, tool *heldfile.File, args []string, workspace string, stdin io.Reader, timeout time.Duration) ([]byte, int, error) {
	return a.commandLimit(ctx, tool, args, workspace, stdin, timeout, maxAPIBytes)
}
func (a *admitted) commandLimit(ctx context.Context, tool *heldfile.File, args []string, workspace string, stdin io.Reader, timeout time.Duration, limit int) ([]byte, int, error) {
	if workspace == "" {
		return nil, 1, e.New(e.CodeUsage, "private command workspace required")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Each command gets its own immutable credential descriptor: heldfile
	// serializes shared descriptors, which otherwise prevents concurrent polling.
	kube, err := heldfile.FromBytesContext(ctx, a.kubeBytes, heldfile.Options{Label: "private kubeconfig", MaxBytes: 1 << 20, ExpectedDigest: a.options.Kubeconfig.Digest})
	if err != nil {
		return nil, 1, captureFailure(ctx, err, e.CodeUnpinnedInput, "holding command kubeconfig")
	}
	defer func() { _ = kube.Close() }()
	path, err := heldfile.ChildPath(1)
	if err != nil {
		return nil, 1, capability(err)
	}
	args = append([]string{"--kubeconfig=" + path}, args...)
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: maxDiagnosticBytes}
	if tool == a.hydrophone {
		for _, target := range []struct {
			name string
			b    *boundedBuffer
		}{{"hydrophone.stdout.log", stdout}, {"hydrophone.stderr.log", stderr}} {
			f, err := os.OpenFile(filepath.Join(workspace, target.name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return nil, 1, e.Wrap(e.CodeFetchFailed, err, "creating private Hydrophone diagnostics")
			}
			defer f.Close()
			target.b.sink = f
		}
	}
	err = heldfile.RunContext(ctx, tool, args, heldfile.RunOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr, Dir: workspace, Env: []string{"HOME=" + workspace, "TMPDIR=" + workspace, "PATH=", "KUBECONFIG=" + path, "KUBECACHEDIR=" + filepath.Join(workspace, "kube-cache")}, TerminationGrace: 250 * time.Millisecond}, kube)
	if tool != a.hydrophone && stdout.overflow && ctx.Err() == nil {
		return bytes.Clone(stdout.Bytes()), 1, failure("command output byte budget exhausted")
	}
	code := 0
	if err != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 && exit.ExitCode() <= 255 {
			code = exit.ExitCode()
		}
		if ctx.Err() != nil {
			return bytes.Clone(stdout.Bytes()), code, e.Wrap(e.CodeTimeout, ctx.Err(), "conformance command canceled or timed out")
		}
		diagnostics := "private diagnostic text excluded from public errors"
		if tool == a.hydrophone {
			diagnostics = "private diagnostics retained in producer workspace"
		}
		return bytes.Clone(stdout.Bytes()), code, failure("pinned command failed (exit " + strconv.Itoa(code) + "); " + diagnostics)
	}
	return bytes.Clone(stdout.Bytes()), code, nil
}
func (a *admitted) kube(ctx context.Context, workspace string, stdin io.Reader, args ...string) ([]byte, int, error) {
	args = append([]string{"--context=" + a.options.Context, "--request-timeout=" + a.options.CommandTimeout.String()}, args...)
	return a.command(ctx, a.kubectl, args, workspace, stdin, a.options.CommandTimeout)
}

func (a *admitted) kubeResults(ctx context.Context, workspace string, args ...string) ([]byte, int, error) {
	args = append([]string{"--context=" + a.options.Context, "--request-timeout=" + a.options.CommandTimeout.String()}, args...)
	return a.commandLimit(ctx, a.kubectl, args, workspace, nil, a.options.CommandTimeout, maxReportBytes)
}

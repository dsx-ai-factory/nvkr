// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

type boundedBuffer struct {
	bytes.Buffer
	limit  int
	cancel context.CancelFunc
}

func (b *boundedBuffer) Write(raw []byte) (int, error) {
	if len(raw) > b.limit-b.Len() {
		b.cancel()
		return 0, errors.New("command output budget")
	}
	return b.Buffer.Write(raw)
}
func (a *admitted) command(ctx context.Context, tool *heldfile.File, args []string, workspace string, stdin io.Reader, timeout time.Duration) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Each command gets its own immutable credential descriptor: heldfile
	// serializes shared descriptors, which otherwise prevents concurrent polling.
	kube, err := heldfile.FromBytesContext(ctx, a.kubeBytes, heldfile.Options{Label: "private kubeconfig", MaxBytes: 1 << 20, ExpectedDigest: a.options.Kubeconfig.Digest})
	if err != nil {
		return nil, 1, e.Wrap(e.CodeUnpinnedInput, err, "holding command kubeconfig")
	}
	defer func() { _ = kube.Close() }()
	path, err := heldfile.ChildPath(1)
	if err != nil {
		return nil, 1, capability(err)
	}
	args = append([]string{"--kubeconfig=" + path}, args...)
	stdout := &boundedBuffer{limit: maxAPIBytes, cancel: cancel}
	stderr := &boundedBuffer{limit: maxDiagnosticBytes, cancel: cancel}
	err = heldfile.RunContext(ctx, tool, args, heldfile.RunOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr, Dir: workspace, Env: []string{"HOME=" + workspace, "TMPDIR=" + workspace, "PATH=", "KUBECONFIG=" + path}, TerminationGrace: 250 * time.Millisecond}, kube)
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
		return bytes.Clone(stdout.Bytes()), code, failure("pinned command failed (exit " + strconv.Itoa(code) + "); private diagnostics excluded")
	}
	return bytes.Clone(stdout.Bytes()), code, nil
}
func (a *admitted) kube(ctx context.Context, workspace string, stdin io.Reader, args ...string) ([]byte, int, error) {
	args = append([]string{"--context=" + a.options.Context, "--request-timeout=" + a.options.CommandTimeout.String()}, args...)
	return a.command(ctx, a.kubectl, args, workspace, stdin, a.options.CommandTimeout)
}

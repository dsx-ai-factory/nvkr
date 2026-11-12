// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestAICRValidationFixedInvocationAndRefusals(t *testing.T) {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "checksums.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body         string
		mismatch, canceled bool
		want               e.Code
	}{
		{name: "fixed verification", body: `case " $* " in *" verify /nvkr/inputs/bundle "*) ;; *) exit 95;; esac`},
		{name: "verification failure", body: "exit 2", want: e.CodeVerifyFailed},
		{name: "tool substitution", body: "exit 0", mismatch: true, want: e.CodeVerifyFailed},
		{name: "cancellation", body: "sleep 30", canceled: true, want: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			toolBytes := []byte("#!/bin/sh\nexit 0\n")
			node := pinnedToolNode(toolBytes)
			node.Platform, node.OutputType, node.ArtifactPath = "linux/"+runtime.GOARCH, "oci-layout", ""
			node.Command = []string{"bundle", "--output", "oci-layout:/nvkr/output/layout"}
			tool := filepath.Join(t.TempDir(), "aicr")
			if tc.mismatch {
				toolBytes = append(toolBytes, '#')
			}
			if err := os.WriteFile(tool, toolBytes, 0o755); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if tc.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}
			err := ValidateAICRBundle(ctx, node, archive.Bytes(), RunOptions{Runtime: fakePinnedToolRuntime(t, tc.body), ToolExecutable: tool, Stdout: io.Discard, Stderr: io.Discard})
			if pinnedToolCode(err) != tc.want {
				t.Fatalf("error %v; want %s", err, tc.want)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCaptureRetainedDiskOwnsSnapshot(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix descriptor snapshot")
	}
	for _, mode := range []string{"unchanged", "source replaced", "source inode modified", "canceled", "bad producer"} {
		t.Run(mode, func(t *testing.T) {
			plan, expected, completion := diskObservationFixture(t, "none")
			payload := testQCOW2HeaderPayload()
			root := t.TempDir()
			path, record := filepath.Join(root, "disk.qcow2"), filepath.Join(root, "result.json")
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if mode == "bad producer" {
				completion.Result.Toolchain = "other"
			}
			raw, err := canonical.Marshal(completion.Result)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(record, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			owner, observed, err := CaptureRetainedDiskPayload(ctx, plan, expected, path, record)
			if mode == "canceled" || mode == "bad producer" {
				code := e.CodeVerifyFailed
				if mode == "canceled" {
					code = e.CodeTimeout
				}
				assertFactoryErrorCode(t, err, code)
				if owner != nil || observed.Digest != "" {
					t.Fatal("failure leaked transport or observation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = owner.Close() }()
			if observed.Outcome != "" || observed.Digest != completion.Result.SHA256 || owner.Inspection().PayloadDigest != observed.Digest {
				t.Fatal("capture changed identity or inferred execution authority")
			}
			switch mode {
			case "source replaced":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("substituted path"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "source inode modified":
				if err := os.WriteFile(path, []byte("substituted inode"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if _, err := owner.WriteToContext(t.Context(), &out); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), payload) {
				t.Fatal("source mutation changed captured payload")
			}
			copyOwner := *owner
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = copyOwner.WriteToContext(t.Context(), io.Discard)
			assertFactoryErrorCode(t, err, e.CodeGateUnmet)
		})
	}
}

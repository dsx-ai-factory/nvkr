// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && cgo

package nvkedemo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func darwinRetainedToken(t *testing.T) string {
	t.Helper()
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw[:])
}

func TestDarwinRetainedEndpointOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	token := darwinRetainedToken(t)
	listener, cleanup, err := listenRetainedEndpoint(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer listener.Close()
	address := listener.Addr().String()
	info, err := os.Lstat(filepath.Dir(address))
	if err != nil || info.Mode().Perm() != 0o700 || !validRetainedEndpoint(address, token) || validRetainedEndpoint(address+"-other", token) {
		t.Fatal("native endpoint is not exact and private", err)
	}
	if duplicate, _, err := listenRetainedEndpoint(ctx, token); duplicate != nil || err == nil {
		t.Fatal("existing endpoint was adopted")
	}
	dialed, err := (&net.Dialer{}).DialContext(ctx, "unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer dialed.Close()
	if err := listener.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	accepted, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	if got := retainedRequesterUID(accepted); got != uint32(os.Getuid()) {
		t.Fatal("kernel requester UID differs", got)
	}
	_ = accepted.Close()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(address); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatal("transport close unlinked before held identity cleanup", err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal("joined endpoint cleanup was not idempotent", err)
	}
	if _, err := os.Lstat(filepath.Dir(address)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned endpoint directory remains", err)
	}
}

func TestDarwinRetainedEndpointPreservesReplacement(t *testing.T) {
	for _, target := range []string{"socket", "directory"} {
		t.Run(target, func(t *testing.T) {
			listener, cleanup, err := listenRetainedEndpoint(t.Context(), darwinRetainedToken(t))
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			directory := filepath.Dir(address)
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			original := address + ".original"
			if target == "directory" {
				original = directory + ".original"
				if err := os.Rename(directory, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Rename(address, original); err != nil {
				t.Fatal(err)
			}
			// All paths here are owned synthetic fixtures, never deployment files.
			defer func() {
				_ = cleanup()
				_ = os.Remove(address)
				_ = os.Remove(filepath.Join(original, "control.sock"))
				_ = os.Remove(original)
				_ = os.Remove(directory)
			}()
			if err := os.WriteFile(address, []byte("foreign synthetic fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			var coded *nvkrerr.Error
			if err := cleanup(); !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
				t.Fatal("replacement was not rejected", err)
			}
			if raw, err := os.ReadFile(address); err != nil || string(raw) != "foreign synthetic fixture" {
				t.Fatal("replacement was deleted or modified", err)
			}
		})
	}
}

func TestDarwinRetainedAdmissionBeforeEndpointWork(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		ctx  context.Context
		code nvkrerr.Code
	}{{"nil", nil, nvkrerr.CodeUsage}, {"canceled", canceled, nvkrerr.CodeTimeout}} {
		t.Run(test.name, func(t *testing.T) {
			token := darwinRetainedToken(t)
			address := filepath.Join(darwinRetainedParent, "nvke-retained-"+token[:32], "control.sock")
			listener, cleanup, err := listenRetainedEndpoint(test.ctx, token)
			var coded *nvkrerr.Error
			if listener != nil || cleanup != nil || !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatal("context admission differs", err)
			}
			if _, err := os.Lstat(filepath.Dir(address)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("context refusal created endpoint output", err)
			}
		})
	}
}

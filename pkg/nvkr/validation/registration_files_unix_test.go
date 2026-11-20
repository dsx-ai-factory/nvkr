// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package validation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func TestRegistrationsRefuseSymlinksAndFIFOs(t *testing.T) {
	for _, tc := range []struct{ name, path, special string }{
		{"registration-symlink", "nvke/nvkectl/contract.yaml", "symlink"},
		{"suite-group-symlink", "nvke/nvkectl", "symlink"},
		{"repository-symlink", "nvke", "symlink"},
		{"registration-fifo", "nvke/nvkectl/contract.yaml", "fifo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := registrationFixture(t)
			path := filepath.Join(root, tc.path)
			// Keep the original outside the catalog so only the link is discovered.
			original := filepath.Join(t.TempDir(), "original")
			if err := os.Rename(path, original); err != nil {
				t.Fatal(err)
			}
			if tc.special == "symlink" {
				if err := os.Symlink(original, path); err != nil {
					t.Fatal(err)
				}
			} else if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := LoadRegistrations(ctx, root)
				done <- err
			}()
			select {
			case err := <-done:
				catalogCode(t, err, e.CodeUsage)
			case <-ctx.Done():
				// Unblock a regressed blocking FIFO open and join the reader before failing.
				if tc.special == "fifo" {
					if fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0); err == nil {
						_ = unix.Close(fd)
					}
				}
				<-done
				t.Fatal("special entry was not refused promptly")
			}
		})
	}
}

func TestRegistrationsRefuseSymlinkedRoot(t *testing.T) {
	root := registrationFixture(t)
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	// Trailing path syntax must not make the final component follow the link.
	for _, spelling := range []string{link, link + "/", link + "/."} {
		_, err := LoadRegistrations(t.Context(), spelling)
		catalogCode(t, err, e.CodeUsage)
	}
	// An ordinary directory still loads.
	if _, err := LoadRegistrations(t.Context(), root); err != nil {
		t.Fatal(err)
	}
}

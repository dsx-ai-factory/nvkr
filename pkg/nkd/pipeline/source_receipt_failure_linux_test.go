// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

// These fixtures exercise concrete receipt IO failure paths, not a forged
// successful source observation. Consumers must still authenticate source bytes.
func TestSourceReceiptHeldCommitFailuresAreCoded(t *testing.T) {
	for _, failure := range []string{"closed parent", "rename obstruction", "directory sync"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "receipt.json")
			parent, _, err := securepath.OpenDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			switch failure {
			case "closed parent":
				if err := parent.Close(); err != nil {
					t.Fatal(err)
				}
			case "rename obstruction":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "directory sync":
				if err := parent.Close(); err != nil {
					t.Fatal(err)
				}
				fd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				parent = os.NewFile(uintptr(fd), "sync-refusal-directory")
				defer parent.Close()
			}
			err = writeSourceReceiptContext(t.Context(), path, sourceReceipt{Schema: sourceReceiptSchema}, parent)
			if code(err) != e.CodeFetchFailed {
				t.Fatalf("receipt %s failure was not coded: %v", failure, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != "receipt.json" {
					t.Fatalf("failed receipt commit leaked temporary member %q", entry.Name())
				}
			}
			if failure == "closed parent" && len(entries) != 0 {
				t.Fatal("closed parent published receipt bytes")
			}
			// A directory-sync failure happens after rename: physical bytes may
			// remain, but the operation must report failure, not durable success.
		})
	}
}

func TestSourceReceiptReadFailuresDoNotBecomeCacheMisses(t *testing.T) {
	for _, held := range []bool{false, true} {
		for _, kind := range []string{"symlink", "directory", "invalid JSON"} {
			t.Run(kind+map[bool]string{false: "/ordinary", true: "/held"}[held], func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "receipt.json")
				outside := filepath.Join(t.TempDir(), "original")
				if err := os.WriteFile(outside, []byte("unchanged test data"), 0o600); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(outside, path)
				case "directory":
					err = os.Mkdir(path, 0o700)
				case "invalid JSON":
					err = os.WriteFile(path, []byte("["), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
				var directories []*os.File
				if held {
					parent, _, err := securepath.OpenDirectory(root)
					if err != nil {
						t.Fatal(err)
					}
					defer parent.Close()
					directories = append(directories, parent)
				}
				_, exists, err := readSourceReceiptContext(t.Context(), path, directories...)
				if exists || code(err) != e.CodeFetchFailed {
					t.Fatalf("invalid receipt became a cache miss/success: %t, %v", exists, err)
				}
				data, err := os.ReadFile(outside)
				if err != nil || string(data) != "unchanged test data" {
					t.Fatalf("receipt IO modified outside bytes: %q, %v", data, err)
				}
			})
		}
	}
}

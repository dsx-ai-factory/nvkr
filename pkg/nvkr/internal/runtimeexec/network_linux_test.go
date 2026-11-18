// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runtimeexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func TestManagedResolverDelegation(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root for real service ownership fixtures")
	}
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string, *unix.Stat_t)
		code   e.Code
	}{
		{name: "protected service-owned stub"},
		{name: "root-owned delegate parent", change: func(t *testing.T, root string, _ *unix.Stat_t) {
			if err := os.Chown(filepath.Join(root, "run/systemd/resolve"), 0, 0); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "absolute upstream resolver", change: func(t *testing.T, root string, _ *unix.Stat_t) {
			parent := filepath.Join(root, "run/systemd/resolve")
			if err := os.Rename(filepath.Join(parent, "stub-resolv.conf"), filepath.Join(parent, "resolv.conf")); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(root, "etc/resolv.conf")
			if err := os.Rename(name, name+".previous"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/run/systemd/resolve/resolv.conf", name); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "foreign held inode", change: func(_ *testing.T, _ string, stat *unix.Stat_t) { stat.Ino++ }, code: e.CodeEngineCapability},
		{name: "writable delegate parent", change: func(t *testing.T, root string, _ *unix.Stat_t) {
			if err := os.Chmod(filepath.Join(root, "run/systemd/resolve"), 0o777); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeEngineCapability},
		{name: "foreign parent owner", change: func(t *testing.T, root string, _ *unix.Stat_t) {
			if err := os.Chown(filepath.Join(root, "run/systemd/resolve"), 103, 103); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeEngineCapability},
		{name: "unprotected root ancestor", change: func(t *testing.T, root string, _ *unix.Stat_t) {
			if err := os.Chmod(filepath.Join(root, "run/systemd"), 0o777); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeEngineCapability},
		{name: "foreign delegation owner", change: func(t *testing.T, root string, _ *unix.Stat_t) {
			if err := os.Lchown(filepath.Join(root, "etc/resolv.conf"), 102, 102); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeEngineCapability},
		{name: "writable target", change: func(t *testing.T, root string, _ *unix.Stat_t) {
			if err := os.Chmod(filepath.Join(root, "run/systemd/resolve/stub-resolv.conf"), 0o666); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeEngineCapability},
		{name: "unlisted target", change: func(t *testing.T, root string, _ *unix.Stat_t) {
			name := filepath.Join(root, "etc/resolv.conf")
			if err := os.Rename(name, name+".previous"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/tmp/other-resolver", name); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, directory := range []string{"etc", "run/systemd/resolve"} {
				if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			parent := filepath.Join(root, "run/systemd/resolve")
			name := filepath.Join(parent, "stub-resolv.conf")
			if err := os.WriteFile(name, []byte("nameserver 127.0.0.53\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, target := range []string{parent, name} {
				if err := os.Chown(target, 102, 102); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("../run/systemd/resolve/stub-resolv.conf", filepath.Join(root, "etc/resolv.conf")); err != nil {
				t.Fatal(err)
			}
			held, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			var stat unix.Stat_t
			if err := unix.Fstat(int(held.Fd()), &stat); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(t, root, &stat)
			}
			directory, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			err = admitManagedResolverAt(t.Context(), directory, stat)
			var coded *e.Error
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("admission = %v, want %s", err, tc.code)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := admitManagedResolverAt(ctx, directory, stat); !errors.As(err, &coded) || coded.Code != e.CodeTimeout {
				t.Fatalf("canceled admission = %v", err)
			}
		})
	}
}

func TestNetworkHandoffRejectsSubstitutedDescriptors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		signal     byte
		descriptor bool
	}{
		{"no descriptor", 'T', false}, {"wrong marker", 'X', true}, {"regular file instead of TAP", 'T', true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, child, err := networkSocketPair()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = parent.Close() }()
			defer child.Close()
			if err := parent.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			file, err := os.CreateTemp(t.TempDir(), "wrong-device-")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			var rights []byte
			if tc.descriptor {
				rights = unix.UnixRights(int(file.Fd()))
			}
			if err := unix.Sendmsg(int(child.Fd()), []byte{tc.signal}, rights, nil, 0); err != nil {
				t.Fatal(err)
			}
			got, err := receiveNetworkTAP(parent)
			var coded *e.Error
			if got != nil || !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("invalid TAP admitted: %v %v", got, err)
			}
		})
	}
}

func TestResolverMountRejectsPackagedSymlink(t *testing.T) {
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := prepareMountPoint(root, "etc"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/outside", root.Name()+"/etc/resolv.conf"); err != nil {
		t.Fatal(err)
	}
	var coded *e.Error
	if err := prepareResolverMountPoint(root); !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
		t.Fatalf("resolver symlink admitted: %v", err)
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runtimeexec

import (
	"errors"
	"os"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

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

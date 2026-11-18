// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package rootfs

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func layerFixture(t *testing.T, headers []tar.Header, trailer []byte) *heldfile.File {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, header := range headers {
		header.ModTime = time.Unix(0, 0)
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := writer.Write(bytes.Repeat([]byte{'x'}, int(header.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	buffer.Write(trailer)
	held, err := heldfile.FromBytesContext(context.Background(), buffer.Bytes(), heldfile.Options{Label: "runtime-fixture", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	return held
}

func rootHeader() tar.Header { return tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755} }

func requireStageCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v; want %s", err, code)
	}
}

func TestStageRootFS(t *testing.T) {
	headers := []tar.Header{
		rootHeader(),
		{Name: "./usr/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "./usr/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "./usr/bin/alias", Typeflag: tar.TypeLink, Linkname: "./usr/bin/tool"},
		{Name: "./usr/bin/tool", Typeflag: tar.TypeReg, Mode: 0o4755, Size: 7},
		{Name: "./bin", Typeflag: tar.TypeSymlink, Linkname: "/usr/bin"},
	}
	held := layerFixture(t, headers, nil)
	scratch := t.TempDir()
	tree, err := Stage(context.Background(), held, scratch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tree.Close() })
	if tree.Digest() != held.Digest() {
		t.Fatal("staging lost exact archive binding")
	}
	file, err := tree.Directory()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := tree.root.ReadFile("usr/bin/tool")
	if err != nil || string(content) != "xxxxxxx" {
		t.Fatalf("staged bytes: %q %v", content, err)
	}
	info, err := tree.root.Stat("usr/bin/tool")
	if err != nil {
		t.Fatal(err)
	}
	alias, err := tree.root.Stat("usr/bin/alias")
	if err != nil || !os.SameFile(info, alias) {
		t.Fatalf("hard link not retained: %v", err)
	}
	if info.Mode()&os.ModeSetuid != 0 || info.Mode().Perm() != 0o755 || !info.ModTime().Equal(time.Unix(0, 0)) {
		t.Fatalf("unexpected execution projection mode/time: %v", info)
	}
	link, err := tree.root.Readlink("bin")
	if err != nil || link != "/usr/bin" {
		t.Fatalf("absolute root-relative symlink lost: %q %v", link, err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = tree.Directory()
	requireStageCode(t, err, e.CodeGateUnmet)
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging leaked after close: %v %v", entries, err)
	}
}

func TestStageRejectsUnsafeLayersAndCleans(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []tar.Header
		trailer []byte
	}{
		{name: "absolute path", members: []tar.Header{{Name: "/escape", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{name: "parent escape", members: []tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{name: "unclean path", members: []tar.Header{{Name: "usr/../escape", Typeflag: tar.TypeReg, Mode: 0o644}}},
		{name: "duplicate root", members: []tar.Header{rootHeader()}},
		{name: "duplicate file", members: []tar.Header{{Name: "x", Typeflag: tar.TypeReg}, {Name: "./x", Typeflag: tar.TypeReg}}},
		{name: "device", members: []tar.Header{{Name: "device", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}}},
		{name: "fifo", members: []tar.Header{{Name: "fifo", Typeflag: tar.TypeFifo}}},
		{name: "symlink parent", members: []tar.Header{{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}, {Name: "escape/file", Typeflag: tar.TypeReg}}},
		{name: "internal symlink parent", members: []tar.Header{{Name: "real", Typeflag: tar.TypeDir, Mode: 0o755}, {Name: "alias", Typeflag: tar.TypeSymlink, Linkname: "real"}, {Name: "alias/file", Typeflag: tar.TypeReg}}},
		{name: "missing parent", members: []tar.Header{{Name: "absent/file", Typeflag: tar.TypeReg}}},
		{name: "hard link escape", members: []tar.Header{{Name: "link", Typeflag: tar.TypeLink, Linkname: "../escape"}}},
		{name: "hard link to symlink", members: []tar.Header{{Name: "symlink", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}, {Name: "link", Typeflag: tar.TypeLink, Linkname: "symlink"}}},
		{name: "hard link cycle", members: []tar.Header{{Name: "first", Typeflag: tar.TypeLink, Linkname: "second"}, {Name: "second", Typeflag: tar.TypeLink, Linkname: "first"}}},
		{name: "missing hard link target", members: []tar.Header{{Name: "link", Typeflag: tar.TypeLink, Linkname: "absent"}}},
		{name: "unknown PAX", members: []tar.Header{{Name: "file", Typeflag: tar.TypeReg, PAXRecords: map[string]string{"SCHILY.xattr.security.capability": "synthetic"}}}},
		{name: "trailing bytes", trailer: bytes.Repeat([]byte{'x'}, 512)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scratch := t.TempDir()
			headers := append([]tar.Header{rootHeader()}, tc.members...)
			held := layerFixture(t, headers, tc.trailer)
			tree, err := Stage(context.Background(), held, scratch)
			if tree != nil {
				_ = tree.Close()
				t.Fatal("unsafe archive issued a staging handle")
			}
			requireStageCode(t, err, e.CodeVerifyFailed)
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed staging left partial output: %v %v", entries, err)
			}
		})
	}
}

func TestStagePathDiagnosticIsBoundedEscapedAndFirstOnly(t *testing.T) {
	for _, tc := range []struct {
		name, member, want string
	}{
		{"traversal", "../first", `"../first"`},
		{"control characters", "bad\\line\nname", `"bad\\line\nname"`},
		{"long name", strings.Repeat("x", 4096) + "unprinted-tail", "4110 name bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held := layerFixture(t, []tar.Header{
				rootHeader(),
				{Name: tc.member, Typeflag: tar.TypeReg},
				{Name: "../second-offender", Typeflag: tar.TypeReg},
			}, nil)
			scratch := t.TempDir()
			_, err := Stage(t.Context(), held, scratch)
			requireStageCode(t, err, e.CodeVerifyFailed)
			message := err.Error()
			if !strings.Contains(message, tc.want) || !strings.Contains(message, "typeflag 0x30") ||
				strings.ContainsAny(message, "\n\r\x1b") || len(message) > 2048 ||
				strings.Contains(message, "second-offender") || strings.Contains(message, "unprinted-tail") {
				t.Fatalf("unsafe or incomplete path diagnostic: %q", message)
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatalf("diagnostic changed failed-stage cleanup: %v", err)
			}
		})
	}
}

func TestStageRequiresCompleteTerminator(t *testing.T) {
	complete := layerFixture(t, []tar.Header{rootHeader()}, nil)
	raw, err := complete.ReadAllContext(context.Background(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{512, 1024} {
		held, err := heldfile.FromBytesContext(context.Background(), raw[:len(raw)-count], heldfile.Options{Label: "truncated", MaxBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = held.Close() })
		_, err = Stage(context.Background(), held, t.TempDir())
		requireStageCode(t, err, e.CodeVerifyFailed)
	}
}

func TestStageAdmissionBeforeFilesystemWork(t *testing.T) {
	held := layerFixture(t, []tar.Header{rootHeader()}, nil)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		layer *heldfile.File
		code  e.Code
	}{
		{"nil context", nil, held, e.CodeUsage},
		{"canceled", canceled, held, e.CodeTimeout},
		{"absent", context.Background(), nil, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Stage(tc.ctx, tc.layer, filepath.Join(t.TempDir(), "absent"))
			requireStageCode(t, err, tc.code)
		})
	}
}

func TestStageCleansReadOnlyDirectories(t *testing.T) {
	held := layerFixture(t, []tar.Header{
		{Name: "./", Typeflag: tar.TypeDir, Mode: 0o500},
		{Name: "./private", Typeflag: tar.TypeDir, Mode: 0o500},
		{Name: "./private/data", Typeflag: tar.TypeReg, Mode: 0o400, Size: 1},
	}, nil)
	tree, err := Stage(context.Background(), held, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
}

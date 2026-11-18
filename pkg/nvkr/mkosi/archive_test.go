// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mkosi

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real GNU tar, separate invocations, different creation and hard-link order,
// and a PAX-length name: do not replace this with argument-string assertions.
func TestArchiveWrapperReproducibility(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, "/usr/bin/tar", "--version").Output()
	if err != nil || !bytes.Contains(version, []byte("GNU tar")) {
		t.Skip("requires GNU tar, exercised by the native Linux fixture")
	}
	wrapper := filepath.Join(t.TempDir(), "tar")
	if err := os.WriteFile(wrapper, []byte(ArchiveWrapper()), 0o555); err != nil {
		t.Fatal(err)
	}
	var archives [][]byte
	for _, order := range [][]string{{"z", "a"}, {"a", "z"}} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, order[0]), []byte("same payload"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(root, order[0]), filepath.Join(root, order[1])); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, strings.Repeat("long-name-", 15)), []byte("long name"), 0o644); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, "/bin/sh", wrapper, "--create", "--file=-", "--directory="+root,
			"--acls", "--selinux", "--xattrs", "--pax-option=delete=atime,delete=ctime,delete=mtime",
			"--mtime=@0", "--owner=0", "--group=0", ".")
		command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		var stderr bytes.Buffer
		command.Stderr = &stderr
		raw, err := command.Output()
		if err != nil {
			t.Fatalf("archive: %v: %s", err, stderr.String())
		}
		archives = append(archives, raw)
	}
	if !bytes.Equal(archives[0], archives[1]) {
		t.Fatal("tar bytes depend on creation order, hard-link enumeration or process metadata")
	}
	archive := tar.NewReader(bytes.NewReader(archives[0]))
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Uname != "" || header.Gname != "" || header.Uid != 0 || header.Gid != 0 {
			t.Fatal("archive retained user/group lookup results")
		}
		if header.Name == "./z" && (header.Typeflag != tar.TypeLink || header.Linkname != "./a") {
			t.Fatal("hard link did not choose its lexical first member")
		}
	}
}

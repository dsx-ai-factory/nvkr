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
		raw := archiveThroughPython(t, ctx, wrapper, root, true)
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

// Exercise Python's executable selection, not a direct invocation of the
// wrapper. Mkosi v26 applies sandbox environment changes in preexec_fn, after
// Python has already computed the candidate executable paths. Both cases use
// the adapter in the child PATH; the control omits it from the parent PATH.
func TestArchiveAdapterSelection(t *testing.T) {
	t.Setenv("PATH", "/untrusted/bin")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, "/usr/bin/tar", "--version").Output()
	if err != nil || !bytes.Contains(version, []byte("GNU tar")) {
		t.Skip("requires GNU tar and Python, exercised by the native Linux fixture")
	}
	wrapper := filepath.Join(t.TempDir(), "tar")
	if err := os.WriteFile(wrapper, []byte(ArchiveWrapper()), 0o555); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "payload"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		outer bool
	}{
		{"outer and inner adapter selection", true},
		{"inner only bypasses adapter", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := archiveThroughPython(t, ctx, wrapper, root, tc.outer)
			header, err := tar.NewReader(bytes.NewReader(raw)).Next()
			if err != nil {
				t.Fatal(err)
			}
			numeric := header.Uname == "" && header.Gname == ""
			if numeric != tc.outer {
				t.Fatalf("wrong adapter selected: numeric ownership=%v, outer adapter=%v", numeric, tc.outer)
			}
		})
	}
}

func archiveThroughPython(t *testing.T, ctx context.Context, wrapper, root string, outer bool) []byte {
	t.Helper()
	operations, err := specFixture("amd64").Invocations("linux/amd64", []string{"packer"})
	if err != nil {
		t.Fatal(err)
	}
	operation := operations[len(operations)-1]
	inner := ""
	for _, argument := range operation.Command {
		if strings.HasPrefix(argument, "--extra-search-path=") {
			inner = strings.TrimPrefix(argument, "--extra-search-path=")
		}
	}
	if inner != ArchiveToolsDirectory {
		t.Fatal("packaging invocation lost the fixed inner adapter path")
	}
	// The only fixture substitutions relocate fixed namespace paths into this
	// temporary directory. No shell, inherited PATH or environment tool lookup.
	environment := operation.Environment()
	for i, value := range environment {
		value = strings.ReplaceAll(value, ArchiveToolsDirectory, filepath.Dir(wrapper))
		if !outer && strings.HasPrefix(value, "PATH=") {
			value = "PATH=/usr/bin:/usr/sbin"
		}
		environment[i] = value
	}
	inner = strings.ReplaceAll(inner, ArchiveToolsDirectory, filepath.Dir(wrapper)) + ":/usr/bin:/usr/sbin"
	const launch = `import os,subprocess,sys
inner = sys.argv[1]
def sandbox():
    os.environ["PATH"] = inner
subprocess.run(["tar", *sys.argv[2:]], preexec_fn=sandbox, check=True)
`
	python := "/usr/bin/python3"
	if _, err := os.Stat(python); os.IsNotExist(err) {
		// The small offline Python container installs its interpreter here.
		python = "/usr/local/bin/python3"
	}
	command := exec.CommandContext(ctx, python, "-I", "-S", "-c", launch, inner,
		"--create", "--file=-", "--directory="+root,
		"--acls", "--selinux", "--xattrs", "--pax-option=delete=atime,delete=ctime,delete=mtime",
		"--mtime=@0", "--owner=0", "--group=0", ".")
	command.Env = environment
	var stderr bytes.Buffer
	command.Stderr = &stderr
	raw, err := command.Output()
	if err != nil {
		t.Fatalf("archive through Python: %v: %s", err, stderr.String())
	}
	return raw
}

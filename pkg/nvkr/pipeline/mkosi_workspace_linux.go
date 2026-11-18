// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/runtimeexec"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
	"golang.org/x/sys/unix"
)

// mkosiWorkspace owns one attempt's namespace launcher and files. Callers prepare
// inputs before execute, inspect outputs only after it returns, and close on every
// path. The bootstrap uses installed host utilities without a hermetic claim;
// source, package selections and output bytes are authenticated separately.
type mkosiWorkspace struct {
	launcher                                    *runtimeexec.Launcher
	parent, source, inputs, work                string
	root, sourceFD, inputsFD, workFD, bootstrap *os.File
}

func newMkosiWorkspace(ctx context.Context, scratch string) (_ *mkosiWorkspace, returnErr error) {
	if err := requireRunContext(ctx, "mkosi assembly admission"); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(scratch) || filepath.Clean(scratch) != scratch {
		return nil, e.New(e.CodeEngineCapability, "mkosi assembly requires an explicit absolute executable scratch directory")
	}
	launcher, err := runtimeexec.AdmitAssembly(ctx)
	if err != nil {
		return nil, err
	}
	workspace := &mkosiWorkspace{launcher: launcher}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, workspace.close())
		}
	}()
	scratchFD, err := openMkosiScratch(scratch)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "opening assembly scratch directory")
	}
	defer func() { _ = scratchFD.Close() }()
	var stat unix.Stat_t
	var filesystem unix.Statfs_t
	if err := unix.Fstat(int(scratchFD.Fd()), &stat); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "inspecting assembly scratch owner")
	}
	if stat.Uid != 0 || stat.Mode&0o022 != 0 || stat.Mode&0o001 == 0 {
		return nil, e.New(e.CodeEngineCapability, "assembly scratch must be root-owned, searchable and not writable by other users")
	}
	if err := unix.Fstatfs(int(scratchFD.Fd()), &filesystem); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "inspecting assembly scratch mount")
	}
	if filesystem.Flags&(unix.ST_NOEXEC|unix.ST_RDONLY) != 0 {
		return nil, e.New(e.CodeEngineCapability, "assembly scratch must already permit execution and writes; inherited flags=%#x", filesystem.Flags)
	}
	// Resolve through the held scratch descriptor when reserving the attempt.
	// No inherited mount flags are cleared and no shared mount is changed.
	private, err := os.MkdirTemp(fmt.Sprintf("/proc/self/fd/%d", scratchFD.Fd()), "nvkr-mkosi-")
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "reserving private assembly workspace")
	}
	workspace.parent = filepath.Join(scratch, filepath.Base(private))
	for _, name := range []string{"root", "source", "inputs", "work"} {
		if err := os.Mkdir(filepath.Join(private, name), 0o755); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "creating assembly workspace directories")
		}
	}
	workspace.source, workspace.inputs, workspace.work = filepath.Join(workspace.parent, "source"), filepath.Join(workspace.parent, "inputs"), filepath.Join(workspace.parent, "work")
	rootPath := filepath.Join(workspace.parent, "root")
	if err := prepareMkosiBootstrap(ctx, rootPath); err != nil {
		return nil, err
	}
	// Only the namespace root and writable state belong to mapped IDs. Source,
	// recipe, packages and reports stay host-root-owned and world-readable, so
	// namespace root cannot modify them even if it remounts its own bind.
	for _, directory := range []string{workspace.parent, rootPath, workspace.work} {
		if err := os.Chown(directory, runtimeexec.AssemblyHostID, runtimeexec.AssemblyHostID); err != nil {
			return nil, e.Wrap(e.CodeBuildFailed, err, "assigning private assembly state to mapped IDs")
		}
	}
	for _, entry := range []struct {
		path string
		slot **os.File
	}{{rootPath, &workspace.root}, {workspace.source, &workspace.sourceFD}, {workspace.inputs, &workspace.inputsFD}, {workspace.work, &workspace.workFD}, {"/usr", &workspace.bootstrap}} {
		file, _, err := securepath.OpenDirectory(entry.path)
		if err != nil {
			return nil, e.Wrap(e.CodeEngineCapability, err, "holding assembly directory")
		}
		*entry.slot = file
	}
	accepted = true
	return workspace, nil
}

// Every ancestor must be owned by host root and searchable by the mapped child.
// Sticky root-owned shared ancestors such as /tmp are permitted; the selected
// scratch directory itself must not be writable by any other principal.
func openMkosiScratch(location string) (*os.File, error) {
	current, _, err := securepath.OpenDirectory("/")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(location, "/"), "/") {
		next, _, openErr := securepath.OpenChild(current, part, true)
		_ = current.Close()
		if openErr != nil {
			return nil, openErr
		}
		current = next
		var stat unix.Stat_t
		if err := unix.Fstat(int(current.Fd()), &stat); err != nil {
			_ = current.Close()
			return nil, err
		}
		if stat.Uid != 0 || stat.Mode&0o001 == 0 || stat.Mode&0o022 != 0 && stat.Mode&unix.S_ISVTX == 0 {
			_ = current.Close()
			return nil, e.New(e.CodeEngineCapability, "assembly scratch ancestors must be root-owned and searchable without permitting replacement")
		}
	}
	return current, nil
}

func prepareMkosiBootstrap(ctx context.Context, root string) error {
	for _, directory := range []string{"etc", "etc/alternatives", "var"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "preparing private assembly bootstrap")
		}
	}
	for _, link := range []struct{ name, target string }{
		{"bin", "usr/bin"}, {"sbin", "usr/sbin"}, {"lib", "usr/lib"}, {"lib64", "usr/lib64"},
		{"etc/os-release", "../usr/lib/os-release"}, {"etc/mtab", "../proc/self/mounts"},
		{"etc/localtime", "../usr/share/zoneinfo/UTC"}, {"etc/alternatives/awk", "/usr/bin/mawk"}, {"var/tmp", "../tmp"},
	} {
		if err := os.Symlink(link.target, filepath.Join(root, link.name)); err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "preparing assembly bootstrap link")
		}
	}
	for _, file := range []struct{ name, contents string }{
		{"etc/passwd", "root:x:0:0:root:/work/home:/bin/sh\n_apt:x:65534:65534:apt:/nonexistent:/usr/sbin/nologin\n"},
		{"etc/group", "root:x:0:\nnogroup:x:65534:\n"},
		{"etc/nsswitch.conf", "passwd: files\ngroup: files\nhosts: files\n"},
		{"etc/hosts", "127.0.0.1 localhost nvkr-runtime\n::1 localhost\n"},
	} {
		if err := writeMkosiFile(ctx, root, file.name, []byte(file.contents)); err != nil {
			return err
		}
	}
	return nil
}

func (workspace *mkosiWorkspace) execute(ctx context.Context, operation mkosi.Invocation, stdout, stderr io.Writer) error {
	return workspace.launcher.Execute(ctx, runtimeexec.Request{Root: workspace.root,
		Projections: []runtimeexec.Projection{
			{Directory: workspace.inputsFD, Target: "/inputs"},
			{Directory: workspace.sourceFD, Target: "/src"},
			{Directory: workspace.bootstrap, Target: "/usr"},
			{Directory: workspace.workFD, Target: "/work", Writable: true},
		},
		Command: operation.Command, Workdir: operation.Project,
		Environment: []string{"HOME=/work/home", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH=/usr/bin:/usr/sbin", "SOURCE_DATE_EPOCH=0", "TMPDIR=/work/tmp", "TZ=UTC"},
		Stdout:      stdout, Stderr: stderr,
	})
}

func (workspace *mkosiWorkspace) close() error {
	if workspace == nil {
		return nil
	}
	var failures []error
	if workspace.launcher != nil {
		failures = append(failures, workspace.launcher.Close())
		workspace.launcher = nil
	}
	for _, file := range []*os.File{workspace.root, workspace.sourceFD, workspace.inputsFD, workspace.workFD, workspace.bootstrap} {
		if file != nil {
			failures = append(failures, file.Close())
		}
	}
	workspace.root, workspace.sourceFD, workspace.inputsFD, workspace.workFD, workspace.bootstrap = nil, nil, nil, nil, nil
	if workspace.parent != "" {
		failures = append(failures, removePrivateTree(workspace.parent))
		workspace.parent = ""
	}
	if err := errors.Join(failures...); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "closing private mkosi assembly workspace")
	}
	return nil
}

func (workspace *mkosiWorkspace) ownWritableState(ctx context.Context) error {
	return filepath.WalkDir(workspace.work, func(location string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || !strings.HasPrefix(location, workspace.work+"/") && location != workspace.work {
			return e.New(e.CodeVerifyFailed, "assembly writable state contains an unexpected entry")
		}
		return os.Chown(location, runtimeexec.AssemblyHostID, runtimeexec.AssemblyHostID)
	})
}

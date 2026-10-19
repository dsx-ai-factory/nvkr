// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package pipeline

import (
	"context"
	"debug/macho"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// Native snapshots support the local unqualified lane. Digest/mode checks
// detect replacement between operations; ordinary pathname execution is not
// Linux sealed-descriptor execution or protection against a hostile same UID.
type darwinPackerExecution struct{}
type darwinPackerExecutable struct {
	*heldfile.File
	root       string
	systemPath string
	files      []privateFileSnapshot
}

func platformPackerExecutionClass() (packerExecutionClass, error) {
	if runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64" {
		return nil, e.New(e.CodeEngineCapability, "native Mac Packer requires ARM64 or AMD64")
	}
	return darwinPackerExecution{}, nil
}
func (darwinPackerExecution) open(ctx context.Context, path string, options heldfile.Options) (packerExecutable, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "native Packer capture requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "capturing native Packer executable")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 {
		return nil, e.New(e.CodeEngineCapability, "native Packer input must be a regular executable")
	}
	options.Executable = false
	held, err := heldfile.OpenContext(ctx, path, options)
	if err != nil {
		return nil, err
	}
	if protectedPackerSystemPath(path) {
		if err := admitPackerSystemTool(path); err != nil {
			_ = held.Close()
			return nil, err
		}
		if err := (darwinPackerExecution{}).format(held, options.Label); err != nil {
			_ = held.Close()
			return nil, err
		}
		value := &darwinPackerExecutable{File: held, systemPath: path}
		if err := value.AssertUnchangedContext(ctx); err != nil {
			return nil, errors.Join(err, value.Close())
		}
		return value, nil
	}
	root, err := os.MkdirTemp("", "nvkr-packer-executable-")
	if err != nil {
		_ = held.Close()
		return nil, err
	}
	value := &darwinPackerExecutable{File: held, root: root,
		files: []privateFileSnapshot{{Path: "bin/executable", SHA256: held.Digest(), Mode: 0o500}}}
	fail := func(cause error) (packerExecutable, error) {
		return nil, errors.Join(cause, value.Close())
	}
	if err := (darwinPackerExecution{}).format(held, options.Label); err != nil {
		return fail(err)
	}
	// Reuse the existing held-input staging owner, including no-replace publish.
	if err := materializeHeldInput(ctx, root, "bin/executable", held); err != nil {
		return fail(err)
	}
	if err := os.Chmod(filepath.Join(root, "bin", "executable"), 0o500); err != nil {
		return fail(err)
	}
	if err := sealPrivateInputDirectories(root); err != nil {
		return fail(err)
	}
	if err := value.AssertUnchangedContext(ctx); err != nil {
		return fail(err)
	}
	return value, nil
}
func (value *darwinPackerExecutable) AssertUnchangedContext(ctx context.Context) error {
	if value == nil || value.File == nil || (value.root == "" && value.systemPath == "") {
		return closedPackerExecutable()
	}
	if err := value.File.AssertUnchangedContext(ctx); err != nil {
		return err
	}
	if value.systemPath != "" {
		if err := admitPackerSystemTool(value.systemPath); err != nil {
			return err
		}
		observed, err := heldfile.OpenContext(ctx, value.systemPath, heldfile.Options{
			Label: "system-" + filepath.Base(value.systemPath), ExpectedDigest: value.Digest(), MaxBytes: maxPackerHostToolBytes,
		})
		if err != nil {
			return e.Wrap(e.CodeUnverifiedRef, err, "verifying protected system tool %q", value.systemPath)
		}
		return observed.Close()
	}
	return verifyPrivateFileSnapshot(ctx, value.root, value.files, "native Packer executable")
}
func (value *darwinPackerExecutable) Close() error {
	if value == nil {
		return nil
	}
	var heldErr error
	if value.File != nil {
		heldErr = value.File.Close()
		value.File = nil
	}
	cleanupErr := removePrivateTree(value.root)
	value.root = ""
	value.systemPath = ""
	return errors.Join(heldErr, cleanupErr)
}
func (darwinPackerExecution) format(reader io.ReaderAt, label string) error {
	want := macho.CpuArm64
	if runtime.GOARCH == "amd64" {
		want = macho.CpuAmd64
	}
	image, err := macho.NewFile(reader)
	if err == nil {
		defer func() { _ = image.Close() }()
		if image.Type == macho.TypeExec && image.Cpu == want {
			return nil
		}
		return e.New(e.CodeEngineCapability, "%s does not match the native Mac executable architecture", label)
	}
	// Apple system tools may be universal binaries. The standard Mach-O
	// implementation owns their fat-header and member validation.
	universal, fatErr := macho.NewFatFile(reader)
	if fatErr != nil {
		return e.Wrap(e.CodeEngineCapability, err, "%s is not a native Mach-O executable", label)
	}
	defer func() { _ = universal.Close() }()
	for _, architecture := range universal.Arches {
		if architecture.Cpu == want && architecture.Type == macho.TypeExec {
			return nil
		}
	}
	return e.New(e.CodeEngineCapability, "%s has no native Mac executable architecture", label)
}
func (darwinPackerExecution) hostToolPath(_ int, executable packerExecutable) (string, error) {
	native, ok := executable.(*darwinPackerExecutable)
	if !ok || native == nil || native.File == nil || (native.root == "" && native.systemPath == "") {
		return "", closedPackerExecutable()
	}
	if native.systemPath != "" {
		return native.systemPath, nil
	}
	return filepath.Join(native.root, "bin", "executable"), nil
}

// QEMU locates ROMs and VNC keymaps relative to its real executable prefix.
// Reuse the authenticated native data closure, retaining private copies in
// the corresponding share/qemu layout through executable close.
func (darwinPackerExecution) bindHostData(ctx context.Context, tools []packerHostTool, source string, files []privateFileSnapshot) error {
	const prefix = "nvkr/toolchain/qemu-data/"
	for _, tool := range tools {
		if !strings.HasPrefix(tool.name, "qemu-system-") {
			continue
		}
		native, ok := tool.file.(*darwinPackerExecutable)
		if !ok || native.root == "" {
			return closedPackerExecutable()
		}
		for _, file := range files {
			if !strings.HasPrefix(file.Path, prefix) {
				continue
			}
			if file.Mode&0o111 != 0 {
				return e.New(e.CodeInvalidCatalog, "QEMU runtime data must not be executable")
			}
			captured, err := heldfile.OpenContext(ctx, filepath.Join(source, file.Path), heldfile.Options{
				Label: "native-qemu-data", ExpectedDigest: file.SHA256, MaxBytes: maxPackerPluginBytes,
			})
			if err != nil {
				return e.Wrap(e.CodeUnverifiedRef, err, "opening captured QEMU runtime data")
			}
			relative := filepath.Join("share", "qemu", strings.TrimPrefix(file.Path, prefix))
			stageErr := os.Chmod(native.root, 0o700)
			if stageErr == nil {
				stageErr = materializeHeldInput(ctx, native.root, relative, captured)
			}
			if stageErr == nil {
				stageErr = os.Chmod(filepath.Join(native.root, relative), 0o400)
			}
			if err := errors.Join(stageErr, captured.Close()); err != nil {
				return e.Wrap(e.CodeUnverifiedRef, err, "staging captured QEMU runtime data")
			}
			native.files = append(native.files, privateFileSnapshot{Path: relative, SHA256: file.SHA256, Mode: 0o400})
		}
		if err := sealPrivateInputDirectories(native.root); err != nil {
			return e.Wrap(e.CodeUnverifiedRef, err, "sealing private QEMU runtime data")
		}
		if err := native.AssertUnchangedContext(ctx); err != nil {
			return err
		}
	}
	return nil
}
func (darwinPackerExecution) projectDirectory(path string, directory *os.File) (string, error) {
	before, err := directory.Stat()
	if err != nil {
		return "", err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !current.IsDir() || !os.SameFile(before, current) {
		return "", e.New(e.CodeUnverifiedRef, "native Packer project directory changed")
	}
	return path, nil
}
func (darwinPackerExecution) accelerator(architecture, value string) error {
	if value != "hvf" && value != "tcg" {
		return e.New(e.CodeEngineCapability, "native Mac Packer requires HVF or TCG")
	}
	if value == "hvf" && architecture != runtime.GOARCH {
		return e.New(e.CodeEngineCapability, "HVF requires matching host and guest architectures")
	}
	return nil
}
func (class darwinPackerExecution) run(ctx context.Context, executable packerExecutable, tools []packerHostTool, args []string, options heldfile.RunOptions) (returnedErr error) {
	path, err := class.hostToolPath(0, executable)
	if err != nil {
		return err
	}
	values := []packerExecutable{executable}
	for _, tool := range tools {
		values = append(values, tool.file)
	}
	verify := func() error {
		for _, value := range values {
			if _, err := class.hostToolPath(0, value); err != nil {
				return err
			}
			if err := value.AssertUnchangedContext(ctx); err != nil {
				return err
			}
		}
		return nil
	}
	if err := verify(); err != nil {
		return err
	}
	// Packer plugins bind Unix sockets beneath TMPDIR. A Run workspace can
	// exceed Darwin's socket pathname limit; use one private short directory
	// through child/group join instead of inheriting that workspace path.
	temporary, err := os.MkdirTemp("/private/tmp", "nvkr-pkr-")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating private native Packer temporary directory")
	}
	defer func() {
		returnedErr = errors.Join(returnedErr, removePrivateTree(temporary))
	}()
	environment := make([]string, 0, len(options.Env)+1)
	for _, value := range options.Env {
		if !strings.HasPrefix(value, "TMPDIR=") {
			environment = append(environment, value)
		}
	}
	environment = append(environment, "TMPDIR="+temporary)
	command := exec.CommandContext(ctx, path, args...)
	// Packer opens /dev/tty even with piped output when its restricted PATH
	// prevents background detection. A background terminal ioctl then stops
	// the group with SIGTTOU. Keep this noninteractive tool off the caller
	// terminal; setsid still gives the owner a private cancellation group.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Dir, command.Env = options.Dir, environment
	command.Stdin, command.Stdout, command.Stderr = options.Stdin, options.Stdout, options.Stderr
	runErr := ownedcmd.Run(ctx, command)
	return errors.Join(runErr, verify())
}

// Apple's platform-signed utilities cannot be relocated into the private
// executable tree. Admit only direct children of the protected system binary
// directories; the observed bytes must equal the selected toolchain bundle.
// This is local system-root trust, never a change to SIP or signature policy.
func protectedPackerSystemPath(path string) bool {
	return filepath.Clean(path) == path && (filepath.Dir(path) == "/usr/bin" || filepath.Dir(path) == "/bin")
}

func admitPackerSystemTool(executable string) error {
	if !protectedPackerSystemPath(executable) {
		return e.New(e.CodeEngineCapability, "system tool is outside the protected executable directories")
	}
	paths := []string{"/"}
	if filepath.Dir(executable) == "/usr/bin" {
		paths = append(paths, "/usr")
	}
	paths = append(paths, filepath.Dir(executable), executable)
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "observing protected system dependency %q", path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 || info.Mode()&os.ModeSymlink != 0 {
			return e.New(e.CodeEngineCapability, "system dependency must be root-owned and not group/world writable")
		}
		if path == executable {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return e.New(e.CodeEngineCapability, "system dependency is not executable")
			}
		} else if !info.IsDir() {
			return e.New(e.CodeEngineCapability, "system dependency parent is not a directory")
		}
	}
	return nil
}

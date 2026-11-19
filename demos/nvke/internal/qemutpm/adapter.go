// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package qemutpm owns the local diagnostic QEMU software-TPM adapter.
// QEMU and swtpm implement the device and protocol; this package only owns their
// launch, inherited network descriptor, private state and joined termination.
package qemutpm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	// LauncherName retains aarch64 because NICoSim selects its machine from it.
	LauncherName = "nvkr-qemu-system-aarch64"
	// ConfigEnvironment selects the private invocation description, not a shell.
	ConfigEnvironment = "NVKR_NATIVE_QEMU_CONFIG"
)

// Accelerator is an explicit execution choice, never a fallback sequence.
type Accelerator string

const (
	HVF Accelerator = "hvf"
	TCG Accelerator = "tcg"
)

func (a Accelerator) valid() bool { return a == HVF || a == TCG }

// Options describes trusted installed host dependencies. Dynamic libraries stay
// installed; copying swtpm alone would not capture its executable closure.
type Options struct {
	Executable, Directory, Instances, QEMU, SWTPM string
	Environment                                   []string
	Accelerator                                   Accelerator
}

type tool struct{ Path, SHA256 string }
type configuration struct {
	Directory, Instances string
	QEMU, SWTPM          tool
	Accelerator          Accelerator
}

type termination struct {
	Joined bool
	State  instanceState
}

type fileIdentity struct{ Device, Inode uint64 }
type instanceState struct{ Guard, Guest, TPM fileIdentity }

// Adapter is issued only after installed-tool admission and private preparation.
// It makes no artifact, security qualification or hardware-TPM claim.
type Adapter struct{ configuration }

// Prepare probes the exact installed tools before any bridge or VM is started.
func Prepare(ctx context.Context, opts Options) (*Adapter, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return nil, e.New(e.CodeEngineCapability, "QEMU software-TPM adapter requires native Darwin ARM64")
	}
	return prepare(ctx, opts)
}

func prepare(ctx context.Context, opts Options) (_ *Adapter, result error) {
	defer func() {
		if result != nil {
			result = e.Wrap(e.CodeEngineCapability, result, "prepare native QEMU TPM adapter")
		}
	}()
	if ctx == nil {
		return nil, errors.New("active context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !opts.Accelerator.valid() {
		return nil, errors.New("explicit hvf or tcg QEMU accelerator required")
	}
	if !filepath.IsAbs(opts.Directory) || filepath.Clean(opts.Directory) != opts.Directory || !filepath.IsAbs(opts.Instances) || filepath.Clean(opts.Instances) != opts.Instances || len(opts.Environment) == 0 {
		return nil, errors.New("absolute private adapter/runtime paths and explicit environment required")
	}
	if strings.ContainsAny(opts.Instances, ",\x00\r\n") {
		return nil, errors.New("QEMU TPM state path contains an unsupported option delimiter")
	}
	parent, _, err := securepath.OpenDirectory(filepath.Dir(opts.Directory))
	if err != nil {
		return nil, err
	}
	err = privateDirectory(parent)
	_ = parent.Close()
	if err != nil || filepath.Dir(opts.Directory) != filepath.Dir(filepath.Dir(opts.Instances)) {
		return nil, errors.Join(err, errors.New("adapter and instance state require one private runtime root"))
	}
	qemu, err := inspectTool(ctx, opts.QEMU)
	if err != nil {
		return nil, err
	}
	swtpm, err := inspectTool(ctx, opts.SWTPM)
	if err != nil {
		return nil, err
	}
	for _, probe := range []struct {
		path string
		args []string
		want string
	}{
		{qemu.Path, []string{"-accel", "help"}, string(opts.Accelerator)},
		{qemu.Path, []string{"-tpmdev", "help"}, "emulator"},
		{qemu.Path, []string{"-device", "help"}, `name "tpm-tis-device"`},
		{swtpm.Path, []string{"socket", "--print-capabilities"}, `"tpm-2.0"`},
	} {
		budget, cancel := context.WithTimeout(ctx, 10*time.Second)
		command := exec.CommandContext(budget, probe.path, probe.args...)
		command.Env = opts.Environment
		var output limitedOutput
		command.Stdout, command.Stderr = &output, &output
		err := ownedcmd.Run(budget, command)
		cancel()
		if err != nil || !strings.Contains(output.String(), probe.want) {
			return nil, errors.Join(err, fmt.Errorf("installed tool lacks required capability %q", probe.want))
		}
		if probe.path == qemu.Path && probe.args[0] == "-accel" && !slices.Contains(strings.Fields(output.String()), probe.want) {
			return nil, errors.New("installed QEMU does not offer the selected accelerator")
		}
		if probe.path == swtpm.Path && len(probe.args) == 2 && probe.args[1] == "--print-capabilities" {
			var caps struct {
				Type     string   `json:"type"`
				Features []string `json:"features"`
			}
			if json.Unmarshal([]byte(output.String()), &caps) != nil || caps.Type != "swtpm" || !slices.Contains(caps.Features, "tpm-2.0") || !slices.Contains(caps.Features, "ctrl-opt-terminate") {
				return nil, errors.New("swtpm must report TPM2 and control-disconnect termination")
			}
		}
	}
	self, _, err := securepath.OpenRegular(opts.Executable)
	if err != nil {
		return nil, err
	}
	defer self.Close()
	if err := protectedOwner(self); err != nil {
		return nil, err
	}
	if err := os.Mkdir(opts.Directory, 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(filepath.Join(opts.Directory, "launches"), 0700); err != nil {
		return nil, err
	}
	launcher, err := os.OpenFile(filepath.Join(opts.Directory, LauncherName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return nil, err
	}
	_, copyErr := io.Copy(launcher, self)
	if err := errors.Join(copyErr, launcher.Close()); err != nil {
		return nil, err
	}
	adapter := &Adapter{configuration{opts.Directory, opts.Instances, qemu, swtpm, opts.Accelerator}}
	if err := writeJSON(filepath.Join(opts.Directory, "configuration.json"), adapter.configuration); err != nil {
		return nil, err
	}
	return adapter, nil
}

// Executable is this CLI's private native snapshot with its QEMU dispatch name.
func (a *Adapter) Executable() string { return filepath.Join(a.Directory, LauncherName) }

// Environment binds subsequent NICoSim QEMU invocations to this private owner.
func (a *Adapter) Environment() map[string]string {
	return map[string]string{ConfigEnvironment: filepath.Join(a.Directory, "configuration.json"), "NICO_VMS_QEMU_SYSTEM_PATH": a.Executable()}
}

// Close confirms every launched supervisor wrote its terminal record only after
// waiting for QEMU and swtpm. Interrupted owners retain state and fail cleanup.
func (a *Adapter) Close(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "native QEMU cleanup requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "native QEMU cleanup")
	}
	if err := closeOwners(ctx, a.configuration); err != nil {
		return e.Wrap(e.CodePreflightFailed, errors.Join(ErrUnjoined, err), "native QEMU owner did not confirm joined children")
	}
	return nil
}

func inspectTool(ctx context.Context, path string) (tool, error) {
	if ctx == nil || !filepath.IsAbs(path) {
		return tool{}, errors.New("active context and absolute tool path required")
	}
	if err := ctx.Err(); err != nil {
		return tool{}, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return tool{}, err
	}
	file, info, err := securepath.OpenRegular(resolved)
	if err != nil {
		return tool{}, err
	}
	defer file.Close()
	if err := protectedOwner(file); err != nil {
		return tool{}, err
	}
	if info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return tool{}, errors.New("installed dependency must be executable without set-ID or group/other write permissions")
	}
	sum := sha256.New()
	if info.Size() > 256<<20 {
		return tool{}, errors.New("installed executable exceeds local diagnostic bound")
	}
	if _, err := io.Copy(sum, file); err != nil {
		return tool{}, err
	}
	if err := ctx.Err(); err != nil {
		return tool{}, err
	}
	return tool{resolved, hex.EncodeToString(sum.Sum(nil))}, nil
}

func writeJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return errors.Join(json.NewEncoder(file).Encode(value), file.Close())
}

func load(path string) (configuration, error) {
	file, info, err := securepath.OpenRegular(path)
	if err != nil {
		return configuration{}, err
	}
	defer file.Close()
	if err := protectedOwner(file); err != nil {
		return configuration{}, err
	}
	if info.Mode().Perm() != 0600 || info.Size() > 16<<10 {
		return configuration{}, errors.New("require private bounded QEMU configuration")
	}
	var cfg configuration
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return configuration{}, err
	}
	if decoder.Decode(new(any)) != io.EOF || filepath.Join(cfg.Directory, "configuration.json") != path {
		return configuration{}, errors.New("QEMU configuration location or trailing data changed")
	}
	if !filepath.IsAbs(cfg.Directory) || !filepath.IsAbs(cfg.Instances) || filepath.Dir(cfg.Directory) != filepath.Dir(filepath.Dir(cfg.Instances)) {
		return configuration{}, errors.New("QEMU state paths differ from private runtime layout")
	}
	if !cfg.Accelerator.valid() {
		return configuration{}, errors.New("private QEMU accelerator selection is invalid")
	}
	return cfg, nil
}

// Tool self-reports are bounded diagnostics, never an unbounded output sink.
type limitedOutput struct{ strings.Builder }

func (w *limitedOutput) Write(data []byte) (int, error) {
	if len(data) > (1<<20)-w.Len() {
		return 0, errors.New("installed-tool report exceeds one MiB")
	}
	return w.Builder.Write(data)
}

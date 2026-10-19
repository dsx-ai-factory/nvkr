// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

type nativeSnapshot struct {
	path   string
	source *heldfile.File
	mode   os.FileMode
	info   os.FileInfo
}

type nativeExecution struct {
	mu        sync.Mutex
	root      string
	snapshots map[*heldfile.File]nativeSnapshot
}

func (owner *nativeExecution) close() error {
	if owner.root != "" {
		if err := os.RemoveAll(owner.root); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "removing private native conformance snapshots")
		}
	}
	return nil
}

// Local diagnostics use native Packer's private snapshot/owned process pattern.
// Each selected tool is copied once per admitted operation. Rechecks detect
// replacement between commands, without claiming Linux's sealed-descriptor
// guarantee against a hostile same UID. Published and qualified paths cannot
// select this adapter; their held executable admission remains unchanged.
func (owner *nativeExecution) tool(ctx context.Context, tool *heldfile.File, workspace string) (nativeSnapshot, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if value, ok := owner.snapshots[tool]; ok {
		return value, nil
	}
	if owner.root == "" {
		root, err := os.MkdirTemp(workspace, ".native-tools-")
		if err != nil {
			return nativeSnapshot{}, err
		}
		owner.root, owner.snapshots = root, make(map[*heldfile.File]nativeSnapshot)
	}
	directory, err := os.MkdirTemp(owner.root, "tool-")
	if err != nil {
		return nativeSnapshot{}, err
	}
	value, err := writeNativeSnapshot(ctx, filepath.Join(directory, "executable"), tool, 0500)
	if err == nil {
		owner.snapshots[tool] = value
	}
	return value, err
}

func writeNativeSnapshot(ctx context.Context, path string, source *heldfile.File, mode os.FileMode) (nativeSnapshot, error) {
	value := nativeSnapshot{path: path, source: source, mode: mode}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return value, err
	}
	_, copyErr := source.WriteToContext(ctx, file)
	info, statErr := file.Stat()
	err = errors.Join(copyErr, statErr, file.Sync(), file.Chmod(mode), file.Close())
	value.info = info
	return value, err
}

func verifyNativeSnapshot(ctx context.Context, value nativeSnapshot) error {
	file, info, err := securepath.OpenRegular(value.path)
	if err != nil {
		return err
	}
	defer file.Close()
	if !os.SameFile(info, value.info) || info.Mode().Perm() != value.mode || info.Size() != value.source.Size() {
		return failure("native diagnostic snapshot changed")
	}
	hash := sha256.New()
	writer := &executableWriter{ctx: ctx, destination: hash}
	if _, err := io.Copy(writer, io.NewSectionReader(file, 0, value.source.Size())); err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil {
		return err
	}
	if !securepath.SameMetadata(info, after) || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != value.source.Digest() {
		return failure("native diagnostic snapshot bytes changed")
	}
	return nil
}

func (a *admitted) runLocalNativeCommand(ctx context.Context, tool *heldfile.File, args []string, options heldfile.RunOptions, kube *heldfile.File) (result error) {
	if len(args) == 0 || !strings.HasPrefix(args[0], "--kubeconfig=") {
		return failure("native diagnostic command requires its private kubeconfig")
	}
	executable, err := a.native.tool(ctx, tool, options.Dir)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp(options.Dir, ".native-command-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
			result = errors.Join(result, cleanupErr)
		}
	}()
	config, err := writeNativeSnapshot(ctx, filepath.Join(root, "kubeconfig"), kube, 0400)
	if err != nil {
		return err
	}
	verify := func() error {
		return errors.Join(verifyNativeSnapshot(ctx, executable), verifyNativeSnapshot(ctx, config))
	}
	if err := verify(); err != nil {
		return err
	}
	argv := append([]string(nil), args...)
	argv[0] = "--kubeconfig=" + config.path
	command := exec.CommandContext(ctx, executable.path, argv...)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Dir, command.Stdin, command.Stdout, command.Stderr = options.Dir, options.Stdin, options.Stdout, options.Stderr
	for _, value := range options.Env {
		if strings.HasPrefix(value, "KUBECONFIG=") {
			value = "KUBECONFIG=" + config.path
		}
		command.Env = append(command.Env, value)
	}
	runErr := ownedcmd.Run(ctx, command)
	if verifyErr := verify(); verifyErr != nil {
		return errors.Join(runErr, verifyErr)
	}
	return runErr
}

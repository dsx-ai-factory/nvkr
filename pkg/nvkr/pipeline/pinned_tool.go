// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const (
	pinnedToolContainerDir = "/nvkr/tool"
	maxPinnedToolBytes     = 512 << 20
	// pinnedToolBootstrap is executor-owned and fixed: it sets umask 022 and replaces
	// the image environment with exactly the arguments that follow (ADR-27 §3). No
	// producer-authored value is interpreted by the shell.
	pinnedToolBootstrap = `umask 022 && exec /usr/bin/env -i "$@"`
)

// admitPinnedTool runs before acquisition, staging, subprocess or output work
// (ADR-27 §8). Unsupported lanes return a coded error and no partial output.
func admitPinnedTool(n ir.Node, opts RunOptions) error {
	if n.Kind != ir.KindBuild || n.BuilderType != ir.BuilderPinnedTool {
		return nil
	}
	if opts.Qualification != nil || opts.execution != nil {
		return e.New(e.CodeEngineCapability, "pinned-tool build %q has no qualified execution lane", n.ID)
	}
	if opts.Package {
		return e.New(e.CodeEngineCapability, "pinned-tool build %q has no packaging lane", n.ID)
	}
	if n.OutputType != ir.OutputFile {
		return e.New(e.CodeEngineCapability,
			"pinned-tool build %q output form %q requires the bundle-oci layout import, which is not implemented", n.ID, n.OutputType)
	}
	if n.Network != ir.NetworkNone {
		return e.New(e.CodeEngineCapability, "pinned-tool build %q must run with network none", n.ID)
	}
	if opts.DryRun {
		return nil
	}
	if err := AdmitPinnedToolHost(n); err != nil {
		return err
	}
	if opts.ToolExecutable == "" {
		return e.New(e.CodeEngineCapability, "pinned-tool build %q has no tool/%s input", n.ID, n.Tool)
	}
	return nil
}

// AdmitPinnedToolHost requires a Linux execution host whose platform equals the
// build's target platform (ADR-27 §3, §8): held executables are Linux-only, and a
// pinned tool never runs under emulation.
func AdmitPinnedToolHost(n ir.Node) error {
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "pinned-tool build %q requires Linux held executables", n.ID)
	}
	if host := runtime.GOOS + "/" + runtime.GOARCH; host != n.Platform {
		return e.New(e.CodeEngineCapability, "pinned-tool build %q targets %s but this host is %s; emulation is not admitted", n.ID, n.Platform, host)
	}
	return nil
}

// buildPinnedTool runs one pinned release executable in its digest-pinned image with
// no network, a read-only root, fixed environment and read-only inputs, then admits
// exactly one regular file. A failed or nonconforming run leaves no output.
func buildPinnedTool(n ir.Node, opts RunOptions) (result *ArtifactResult, returnErr error) {
	if err := admitPinnedTool(n, opts); err != nil {
		return nil, err
	}
	if err := admitBuildMaterials(n, opts); err != nil {
		return nil, err
	}
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[build:%s] run pinned tool %s (%s) with %d material(s) in %s with --network=none\n",
			n.Component, n.Tool, n.Digest, len(n.Materials), n.Toolchain)
		return nil, nil
	}
	// The caller's context bounds the run; exec-node rejects per-node requirements.
	ctx := opts.ctx()

	outRoot, err := filepath.Abs(privOutDir(opts, n))
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolving output dir for %s", n.Component)
	}
	if err := os.RemoveAll(outRoot); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resetting output dir for %s", n.Component)
	}
	defer func() {
		if returnErr != nil {
			result = nil
			if err := os.RemoveAll(outRoot); err != nil {
				returnErr = errors.Join(returnErr, e.Wrap(e.CodeInternal, err, "removing failed pinned-tool output"))
			}
		}
	}()
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating output dir for %s", n.Component)
	}
	toolDir, toolName, err := stagePinnedToolExecutable(ctx, n, opts.ToolExecutable, filepath.Dir(outRoot))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := removePrivateTree(toolDir); err != nil {
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeInternal, err, "removing staged pinned tool"))
		}
	}()
	inputs := ""
	var manifestDigest string
	if len(n.Materials) != 0 {
		staged, err := opts.BuildMaterials.stage(ctx, n, filepath.Dir(outRoot))
		if err != nil {
			return nil, err
		}
		defer func() {
			if err := staged.cleanup(); err != nil {
				returnErr = errors.Join(returnErr, e.Wrap(e.CodeInternal, err, "removing private build-material root"))
			}
		}()
		inputs, manifestDigest = staged.root, staged.manifestDigest
	}
	// Re-hash the staged executable immediately before launch; the acquisition's own
	// check never substitutes for this comparison (ADR-27 §2).
	if sum, _, _, err := hashRegularBeneathContext(ctx, toolDir, toolName); err != nil || sum != n.Digest {
		return nil, e.New(e.CodeVerifyFailed, "staged pinned tool for %q differs from %s", n.ID, n.Digest)
	}
	name := containerName(n)
	fmt.Fprintf(opts.Stdout, "\n=== build %s (pinned tool %s, %s, --network=none) ===\n", n.Component, n.Tool, n.Toolchain)
	if err := registerContainer(opts, name); err != nil {
		return nil, err
	}
	if err := runContainerRuntime(opts, pinnedToolRunArgs(n, name, toolDir, toolName, inputs, outRoot)...); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, e.Wrap(e.CodeTimeout, ctxErr, "pinned tool for %s did not finish", n.Component)
		}
		return nil, e.Wrap(e.CodeBuildFailed, err, "running pinned tool for %s", n.Component)
	}
	opts.reap.forget(name)
	sum, size, err := admitPinnedToolOutput(ctx, outRoot, n)
	if err != nil {
		return nil, err
	}
	return &ArtifactResult{
		Component:     n.Component,
		OutputType:    n.OutputType,
		Qualification: ArtifactUnqualified,
		Toolchain:     n.Toolchain,
		// The executed tool is this producer's input; the observed result binds to it.
		InputDigest:            n.Digest,
		MaterialManifestDigest: manifestDigest,
		BuilderType:            n.BuilderType,
		EngineClass:            n.BuilderType,
		Path:                   n.ArtifactPath,
		SHA256:                 sum,
		Size:                   size,
		AbsPath:                filepath.Join(outRoot, n.ArtifactPath),
	}, nil
}

// stagePinnedToolExecutable snapshots the planned acquisition against the IR's
// executable digest and writes that snapshot into a private read-only directory.
func stagePinnedToolExecutable(ctx context.Context, n ir.Node, source, parent string) (string, string, error) {
	held, err := heldfile.OpenContext(ctx, source, heldfile.Options{
		Label: "pinned tool " + n.Tool, Executable: true, MaxBytes: maxPinnedToolBytes, ExpectedDigest: n.Digest})
	if err != nil {
		if ctx.Err() != nil {
			return "", "", e.Wrap(e.CodeTimeout, ctx.Err(), "reading pinned tool for %q", n.ID)
		}
		return "", "", e.Wrap(e.CodeVerifyFailed, err, "pinned tool for %q differs from %s", n.ID, n.Digest)
	}
	defer func() { _ = held.Close() }() // the staged copy is re-hashed before launch
	dir, err := os.MkdirTemp(parent, ".nvkr-tool-")
	if err != nil {
		return "", "", e.Wrap(e.CodeBuildFailed, err, "creating private tool directory")
	}
	name := path.Base(n.ExecutableArchive.Member)
	// Create the copy with its final mode: some bind-mount filesystems refuse a
	// later chmod of a read-only file. O_EXCL never follows an existing entry.
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o555)
	if err == nil {
		written, writeErr := held.WriteToContext(ctx, file)
		err = errors.Join(writeErr, file.Sync(), file.Close())
		if err == nil && written != held.Size() {
			err = fmt.Errorf("wrote %d of %d bytes", written, held.Size())
		}
	}
	if err == nil {
		err = os.Chmod(dir, 0o555)
	}
	if err != nil {
		_ = removePrivateTree(dir)
		return "", "", e.Wrap(e.CodeBuildFailed, err, "staging pinned tool for %q", n.ID)
	}
	return dir, name, nil
}

// pinnedToolRunArgs is the complete, fixed container invocation. Only the exact
// executable and declared materials are mounted, both read-only; HOME, TMPDIR and
// the working directory are private and empty.
func pinnedToolRunArgs(n ir.Node, name, toolDir, toolName, inputs, output string) []string {
	args := []string{"run", "--rm", "--name", name, "--platform", n.Platform, "--network=none", "--read-only",
		"--tmpfs", ir.PinnedToolExecutorEnv["HOME"], "--tmpfs", ir.PinnedToolExecutorEnv["TMPDIR"], "--tmpfs", ir.PinnedToolWorkdir,
		"-v", toolDir + ":" + pinnedToolContainerDir + ":ro", "-v", output + ":" + ir.PinnedToolOutputDir}
	if inputs != "" {
		args = append(args, buildMaterialMountArgs(inputs)...)
	}
	args = append(args, "-w", ir.PinnedToolWorkdir, "--entrypoint", "/bin/sh", n.Toolchain, "-c", pinnedToolBootstrap, "nvkr-pinned-tool")
	owned := make([]string, 0, len(ir.PinnedToolExecutorEnv))
	for key, value := range ir.PinnedToolExecutorEnv {
		owned = append(owned, key+"="+value)
	}
	sort.Strings(owned)
	args = append(args, owned...)
	for _, variable := range n.Env {
		args = append(args, variable.Name+"="+variable.Value)
	}
	args = append(args, pinnedToolContainerDir+"/"+toolName)
	return append(args, n.Command...)
}

// admitPinnedToolOutput requires the output directory to hold exactly the declared
// regular file (ADR-27 §5). Directories, links, special files and extra entries fail.
func admitPinnedToolOutput(ctx context.Context, root string, n ir.Node) (string, int64, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", 0, e.Wrap(e.CodeBuildFailed, err, "reading pinned-tool output for %s", n.Component)
	}
	if len(entries) != 1 || entries[0].Name() != n.ArtifactPath || !entries[0].Type().IsRegular() {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return "", 0, e.New(e.CodeBuildFailed,
			"pinned tool for %s left %q; want exactly the regular file %q", n.Component, names, n.ArtifactPath)
	}
	sum, size, _, err := hashRegularBeneathContext(ctx, root, n.ArtifactPath)
	if err != nil {
		return "", 0, e.Wrap(e.CodeBuildFailed, err, "collecting pinned-tool output for %s", n.Component)
	}
	return sum, size, nil
}

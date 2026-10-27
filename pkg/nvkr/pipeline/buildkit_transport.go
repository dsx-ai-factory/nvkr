// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Only package-produced tar bytes enter this offline stage. Neither a host bind
// mount nor a source-owned command is present. Ownership changes are confined to
// fresh volumes; source modes and the authenticated input digest are unchanged.
const buildKitExtractScript = `exec 3</nvkr/output/input.tar
printf '%s  %s\n' "$1" /proc/self/fd/3 | sha256sum -c - >/dev/null
tar -xpf /proc/self/fd/3 --strip-components=1 -C /nvkr/source "$2"
rm /nvkr/output/input.tar
chmod 0700 /nvkr/source /nvkr/output
chown -hR 1000:1000 /nvkr/source /nvkr/output`

// This adapter retains the ordinary unqualified Local subprocess boundary. It
// implements the existing resource owner's IO interface, not VM admission.
type buildKitCommandRuntime struct{ opts RunOptions }

func (r buildKitCommandRuntime) Endpoint() string                   { return "" }
func (r buildKitCommandRuntime) RuntimeName() string                { return filepath.Base(r.opts.Runtime) }
func (r buildKitCommandRuntime) AdmittedIdentity() (string, string) { return "", "" }
func (r buildKitCommandRuntime) Run(ctx context.Context, in io.Reader, out, stderr io.Writer, args ...string) error {
	opts := r.opts
	opts.Ctx = ctx
	cmd := execCmd(opts, opts.Runtime, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
	return runOwnedCommand(ctx, cmd)
}
func (r buildKitCommandRuntime) Cleanup(ctx context.Context, grace time.Duration, out, stderr io.Writer, args ...string) error {
	opts := r.opts
	opts.Ctx = ctx
	cmd := execCmd(opts, opts.Runtime, args...)
	configureCommandCancellation(cmd, grace)
	cmd.Stdout, cmd.Stderr = out, stderr
	return runOwnedCommand(ctx, cmd)
}

type buildKitTransport struct {
	resources *packerContainerResources
	root      string
	source    string
	output    string
	limit     int64
}

func prepareBuildKitTransport(ctx context.Context, n ir.Node, opts RunOptions, source string, runtime packerContainerRuntime) (transport *buildKitTransport, returnErr error) {
	if err := requireRunContext(ctx, "BuildKit transport"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "BuildKit transport canceled")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
	}
	callerCtx := ctx
	started := time.Now()
	step := 0
	defer func() {
		opts.progress.printf("[transport] build %s — finished; steps=%d elapsed=%s failed=%t\n", n.Component,
			step, time.Since(started).Round(time.Millisecond), returnErr != nil)
	}()
	// An explicit caller budget wins; otherwise this transport has one fallback
	// deadline across staging and completion. Never reset it per operation.
	// Failure diagnostics and owned teardown retain independent cancellation bounds.
	operation := func(label string, failureCode e.Code, run func(context.Context) error) error {
		operationCtx := callerCtx
		step++
		remaining := "caller cancellation"
		if deadline, ok := operationCtx.Deadline(); ok {
			remaining = max(time.Duration(0), time.Until(deadline)).Round(time.Millisecond).String()
		}
		opts.progress.printf("[transport] build %s — %s; step=%d elapsed=%s caller_remaining=%s\n", n.Component, label,
			step, time.Since(started).Round(time.Millisecond), remaining)
		err := operationCtx.Err()
		if err == nil {
			err = run(operationCtx)
		}
		if operationCtx.Err() != nil {
			err = runtimeCommandError(operationCtx, err, "", failureCode, "%s", label)
		} else if err != nil {
			// Preserve an owning module's coded failure. Classify and name the
			// initiating operation before later diagnostics or cleanup can fail.
			var coded *e.Error
			if errors.As(err, &coded) {
				failureCode = coded.Code
			}
			err = e.Wrap(failureCode, err, "%s", label)
		}
		if err != nil {
			var coded *e.Error
			if errors.As(err, &coded) {
				opts.progress.printf("[transport] build %s — failed %s; step=%d code=%s elapsed=%s\n", n.Component, label,
					step, coded.Code, time.Since(started).Round(time.Millisecond))
			}
		}
		return err
	}
	base, err := filepath.Abs(filepath.Dir(source))
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolving BuildKit transport root")
	}
	member := filepath.Base(source)
	if !securepath.WorkspaceRelative(member) {
		return nil, e.New(e.CodeBuildFailed, "BuildKit source has an unsafe transport member")
	}
	root, err := os.MkdirTemp(base, ".nvkr-buildkit-")
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating private BuildKit transport")
	}
	transport = &buildKitTransport{root: root, resources: &packerContainerResources{runtime: runtime}}
	owner := transport
	var prep string
	prepCreated, diagnosticsAttempted := false, false
	defer func() {
		if returnErr != nil {
			// Failure observation is bounded independently of the expired operation,
			// just like owned teardown. Preserve helper output before removing it.
			if prepCreated && !diagnosticsAttempted {
				diagnosticCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				logErr := runtime.Run(diagnosticCtx, nil, opts.Stdout, opts.Stderr, "logs", prep)
				stop()
				if logErr != nil {
					returnErr = errors.Join(returnErr, e.Wrap(e.CodeBuildFailed, logErr, "retaining private BuildKit preparation diagnostics"))
				}
			}
			var coded *e.Error
			if !errors.As(returnErr, &coded) {
				returnErr = runtimeCommandError(ctx, returnErr, "", e.CodeBuildFailed, "preparing BuildKit transport")
			}
			if cleanupErr := owner.close(); cleanupErr != nil {
				returnErr = errors.Join(returnErr, e.Wrap(e.CodeBuildFailed, cleanupErr, "cleaning failed BuildKit transport"))
			}
			transport = nil
		}
	}()
	if err = operation("admit private BuildKit transport", e.CodeEngineCapability, func(ctx context.Context) error {
		directory, _, err := securepath.OpenDirectory(root)
		if err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "opening private BuildKit transport")
		}
		available, capacityErr := securepath.AvailableDiskBackedBytes(ctx, directory)
		closeErr := directory.Close()
		if err = errors.Join(capacityErr, closeErr); err != nil || available/3 == 0 {
			return e.Wrap(e.CodeEngineCapability, err, "BuildKit export requires disk-backed transport headroom")
		}
		transport.limit = int64(min(available/3, uint64(transportarchive.MaxArchiveBytes)))
		return nil
	}); err != nil {
		return nil, err
	}
	innerPath := filepath.Join(root, "input.tar")
	if err = operation("pack source archive", e.CodeBuildFailed, func(ctx context.Context) error {
		return transportarchive.Pack(ctx, innerPath, base, []string{member})
	}); err != nil {
		return nil, err
	}
	var inner *heldfile.File
	if err = operation("verify source archive", e.CodeBuildFailed, func(ctx context.Context) error {
		inner, err = heldfile.OpenContext(ctx, innerPath, heldfile.Options{Label: "buildkit-input", MaxBytes: transportarchive.MaxArchiveBytes})
		if inner != nil {
			transport.resources.held = append(transport.resources.held, inner)
		}
		return err
	}); err != nil {
		return nil, err
	}
	outerPath := filepath.Join(root, "carrier.tar")
	if err = operation("pack transport carrier", e.CodeBuildFailed, func(ctx context.Context) error {
		return transportarchive.Pack(ctx, outerPath, root, []string{"input.tar"})
	}); err != nil {
		return nil, err
	}
	var outer *heldfile.File
	if err = operation("verify transport carrier", e.CodeBuildFailed, func(ctx context.Context) error {
		outer, err = heldfile.OpenContext(ctx, outerPath, heldfile.Options{Label: "buildkit-carrier", MaxBytes: transportarchive.MaxArchiveBytes})
		if outer != nil {
			transport.resources.held = append(transport.resources.held, outer)
		}
		return err
	}); err != nil {
		return nil, err
	}
	opts.progress.printf("[transport] build %s — authenticated bytes; source_archive_bytes=%d carrier_bytes=%d carrier_members=1\n", n.Component, inner.Size(), outer.Size())
	attempt, err := packerAttemptID()
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "allocating fresh BuildKit volume identity")
	}
	name := "nvkr-buildkit-" + attempt
	transport.source, transport.output = name+"-source", name+"-output"
	for i, volume := range []string{transport.source, transport.output} {
		transport.resources.volumes = append(transport.resources.volumes, volume)
		label := "create private source volume"
		if i == 1 {
			label = "create private output volume"
		}
		if err = operation(label, e.CodeBuildFailed, func(ctx context.Context) error {
			return createPackerVolume(ctx, runtime, volume, []string{"nvkr.dev/role=buildkit-transport"})
		}); err != nil {
			return nil, err
		}
	}
	prep = name + "-prepare"
	transport.resources.containers = append(transport.resources.containers, prep)
	args := []string{"create", "--name", prep, "--platform", n.Platform, "--network=none", "--read-only", "--user=0:0", "--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=DAC_OVERRIDE", "--security-opt=no-new-privileges", "--no-healthcheck", "--entrypoint=sh"}
	for _, mount := range []struct{ volume, destination string }{{transport.source, "/nvkr/source"}, {transport.output, "/nvkr/output"}} {
		args = append(args, buildKitVolumeArgs(runtime.RuntimeName(), mount.volume, mount.destination, false)...)
	}
	args = append(args, n.Toolchain, "-ec", buildKitExtractScript, "nvkr-buildkit-input", strings.TrimPrefix(inner.Digest(), "sha256:"), member)
	if err = operation("create preparation container", e.CodeBuildFailed, func(ctx context.Context) error {
		err := runtime.Run(ctx, nil, io.Discard, opts.Stderr, args...)
		prepCreated = err == nil
		return err
	}); err != nil {
		return nil, err
	}
	if err = operation("copy authenticated carrier", e.CodeBuildFailed, func(ctx context.Context) error {
		return copyArchiveToContainer(ctx, runtime, outer, prep, "/nvkr/output")
	}); err != nil {
		return nil, err
	}
	// Completion retains the caller's budget and existing public error mapping.
	completion := func(label string, failureCode e.Code, run func(context.Context) error) error {
		return operation(label, failureCode, func(ctx context.Context) error {
			if err := run(ctx); err != nil {
				// Completion RPCs retain their existing public error mapping;
				// staging alone preserves archive/transport owner codes.
				return runtimeCommandError(ctx, err, "", failureCode, "%s", label)
			}
			return nil
		})
	}
	// Observe the explicit helper exit before collecting or removing it.
	if err = completion("start preparation container", e.CodeBuildFailed, func(operation context.Context) error {
		return runtime.Run(operation, nil, io.Discard, opts.Stderr, "start", prep)
	}); err != nil {
		return nil, err
	}
	var status bytes.Buffer
	statusOutput := &packerContainerLimitWriter{writer: &status, remaining: 32}
	if err = completion("wait for preparation exit", e.CodeBuildFailed, func(operation context.Context) error {
		return runtime.Run(operation, nil, statusOutput, opts.Stderr, "wait", prep)
	}); err != nil {
		return nil, err
	}
	exit, parseErr := strconv.Atoi(strings.TrimSpace(status.String()))
	if parseErr != nil || exit < 0 || exit > 255 {
		return nil, e.New(e.CodeBuildFailed, "private BuildKit volume preparation returned no valid exit code")
	}
	diagnosticsAttempted = true
	logErr := completion("collect preparation diagnostics", e.CodeBuildFailed, func(operation context.Context) error {
		return runtime.Run(operation, nil, opts.Stdout, opts.Stderr, "logs", prep)
	})
	if exit != 0 {
		return nil, e.Wrap(e.CodeBuildFailed, logErr, "private BuildKit volume preparation exited %d", exit)
	}
	if logErr != nil {
		return nil, logErr
	}
	if err = completion("remove completed preparation container", e.CodeInternal, func(operation context.Context) error {
		return transport.resources.removeContainer(operation, prep)
	}); err != nil {
		return nil, err
	}
	if err = callerCtx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "completing private BuildKit transport")
	}
	return transport, nil
}

func buildKitVolumeArgs(runtime, volume, destination string, readOnly bool) []string {
	if runtime == "podman" {
		options := "nocopy"
		if readOnly {
			options += ",ro"
		}
		return []string{"--volume", volume + ":" + destination + ":" + options}
	}
	options := "type=volume,source=" + volume + ",destination=" + destination + ",volume-nocopy"
	if readOnly {
		options += ",readonly"
	}
	return []string{"--mount", options}
}

func (t *buildKitTransport) collect(ctx context.Context, opts RunOptions, n ir.Node, container string) (returnErr error) {
	if err := requireRunContext(ctx, "BuildKit export transport"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "BuildKit export canceled")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
	}
	defer func() {
		var coded *e.Error
		if returnErr != nil && !errors.As(returnErr, &coded) {
			returnErr = runtimeCommandError(ctx, returnErr, "", e.CodeBuildFailed, "collecting BuildKit export")
		}
	}()
	file, err := os.OpenFile(filepath.Join(t.root, "export.tar"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating bounded BuildKit export transport")
	}
	var stderr bytes.Buffer
	limit := &packerContainerLimitWriter{writer: file, remaining: t.limit}
	runErr := t.resources.runtime.Run(ctx, nil, limit, &stderr, "cp", container+":"+path.Join(n.Workdir, n.ArtifactPath), "-")
	if err := errors.Join(runErr, file.Sync(), file.Close()); err != nil {
		return runtimeCommandError(ctx, err, stderr.String(), e.CodeBuildFailed, "collecting declared BuildKit export")
	}
	archive, err := heldfile.OpenContext(ctx, file.Name(), heldfile.Options{Label: "buildkit-export", MaxBytes: t.limit})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, archive.Close()) }()
	base := path.Base(n.ArtifactPath)
	extracted := filepath.Join(t.root, "collected")
	if err = transportarchive.UnpackOpen(ctx, archive.OSFile(), extracted, []string{base}); err != nil {
		return err
	}
	if err = archive.AssertUnchangedContext(ctx); err != nil {
		return err
	}
	if err := copyCollectedArtifactContext(ctx, extracted, base, artifactHostPath(opts, n)); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "persisting declared BuildKit export")
	}
	return nil
}

func (t *buildKitTransport) close() error {
	return errors.Join(t.resources.cleanup(), removePrivateTree(t.root))
}

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
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	started := time.Now()
	phase := func(label string) {
		deadline, _ := ctx.Deadline()
		opts.progress.printf("[transport] build %s — %s; elapsed=%s remaining=%s\n", n.Component, label,
			time.Since(started).Round(time.Millisecond), max(time.Duration(0), time.Until(deadline)).Round(time.Millisecond))
	}
	defer phase("finished")
	phase("admit private BuildKit transport")
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
	directory, _, err := securepath.OpenDirectory(root)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "opening private BuildKit transport")
	}
	available, capacityErr := securepath.AvailableDiskBackedBytes(ctx, directory)
	closeErr := directory.Close()
	if err = errors.Join(capacityErr, closeErr); err != nil || available/3 == 0 {
		return nil, e.Wrap(e.CodeEngineCapability, err, "BuildKit export requires disk-backed transport headroom")
	}
	transport.limit = int64(min(available/3, uint64(transportarchive.MaxArchiveBytes)))
	phase("pack source archive")
	innerPath := filepath.Join(root, "input.tar")
	if err = transportarchive.Pack(ctx, innerPath, base, []string{member}); err != nil {
		return nil, err
	}
	phase("verify source archive")
	inner, err := heldfile.OpenContext(ctx, innerPath, heldfile.Options{Label: "buildkit-input", MaxBytes: transportarchive.MaxArchiveBytes})
	if err != nil {
		return nil, err
	}
	transport.resources.held = append(transport.resources.held, inner)
	phase("pack transport carrier")
	outerPath := filepath.Join(root, "carrier.tar")
	if err = transportarchive.Pack(ctx, outerPath, root, []string{"input.tar"}); err != nil {
		return nil, err
	}
	phase("verify transport carrier")
	outer, err := heldfile.OpenContext(ctx, outerPath, heldfile.Options{Label: "buildkit-carrier", MaxBytes: transportarchive.MaxArchiveBytes})
	if err != nil {
		return nil, err
	}
	transport.resources.held = append(transport.resources.held, outer)
	attempt, err := packerAttemptID()
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "allocating fresh BuildKit volume identity")
	}
	name := "nvkr-buildkit-" + attempt
	transport.source, transport.output = name+"-source", name+"-output"
	phase("create private volumes")
	for _, volume := range []string{transport.source, transport.output} {
		transport.resources.volumes = append(transport.resources.volumes, volume)
		if err = createPackerVolume(ctx, runtime, volume, []string{"nvkr.dev/role=buildkit-transport"}); err != nil {
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
	phase("create preparation container")
	if err = runtime.Run(ctx, nil, io.Discard, opts.Stderr, args...); err != nil {
		return nil, runtimeCommandError(ctx, err, "", e.CodeBuildFailed, "creating offline BuildKit input stage")
	}
	prepCreated = true
	phase("copy authenticated carrier")
	if err = copyArchiveToContainer(ctx, runtime, outer, prep, "/nvkr/output"); err != nil {
		return nil, err
	}
	// Staging may use most of its allowance under storage pressure. Completion
	// has a separate finite budget, still bounded by the original caller: slow
	// create/copy responses must not consume the time for observing helper exit.
	cancel()
	completionCtx, finishCompletion := context.WithTimeout(callerCtx, 2*time.Minute)
	defer finishCompletion()
	ctx = completionCtx
	// The preparation script emits no progress. Observe its explicit exit instead
	// of coupling completion to Docker's attached output stream.
	phase("start preparation container")
	if err = runtime.Run(ctx, nil, io.Discard, opts.Stderr, "start", prep); err != nil {
		return nil, runtimeCommandError(ctx, err, "", e.CodeBuildFailed, "starting private BuildKit volume preparation")
	}
	phase("wait for preparation exit")
	var status bytes.Buffer
	statusOutput := &packerContainerLimitWriter{writer: &status, remaining: 32}
	if err = runtime.Run(ctx, nil, statusOutput, opts.Stderr, "wait", prep); err != nil {
		return nil, runtimeCommandError(ctx, err, "", e.CodeBuildFailed, "waiting for private BuildKit volume preparation")
	}
	if err = ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "observing private BuildKit volume preparation")
	}
	exit, parseErr := strconv.Atoi(strings.TrimSpace(status.String()))
	if parseErr != nil || exit < 0 || exit > 255 {
		return nil, e.New(e.CodeBuildFailed, "private BuildKit volume preparation returned no valid exit code")
	}
	phase("collect preparation diagnostics")
	diagnosticsAttempted = true
	logErr := runtime.Run(ctx, nil, opts.Stdout, opts.Stderr, "logs", prep)
	if exit != 0 {
		return nil, e.Wrap(e.CodeBuildFailed, logErr, "private BuildKit volume preparation exited %d", exit)
	}
	if logErr != nil {
		return nil, runtimeCommandError(ctx, logErr, "", e.CodeBuildFailed, "collecting private BuildKit volume preparation diagnostics")
	}
	phase("remove completed preparation container")
	if err = transport.resources.removeContainer(ctx, prep); err != nil {
		return nil, err
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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
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

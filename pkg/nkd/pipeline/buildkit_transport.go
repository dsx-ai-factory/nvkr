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
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/internal/transportarchive"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Only package-produced tar bytes enter this offline stage. Neither a host bind
// mount nor a source-owned command is present. Ownership changes are confined to
// fresh volumes; source modes and the authenticated input digest are unchanged.
const buildKitExtractScript = `exec 3</nkd/output/input.tar
printf '%s  %s\n' "$1" /proc/self/fd/3 | sha256sum -c - >/dev/null
tar -xpf /proc/self/fd/3 --strip-components=1 -C /nkd/source "$2"
rm /nkd/output/input.tar
chmod 0700 /nkd/source /nkd/output
chown -hR 1000:1000 /nkd/source /nkd/output`

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
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	base, err := filepath.Abs(filepath.Dir(source))
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "resolving BuildKit transport root")
	}
	member := filepath.Base(source)
	if !securepath.WorkspaceRelative(member) {
		return nil, e.New(e.CodeBuildFailed, "BuildKit source has an unsafe transport member")
	}
	root, err := os.MkdirTemp(base, ".nkd-buildkit-")
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "creating private BuildKit transport")
	}
	transport = &buildKitTransport{root: root, resources: &packerContainerResources{runtime: runtime}}
	owner := transport
	defer func() {
		if returnErr != nil {
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
	innerPath := filepath.Join(root, "input.tar")
	if err = transportarchive.Pack(ctx, innerPath, base, []string{member}); err != nil {
		return nil, err
	}
	inner, err := heldfile.OpenContext(ctx, innerPath, heldfile.Options{Label: "buildkit-input", MaxBytes: transportarchive.MaxArchiveBytes})
	if err != nil {
		return nil, err
	}
	transport.resources.held = append(transport.resources.held, inner)
	outerPath := filepath.Join(root, "carrier.tar")
	if err = transportarchive.Pack(ctx, outerPath, root, []string{"input.tar"}); err != nil {
		return nil, err
	}
	outer, err := heldfile.OpenContext(ctx, outerPath, heldfile.Options{Label: "buildkit-carrier", MaxBytes: transportarchive.MaxArchiveBytes})
	if err != nil {
		return nil, err
	}
	transport.resources.held = append(transport.resources.held, outer)
	attempt, err := packerAttemptID()
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "allocating fresh BuildKit volume identity")
	}
	name := "nkd-buildkit-" + attempt
	transport.source, transport.output = name+"-source", name+"-output"
	for _, volume := range []string{transport.source, transport.output} {
		transport.resources.volumes = append(transport.resources.volumes, volume)
		if err = createPackerVolume(ctx, runtime, volume, []string{"nkd.dev/role=buildkit-transport"}); err != nil {
			return nil, err
		}
	}
	prep := name + "-prepare"
	transport.resources.containers = append(transport.resources.containers, prep)
	args := []string{"create", "--name", prep, "--platform", n.Platform, "--network=none", "--read-only", "--user=0:0", "--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=DAC_OVERRIDE", "--security-opt=no-new-privileges", "--no-healthcheck", "--entrypoint=sh"}
	for _, mount := range []struct{ volume, destination string }{{transport.source, "/nkd/source"}, {transport.output, "/nkd/output"}} {
		args = append(args, buildKitVolumeArgs(runtime.RuntimeName(), mount.volume, mount.destination, false)...)
	}
	args = append(args, n.Toolchain, "-ec", buildKitExtractScript, "nkd-buildkit-input", strings.TrimPrefix(inner.Digest(), "sha256:"), member)
	if err = runtime.Run(ctx, nil, io.Discard, opts.Stderr, args...); err != nil {
		return nil, runtimeCommandError(ctx, err, "", e.CodeBuildFailed, "creating offline BuildKit input stage")
	}
	if err = copyArchiveToContainer(ctx, runtime, outer, prep, "/nkd/output"); err != nil {
		return nil, err
	}
	if err = runtime.Run(ctx, nil, opts.Stdout, opts.Stderr, "start", "--attach", prep); err != nil {
		return nil, runtimeCommandError(ctx, err, "", e.CodeBuildFailed, "preparing private BuildKit volumes")
	}
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

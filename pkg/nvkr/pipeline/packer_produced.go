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
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/runtimeexec"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"golang.org/x/sys/unix"
)

// runProducedPackerNode is called only inside the original production's
// runtime-use receipt. The root is borrowed for this call; neither its pathname
// nor a serialized runtime binding can recreate the execution capability.
func runProducedPackerNode(ctx context.Context, launcher *runtimeexec.Launcher, root *os.File,
	scratch string, request packerContainerRunRequest,
) (artifact *PackerArtifact, returnedErr error) {
	defer func() {
		if returnedErr != nil && artifact != nil {
			returnedErr = errors.Join(returnedErr, artifact.Close())
			artifact = nil
		}
	}()
	if err := requireRunContext(ctx, "produced-runtime Packer execution"); err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok || launcher == nil || root == nil || request.node.Runtime == nil || request.node.Network != ir.NetworkDefault {
		return nil, e.New(e.CodeEngineCapability, "produced Packer requires its admitted runtime, network and deadline")
	}
	if err := ValidateVMImagePackerDiskExecution(request.node); err != nil {
		return nil, err
	}
	if err := request.node.Validate(); err != nil {
		return nil, err
	}
	if request.source == nil || request.node.Packer == nil || request.node.ExecutionBinding == nil || request.node.Requirements == nil || request.node.Requirements.Resources == nil {
		return nil, e.New(e.CodeEngineCapability, "produced Packer requires authenticated source and admitted resources")
	}
	kvm, err := resolvedPackerKVM(request.node)
	if err != nil {
		return nil, err
	}
	if err := validatePackerContainerRequest(request, kvm); err != nil {
		return nil, err
	}
	if _, err := packerKVMGroup(request.kvm, kvm); err != nil {
		return nil, err
	}
	parent, _, err := securepath.OpenDirectory(scratch)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "holding Run-owned Packer scratch")
	}
	defer func() { returnedErr = errors.Join(returnedErr, parent.Close()) }()
	attempt, err := packerAttemptID()
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "allocating produced Packer attempt")
	}
	work, err := createPackerWorkspaceRoot(parent, attempt)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnedErr = errors.Join(returnedErr, work.cleanup())
		if returnedErr != nil && artifact != nil {
			returnedErr = errors.Join(returnedErr, artifact.Close())
			artifact = nil
		}
	}()
	target, err := request.diagnostics.start(ctx, request.subject, request.manifest, request.digest)
	if err != nil {
		return nil, err
	}
	defer func() { returnedErr = errors.Join(returnedErr, target.close()) }()
	staged, err := preparePackerConsumerInputs(ctx, work.path(), request)
	if err != nil {
		return nil, err
	}
	input, _, err := securepath.OpenChild(work.root, "input", true)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "holding produced Packer inputs")
	}
	defer func() { returnedErr = errors.Join(returnedErr, input.Close()) }()
	output, _, err := securepath.OpenChild(work.root, "work", true)
	if err != nil {
		return nil, e.Wrap(e.CodeBuildFailed, err, "holding produced Packer work")
	}
	defer func() { returnedErr = errors.Join(returnedErr, output.Close()) }()
	for _, directory := range []*os.File{input, output} {
		if err := assignPackerConsumerTree(ctx, directory, ""); err != nil {
			return nil, err
		}
	}
	if err := staged.verify(ctx); err != nil {
		return nil, err
	}
	invocation := runtimeexec.Request{Root: root, Projections: []runtimeexec.Projection{
		{Directory: input, Target: packerContainerInputRoot},
		{Directory: output, Target: packerContainerWorkRoot, Writable: true},
	}, Environment: producedPackerEnvironment(), Workdir: path.Join(packerContainerInputRoot, "source", request.node.Packer.ProjectPath)}
	version, versionErr := newPackerValidationOutput(io.Discard), newPackerValidationOutput(io.Discard)
	invocation.Command, invocation.Stdout, invocation.Stderr = []string{"/usr/local/bin/packer", "version"}, version, versionErr
	if err := launcher.Execute(ctx, invocation); err != nil {
		return nil, err
	}
	if !version.exact("Packer v"+request.bindings.packerVersion) || !versionErr.empty() {
		return nil, e.New(e.CodeUnverifiedRef, "produced runtime Packer version differs from its declared version")
	}
	if request.bindings.sshPrivateKeyVariable != "" {
		if err := prepareProducedPackerSSH(ctx, launcher, invocation, filepath.Join(work.path(), "work", "ssh")); err != nil {
			return nil, err
		}
	}
	base := packerContainerBaseArgs(request.node, request.bindings, kvm)
	validated, validationErr := newPackerValidationOutput(request.stdout), newPackerValidationOutput(request.stderr)
	invocation.Command = append([]string{"/usr/local/bin/packer", "validate"}, base...)
	invocation.Stdout, invocation.Stderr = validated, validationErr
	if err := launcher.Execute(ctx, invocation); err != nil {
		return nil, err
	}
	if !validated.exact("The configuration is valid.") || !validationErr.empty() {
		return nil, e.New(e.CodeBuildFailed, "Packer validation emitted diagnostics or did not confirm one clean configuration")
	}
	invocation.Command = append([]string{"/usr/local/bin/packer", "build", "-on-error=cleanup"}, base...)
	invocation.Stdout, invocation.Stderr, invocation.KVM, invocation.Outbound = request.stdout, request.stderr, request.kvm, true
	buildErr := launcher.Execute(ctx, invocation)
	// Execute joins both the consumer and the outbound helper before returning.
	// Cleanup may retain diagnostics after cancellation, but launches no payload.
	var logsErr error
	if request.bindings.buildLogDirectoryVariable != "" {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		logsErr = collectProducedPackerBuildLogs(cleanupCtx, output, target)
		cancel()
		if errors.Is(logsErr, errPackerDiagnosticsIncomplete) || errors.Is(logsErr, context.DeadlineExceeded) {
			markerCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			markerErr := target.recordIncomplete(markerCtx, logsErr)
			stop()
			if buildErr == nil && markerErr == nil {
				logsErr = nil
			} else {
				logsErr = errors.Join(logsErr, markerErr)
			}
		}
	}
	if err := errors.Join(buildErr, logsErr); err != nil {
		return nil, err
	}
	if err := staged.verify(ctx); err != nil {
		return nil, err
	}
	outputPath := filepath.Join(work.path(), "work", "output")
	artifact, err = collectProducedPackerArtifact(ctx, outputPath, request.node.ArtifactPath, request.node.Requirements.Resources.EphemeralStorageMiB)
	if err != nil {
		return nil, err
	}
	if request.node.Packer.AcquisitionArchive != "" {
		artifact.acquisitions, err = collectNativePackerAcquisitions(ctx, outputPath, &nativePackerAcquisitionRequest{
			archivePath: request.node.Packer.AcquisitionArchive, workspace: request.workspace, budget: *request.node.Requirements.Resources,
		}, artifact.Size())
		if err != nil {
			return artifact, err
		}
	}
	return artifact, nil
}

func producedPackerEnvironment() []string {
	return []string{"CHECKPOINT_DISABLE=1", "HOME=" + packerContainerWorkRoot + "/home",
		"PACKER_CACHE_DIR=" + packerContainerWorkRoot + "/cache", "PACKER_CONFIG=" + packerContainerInputRoot + "/config/packer.json",
		"PACKER_NO_COLOR=1", "PACKER_PLUGIN_PATH=" + packerContainerPluginPath, "PATH=" + packerContainerPATH,
		"TMPDIR=" + packerContainerWorkRoot + "/tmp", "LANG=C", "LC_ALL=C", "TZ=UTC"}
}

// The private parent remains root-owned 0700. Ownership transfer occurs before
// any child starts, through held descriptors, without following source links.
func assignPackerConsumerTree(ctx context.Context, directory *os.File, relative string) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "assigning Packer consumer input ownership")
	}
	names, err := directory.Readdirnames(-1)
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "reading Packer consumer directory")
	}
	for _, name := range names {
		entry, err := securepath.InspectChild(directory, name, path.Join(relative, name))
		if err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "holding Packer consumer entry")
		}
		if entry.File == nil {
			err = unix.Fchownat(int(directory.Fd()), name, 65534, 65534, unix.AT_SYMLINK_NOFOLLOW)
		} else {
			if entry.Info.IsDir() {
				err = assignPackerConsumerTree(ctx, entry.File, path.Join(relative, name))
			} else {
				err = entry.File.Chown(65534, 65534)
			}
			err = errors.Join(err, entry.File.Close())
		}
		if err != nil {
			return e.Wrap(e.CodeBuildFailed, err, "assigning Packer consumer entry ownership")
		}
	}
	if err := directory.Chown(65534, 65534); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "assigning Packer consumer directory ownership")
	}
	return nil
}

func prepareProducedPackerSSH(ctx context.Context, launcher *runtimeexec.Launcher, invocation runtimeexec.Request, directory string) error {
	key := packerContainerWorkRoot + "/ssh/id_ed25519"
	invocation.Command = []string{"/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key}
	invocation.Stdout, invocation.Stderr = io.Discard, io.Discard
	if err := launcher.Execute(ctx, invocation); err != nil {
		return err
	}
	var fingerprints [2]string
	for index, name := range []string{"id_ed25519", "id_ed25519.pub"} {
		file, info, err := openRegularBeneath(directory, name)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "opening produced Packer SSH key")
		}
		if err := file.Close(); err != nil {
			return e.Wrap(e.CodeInternal, err, "closing produced Packer SSH key")
		}
		if info.Size() <= 0 || info.Size() > 16<<10 || (index == 0 && info.Mode().Perm() != 0o600) {
			return e.New(e.CodeVerifyFailed, "produced Packer SSH key has invalid size or permissions")
		}
		capture, diagnostic := newPackerValidationOutput(io.Discard), newPackerValidationOutput(io.Discard)
		invocation.Command = []string{"/usr/bin/ssh-keygen", "-l", "-E", "sha256", "-f", path.Join(packerContainerWorkRoot, "ssh", name)}
		invocation.Stdout, invocation.Stderr = capture, diagnostic
		if err := launcher.Execute(ctx, invocation); err != nil {
			return err
		}
		if capture.overflow || !diagnostic.empty() {
			return e.New(e.CodeVerifyFailed, "produced Packer SSH fingerprint emitted diagnostics")
		}
		fingerprints[index], err = packerSSHKeyFingerprint(capture.buffer.Bytes())
		if err != nil {
			return err
		}
	}
	if fingerprints[0] != fingerprints[1] {
		return e.New(e.CodeVerifyFailed, "produced Packer SSH keys do not match")
	}
	return nil
}

func collectProducedPackerArtifact(ctx context.Context, directory, name string, storage uint64) (_ *PackerArtifact, returnedErr error) {
	file, info, err := openRegularBeneath(directory, name)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "holding produced Packer disk")
	}
	accepted := false
	defer func() {
		if !accepted {
			returnedErr = errors.Join(returnedErr, file.Close())
		}
	}()
	if info.Size() <= 4 || info.Size() > packerArtifactArchiveLimit(storage) {
		return nil, e.New(e.CodeVerifyFailed, "produced Packer disk exceeds its size bound")
	}
	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil || !bytes.Equal(magic[:], []byte{'Q', 'F', 'I', 0xfb}) {
		return nil, e.New(e.CodeVerifyFailed, "produced Packer disk is not QCOW2")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rewinding produced disk")
	}
	digest, size, _, err := hashOpenRegularContext(ctx, file, info)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "hashing produced Packer disk")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "rewinding produced disk")
	}
	if err := os.Remove(filepath.Join(directory, name)); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "unlinking private produced disk")
	}
	accepted = true
	return &PackerArtifact{file: file, digest: digest, size: size}, nil
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
)

// ToolSmokeProfile admits only the version commands owned by this package.
type ToolSmokeProfile string

const (
	ToolSmokeNVKECTL            ToolSmokeProfile = "nvkectl"
	ToolSmokeHelm               ToolSmokeProfile = "helm"
	ToolSmokeKubectl            ToolSmokeProfile = "kubectl"
	ToolSmokeKind               ToolSmokeProfile = "kind"
	ToolSmokeClusterctl         ToolSmokeProfile = "clusterctl"
	ToolSmokeORAS               ToolSmokeProfile = "oras"
	ToolSmokeNICoSim            ToolSmokeProfile = "nicosim"
	MaxToolSmokeExecutableBytes int64            = 128 << 20
	MaxToolSmokeOutputBytes                      = 64 << 10
	toolSmokeTimeout                             = 30 * time.Second
	toolSmokeCleanupTimeout                      = 10 * time.Second
	toolSmokeControlBytes                        = 1 << 20
)

// ToolSmokeSelection selects an already-local pinned image and native Linux
// platform. The trusted image supplies /bin/sh and standard POSIX/coreutils
// commands. There is no image pull, caller command, mount or environment option.
type ToolSmokeSelection struct {
	RuntimeExecutable string
	Image             string
	Platform          string
}

// ToolSmokeContainer owns the held runtime and every operation through cleanup.
// The Docker daemon and selected image are trusted. The delivered executable
// runs without credentials or network inside a bounded, unprivileged container.
// This owner does not confer qualification or authorize signing.
type ToolSmokeContainer struct{ *toolSmokeState }

// Copies share one private lifetime rather than copying mutexes or authority.
type toolSmokeState struct {
	mu         sync.Mutex
	lifetimeMu sync.Mutex
	lifetime   context.Context
	stop       context.CancelFunc
	closing    bool
	closeErr   error
	operations sync.WaitGroup
	held       *heldRuntimeExecutable
	runtime    packerContainerRuntime
	selection  ToolSmokeSelection
	imageID    string
}

// ToolSmokeObservation records execution of exact held bytes. The caller must
// independently evaluate the output against its authenticated expected version.
// Fields stay private so an observation cannot be constructed by a caller.
type ToolSmokeObservation struct {
	profile                          ToolSmokeProfile
	digest                           string
	size                             int64
	platform, image, runtimeIdentity string
	stdout, stderr                   []byte
}

func (o *ToolSmokeObservation) Profile() ToolSmokeProfile {
	if o == nil {
		return ""
	}
	return o.profile
}
func (o *ToolSmokeObservation) Subject() (string, int64) {
	if o == nil {
		return "", 0
	}
	return o.digest, o.size
}
func (o *ToolSmokeObservation) Platform() string {
	if o == nil {
		return ""
	}
	return o.platform
}
func (o *ToolSmokeObservation) Image() string {
	if o == nil {
		return ""
	}
	return o.image
}
func (o *ToolSmokeObservation) RuntimeIdentity() string {
	if o == nil {
		return ""
	}
	return o.runtimeIdentity
}
func (o *ToolSmokeObservation) Stdout() []byte {
	if o == nil {
		return nil
	}
	return bytes.Clone(o.stdout)
}
func (o *ToolSmokeObservation) Stderr() []byte {
	if o == nil {
		return nil
	}
	return bytes.Clone(o.stderr)
}

// OpenToolSmokeContainer admits capabilities before resolving the runtime. The
// held executable, local daemon identity and pinned image are rechecked for each
// RunHeld operation. Linux amd64 and arm64 execute natively; emulation is denied.
func OpenToolSmokeContainer(ctx context.Context, selection ToolSmokeSelection) (*ToolSmokeContainer, error) {
	if err := requireRunContext(ctx, "opening tool smoke runtime"); err != nil {
		return nil, err
	}
	if err := validateToolSmokeSelection(selection); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" || selection.Platform != "linux/"+runtime.GOARCH {
		return nil, e.New(e.CodeEngineCapability, "tool smoke requires native Linux execution for %s", selection.Platform)
	}
	ctx, cancel := context.WithTimeout(ctx, toolSmokeTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening tool smoke runtime")
	}
	runtimePath, err := resolveExecutionRuntime(selection.RuntimeExecutable)
	if err != nil {
		return nil, err
	}
	held, err := snapshotRuntimeExecutableContext(ctx, runtimePath)
	if err != nil {
		return nil, err
	}
	container := &ToolSmokeContainer{toolSmokeState: &toolSmokeState{held: held, selection: selection, runtime: toolSmokeBoundedRuntime{heldPackerContainerRuntime{runtime: held}}}}
	// Bound the identity observer through the same runtime adapter used below.
	executable := held.capability.Digest()
	var version, diagnostic, daemon diskInspectionBuffer
	version.limit, diagnostic.limit, daemon.limit = toolSmokeControlBytes, toolSmokeControlBytes, toolSmokeControlBytes
	if err = container.runtime.Run(ctx, nil, &version, &diagnostic, "version", "--format", "{{json .}}"); err == nil {
		err = container.runtime.Run(ctx, nil, &daemon, &diagnostic, "info", "--format", "{{json .ID}}")
	}
	if err == nil {
		held.identity, err = toolSmokeRuntimeIdentity(executable, held.endpoint, version.Bytes(), daemon.Bytes())
	}
	if err == nil && diagnostic.Len() != 0 {
		err = e.New(e.CodeEngineCapability, "tool smoke runtime admission returned diagnostics")
	}
	if err == nil {
		err = container.observeBinding(ctx)
	}
	if err != nil {
		err = errors.Join(err, held.close())
		return nil, runtimeCommandError(ctx, err, "", e.CodeEngineCapability, "admitting tool smoke runtime")
	}
	return container, nil
}

func validateToolSmokeSelection(selection ToolSmokeSelection) error {
	if selection.RuntimeExecutable == "" || filepath.Base(selection.RuntimeExecutable) != "docker" {
		return e.New(e.CodeEngineCapability, "tool smoke requires the Docker runtime")
	}
	if selection.Platform != "linux/amd64" && selection.Platform != "linux/arm64" {
		return e.New(e.CodeEngineCapability, "tool smoke requires an explicit native Linux amd64 or arm64 platform")
	}
	if canonical.IsDigest(selection.Image) {
		return nil
	}
	digest, err := name.NewDigest(selection.Image, name.StrictValidation)
	if err != nil || !canonical.IsDigest(digest.DigestStr()) {
		return e.New(e.CodeUnpinnedInput, "tool smoke requires an exact SHA-256 image pin")
	}
	return nil
}

func toolSmokeArguments(profile ToolSmokeProfile) ([]string, error) {
	switch profile {
	case ToolSmokeNVKECTL:
		return []string{"version", "--output", "json"}, nil
	case ToolSmokeHelm:
		return []string{"version", "--template", "{{.Version}}"}, nil
	case ToolSmokeKubectl:
		return []string{"version", "--client", "--output", "json"}, nil
	case ToolSmokeKind:
		return []string{"version", "-q"}, nil
	case ToolSmokeClusterctl:
		return []string{"version", "--output=json"}, nil
	case ToolSmokeNICoSim:
		return []string{"version", "--output=json"}, nil
	case ToolSmokeORAS:
		return []string{"version"}, nil
	default:
		return nil, e.New(e.CodeEngineCapability, "unsupported tool smoke profile %q", profile)
	}
}

func (container *ToolSmokeContainer) observeBinding(ctx context.Context) error {
	if container.runtime == nil {
		return e.New(e.CodeEngineCapability, "tool smoke runtime is closed")
	}
	if err := observePackerRuntimeFreshness(ctx, container.runtime); err != nil {
		return err
	}
	image, err := observeLocalContainerImage(ctx, container.runtime, container.selection.Image, container.selection.Platform, "tool smoke image")
	if err != nil {
		return err
	}
	id := normalizeLocalImageID(image.ID)
	if !canonical.IsDigest(id) || (container.imageID != "" && container.imageID != id) {
		return e.New(e.CodeVerifyFailed, "tool smoke image identity changed or is absent")
	}
	container.imageID = id
	return nil
}

func (container *ToolSmokeContainer) beginOperation(ctx context.Context) (context.Context, func(), error) {
	container.lifetimeMu.Lock()
	defer container.lifetimeMu.Unlock()
	if container.closing {
		return nil, nil, e.New(e.CodeEngineCapability, "tool smoke runtime is closed")
	}
	if container.lifetime == nil {
		container.lifetime, container.stop = context.WithCancel(context.WithoutCancel(ctx))
	}
	operation, cancel := context.WithTimeout(ctx, toolSmokeTimeout)
	stop := context.AfterFunc(container.lifetime, cancel)
	container.operations.Add(1)
	return operation, func() { stop(); cancel(); container.operations.Done() }, nil
}

// RunHeld borrows the caller's held file through the complete joined operation.
// It validates its recorded digest, size and native ELF header before Docker
// work, streams only those bytes, and revalidates them before returning output.
func (container *ToolSmokeContainer) RunHeld(ctx context.Context, subject *heldfile.File, profile ToolSmokeProfile) (*ToolSmokeObservation, error) {
	if err := requireRunContext(ctx, "running held tool smoke"); err != nil {
		return nil, err
	}
	if container == nil || container.toolSmokeState == nil {
		return nil, e.New(e.CodeEngineCapability, "tool smoke runtime is required")
	}
	if _, err := toolSmokeArguments(profile); err != nil {
		return nil, err
	}
	ctx, finish, err := container.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	if err := lockDiskInspection(ctx, &container.mu); err != nil {
		return nil, err
	}
	defer container.mu.Unlock()
	if err := validateToolSmokeSubject(ctx, subject, container.selection.Platform); err != nil {
		return nil, err
	}
	if err := container.observeBinding(ctx); err != nil {
		return nil, err
	}
	stdout, stderr, err := runToolSmokeContainer(ctx, container.runtime, container.selection, container.imageID, subject, profile, toolSmokeCleanupTimeout)
	if err != nil {
		return nil, err
	}
	if err := validateToolSmokeSubject(ctx, subject, container.selection.Platform); err != nil {
		return nil, err
	}
	if err := container.observeBinding(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "tool smoke canceled before observation")
	}
	_, identity := container.runtime.AdmittedIdentity()
	return &ToolSmokeObservation{profile: profile, digest: subject.Digest(), size: subject.Size(), platform: container.selection.Platform,
		image: container.imageID, runtimeIdentity: identity, stdout: stdout, stderr: stderr}, nil
}

// Close cancels and joins streaming, runtime clients and container cleanup before
// releasing its held runtime. Repeated calls are safe. It never closes the input.
func (container *ToolSmokeContainer) Close() error {
	if container == nil || container.toolSmokeState == nil {
		return nil
	}
	container.lifetimeMu.Lock()
	container.closing = true
	if container.stop != nil {
		container.stop()
	}
	container.lifetimeMu.Unlock()
	container.operations.Wait()
	container.mu.Lock()
	defer container.mu.Unlock()
	container.runtime = nil
	if container.held == nil {
		return container.closeErr
	}
	if err := container.held.close(); err != nil {
		container.closeErr = e.Wrap(e.CodeInternal, err, "closing tool smoke runtime")
	}
	container.held = nil
	return container.closeErr
}

func validateToolSmokeSubject(ctx context.Context, subject *heldfile.File, platform string) error {
	if subject == nil || subject.Size() < 64 || subject.Size() > MaxToolSmokeExecutableBytes || !canonical.IsDigest(subject.Digest()) {
		return e.New(e.CodeEngineCapability, "tool smoke requires one bounded held ELF executable")
	}
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return runtimeCommandError(ctx, err, "", e.CodeVerifyFailed, "checking tool smoke subject")
	}
	var header toolSmokeELFHeader
	_, err := subject.WriteToContext(ctx, &header)
	if err != nil && !errors.Is(err, io.EOF) {
		return runtimeCommandError(ctx, err, "", e.CodeVerifyFailed, "reading tool smoke ELF header")
	}
	h := header.raw[:]
	machine := uint16(0)
	switch platform {
	case "linux/amd64":
		machine = 62
	case "linux/arm64":
		machine = 183
	}
	elfType := binary.LittleEndian.Uint16(h[16:18])
	// ELF64, little-endian, original ELF version, System V/Linux ABI, executable
	// or PIE, and the selected native machine. Never execute the subject on host.
	if machine == 0 || !bytes.Equal(h[:7], []byte{'\x7f', 'E', 'L', 'F', 2, 1, 1}) || (h[7] != 0 && h[7] != 3) || h[8] != 0 ||
		(elfType != 2 && elfType != 3) || binary.LittleEndian.Uint16(h[18:20]) != machine || binary.LittleEndian.Uint32(h[20:24]) != 1 || binary.LittleEndian.Uint16(h[52:54]) != 64 {
		return e.New(e.CodeEngineCapability, "tool smoke subject is not a native Linux ELF executable for %s", platform)
	}
	return nil
}

type toolSmokeELFHeader struct {
	raw  [64]byte
	size int
}

func (h *toolSmokeELFHeader) Write(raw []byte) (int, error) {
	n := copy(h.raw[h.size:], raw)
	h.size += n
	return n, io.EOF
}

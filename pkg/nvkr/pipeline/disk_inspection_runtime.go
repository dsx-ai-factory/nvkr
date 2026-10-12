// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/diskdiagnostic"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
)

// DiskInspectionSelection describes an already-local inspection closure. Pins
// are expectations, not authority: admission checks image bytes. The selected
// local runtime and daemon are trusted, held and rechecked for freshness, not
// compared with an independently supplied runtime pin. Workspace remains
// caller-owned and must remain open until Close.
// This development capability does not grant publication or qualification.
type DiskInspectionSelection struct {
	PodmanStorageDriver definition.PodmanStorageDriver
	RuntimeExecutable   string
	Image               string
	Platform            string
	InspectorDigest     string
	CodecDigest         string
	GuestfishDigest     string
	ScannerDigest       string
	PackageReaderDigest string
	MaxFilesystemBytes  uint64
	ScratchStorage      string
	compositionKind     string
	consumerPlatform    string
	scratchVolume       string
	Resources           execution.ResourceRequest
	TimeoutSeconds      uint64
	Workspace           *os.File
	// Diagnostics receives sanitized phase records only. It is not part of
	// the authenticated observation profile and never grants evidence authority.
	Diagnostics *diskdiagnostic.Collector
}

// DiskInspectionContainer retains the exact runtime executable, daemon identity
// and complete immutable image closure. No caller command, mount or device can
// be passed through it. The daemon is trusted, as in the local Packer adapter.
type DiskInspectionContainer struct {
	mu              sync.Mutex
	lifetimeMu      sync.Mutex
	lifetime        context.Context
	stop            context.CancelFunc
	closing         bool
	operations      sync.WaitGroup
	runtime         *heldRuntimeExecutable
	selection       DiskInspectionSelection
	imageID         string
	observeHost     func(context.Context, *os.File) (execution.ObservedHostFacts, error)
	waitForCapacity func(context.Context) error
}

const (
	diskInspectionPIDsLimit    uint64 = 256
	diskInspectionApplianceMiB uint64 = 2048
	// MaxDiagnosticDiskBytes bounds the held diagnostic subject and its copies.
	MaxDiagnosticDiskBytes = 8 << 30
	// MaxDiskInspectionReportBytes bounds helper reports and runtime diagnostics.
	MaxDiskInspectionReportBytes = 8 << 20
	// Composition reports carry complete file inventories. Diagnostics and
	// installed-package-only reports retain their independent smaller bound.
	MaxDiskContentReportBytes = 16 << 20
	// Buffer, returned report, observation copy and decoder input may overlap.
	diskContentReportMemoryMiB = 4 * (MaxDiskContentReportBytes - MaxDiskInspectionReportBytes) >> 20
	// MaxDiskInspectionTimeout bounds the complete operation, including revalidation.
	MaxDiskInspectionTimeout = 10 * time.Minute
)

// One process-wide permit covers every inspection runtime, not just one
// container. Availability observations are not allocations: serializing the
// admitted snapshot/scan lifetime prevents our own callers spending the same
// observed headroom twice, but cannot reserve resources against other processes.
var diskInspectionCapacity sync.Mutex

// diskInspectionReservation is private to the owning operation. It remains held
// until that operation closes its snapshot and joins container cleanup.
type diskInspectionReservation struct {
	state *diskInspectionReservationState
}

type diskInspectionReservationState struct {
	mu             sync.Mutex
	container      *DiskInspectionContainer
	size           int64
	timeoutSeconds uint64
}

func (reservation *diskInspectionReservation) Close() error {
	if reservation == nil || reservation.state == nil {
		return nil
	}
	state := reservation.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.container != nil {
		state.container = nil
		diskInspectionCapacity.Unlock()
	}
	return nil
}

// InspectHeld consumes only a subject of the admitted size, without reacquiring
// capacity. The caller must keep the reservation until that subject is closed.
func (reservation *diskInspectionReservation) InspectHeld(ctx context.Context, subject *heldfile.File) (*DiskInspectionObservation, error) {
	return reservation.inspectContent(ctx, subject, "", "")
}

func (reservation *diskInspectionReservation) inspectContent(ctx context.Context, subject *heldfile.File, kind, platform string) (*DiskInspectionObservation, error) {
	if err := requireRunContext(ctx, "reserved disk inspection"); err != nil {
		return nil, err
	}
	if reservation == nil || reservation.state == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspection reservation is absent")
	}
	state := reservation.state
	ctx, cancel := context.WithTimeout(ctx, time.Duration(state.timeoutSeconds)*time.Second)
	defer cancel()
	if err := lockDiskInspection(ctx, &state.mu); err != nil {
		return nil, err
	}
	defer state.mu.Unlock()
	if state.container == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspection reservation is closed")
	}
	if subject == nil || subject.Size() != state.size {
		return nil, e.New(e.CodeVerifyFailed, "inspection subject differs from its admitted size")
	}
	return state.container.inspectHeldMode(ctx, subject, kind, platform)
}

// OpenDiskInspectionContainer admits the selected native Linux runtime without
// starting a container or pulling code. Both supported architectures use the
// same restrictive launcher; cross-architecture emulation is not admitted.
func OpenDiskInspectionContainer(ctx context.Context, selection DiskInspectionSelection) (*DiskInspectionContainer, error) {
	if err := requireRunContext(ctx, "disk inspection runtime admission"); err != nil {
		return nil, err
	}
	if err := validateDiskInspectionSelection(selection); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" || selection.Platform != "linux/"+runtime.GOARCH {
		return nil, e.New(e.CodeEngineCapability, "disk inspection requires its native Linux host architecture")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runtimePath, err := resolveExecutionRuntime(selection.RuntimeExecutable)
	if err != nil {
		return nil, err
	}
	held, err := snapshotRuntimeExecutableContext(ctx, runtimePath)
	if err != nil {
		return nil, err
	}
	held.storageDriver = selection.PodmanStorageDriver
	held.identity, err = observeHeldRuntimeIdentity(ctx, held)
	if err != nil {
		_ = held.close()
		return nil, runtimeCommandError(ctx, err, "", e.CodeEngineCapability, "admitting disk inspection runtime identity")
	}
	container := &DiskInspectionContainer{runtime: held, selection: selection}
	if err := container.observeBinding(ctx); err != nil {
		_ = held.close()
		return nil, err
	}
	return container, nil
}

func validateDiskInspectionSelection(selection DiskInspectionSelection) error {
	if err := selection.PodmanStorageDriver.Validate(filepath.Base(selection.RuntimeExecutable)); err != nil {
		return err
	}
	if filepath.Base(selection.RuntimeExecutable) != "docker" && filepath.Base(selection.RuntimeExecutable) != "podman" {
		return e.New(e.CodeEngineCapability, "disk inspection requires an explicit Docker or Podman runtime")
	}
	if !canonical.IsDigest(selection.Image) {
		if _, err := name.NewDigest(selection.Image, name.StrictValidation); err != nil {
			return e.Wrap(e.CodeUnpinnedInput, err, "disk inspection requires an exact OCI image pin")
		}
	}
	if selection.Platform != "linux/amd64" && selection.Platform != "linux/arm64" {
		return e.New(e.CodeEngineCapability, "disk inspection supports Linux AMD64 and ARM64")
	}
	if !canonical.IsDigest(selection.InspectorDigest) || !canonical.IsDigest(selection.CodecDigest) {
		return e.New(e.CodeUnpinnedInput, "disk inspection requires exact inspector and codec pins")
	}
	if selection.Resources.CPUMillis > uint64(math.MaxInt64)/100 {
		return e.New(e.CodeEngineCapability, "disk inspection CPU request exceeds the exact runtime quota range")
	}
	if selection.Workspace == nil || selection.Resources.CPUMillis < 10 || selection.Resources.MemoryMiB < diskInspectionApplianceMiB || selection.TimeoutSeconds == 0 || selection.TimeoutSeconds > uint64(MaxDiskInspectionTimeout/time.Second) {
		return e.New(e.CodeEngineCapability, "disk inspection requires a held workspace, at least 10 CPU millis, at least 2 GiB memory and a timeout of at most ten minutes")
	}
	if selection.GuestfishDigest != "" || selection.ScannerDigest != "" || selection.PackageReaderDigest != "" || selection.MaxFilesystemBytes != 0 {
		if !canonical.IsDigest(selection.GuestfishDigest) || !canonical.IsDigest(selection.ScannerDigest) || !canonical.IsDigest(selection.PackageReaderDigest) || selection.MaxFilesystemBytes == 0 || selection.MaxFilesystemBytes > 16<<30 {
			return e.New(e.CodeUnpinnedInput, "composition inspection requires complete exporter, scanner, package reader and filesystem budget pins")
		}
		if selection.ScratchStorage != "memory" && selection.ScratchStorage != "private-volume" {
			return e.New(e.CodeEngineCapability, "composition inspection requires explicit scratch storage")
		}
	}
	return nil
}

func (container *DiskInspectionContainer) observeBinding(ctx context.Context) error {
	if container == nil || container.runtime == nil {
		return e.New(e.CodeEngineCapability, "disk inspection runtime is closed")
	}
	adapter := heldPackerContainerRuntime{runtime: container.runtime}
	if err := observePackerRuntimeFreshness(ctx, adapter); err != nil {
		return err
	}
	image, err := observeLocalContainerImage(ctx, adapter, container.selection.Image, container.selection.Platform, "local disk inspection image")
	if err != nil {
		return err
	}
	id := normalizeLocalImageID(image.ID)
	if !canonical.IsDigest(id) {
		return e.New(e.CodeEngineCapability, "runtime returned no exact inspection image identity")
	}
	if container.imageID != "" && container.imageID != id {
		return e.New(e.CodeVerifyFailed, "disk inspection image changed after admission")
	}
	container.imageID = id
	return nil
}

// BindingContext checks that the capability remains open and unchanged. Image
// identity is the runtime-reported image ID, not an independently approved pin.
// The recorded Podman config identity binds rootfs content; the unexercised
// Docker route does not establish which image-store identity .Id represents.
func (container *DiskInspectionContainer) BindingContext(ctx context.Context) (string, string, string, error) {
	if err := requireRunContext(ctx, "disk inspection runtime binding"); err != nil {
		return "", "", "", err
	}
	if container == nil {
		return "", "", "", e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
	ctx, finish, err := container.beginOperation(ctx)
	if err != nil {
		return "", "", "", err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := lockDiskInspection(ctx, &container.mu); err != nil {
		return "", "", "", err
	}
	defer container.mu.Unlock()
	if err := container.observeBinding(ctx); err != nil {
		return "", "", "", err
	}
	return container.imageID, container.selection.InspectorDigest, container.selection.CodecDigest, nil
}

// Close cancels registered operations, then joins their streaming, snapshot
// closure and deferred cleanup before closing the held runtime.
func (container *DiskInspectionContainer) Close() error {
	if container == nil {
		return nil
	}
	container.lifetimeMu.Lock()
	container.closing = true
	stop := container.stop
	container.lifetimeMu.Unlock()
	if stop != nil {
		stop()
	}
	container.operations.Wait()
	container.mu.Lock()
	defer container.mu.Unlock()
	if container.runtime == nil {
		return nil
	}
	err := container.runtime.close()
	container.runtime = nil
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "closing disk inspection runtime")
	}
	return nil
}

// beginOperation registers before Close can start its join. Completion is
// signaled only after owned subprocess streaming and deferred cleanup return.
func (container *DiskInspectionContainer) beginOperation(ctx context.Context) (context.Context, func(), error) {
	container.lifetimeMu.Lock()
	defer container.lifetimeMu.Unlock()
	if container.closing {
		return nil, nil, e.New(e.CodeEngineCapability, "disk inspection runtime is closed")
	}
	if container.lifetime == nil {
		container.lifetime, container.stop = context.WithCancel(context.WithoutCancel(ctx))
	}
	operation, cancel := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(container.lifetime, cancel)
	container.operations.Add(1)
	return operation, func() { stopCancellation(); cancel(); container.operations.Done() }, nil
}

// InspectPackerArtifact owns admission, original-inode snapshot creation,
// inspection and cleanup as one registered operation. No caller can retain its
// capacity permit or temporary snapshot. Close cancels and joins the complete
// operation before the permit is released. Artifact ownership stays with Run.
func (container *DiskInspectionContainer) InspectPackerArtifact(ctx context.Context, artifact *PackerArtifact) (*DiskInspectionObservation, error) {
	if err := requireRunContext(ctx, "Packer disk inspection"); err != nil {
		return nil, err
	}
	if container == nil || artifact == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspection requires a runtime and original Packer artifact")
	}
	if container.selection.TimeoutSeconds == 0 || container.selection.TimeoutSeconds > uint64(MaxDiskInspectionTimeout/time.Second) {
		return nil, e.New(e.CodeEngineCapability, "inspection runtime lacks its bounded declared operation timeout")
	}
	ctx, finish, err := container.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(container.selection.TimeoutSeconds)*time.Second)
	defer cancel()
	reservation, err := container.reserveSubject(ctx, artifact.Size(), true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reservation.Close() }()
	subject, err := artifact.snapshotHeldContext(ctx, MaxDiagnosticDiskBytes)
	if err != nil {
		return nil, err
	}
	return inspectAndCloseDiskSnapshot(ctx, subject, reservation.InspectHeld, subject.Close)
}

// The private seam tests cleanup failure without making caller callbacks or
// temporary snapshot ownership part of the public inspection contract.
func inspectAndCloseDiskSnapshot(ctx context.Context, subject *heldfile.File,
	scan func(context.Context, *heldfile.File) (*DiskInspectionObservation, error), closeSubject func() error,
) (observation *DiskInspectionObservation, returnErr error) {
	defer func() {
		if closeErr := closeSubject(); closeErr != nil {
			code := e.CodeInternal
			var coded *e.Error
			if errors.As(returnErr, &coded) {
				code = coded.Code
			}
			observation = nil
			returnErr = e.Wrap(code, errors.Join(returnErr, closeErr), "inspecting and closing the held disk snapshot")
		}
	}()
	observation, returnErr = scan(ctx, subject)
	if returnErr != nil {
		return nil, returnErr
	}
	if observation == nil {
		return nil, e.New(e.CodeVerifyFailed, "disk inspector returned no observation")
	}
	return observation, nil
}

func (container *DiskInspectionContainer) reserveSubject(ctx context.Context, size int64, beforeSnapshot bool) (*diskInspectionReservation, error) {
	if err := requireRunContext(ctx, "inspection subject admission"); err != nil {
		return nil, err
	}
	if container == nil || size <= 0 || size > MaxDiagnosticDiskBytes {
		return nil, e.New(e.CodeEngineCapability, "inspection subject exceeds its supported byte budget")
	}
	if container.selection.TimeoutSeconds == 0 || container.selection.TimeoutSeconds > uint64(MaxDiskInspectionTimeout/time.Second) {
		return nil, e.New(e.CodeEngineCapability, "inspection runtime lacks its bounded declared operation timeout")
	}
	ctx, finish, err := container.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(container.selection.TimeoutSeconds)*time.Second)
	defer cancel()
	wait := container.waitForCapacity
	if wait == nil {
		wait = func(ctx context.Context) error { return lockDiskInspection(ctx, &diskInspectionCapacity) }
	}
	if err := wait(ctx); err != nil {
		return nil, err
	}
	admitted := false
	defer func() {
		if !admitted {
			diskInspectionCapacity.Unlock()
		}
	}()
	if err := lockDiskInspection(ctx, &container.mu); err != nil {
		return nil, err
	}
	defer container.mu.Unlock()
	if container.runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "disk inspection runtime is closed")
	}
	observationCtx, observationCancel := context.WithTimeout(ctx, 30*time.Second)
	defer observationCancel()
	facts, err := container.hostFacts(observationCtx)
	if err != nil {
		return nil, err
	}
	if err := validateDiskInspectionResources(container.selection, facts, size, beforeSnapshot); err != nil {
		return nil, err
	}
	admitted = true
	return &diskInspectionReservation{state: &diskInspectionReservationState{
		container: container, size: size, timeoutSeconds: container.selection.TimeoutSeconds,
	}}, nil
}

// DiskInspectionObservation can be issued only by this adapter after the fixed
// command has completed and the held subject/runtime have been revalidated.
// Description methods do not mint another observation.
type DiskInspectionObservation struct {
	report                                                   []byte
	subject, image, inspector, codec                         string
	size                                                     int64
	contentKind, guestfish, scanner, packageReader, platform string
	maxFilesystemBytes                                       uint64
}

func (observation *DiskInspectionObservation) Report() []byte {
	if observation == nil {
		return nil
	}
	return bytes.Clone(observation.report)
}

func (observation *DiskInspectionObservation) Binding() (subject string, size int64, image, inspector, codec string) {
	if observation == nil {
		return "", 0, "", "", ""
	}
	return observation.subject, observation.size, observation.image, observation.inspector, observation.codec
}

// InspectHeld passes only sealed input bytes to the fixed helper. The temporary
// filesystem is bounded RAM, not an uncontrolled daemon volume or host bind.
// Admission accounts for the helper's held copy, its read-only disk snapshot and
// the appliance. No image pull, credential, device or caller mount is permitted.
func (container *DiskInspectionContainer) InspectHeld(ctx context.Context, subject *heldfile.File) (*DiskInspectionObservation, error) {
	if err := requireRunContext(ctx, "container disk inspection"); err != nil {
		return nil, err
	}
	if container == nil || subject == nil || subject.Size() <= 0 || subject.Size() > MaxDiagnosticDiskBytes {
		return nil, e.New(e.CodeEngineCapability, "container inspection requires one bounded held disk")
	}
	ctx, finish, err := container.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(container.selection.TimeoutSeconds)*time.Second)
	defer cancel()
	reservation, err := container.reserveSubject(ctx, subject.Size(), false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reservation.Close() }()
	return reservation.InspectHeld(ctx, subject)
}

func (container *DiskInspectionContainer) inspectHeldMode(ctx context.Context, subject *heldfile.File, kind, platform string) (*DiskInspectionObservation, error) {
	ctx, finish, err := container.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(container.selection.TimeoutSeconds)*time.Second)
	defer cancel()
	if err := lockDiskInspection(ctx, &container.mu); err != nil {
		return nil, err
	}
	defer container.mu.Unlock()
	if err := container.observeBinding(ctx); err != nil {
		return nil, err
	}
	if err := checkDiskInspectionSubject(ctx, subject, "checking inspection subject"); err != nil {
		return nil, err
	}
	if err := container.admitResources(ctx, subject.Size()); err != nil {
		return nil, err
	}
	selection := container.selection
	if kind != "" {
		if !diskContentKind(kind) || selection.GuestfishDigest == "" || selection.ScannerDigest == "" || selection.PackageReaderDigest == "" || selection.MaxFilesystemBytes == 0 {
			return nil, e.New(e.CodeEngineCapability, "disk composition inspection requires a complete admitted runtime profile")
		}
		selection.compositionKind, selection.consumerPlatform = kind, platform
	}
	report, err := runDiskInspectionContainer(ctx, heldPackerContainerRuntime{runtime: container.runtime}, selection, container.imageID, subject)
	if err != nil {
		return nil, err
	}
	if err := checkDiskInspectionSubject(ctx, subject, "rechecking inspected disk"); err != nil {
		return nil, err
	}
	if err := container.observeBinding(ctx); err != nil {
		return nil, err
	}
	return &DiskInspectionObservation{report: report, subject: subject.Digest(), size: subject.Size(), image: container.imageID, inspector: container.selection.InspectorDigest, codec: container.selection.CodecDigest,
		contentKind: kind, guestfish: selection.GuestfishDigest, scanner: selection.ScannerDigest, packageReader: selection.PackageReaderDigest, platform: selection.consumerPlatform, maxFilesystemBytes: selection.MaxFilesystemBytes}, nil
}

// runDiskInspectionContainer owns subprocess streaming and cleanup, but issues
// no observation. Its caller must revalidate the held subject and runtime after
// this operation returns. The seam is the existing concrete-runtime adapter.
func runDiskInspectionContainer(ctx context.Context, runtime packerContainerRuntime, selection DiskInspectionSelection, imageID string, subject *heldfile.File) (raw []byte, returnErr error) {
	return runDiskInspectionContainerWithCleanupTimeout(ctx, runtime, selection, imageID, subject, 10*time.Second)
}

// The private timeout argument lets contract tests bound uncertain registration
// without a mutable process-global policy. Production always grants ten seconds.
func runDiskInspectionContainerWithCleanupTimeout(ctx context.Context, runtime packerContainerRuntime, selection DiskInspectionSelection, imageID string, subject *heldfile.File, cleanupTimeout time.Duration) (raw []byte, returnErr error) {
	attempt, err := packerAttemptID()
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "allocating disk inspection attempt identity")
	}
	name := "nvkr-inspect-" + attempt
	if selection.compositionKind != "" && selection.ScratchStorage == "private-volume" {
		scratch, err := openDiskInspectionScratch(ctx, runtime, selection, imageID, attempt)
		if err != nil {
			return nil, err
		}
		selection.scratchVolume = scratch.state.volume
		defer func() {
			if err := scratch.Close(ctx); err != nil {
				raw = nil
				returnErr = errors.Join(returnErr, err)
			}
		}()
	}
	creationUncertain := true
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cleanupCancel()
		err := removeDiskInspectionContainer(cleanupCtx, runtime, attempt, creationUncertain)
		if err != nil {
			raw = nil
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeInternal, err, "removing inspection container"))
		}
	}()
	var created, createDiagnostic, report diskInspectionBuffer
	diagnostic := diskdiagnostic.NewSubjectReceiver(selection.Diagnostics, selection.compositionKind, subject.Digest())
	if selection.compositionKind != "" {
		report.limit = MaxDiskContentReportBytes
	}
	if err := runtime.Run(ctx, nil, &created, &createDiagnostic, diskInspectionCommandArgs(runtime.RuntimeName(), diskInspectionCreateArgs(selection, imageID, name, attempt, subject)...)...); err != nil {
		return nil, runtimeCommandError(ctx, err, diskInspectionDiagnostic(createDiagnostic.Bytes()), e.CodeEngineCapability, "creating disk inspection container")
	}
	creationUncertain = false
	// A successful create can still warn that a requested sandbox limit was
	// ignored. Reject that capability gap before streaming bytes or starting QEMU.
	if createDiagnostic.Len() != 0 {
		return nil, e.New(e.CodeEngineCapability, "disk inspection creation returned diagnostics: %s", diskInspectionDiagnostic(createDiagnostic.Bytes()))
	}
	ids := strings.Fields(created.String())
	if len(ids) != 1 || !canonical.IsDigest("sha256:"+ids[0]) {
		return nil, e.New(e.CodeVerifyFailed, "disk inspection creation returned no unique immutable container ID")
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { _, err := subject.WriteToContext(ctx, writer); _ = writer.CloseWithError(err); done <- err }()
	err = runtime.Run(ctx, reader, &report, diagnostic, diskInspectionCommandArgs(runtime.RuntimeName(), "start", "--attach", "--interactive", ids[0])...)
	diagnostic.Finish()
	_ = reader.Close()
	copyErr := <-done
	if err := errors.Join(err, copyErr); err != nil {
		// Preserve allowlisted phase/timing information even when the context
		// branch of the shared runtime error mapper omits child stderr.
		return nil, runtimeCommandError(ctx, err, diskInspectionDiagnostic(diagnostic.FailureBytes()), diskInspectionHelperCode(err), "inspecting held disk: %s", diagnostic.Summary())
	}
	if diagnostic.Unexpected() || report.Len() == 0 {
		return nil, e.New(e.CodeEnvelopeDecode, "disk inspection returned diagnostics or no report")
	}
	return bytes.Clone(report.Bytes()), nil
}

// Podman adds mounts.conf content at start, even when create reports no mounts.
// Its native global override suppresses those automatic host inputs. Missing
// flag support fails at the command boundary; Docker receives no new flag.
func diskInspectionCommandArgs(runtimeName string, args ...string) []string {
	if runtimeName == "podman" {
		return append([]string{"--default-mounts-file=/dev/null"}, args...)
	}
	return args
}

// Attached Docker/Podman start propagates the fixed helper's exit status. Reuse
// the owning frozen mapping for its expected error classes, not diagnostic text.
// Generic CLI statuses (including 1/2), runtime statuses 125/126/127, signals and
// unknown statuses are ambiguous and retain envelope-decode. Caller cancellation
// still takes precedence in runtimeCommandError.
func diskInspectionHelperCode(err error) e.Code {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		for _, candidate := range []e.Code{
			e.CodeEngineCapability, e.CodeEncryptedArtifact, e.CodeProvenanceMissing,
			e.CodeVerifyFailed, e.CodeEnvelopeDecode, e.CodeTimeout, e.CodeUnpinnedInput, e.CodeUnverifiedRef,
		} {
			if exit.ExitCode() == e.ExitCode(e.New(candidate, "")) {
				return candidate
			}
		}
	}
	return e.CodeEnvelopeDecode
}

func diskInspectionDiagnostic(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	const maxBytes = 4096
	truncated := len(raw) > maxBytes
	if truncated {
		raw = raw[:maxBytes]
	}
	return strconv.Quote(strings.TrimSpace(string(raw))) + " (truncated=" + strconv.FormatBool(truncated) + ")"
}

func checkDiskInspectionSubject(ctx context.Context, subject *heldfile.File, phase string) error {
	if err := subject.AssertUnchangedContext(ctx); err != nil {
		return runtimeCommandError(ctx, err, "", e.CodeVerifyFailed, "%s", phase)
	}
	return nil
}

func (container *DiskInspectionContainer) admitResources(ctx context.Context, size int64) error {
	facts, err := container.hostFacts(ctx)
	if err != nil {
		return err
	}
	return validateDiskInspectionResources(container.selection, facts, size, false)
}

func (container *DiskInspectionContainer) hostFacts(ctx context.Context) (execution.ObservedHostFacts, error) {
	observe := container.observeHost
	if observe == nil {
		observe = execution.ObserveLocalHost
	}
	return observe(ctx, container.selection.Workspace)
}

func validateDiskInspectionResources(selection DiskInspectionSelection, facts execution.ObservedHostFacts, size int64, beforeSnapshot bool) error {
	request := selection.Resources
	workspaceMiB := request.EphemeralStorageMiB
	snapshotMiB := uint64((size + (1 << 20) - 1) >> 20)
	needMemory := 2*snapshotMiB + diskInspectionApplianceMiB
	if selection.MaxFilesystemBytes != 0 {
		needMemory += diskContentReportMemoryMiB
		filesystemMiB := (selection.MaxFilesystemBytes + (1 << 20) - 1) / (1 << 20)
		if selection.ScratchStorage == "private-volume" {
			// Linux heldfile snapshots are sealed memfds, accounted in RAM
			// below. No subject or helper scratch is written to Workspace:
			// the attempt-private daemon volume is admitted separately before
			// streaming. Do not demand that same scratch on two filesystems.
			workspaceMiB = 0
			needMemory += 6144 + 2*min(filesystemMiB, 1024)
			needScratch := 12*filesystemMiB + 2*snapshotMiB + diskInspectionApplianceMiB
			if request.EphemeralStorageMiB < needScratch {
				return e.New(e.CodeEngineCapability, "inspection scratch capacity: required=%dMiB declared=%dMiB (filesystem=%dMiB subject=%dMiB appliance=%dMiB)", needScratch, request.EphemeralStorageMiB, filesystemMiB, snapshotMiB, diskInspectionApplianceMiB)
			}
		} else {
			needMemory += 12*filesystemMiB + 2048
		}
	}
	hostMemory := request.MemoryMiB
	if beforeSnapshot {
		if hostMemory > math.MaxUint64-snapshotMiB {
			return e.New(e.CodeEngineCapability, "inspection host memory requirement overflows: declared=%dMiB snapshot=%dMiB", request.MemoryMiB, snapshotMiB)
		}
		// Compare the full declared grant plus external snapshot with the
		// caller-side observation, not merely their independent maxima. This
		// conservative check does not authenticate the daemon's cgroup budget.
		hostMemory += snapshotMiB
	}
	if request.MemoryMiB < needMemory {
		return e.New(e.CodeEngineCapability, "inspection memory capacity: required=%dMiB declared=%dMiB (subject=%dMiB)", needMemory, request.MemoryMiB, snapshotMiB)
	}
	if facts.Platform != execution.HostPlatform(selection.Platform) {
		return e.New(e.CodeEngineCapability, "inspection host platform: required=%s observed=%s", selection.Platform, facts.Platform)
	}
	if facts.Resources.CPUMillis < request.CPUMillis {
		return e.New(e.CodeEngineCapability, "inspection host CPU capacity: required=%dm observed=%dm", request.CPUMillis, facts.Resources.CPUMillis)
	}
	if facts.Resources.MemoryMiB < hostMemory {
		return e.New(e.CodeEngineCapability, "inspection host memory capacity: required=%dMiB observed=%dMiB", hostMemory, facts.Resources.MemoryMiB)
	}
	if facts.Resources.EphemeralStorageMiB < workspaceMiB {
		return e.New(e.CodeEngineCapability, "inspection workspace capacity: required=%dMiB observed=%dMiB", workspaceMiB, facts.Resources.EphemeralStorageMiB)
	}
	return nil
}

func diskInspectionCreateArgs(selection DiskInspectionSelection, image, containerName, attempt string, subject *heldfile.File) []string {
	args := []string{"create", "--name", containerName, "--pull=never", "--platform", selection.Platform,
		"--label=nvkr.dev/inspection-attempt=" + attempt,
		"--network=none", "--log-driver=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--user=65534:65534", "--hostname=nvkr-inspector", "--no-healthcheck", "--init", "--interactive",
		diskInspectionCPUPeriodArg, diskInspectionCPUQuotaArg(selection.Resources.CPUMillis),
		"--memory=" + strconv.FormatUint(selection.Resources.MemoryMiB, 10) + "m", "--memory-swap=" + strconv.FormatUint(selection.Resources.MemoryMiB, 10) + "m",
		"--pids-limit=" + strconv.FormatUint(diskInspectionPIDsLimit, 10),
	}
	if selection.scratchVolume != "" {
		args = append(args, "--volume", selection.scratchVolume+":/tmp:nocopy")
	} else {
		args = append(args, "--tmpfs", "/tmp:rw,nosuid,nodev,size="+strconv.FormatUint(selection.Resources.MemoryMiB, 10)+"m,mode=1777")
	}
	args = append(args,
		"--env", "PATH=/usr/bin:/bin", "--env", "HOME=/tmp", "--env", "LC_ALL=C", "--env", "TZ=UTC",
		"--entrypoint=/usr/local/bin/nkd-disk-inspect", image,
		selection.InspectorDigest, selection.CodecDigest, subject.Digest(), strconv.FormatInt(subject.Size(), 10))
	if selection.compositionKind != "" {
		args = append(args, "composition", selection.compositionKind, selection.GuestfishDigest, selection.ScannerDigest, selection.PackageReaderDigest, strconv.FormatUint(selection.MaxFilesystemBytes, 10), selection.consumerPlatform)
	}
	args = append(args, strconv.FormatUint(selection.Resources.CPUMillis, 10), strconv.FormatUint(selection.Resources.MemoryMiB, 10), strconv.FormatUint(diskInspectionPIDsLimit, 10))
	return args
}

const diskInspectionCPUPeriodArg = "--cpu-period=100000"

// Selection admission bounds cpuMillis before either launcher uses it. Integer
// period/quota avoids runtime float conversion truncating an exact grant.
func diskInspectionCPUQuotaArg(cpuMillis uint64) string {
	return "--cpu-quota=" + strconv.FormatUint(cpuMillis*100, 10)
}

// removeDiskInspectionContainer removes only this attempt's immutable container
// ID. Cleanup discovers its unique label rather than deleting by a reusable name.
// The held local daemon is trusted; labels are not cross-process authority.
// A joined client is not proof that the trusted daemon has finished creation.
// After a failed create, reconcile the attempt's label until it appears or the
// bounded cleanup context expires. An empty lookup alone cannot prove cleanup.
func removeDiskInspectionContainer(ctx context.Context, runtime packerContainerRuntime, attempt string, creationUncertain bool) error {
	var stdout, stderr diskInspectionBuffer
	// Retain only the latest bounded diagnostic, not an error chain per poll.
	// A warning cannot prove a failed create will never register with the daemon.
	var diagnosticErr error
	for {
		stdout.Reset()
		stderr.Reset()
		err := runtime.Cleanup(ctx, time.Second, &stdout, &stderr, diskInspectionCommandArgs(runtime.RuntimeName(), "ps", "--all", "--no-trunc",
			"--filter", "label=nvkr.dev/inspection-attempt="+attempt, "--format", "{{.ID}}")...)
		if stderr.Len() != 0 {
			diagnosticErr = e.New(e.CodeVerifyFailed, "disk inspection cleanup discovery returned diagnostics: %s", diskInspectionDiagnostic(stderr.Bytes()))
		}
		if err != nil {
			return errors.Join(diagnosticErr, e.Wrap(e.CodeInternal, err, "finding the owned disk inspection container"))
		}
		ids := strings.Fields(stdout.String())
		if len(ids) == 0 {
			if !creationUncertain {
				return diagnosticErr
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errors.Join(diagnosticErr, e.Wrap(e.CodeInternal, ctx.Err(), "disk inspection creation outcome remains uncertain"))
			case <-timer.C:
				continue
			}
		}
		if len(ids) != 1 || !canonical.IsDigest("sha256:"+ids[0]) {
			return errors.Join(diagnosticErr, e.New(e.CodeVerifyFailed, "disk inspection cleanup has an ambiguous container identity"))
		}
		if err := runtime.Cleanup(ctx, time.Second, io.Discard, io.Discard, diskInspectionCommandArgs(runtime.RuntimeName(), "rm", "--force", ids[0])...); err != nil {
			return errors.Join(diagnosticErr, e.Wrap(e.CodeInternal, err, "removing the owned disk inspection container"))
		}
		return diagnosticErr
	}
}

func lockDiskInspection(ctx context.Context, mu *sync.Mutex) error {
	for !mu.TryLock() {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return e.Wrap(e.CodeTimeout, ctx.Err(), "waiting for inspection runtime")
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		mu.Unlock()
		return e.Wrap(e.CodeTimeout, ctx.Err(), "inspection runtime canceled")
	}
	return nil
}

// Do not embed bytes.Buffer: promoted ReadFrom and WriteString would let
// io.Copy and io.WriteString bypass the bounded Write operation below.
type diskInspectionBuffer struct {
	data  bytes.Buffer
	limit int
}

func (buffer *diskInspectionBuffer) Len() int       { return buffer.data.Len() }
func (buffer *diskInspectionBuffer) Bytes() []byte  { return buffer.data.Bytes() }
func (buffer *diskInspectionBuffer) String() string { return buffer.data.String() }
func (buffer *diskInspectionBuffer) Reset()         { buffer.data.Reset() }

func (buffer *diskInspectionBuffer) Write(raw []byte) (int, error) {
	limit := buffer.limit
	if limit == 0 {
		limit = MaxDiskInspectionReportBytes
	}
	if len(raw) > limit-buffer.Len() {
		return 0, e.New(e.CodeEnvelopeDecode, "disk inspection output exceeds its bound")
	}
	return buffer.data.Write(raw)
}

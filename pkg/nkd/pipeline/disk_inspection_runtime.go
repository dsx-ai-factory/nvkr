package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/google/go-containerregistry/pkg/name"
)

// DiskInspectionSelection describes an already-local inspection closure. Pins
// are expectations, not authority: admission checks image bytes. The selected
// local runtime and daemon are trusted, held and rechecked for freshness, not
// compared with an independently supplied runtime pin. Workspace remains
// caller-owned and must remain open until Close.
// This development capability does not grant publication or qualification.
type DiskInspectionSelection struct {
	RuntimeExecutable string
	Image             string
	Platform          string
	InspectorDigest   string
	CodecDigest       string
	Resources         execution.ResourceRequest
	TimeoutSeconds    uint64
	Workspace         *os.File
}

// DiskInspectionContainer retains the exact runtime executable, daemon identity
// and complete immutable image closure. No caller command, mount or device can
// be passed through it. The daemon is trusted, as in the local Packer adapter.
type DiskInspectionContainer struct {
	mu        sync.Mutex
	runtime   *heldRuntimeExecutable
	selection DiskInspectionSelection
	imageID   string
}

const diskInspectionApplianceMiB uint64 = 2048

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
	held.identity, err = observeHeldRuntimeIdentity(ctx, held)
	if err != nil {
		_ = held.capability.Close()
		return nil, runtimeCommandError(ctx, err, "", e.CodeEngineCapability, "admitting disk inspection runtime identity")
	}
	container := &DiskInspectionContainer{runtime: held, selection: selection}
	if err := container.observeBinding(ctx); err != nil {
		_ = held.capability.Close()
		return nil, err
	}
	return container, nil
}

func validateDiskInspectionSelection(selection DiskInspectionSelection) error {
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
	if selection.Workspace == nil || selection.Resources.CPUMillis == 0 || selection.Resources.MemoryMiB < diskInspectionApplianceMiB || selection.TimeoutSeconds == 0 || selection.TimeoutSeconds > 600 {
		return e.New(e.CodeEngineCapability, "disk inspection requires a held workspace, positive CPU, at least 2 GiB memory and a timeout of at most ten minutes")
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
	node := ir.Node{ID: "disk-inspection", Toolchain: container.selection.Image, ToolchainPlatform: container.selection.Platform}
	if err := observeLocalPackerImage(ctx, adapter, node); err != nil {
		return err
	}
	raw, err := heldExecutionOutput(ctx, container.runtime, "image", "inspect", "--format", "{{json .}}", container.selection.Image)
	if err != nil {
		return runtimeCommandError(ctx, err, "", e.CodeEngineCapability, "observing inspection image identity")
	}
	id, err := decodeDiskInspectionImageID(raw)
	if err != nil {
		return err
	}
	if container.imageID != "" && container.imageID != id {
		return e.New(e.CodeVerifyFailed, "disk inspection image changed after admission")
	}
	container.imageID = id
	return nil
}

// BindingContext checks that the capability remains open and unchanged. Image
// config identity binds its immutable rootfs diff IDs and therefore the complete
// inspector, loader, libraries, emulator, appliance and kernel, not just two tools.
func (container *DiskInspectionContainer) BindingContext(ctx context.Context) (string, string, string, error) {
	if err := requireRunContext(ctx, "disk inspection runtime binding"); err != nil {
		return "", "", "", err
	}
	if container == nil {
		return "", "", "", e.New(e.CodeEngineCapability, "disk inspection runtime is absent")
	}
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

// Close revokes this capability after active inspection and cleanup have joined.
// Cancel an active inspection's context first for immediate shutdown.
func (container *DiskInspectionContainer) Close() error {
	if container == nil {
		return nil
	}
	container.mu.Lock()
	defer container.mu.Unlock()
	if container.runtime == nil {
		return nil
	}
	err := container.runtime.capability.Close()
	container.runtime = nil
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "closing disk inspection runtime")
	}
	return nil
}

// AdmitSubject checks capacity before the factory creates its anonymous held
// snapshot. In addition to the container's two copies, this budgets that extra
// factory-held copy. The original producer inode remains disk-backed. Admission
// compares that combined budget with host availability and the NKD process's
// cgroup headroom. It does not observe the daemon's separate cgroup/slice or
// reserve capacity there; differing scopes may reject an otherwise usable
// daemon or permit a container later killed by its enclosing limit.
func (container *DiskInspectionContainer) AdmitSubject(ctx context.Context, size int64) error {
	if err := requireRunContext(ctx, "inspection subject admission"); err != nil {
		return err
	}
	if container == nil || size <= 0 || size > 8<<30 {
		return e.New(e.CodeEngineCapability, "inspection subject exceeds its supported byte budget")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := lockDiskInspection(ctx, &container.mu); err != nil {
		return err
	}
	defer container.mu.Unlock()
	if container.runtime == nil {
		return e.New(e.CodeEngineCapability, "disk inspection runtime is closed")
	}
	facts, err := execution.ObserveLocalHost(ctx, container.selection.Workspace)
	if err != nil {
		return err
	}
	return validateDiskInspectionResources(container.selection, facts, size, true)
}

// DiskInspectionObservation can be issued only by this adapter after the fixed
// command has completed and the held subject/runtime have been revalidated.
// Description methods do not mint another observation.
type DiskInspectionObservation struct {
	report                           []byte
	subject, image, inspector, codec string
	size                             int64
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
	if container == nil || subject == nil || subject.Size() <= 0 || subject.Size() > 8<<30 {
		return nil, e.New(e.CodeEngineCapability, "container inspection requires one bounded held disk")
	}
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
	report, err := runDiskInspectionContainer(ctx, heldPackerContainerRuntime{runtime: container.runtime}, container.selection, container.imageID, subject)
	if err != nil {
		return nil, err
	}
	if err := checkDiskInspectionSubject(ctx, subject, "rechecking inspected disk"); err != nil {
		return nil, err
	}
	if err := container.observeBinding(ctx); err != nil {
		return nil, err
	}
	return &DiskInspectionObservation{report: report, subject: subject.Digest(), size: subject.Size(), image: container.imageID, inspector: container.selection.InspectorDigest, codec: container.selection.CodecDigest}, nil
}

// runDiskInspectionContainer owns subprocess streaming and cleanup, but issues
// no observation. Its caller must revalidate the held subject and runtime after
// this operation returns. The seam is the existing concrete-runtime adapter.
func runDiskInspectionContainer(ctx context.Context, runtime packerContainerRuntime, selection DiskInspectionSelection, imageID string, subject *heldfile.File) (raw []byte, returnErr error) {
	attempt, err := packerAttemptID()
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "allocating disk inspection attempt identity")
	}
	name := "nkd-inspect-" + attempt
	creationUncertain := true
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		err := removeDiskInspectionContainer(cleanupCtx, runtime, attempt, creationUncertain)
		if err != nil {
			raw = nil
			returnErr = errors.Join(returnErr, e.Wrap(e.CodeInternal, err, "removing inspection container"))
		}
	}()
	var createDiagnostic, diagnostic, report diskInspectionBuffer
	if err := runtime.Run(ctx, nil, io.Discard, &createDiagnostic, diskInspectionCreateArgs(selection, imageID, name, attempt, subject)...); err != nil {
		return nil, runtimeCommandError(ctx, err, diskInspectionDiagnostic(createDiagnostic.Bytes()), e.CodeEngineCapability, "creating disk inspection container")
	}
	creationUncertain = false
	// A successful create can still warn that a requested sandbox limit was
	// ignored. Reject that capability gap before streaming bytes or starting QEMU.
	if createDiagnostic.Len() != 0 {
		return nil, e.New(e.CodeEngineCapability, "disk inspection creation returned diagnostics: %s", diskInspectionDiagnostic(createDiagnostic.Bytes()))
	}
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { _, err := subject.WriteToContext(ctx, writer); _ = writer.CloseWithError(err); done <- err }()
	err = runtime.Run(ctx, reader, &report, &diagnostic, "start", "--attach", "--interactive", name)
	_ = reader.Close()
	copyErr := <-done
	if err := errors.Join(err, copyErr); err != nil {
		return nil, runtimeCommandError(ctx, err, diskInspectionDiagnostic(diagnostic.Bytes()), e.CodeEnvelopeDecode, "inspecting held disk")
	}
	if diagnostic.Len() != 0 || report.Len() == 0 {
		return nil, e.New(e.CodeEnvelopeDecode, "disk inspection returned diagnostics or no report")
	}
	return bytes.Clone(report.Bytes()), nil
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
	facts, err := execution.ObserveLocalHost(ctx, container.selection.Workspace)
	if err != nil {
		return err
	}
	return validateDiskInspectionResources(container.selection, facts, size, false)
}

func validateDiskInspectionResources(selection DiskInspectionSelection, facts execution.ObservedHostFacts, size int64, beforeSnapshot bool) error {
	request := selection.Resources
	snapshotMiB := uint64((size + (1 << 20) - 1) >> 20)
	needMemory := 2*snapshotMiB + diskInspectionApplianceMiB
	hostMemory := request.MemoryMiB
	if beforeSnapshot {
		if hostMemory > math.MaxUint64-snapshotMiB {
			return e.New(e.CodeEngineCapability, "inspection memory request exceeds its supported budget")
		}
		// Compare the full declared grant plus external snapshot with the
		// caller-side observation, not merely their independent maxima. This
		// conservative check does not authenticate the daemon's cgroup budget.
		hostMemory += snapshotMiB
	}
	if request.MemoryMiB < needMemory || facts.Platform != execution.HostPlatform(selection.Platform) || facts.Resources.CPUMillis < request.CPUMillis || facts.Resources.MemoryMiB < hostMemory || facts.Resources.EphemeralStorageMiB < request.EphemeralStorageMiB {
		return e.New(e.CodeEngineCapability, "inspection host lacks declared CPU, memory or workspace capacity (RAM must cover two disk copies plus appliance)")
	}
	return nil
}

func diskInspectionCreateArgs(selection DiskInspectionSelection, image, containerName, attempt string, subject *heldfile.File) []string {
	return []string{"create", "--name", containerName, "--pull=never", "--platform", selection.Platform,
		"--label=nkd.dev/inspection-attempt=" + attempt,
		"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--user=65534:65534", "--hostname=nkd-inspector", "--no-healthcheck", "--init", "--interactive",
		"--cpus=" + strconv.FormatFloat(float64(selection.Resources.CPUMillis)/1000, 'f', 3, 64),
		"--memory=" + strconv.FormatUint(selection.Resources.MemoryMiB, 10) + "m", "--memory-swap=" + strconv.FormatUint(selection.Resources.MemoryMiB, 10) + "m",
		"--pids-limit=256", "--tmpfs", "/tmp:rw,nosuid,nodev,size=" + strconv.FormatUint(selection.Resources.MemoryMiB, 10) + "m,mode=1777",
		"--env", "PATH=/usr/bin:/bin", "--env", "HOME=/tmp", "--env", "LC_ALL=C", "--env", "TZ=UTC",
		"--entrypoint=/usr/local/bin/nkd-disk-inspect", image,
		selection.InspectorDigest, selection.CodecDigest, subject.Digest(), strconv.FormatInt(subject.Size(), 10)}
}

// removeDiskInspectionContainer removes only this attempt's immutable container
// ID. Cleanup discovers its unique label rather than deleting by a reusable name.
// The held local daemon is trusted; labels are not cross-process authority.
// A joined client is not proof that the trusted daemon has finished creation.
// After a failed create, reconcile the attempt's label until it appears or the
// bounded cleanup context expires. An empty lookup alone cannot prove cleanup.
func removeDiskInspectionContainer(ctx context.Context, runtime packerContainerRuntime, attempt string, creationUncertain bool) error {
	var stdout, stderr diskInspectionBuffer
	for {
		stdout.Reset()
		stderr.Reset()
		if err := runtime.Cleanup(ctx, time.Second, &stdout, &stderr, "ps", "--all", "--no-trunc",
			"--filter", "label=nkd.dev/inspection-attempt="+attempt, "--format", "{{.ID}}"); err != nil {
			return e.Wrap(e.CodeInternal, err, "finding the owned disk inspection container")
		}
		if stderr.Len() != 0 {
			return e.New(e.CodeVerifyFailed, "disk inspection cleanup discovery returned diagnostics")
		}
		ids := strings.Fields(stdout.String())
		if len(ids) == 0 {
			if !creationUncertain {
				return nil
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return e.Wrap(e.CodeInternal, ctx.Err(), "disk inspection creation outcome remains uncertain")
			case <-timer.C:
				continue
			}
		}
		if len(ids) != 1 || !canonical.IsDigest("sha256:"+ids[0]) {
			return e.New(e.CodeVerifyFailed, "disk inspection cleanup has an ambiguous container identity")
		}
		if err := runtime.Cleanup(ctx, time.Second, io.Discard, io.Discard, "rm", "--force", ids[0]); err != nil {
			return e.Wrap(e.CodeInternal, err, "removing the owned disk inspection container")
		}
		return nil
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
type diskInspectionBuffer struct{ data bytes.Buffer }

func (buffer *diskInspectionBuffer) Len() int       { return buffer.data.Len() }
func (buffer *diskInspectionBuffer) Bytes() []byte  { return buffer.data.Bytes() }
func (buffer *diskInspectionBuffer) String() string { return buffer.data.String() }
func (buffer *diskInspectionBuffer) Reset()         { buffer.data.Reset() }

func (buffer *diskInspectionBuffer) Write(raw []byte) (int, error) {
	if buffer.Len()+len(raw) > 8<<20 {
		return 0, e.New(e.CodeEnvelopeDecode, "disk inspection output exceeds its bound")
	}
	return buffer.data.Write(raw)
}

func decodeDiskInspectionImageID(raw []byte) (string, error) {
	var image struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &image); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "decoding inspection image identity")
	}
	id := image.ID
	if !canonical.IsDigest(id) && canonical.IsDigest("sha256:"+id) {
		id = "sha256:" + id
	}
	if !canonical.IsDigest(id) {
		return "", e.New(e.CodeEngineCapability, "runtime returned no exact inspection image identity")
	}
	return id, nil
}

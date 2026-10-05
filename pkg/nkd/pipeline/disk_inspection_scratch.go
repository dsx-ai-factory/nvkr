package pipeline

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// diskInspectionScratch owns one attempt-private, disk-backed daemon volume.
// The held daemon/image are trusted. Available space is observed, not reserved
// or enforced as a hard quota. The caller bounds generated files and joins the
// consumer container before Close; no source, credential or host path is mounted.
type diskInspectionScratch struct{ state *diskInspectionScratchState }

type diskInspectionScratchState struct {
	mu      sync.Mutex
	runtime packerContainerRuntime
	volume  string
	attempt string
}

const diskInspectionScratchPrepare = `set -eu
test -d /tmp
test -z "$(ls -A /tmp)"
test "$(stat -f -c %T /tmp)" != tmpfs
chmod 0700 /tmp
chown 65534:65534 /tmp
stat -f -c '%a %S' /tmp
`

// openDiskInspectionScratch runs only the fixed capacity/preparation operation
// from the already-admitted image. It completes before any subject streaming.
func openDiskInspectionScratch(ctx context.Context, runtime packerContainerRuntime, selection DiskInspectionSelection, imageID, attempt string) (result *diskInspectionScratch, returnErr error) {
	if err := requireRunContext(ctx, "inspection scratch admission"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "inspection scratch canceled")
	}
	if err := validateDiskInspectionSelection(selection); err != nil {
		return nil, err
	}
	if runtime == nil || (runtime.RuntimeName() != "docker" && runtime.RuntimeName() != "podman") ||
		selection.MaxFilesystemBytes == 0 || selection.Resources.EphemeralStorageMiB == 0 ||
		!canonical.IsDigest(imageID) || len(attempt) != 24 {
		return nil, e.New(e.CodeEngineCapability, "inspection scratch requires its admitted image, runtime, storage request and private attempt")
	}
	if _, err := hex.DecodeString(attempt); err != nil || strings.ToLower(attempt) != attempt {
		return nil, e.New(e.CodeEngineCapability, "inspection scratch attempt is malformed")
	}
	volume := "nkd-inspect-" + attempt + "-scratch"
	scratch := &diskInspectionScratch{state: &diskInspectionScratchState{runtime: runtime, volume: volume, attempt: attempt}}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, scratch.Close(ctx))
			result = nil
		}
	}()
	adapter := diskInspectionScratchRuntime{runtime}
	if err := createPackerVolume(ctx, adapter, volume, []string{"nkd.dev/inspection-scratch-attempt=" + attempt}); err != nil {
		return nil, err
	}
	if err := scratch.state.verifyVolume(ctx); err != nil {
		return nil, err
	}
	name := "nkd-inspect-" + attempt + "-scratch-prepare"
	uncertain := true
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		returnErr = errors.Join(returnErr, removeDiskInspectionContainer(cleanupCtx, runtime, attempt, uncertain))
	}()
	args := []string{"create", "--name", name, "--pull=never", "--platform", selection.Platform,
		"--label=nkd.dev/inspection-attempt=" + attempt, "--network=none", "--log-driver=none", "--read-only",
		"--cap-drop=ALL", "--cap-add=CHOWN", "--security-opt=no-new-privileges", "--user=0:0",
		"--no-healthcheck", "--init", "--pids-limit=32", "--memory=64m", "--memory-swap=64m",
		"--cpus=" + strconv.FormatFloat(float64(selection.Resources.CPUMillis)/1000, 'f', 3, 64),
		"--volume", volume + ":/tmp:nocopy", "--env", "PATH=/usr/bin:/bin", "--env", "LC_ALL=C",
		"--entrypoint=/bin/sh", imageID, "-ec", diskInspectionScratchPrepare}
	var output, diagnostic diskInspectionBuffer
	if err := adapter.Run(ctx, nil, &output, &diagnostic, args...); err != nil {
		return nil, runtimeCommandError(ctx, err, diskInspectionDiagnostic(diagnostic.Bytes()), e.CodeEngineCapability, "creating inspection scratch preparer")
	}
	uncertain = false
	if diagnostic.Len() != 0 || !canonical.IsDigest("sha256:"+strings.TrimSpace(output.String())) {
		return nil, e.New(e.CodeEngineCapability, "inspection scratch preparation returned no exact container identity or unsupported limits")
	}
	output.Reset()
	if err := adapter.Run(ctx, nil, &output, &diagnostic, "start", "--attach", name); err != nil {
		return nil, runtimeCommandError(ctx, err, diskInspectionDiagnostic(diagnostic.Bytes()), e.CodeEngineCapability, "preparing inspection scratch")
	}
	if diagnostic.Len() != 0 {
		return nil, e.New(e.CodeEngineCapability, "inspection scratch preparation returned diagnostics")
	}
	if err := validateDiskInspectionScratchCapacity(output.Bytes(), selection.Resources.EphemeralStorageMiB); err != nil {
		return nil, err
	}
	return scratch, nil
}

func (scratch *diskInspectionScratch) MountArgs() []string {
	if scratch == nil || scratch.state == nil {
		return nil
	}
	scratch.state.mu.Lock()
	defer scratch.state.mu.Unlock()
	if scratch.state.volume == "" {
		return nil
	}
	return []string{"--volume", scratch.state.volume + ":/tmp:nocopy"}
}

// Close is idempotent across copies. Its bounded cleanup runs independently of
// an already-canceled scan; a failed removal revokes success, never the subject.
func (scratch *diskInspectionScratch) Close(_ context.Context) error {
	if scratch == nil || scratch.state == nil {
		return nil
	}
	state := scratch.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.volume == "" {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := state.verifyVolume(cleanupCtx); err != nil {
		return err
	}
	var diagnostic diskInspectionBuffer
	if err := state.runtime.Cleanup(cleanupCtx, time.Second, io.Discard, &diagnostic,
		diskInspectionCommandArgs(state.runtime.RuntimeName(), "volume", "rm", "--force", state.volume)...); err != nil {
		return e.Wrap(e.CodeInternal, err, "removing owned inspection scratch")
	}
	if diagnostic.Len() != 0 {
		return e.New(e.CodeInternal, "inspection scratch removal returned diagnostics")
	}
	state.volume = ""
	return nil
}

func (state *diskInspectionScratchState) verifyVolume(ctx context.Context) error {
	var output, diagnostic diskInspectionBuffer
	if err := state.runtime.Run(ctx, nil, &output, &diagnostic,
		diskInspectionCommandArgs(state.runtime.RuntimeName(), "volume", "inspect", "--format", "{{json .}}", state.volume)...); err != nil {
		return runtimeCommandError(ctx, err, diskInspectionDiagnostic(diagnostic.Bytes()), e.CodeVerifyFailed, "authenticating inspection scratch volume")
	}
	var observed struct {
		Name    string
		Driver  string
		Options map[string]string
		Labels  map[string]string
	}
	if err := json.Unmarshal(output.Bytes(), &observed); err != nil || diagnostic.Len() != 0 ||
		observed.Name != state.volume || observed.Driver != "local" || len(observed.Options) != 0 ||
		observed.Labels["nkd.dev/inspection-scratch-attempt"] != state.attempt {
		return e.New(e.CodeVerifyFailed, "inspection scratch volume identity, driver or backing changed")
	}
	return nil
}

func validateDiskInspectionScratchCapacity(raw []byte, requestedMiB uint64) error {
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || requestedMiB == 0 {
		return e.New(e.CodeEngineCapability, "inspection scratch returned no bounded available-space observation")
	}
	blocks, blocksErr := strconv.ParseUint(fields[0], 10, 64)
	blockSize, sizeErr := strconv.ParseUint(fields[1], 10, 64)
	if blocksErr != nil || sizeErr != nil || blockSize == 0 || blocks > math.MaxUint64/blockSize ||
		(blocks*blockSize)>>20 < requestedMiB {
		return e.New(e.CodeEngineCapability, "inspection scratch lacks its declared disk headroom")
	}
	return nil
}

// Reuse the Packer volume owner while preserving inspection's Podman automatic
// host-mount suppression on every preparer operation.
type diskInspectionScratchRuntime struct{ packerContainerRuntime }

func (runtime diskInspectionScratchRuntime) Run(ctx context.Context, input io.Reader, output, diagnostic io.Writer, args ...string) error {
	return runtime.packerContainerRuntime.Run(ctx, input, output, diagnostic, diskInspectionCommandArgs(runtime.RuntimeName(), args...)...)
}

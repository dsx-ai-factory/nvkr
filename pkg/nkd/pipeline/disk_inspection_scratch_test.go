package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

type scratchTestRuntime struct {
	packerContainerRuntime
	name, volume, attempt, capacity, volumeOptions string
	commands                                       [][]string
	warning, cleanupFailure                        bool
	removed                                        int
}

// This opt-in probe proves the actual private-volume preparation and cleanup,
// not libguestfs scanning, a GitHub runner, or provider resource allocation.
func TestDiskInspectionScratchNativeVolume(t *testing.T) {
	image := os.Getenv("NKD_TEST_INSPECTION_SCRATCH_IMAGE")
	if image == "" {
		t.Skip("requires a native already-local pinned toolchain image")
	}
	if goruntime.GOOS != "linux" || os.Getuid() == 0 {
		t.Fatal("native scratch proof requires a non-root Linux Podman owner")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	path, err := exec.LookPath("podman")
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewNodeSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	held, err := session.state.admitPackerRuntime(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	selection := testDiskInspectionSelection(t)
	selection.RuntimeExecutable = path
	selection.Platform = "linux/" + goruntime.GOARCH
	selection.Image = image
	selection.MaxFilesystemBytes = 1 << 20
	selection.ScratchStorage = "private-volume"
	selection.Resources.EphemeralStorageMiB = 32
	selection.GuestfishDigest = selection.InspectorDigest
	selection.ScannerDigest = selection.InspectorDigest
	selection.PackageReaderDigest = selection.InspectorDigest
	attempt, err := packerAttemptID()
	if err != nil {
		t.Fatal(err)
	}
	scratch, err := openDiskInspectionScratch(ctx, heldPackerContainerRuntime{runtime: held}, selection, image, attempt)
	if err != nil {
		t.Fatal(err)
	}
	volume := scratch.state.volume
	if err := scratch.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic diskInspectionBuffer
	if err := held.runIO(ctx, nil, &output, &diagnostic, "volume", "exists", volume); !scratchVolumeAbsent(err) {
		t.Fatalf("private volume absence was not proven after checked cleanup: %v", err)
	}
	t.Logf("actual scratch creation, fixed capacity/preparation and cleanup passed: platform=%s uid=%d image=%s", selection.Platform, os.Getuid(), image)
}

// Podman's owning command defines 1 as absent, 0 as present. Runtime or
// permission failures do not establish cleanup, even when they are nonzero.
func scratchVolumeAbsent(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

func TestScratchVolumeAbsenceRequiresExactExitStatus(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		absent        bool
	}{
		{name: "present", command: "exit 0"},
		{name: "absent", command: "exit 1", absent: true},
		{name: "runtime failure", command: "exit 125"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := exec.CommandContext(t.Context(), "sh", "-c", tc.command).Run()
			if got := scratchVolumeAbsent(err); got != tc.absent {
				t.Fatalf("absence=%v, want %v: %v", got, tc.absent, err)
			}
		})
	}
	if scratchVolumeAbsent(errors.New("runtime unavailable")) {
		t.Fatal("unclassified runtime error proved absence")
	}
}

func (runtime *scratchTestRuntime) RuntimeName() string { return runtime.name }
func (runtime *scratchTestRuntime) Run(_ context.Context, _ io.Reader, output, diagnostic io.Writer, args ...string) error {
	runtime.commands = append(runtime.commands, slices.Clone(args))
	if args[0] == "--default-mounts-file=/dev/null" {
		args = args[1:]
	}
	switch args[0] {
	case "volume":
		if args[1] == "create" {
			runtime.volume = args[len(args)-1]
			runtime.attempt = strings.TrimPrefix(args[3], "nkd.dev/inspection-scratch-attempt=")
			_, _ = io.WriteString(output, runtime.volume)
		} else {
			options := runtime.volumeOptions
			if options == "" {
				options = "null"
			}
			_, _ = fmt.Fprintf(output, `{"Name":%q,"Driver":"local","Options":%s,"Labels":{"nkd.dev/inspection-scratch-attempt":%q}}`, runtime.volume, options, runtime.attempt)
		}
	case "create":
		_, _ = io.WriteString(output, strings.Repeat("a", 64))
		if runtime.warning {
			_, _ = io.WriteString(diagnostic, "ignored memory limit")
		}
	case "start":
		_, _ = io.WriteString(output, runtime.capacity)
	}
	return nil
}

func (runtime *scratchTestRuntime) Cleanup(_ context.Context, _ time.Duration, output, _ io.Writer, args ...string) error {
	runtime.commands = append(runtime.commands, slices.Clone(args))
	if args[0] == "--default-mounts-file=/dev/null" {
		args = args[1:]
	}
	if args[0] == "ps" {
		_, _ = io.WriteString(output, strings.Repeat("a", 64))
	}
	if args[0] == "volume" {
		runtime.removed++
	}
	if runtime.cleanupFailure {
		return errors.New("cleanup failure")
	}
	return nil
}

func TestDiskInspectionScratchOwnsPrivateDiskVolume(t *testing.T) {
	for _, tc := range []struct {
		name, capacity, volumeOptions     string
		warning, canceled, cleanupFailure bool
		want                              e.Code
	}{
		{name: "complete", capacity: "8192 4096"},
		{name: "low disk headroom", capacity: "1 4096", want: e.CodeEngineCapability},
		{name: "malformed capacity", capacity: "123", want: e.CodeEngineCapability},
		{name: "overflow capacity", capacity: "18446744073709551615 4096", want: e.CodeEngineCapability},
		{name: "host bind volume refused", volumeOptions: `{"device":"/host","o":"bind"}`, want: e.CodeVerifyFailed},
		{name: "ignored sandbox limit", warning: true, want: e.CodeEngineCapability},
		{name: "canceled before work", canceled: true, want: e.CodeTimeout},
		{name: "cleanup revokes result", capacity: "8192 4096", cleanupFailure: true, want: e.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := testDiskInspectionSelection(t)
			selection.Resources.CPUMillis = 2300
			selection.MaxFilesystemBytes = 1 << 20
			selection.ScratchStorage = "private-volume"
			selection.Resources.EphemeralStorageMiB = 4
			selection.GuestfishDigest = selection.InspectorDigest
			selection.ScannerDigest = selection.InspectorDigest
			selection.PackageReaderDigest = selection.InspectorDigest
			runtime := &scratchTestRuntime{name: "podman", capacity: tc.capacity, volumeOptions: tc.volumeOptions, warning: tc.warning, cleanupFailure: tc.cleanupFailure}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if tc.canceled {
				cancel()
			}
			scratch, err := openDiskInspectionScratch(ctx, runtime, selection, "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 24))
			if code(err) != tc.want {
				t.Fatalf("scratch=%v error=%v want=%s", scratch, err, tc.want)
			}
			if tc.want != "" {
				if scratch != nil {
					t.Fatal("failed preparation returned scratch")
				}
				if tc.canceled && len(runtime.commands) != 0 {
					t.Fatal("canceled admission touched runtime")
				}
				return
			}
			copy := *scratch
			if !slices.Equal(scratch.MountArgs(), []string{"--volume", runtime.volume + ":/tmp:nocopy"}) {
				t.Fatalf("mounts=%v", scratch.MountArgs())
			}
			if err := scratch.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if err := copy.Close(ctx); err != nil || len(copy.MountArgs()) != 0 || runtime.removed != 1 {
				t.Fatalf("copy cleanup=%v mounts=%v removed=%d", err, copy.MountArgs(), runtime.removed)
			}
			for _, args := range runtime.commands {
				if args[0] != "--default-mounts-file=/dev/null" {
					t.Fatalf("automatic mounts not suppressed: %v", args)
				}
				if len(args) > 1 && args[1] == "create" {
					for _, required := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--cap-add=CHOWN", "--security-opt=no-new-privileges", "--user=0:0", "--cpu-period=100000", "--cpu-quota=230000"} {
						if !slices.Contains(args, required) {
							t.Fatalf("missing %s in %v", required, args)
						}
					}
				}
			}
		})
	}
}

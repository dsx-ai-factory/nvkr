package pipeline

import (
	"context"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

func testDiskInspectionSelection(t *testing.T) DiskInspectionSelection {
	t.Helper()
	workspace, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	return DiskInspectionSelection{RuntimeExecutable: "docker", Image: "sha256:" + strings.Repeat("1", 64), Platform: "linux/amd64",
		InspectorDigest: "sha256:" + strings.Repeat("2", 64), CodecDigest: "sha256:" + strings.Repeat("3", 64),
		Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 16384, EphemeralStorageMiB: 1024}, TimeoutSeconds: 600, Workspace: workspace}
}

func TestDiskInspectionSelectionFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		edit func(*DiskInspectionSelection)
		want e.Code
	}{
		{name: "amd64"}, {name: "arm64", edit: func(s *DiskInspectionSelection) { s.Platform = "linux/arm64" }},
		{name: "OCI manifest", edit: func(s *DiskInspectionSelection) {
			s.Image = "example.com/nkd/inspector@sha256:" + strings.Repeat("4", 64)
		}},
		{name: "mutable tag", edit: func(s *DiskInspectionSelection) { s.Image = "example.com/nkd/inspector:latest" }, want: e.CodeUnpinnedInput},
		{name: "wrong runtime", edit: func(s *DiskInspectionSelection) { s.RuntimeExecutable = "sh" }, want: e.CodeEngineCapability},
		{name: "unsupported arch", edit: func(s *DiskInspectionSelection) { s.Platform = "linux/riscv64" }, want: e.CodeEngineCapability},
		{name: "missing workspace", edit: func(s *DiskInspectionSelection) { s.Workspace = nil }, want: e.CodeEngineCapability},
		{name: "missing inspector", edit: func(s *DiskInspectionSelection) { s.InspectorDigest = "" }, want: e.CodeUnpinnedInput},
		{name: "missing codec", edit: func(s *DiskInspectionSelection) { s.CodecDigest = "" }, want: e.CodeUnpinnedInput},
		{name: "no cpu", edit: func(s *DiskInspectionSelection) { s.Resources.CPUMillis = 0 }, want: e.CodeEngineCapability},
		{name: "no appliance memory", edit: func(s *DiskInspectionSelection) { s.Resources.MemoryMiB = 1024 }, want: e.CodeEngineCapability},
		{name: "no timeout", edit: func(s *DiskInspectionSelection) { s.TimeoutSeconds = 0 }, want: e.CodeEngineCapability},
		{name: "unbounded timeout", edit: func(s *DiskInspectionSelection) { s.TimeoutSeconds = 601 }, want: e.CodeEngineCapability},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selection := testDiskInspectionSelection(t)
			if tt.edit != nil {
				tt.edit(&selection)
			}
			if got := code(validateDiskInspectionSelection(selection)); got != tt.want {
				t.Fatalf("error code %s, want %s", got, tt.want)
			}
		})
	}
}

func TestDiskInspectionContainerArgumentsEnforceContainment(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			selection := testDiskInspectionSelection(t)
			selection.Platform = "linux/" + arch
			subject, err := heldfile.FromBytesContext(t.Context(), []byte("held subject"), heldfile.Options{Label: "fixture", MaxBytes: 128})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			args := diskInspectionCreateArgs(selection, selection.Image, "owned-attempt", "attempt-token", subject)
			for _, required := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=65534:65534", "--init", "--pids-limit=256", "--pull=never", "--no-healthcheck", "--memory=16384m", "--memory-swap=16384m", "/tmp:rw,nosuid,nodev,size=16384m,mode=1777", selection.Platform, "--entrypoint=/usr/local/bin/nkd-disk-inspect", subject.Digest()} {
				if !slices.Contains(args, required) {
					t.Fatalf("missing containment argument %q", required)
				}
			}
			if !slices.Contains(args, "--label=nkd.dev/inspection-attempt=attempt-token") {
				t.Fatal("missing cleanup ownership label")
			}
			for _, arg := range args {
				for _, forbidden := range []string{"--device", "--mount", "--volume", "--privileged", "--cap-add", "--env-file", "--pid=host", "--user=0"} {
					if strings.HasPrefix(arg, forbidden) {
						t.Fatalf("unexpected authority %q", arg)
					}
				}
			}
		})
	}
}

type diskInspectionCleanupRuntime struct {
	packerContainerRuntime
	listed   string
	commands [][]string
}

func (runtime *diskInspectionCleanupRuntime) Cleanup(_ context.Context, _ time.Duration,
	stdout, _ io.Writer, args ...string,
) error {
	runtime.commands = append(runtime.commands, slices.Clone(args))
	if args[0] == "ps" {
		_, err := io.WriteString(stdout, runtime.listed)
		return err
	}
	return nil
}

func TestDiskInspectionCleanupRemovesOnlyOwnedIdentity(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tt := range []struct {
		name, listed string
		wantRemove   bool
		wantCode     e.Code
	}{
		{name: "failed create collision leaves sibling untouched"},
		{name: "canceled create cleans its owned container", listed: id + "\n", wantRemove: true},
		{name: "invalid identity", listed: "nkd-inspect-sibling\n", wantCode: e.CodeVerifyFailed},
		{name: "short identity", listed: id[:12] + "\n", wantCode: e.CodeVerifyFailed},
		{name: "ambiguous ownership", listed: id + "\n" + strings.Repeat("b", 64) + "\n", wantCode: e.CodeVerifyFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runtime := &diskInspectionCleanupRuntime{listed: tt.listed}
			if err := removeDiskInspectionContainer(t.Context(), runtime, "attempt-token"); code(err) != tt.wantCode {
				t.Fatalf("cleanup error %v, want %s", err, tt.wantCode)
			}
			wantLookup := []string{"ps", "--all", "--no-trunc", "--filter", "label=nkd.dev/inspection-attempt=attempt-token", "--format", "{{.ID}}"}
			if !slices.Equal(runtime.commands[0], wantLookup) {
				t.Fatalf("cleanup does not discover owned IDs: %v", runtime.commands)
			}
			if tt.wantRemove {
				if len(runtime.commands) != 2 || !slices.Equal(runtime.commands[1], []string{"rm", "--force", id}) {
					t.Fatalf("cleanup did not remove the exact owned ID: %v", runtime.commands)
				}
			} else if len(runtime.commands) != 1 {
				t.Fatalf("cleanup removed an unowned or ambiguous container: %v", runtime.commands)
			}
		})
	}
}

func TestDiskInspectionRuntimeRevocationAndCancellation(t *testing.T) {
	container := &DiskInspectionContainer{}
	if _, _, _, err := container.BindingContext(t.Context()); code(err) != e.CodeEngineCapability {
		t.Fatalf("closed binding: %v", err)
	}
	if err := container.AdmitSubject(t.Context(), 128); code(err) != e.CodeEngineCapability {
		t.Fatalf("closed admission: %v", err)
	}
	if err := container.Close(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := lockDiskInspection(ctx, &mu); code(err) != e.CodeTimeout {
		t.Fatalf("canceled lock: %v", err)
	}
}

func TestDiskInspectionSubjectCancellationKeepsTimeoutCode(t *testing.T) {
	subject, err := heldfile.FromBytesContext(t.Context(), []byte("held subject"), heldfile.Options{Label: "fixture", MaxBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	for _, phase := range []string{"checking inspection subject", "rechecking inspected disk"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := checkDiskInspectionSubject(ctx, subject, phase); code(err) != e.CodeTimeout {
				t.Fatalf("canceled verification has error %v, want timeout", err)
			}
		})
	}
	if err := subject.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkDiskInspectionSubject(t.Context(), subject, "checking inspection subject"); code(err) != e.CodeVerifyFailed {
		t.Fatalf("closed subject has error %v, want verify-failed", err)
	}
}

func TestDiskInspectionDeadlineIncludesQueuedWait(t *testing.T) {
	subject, err := heldfile.FromBytesContext(t.Context(), []byte("held"), heldfile.Options{Label: "queued-subject", MaxBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	container := &DiskInspectionContainer{selection: testDiskInspectionSelection(t)}
	container.selection.TimeoutSeconds = 1
	container.mu.Lock()
	defer container.mu.Unlock()
	// No caller deadline: the operation's own configured timeout must cover
	// queue admission, rather than starting only after the lock is obtained.
	started := time.Now()
	observation, err := container.InspectHeld(t.Context(), subject)
	if observation != nil || code(err) != e.CodeTimeout || time.Since(started) > 3*time.Second {
		t.Fatalf("queued inspection escaped its deadline: %v, %v", observation, err)
	}
	for _, tt := range []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "binding", run: func(ctx context.Context) error { _, _, _, err := container.BindingContext(ctx); return err }},
		{name: "admission", run: func(ctx context.Context) error { return container.AdmitSubject(ctx, subject.Size()) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			defer cancel()
			if err := tt.run(ctx); code(err) != e.CodeTimeout {
				t.Fatalf("queued operation ignored cancellation: %v", err)
			}
		})
	}
}

func TestDiskInspectionImageIdentity(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tt := range []struct {
		name, raw, want string
		wantCode        e.Code
	}{
		{name: "Docker", raw: `{"Id":"` + digest + `"}`, want: digest},
		{name: "Podman", raw: `{"Id":"` + strings.Repeat("a", 64) + `"}`, want: digest},
		{name: "tag", raw: `{"Id":"mutable-tag"}`, wantCode: e.CodeEngineCapability},
		{name: "empty", raw: `{"Id":""}`, wantCode: e.CodeEngineCapability},
		{name: "wrong shape", raw: `"invalid"`, wantCode: e.CodeEngineCapability},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeDiskInspectionImageID([]byte(tt.raw))
			if got != tt.want || code(err) != tt.wantCode {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestDiskInspectionResourceBudgetIncludesEntireContainerGrant(t *testing.T) {
	for _, tt := range []struct {
		name   string
		memory uint64
		before bool
		want   e.Code
	}{
		{name: "container can consume full request", memory: 16384, before: false},
		{name: "snapshot cannot share container grant", memory: 16384, before: true, want: e.CodeEngineCapability},
		{name: "both fit exactly", memory: 17408, before: true},
		{name: "one MiB short", memory: 17407, before: true, want: e.CodeEngineCapability},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selection := testDiskInspectionSelection(t)
			facts := execution.ObservedHostFacts{Platform: execution.HostPlatformLinuxAMD64, Resources: execution.ResourceCapacity{CPUMillis: 1000, MemoryMiB: tt.memory, EphemeralStorageMiB: 1024}}
			if err := validateDiskInspectionResources(selection, facts, 1<<30, tt.before); code(err) != tt.want {
				t.Fatalf("got %v, want %s", err, tt.want)
			}
		})
	}
	selection := testDiskInspectionSelection(t)
	selection.Resources.MemoryMiB = math.MaxUint64
	facts := execution.ObservedHostFacts{Platform: execution.HostPlatformLinuxAMD64, Resources: execution.ResourceCapacity{CPUMillis: 1000, MemoryMiB: math.MaxUint64, EphemeralStorageMiB: 1024}}
	if err := validateDiskInspectionResources(selection, facts, 1, true); code(err) != e.CodeEngineCapability {
		t.Fatalf("overflow budget accepted: %v", err)
	}
}

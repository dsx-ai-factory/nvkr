package pipeline

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

type diskInspectionRunRuntime struct {
	packerContainerRuntime
	name                                     string
	commands                                 [][]string
	createDiagnostic, scanDiagnostic, report string
	createErr, scanErr, cleanupErr           error
	createID                                 string
	cancel                                   context.CancelFunc
	started, copied                          bool
	absent                                   bool
}

func (runtime *diskInspectionRunRuntime) RuntimeName() string {
	if runtime.name == "" {
		return "docker"
	}
	return runtime.name
}

func (runtime *diskInspectionRunRuntime) Run(_ context.Context, input io.Reader, output, diagnostic io.Writer, args ...string) error {
	runtime.commands = append(runtime.commands, slices.Clone(args))
	if args[0] == "--default-mounts-file=/dev/null" {
		args = args[1:]
	}
	if args[0] == "create" {
		_, _ = io.WriteString(diagnostic, runtime.createDiagnostic)
		id := runtime.createID
		if id == "" {
			id = strings.Repeat("a", 64)
		}
		_, _ = io.WriteString(output, id)
		return runtime.createErr
	}
	runtime.started = true
	if runtime.cancel != nil {
		runtime.cancel()
	}
	if runtime.scanErr != nil {
		_, _ = io.WriteString(diagnostic, runtime.scanDiagnostic)
		return runtime.scanErr
	}
	_, err := io.Copy(io.Discard, input)
	runtime.copied = err == nil
	if err != nil {
		return err
	}
	_, _ = io.WriteString(diagnostic, runtime.scanDiagnostic)
	_, err = io.WriteString(output, runtime.report)
	return err
}

func (runtime *diskInspectionRunRuntime) Cleanup(_ context.Context, _ time.Duration, output, _ io.Writer, args ...string) error {
	runtime.commands = append(runtime.commands, slices.Clone(args))
	if args[0] == "--default-mounts-file=/dev/null" {
		args = args[1:]
	}
	if args[0] == "ps" {
		if runtime.absent {
			return nil
		}
		_, err := io.WriteString(output, strings.Repeat("a", 64))
		return err
	}
	return runtime.cleanupErr
}

func TestDiskInspectionCommandsSuppressPodmanAutomaticMounts(t *testing.T) {
	for _, runtimeName := range []string{"docker", "podman"} {
		t.Run(runtimeName, func(t *testing.T) {
			subject, err := heldfile.FromBytesContext(t.Context(), []byte("held subject"), heldfile.Options{Label: "fixture", MaxBytes: 128})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			runtime := &diskInspectionRunRuntime{name: runtimeName, report: "report"}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			raw, err := runDiskInspectionContainer(ctx, runtime, testDiskInspectionSelection(t), "image", subject)
			if err != nil || string(raw) != "report" || !runtime.copied {
				t.Fatalf("inspection did not complete its streaming and cleanup: %q, %v", raw, err)
			}
			wantCommands := []string{"create", "start", "ps", "rm"}
			if len(runtime.commands) != len(wantCommands) {
				t.Fatalf("unexpected operation sequence: %v", runtime.commands)
			}
			for index, args := range runtime.commands {
				if runtimeName == "podman" {
					if len(args) < 2 || args[0] != "--default-mounts-file=/dev/null" {
						t.Fatalf("Podman operation lacks its fixed global override: %v", args)
					}
					args = args[1:]
				} else if slices.Contains(args, "--default-mounts-file=/dev/null") {
					t.Fatalf("Docker received a Podman-only option: %v", args)
				}
				if args[0] != wantCommands[index] {
					t.Fatalf("operation %d = %v, want %s", index, args, wantCommands[index])
				}
				if args[0] == "start" && args[len(args)-1] != strings.Repeat("a", 64) {
					t.Fatalf("start did not use immutable create ID: %v", args)
				}
			}
		})
	}
}

func TestDiskInspectionRunJoinsStreamingAndCleanup(t *testing.T) {
	for _, tt := range []struct {
		name         string
		runtime      diskInspectionRunRuntime
		closeSubject bool
		canceled     bool
		want         e.Code
		wantStart    bool
		wantReport   bool
		uncertain    bool
	}{
		{name: "complete", runtime: diskInspectionRunRuntime{report: "report"}, wantStart: true, wantReport: true},
		{name: "create failure", runtime: diskInspectionRunRuntime{createErr: errors.New("create")}, want: e.CodeEngineCapability},
		{name: "unsupported Podman override", runtime: diskInspectionRunRuntime{name: "podman", createErr: errors.New("unknown flag: --default-mounts-file")}, want: e.CodeEngineCapability},
		{name: "create failure with unresolved registration", runtime: diskInspectionRunRuntime{createErr: errors.New("create"), absent: true}, want: e.CodeEngineCapability, uncertain: true},
		{name: "create warning before start", runtime: diskInspectionRunRuntime{createDiagnostic: "requested limit ignored"}, want: e.CodeEngineCapability},
		{name: "invalid create ID", runtime: diskInspectionRunRuntime{createID: "reusable-name"}, want: e.CodeVerifyFailed},
		{name: "ambiguous create ID", runtime: diskInspectionRunRuntime{createID: strings.Repeat("a", 64) + " " + strings.Repeat("b", 64)}, want: e.CodeVerifyFailed},
		{name: "start failure joins blocked writer", runtime: diskInspectionRunRuntime{scanErr: errors.New("start")}, want: e.CodeEnvelopeDecode, wantStart: true},
		{name: "copy failure", closeSubject: true, want: e.CodeEnvelopeDecode, wantStart: true},
		{name: "scan diagnostic", runtime: diskInspectionRunRuntime{report: "report", scanDiagnostic: "scan warning"}, want: e.CodeEnvelopeDecode, wantStart: true},
		{name: "empty report", want: e.CodeEnvelopeDecode, wantStart: true},
		{name: "cleanup revokes report", runtime: diskInspectionRunRuntime{report: "report", cleanupErr: errors.New("cleanup")}, want: e.CodeInternal, wantStart: true},
		{name: "canceled scan", runtime: diskInspectionRunRuntime{scanErr: context.Canceled}, canceled: true, want: e.CodeTimeout, wantStart: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			subject, err := heldfile.FromBytesContext(t.Context(), []byte("held subject"), heldfile.Options{Label: "fixture", MaxBytes: 128})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			if tt.closeSubject {
				if err := subject.Close(); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if tt.canceled {
				tt.runtime.cancel = cancel
			}
			started := time.Now()
			raw, err := runDiskInspectionContainerWithCleanupTimeout(ctx, &tt.runtime, testDiskInspectionSelection(t), "image", subject, 100*time.Millisecond)
			if code(err) != tt.want || tt.runtime.started != tt.wantStart || (raw != nil) != tt.wantReport {
				t.Fatalf("raw=%q error=%v started=%t", raw, err, tt.runtime.started)
			}
			if tt.wantReport && (!tt.runtime.copied || string(raw) != "report") {
				t.Fatal("accepted report before input streaming completed")
			}
			if tt.runtime.name == "podman" {
				creates := 0
				for _, args := range tt.runtime.commands {
					if len(args) < 2 || args[0] != "--default-mounts-file=/dev/null" {
						t.Fatalf("Podman failure path omitted its fixed override: %v", args)
					}
					if args[1] == "create" {
						creates++
					}
				}
				if creates != 1 {
					t.Fatalf("unsupported Podman override retried creation: %v", tt.runtime.commands)
				}
			}
			if tt.uncertain && (!strings.Contains(err.Error(), "creation outcome remains uncertain") || time.Since(started) > 15*time.Second) {
				t.Fatalf("missing or unbounded cleanup uncertainty: %v", err)
			}
		})
	}
}

func TestDiskInspectionDiagnosticsAreBoundedAndQuoted(t *testing.T) {
	for _, raw := range []string{"\x1b[31merror\n\x00", strings.Repeat("x", 8192)} {
		got := diskInspectionDiagnostic([]byte(raw))
		if len(got) > 6*4096+64 || strings.ContainsAny(got, "\x1b\n\x00") {
			t.Fatalf("unsafe or unbounded diagnostic: %q", got)
		}
		want := raw
		if len(want) > 4096 {
			want = want[:4096]
		}
		if !strings.HasPrefix(got, strconv.Quote(strings.TrimSpace(want))) {
			t.Fatalf("diagnostic not quoted: %q", got)
		}
		if !strings.HasSuffix(got, "(truncated="+strconv.FormatBool(len(raw) > 4096)+")") {
			t.Fatal("diagnostic truncation not recorded")
		}
	}
}

func TestDiskInspectionExitProcessHelper(t *testing.T) {
	raw := os.Getenv("NKD_DISK_INSPECTION_EXIT_HELPER")
	if raw == "" {
		return
	}
	if raw == "signal" {
		process, err := os.FindProcess(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Kill(); err != nil {
			t.Fatal(err)
		}
		return
	}
	status, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(status)
}

func TestDiskInspectionRunPreservesHelperErrorClasses(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		status string
		want   e.Code
		cancel bool
	}{
		{name: "unsupported root", status: strconv.Itoa(e.ExitCode(e.New(e.CodeEngineCapability, ""))), want: e.CodeEngineCapability},
		{name: "encrypted guest", status: strconv.Itoa(e.ExitCode(e.New(e.CodeEncryptedArtifact, ""))), want: e.CodeEncryptedArtifact},
		{name: "empty inventory", status: strconv.Itoa(e.ExitCode(e.New(e.CodeProvenanceMissing, ""))), want: e.CodeProvenanceMissing},
		{name: "changed subject", status: strconv.Itoa(e.ExitCode(e.New(e.CodeVerifyFailed, ""))), want: e.CodeVerifyFailed},
		{name: "bad envelope", status: strconv.Itoa(e.ExitCode(e.New(e.CodeEnvelopeDecode, ""))), want: e.CodeEnvelopeDecode},
		{name: "helper timeout", status: strconv.Itoa(e.ExitCode(e.New(e.CodeTimeout, ""))), want: e.CodeTimeout},
		{name: "missing pin", status: strconv.Itoa(e.ExitCode(e.New(e.CodeUnpinnedInput, ""))), want: e.CodeUnpinnedInput},
		{name: "changed tool", status: strconv.Itoa(e.ExitCode(e.New(e.CodeUnverifiedRef, ""))), want: e.CodeUnverifiedRef},
		{name: "generic CLI failure", status: "1", want: e.CodeEnvelopeDecode},
		{name: "usage ambiguity", status: "2", want: e.CodeEnvelopeDecode},
		{name: "unknown status", status: "99", want: e.CodeEnvelopeDecode},
		{name: "runtime failure", status: "125", want: e.CodeEnvelopeDecode},
		{name: "runtime permission", status: "126", want: e.CodeEnvelopeDecode},
		{name: "runtime executable", status: "127", want: e.CodeEnvelopeDecode},
		{name: "signal", status: "signal", want: e.CodeEnvelopeDecode},
		{name: "caller cancellation wins", status: strconv.Itoa(e.ExitCode(e.New(e.CodeEngineCapability, ""))), cancel: true, want: e.CodeTimeout},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, path, "-test.run=^TestDiskInspectionExitProcessHelper$")
			command.Env = append(os.Environ(), "NKD_DISK_INSPECTION_EXIT_HELPER="+tt.status)
			exitErr := command.Run()
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(exitErr, &exit) {
				t.Fatalf("helper did not produce a process exit: %v", exitErr)
			}
			subject, err := heldfile.FromBytesContext(ctx, []byte("held subject"), heldfile.Options{Label: "fixture", MaxBytes: 128})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			runtime := &diskInspectionRunRuntime{scanErr: exitErr, scanDiagnostic: "helper failure"}
			if tt.cancel {
				runtime.cancel = cancel
			}
			raw, err := runDiskInspectionContainer(ctx, runtime, testDiskInspectionSelection(t), "image", subject)
			if raw != nil || code(err) != tt.want || !errors.As(err, &exit) {
				t.Fatalf("helper status=%s returned raw=%q err=%v", tt.status, raw, err)
			}
		})
	}
}

func TestDiskInspectionBufferHasNoUnboundedWriteFastPaths(t *testing.T) {
	for _, tt := range []struct {
		name string
		copy func(io.Writer, string) (int64, error)
	}{
		{name: "write", copy: func(output io.Writer, raw string) (int64, error) {
			n, err := output.Write([]byte(raw))
			return int64(n), err
		}},
		{name: "copy", copy: func(output io.Writer, raw string) (int64, error) {
			return io.Copy(output, io.LimitReader(strings.NewReader(raw), int64(len(raw))))
		}},
		{name: "string", copy: func(output io.Writer, raw string) (int64, error) {
			n, err := io.WriteString(output, raw)
			return int64(n), err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buffer diskInspectionBuffer
			if _, promoted := any(&buffer).(io.ReaderFrom); promoted {
				t.Fatal("unbounded ReadFrom bypasses Write")
			}
			if _, promoted := any(&buffer).(io.StringWriter); promoted {
				t.Fatal("unbounded WriteString bypasses Write")
			}
			if _, err := tt.copy(&buffer, strings.Repeat("x", 9<<20)); code(err) != e.CodeEnvelopeDecode || buffer.Len() > 8<<20 {
				t.Fatalf("output bound: bytes=%d err=%v", buffer.Len(), err)
			}
		})
	}
}

func TestDiskInspectionBufferProcessHelper(t *testing.T) {
	mode := os.Getenv("NKD_DISK_INSPECTION_OUTPUT_HELPER")
	if mode == "" {
		return
	}
	done := make(chan struct{})
	if mode == "stdout" || mode == "both" {
		go func() { _, _ = io.WriteString(os.Stdout, strings.Repeat("x", 9<<20)); close(done) }()
	} else {
		close(done)
	}
	if mode == "stderr" || mode == "both" {
		_, _ = io.WriteString(os.Stderr, strings.Repeat("x", 9<<20))
	}
	<-done
}

func TestDiskInspectionHeldSubprocessOutputIsBounded(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held executable inputs are Linux-only")
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err := heldfile.OpenContext(t.Context(), path, heldfile.Options{Label: "test-helper", Executable: true, MaxBytes: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()
	for _, mode := range []string{"stdout", "stderr", "both"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var output, diagnostic diskInspectionBuffer
			err := heldfile.RunContext(ctx, executable, []string{"-test.run=^TestDiskInspectionBufferProcessHelper$"}, heldfile.RunOptions{
				Stdout: &output, Stderr: &diagnostic, Env: append(os.Environ(), "NKD_DISK_INSPECTION_OUTPUT_HELPER="+mode),
			})
			if err == nil || ctx.Err() != nil || output.Len() > 8<<20 || diagnostic.Len() > 8<<20 {
				t.Fatalf("unbounded held subprocess output: stdout=%d stderr=%d err=%v context=%v", output.Len(), diagnostic.Len(), err, ctx.Err())
			}
		})
	}
}

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
		{name: "below CPU runtime floor", edit: func(s *DiskInspectionSelection) { s.Resources.CPUMillis = 9 }, want: e.CodeEngineCapability},
		{name: "CPU runtime floor", edit: func(s *DiskInspectionSelection) { s.Resources.CPUMillis = 10 }},
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
			if !slices.Contains(args, "--log-driver=none") {
				t.Fatal("daemon log persistence was not disabled")
			}
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
	listed                  string
	commands                [][]string
	lateID                  string
	diagnostic              string
	discoveryErr, removeErr error
}

func (*diskInspectionCleanupRuntime) RuntimeName() string { return "docker" }

func (runtime *diskInspectionCleanupRuntime) Cleanup(_ context.Context, _ time.Duration,
	stdout, stderr io.Writer, args ...string,
) error {
	runtime.commands = append(runtime.commands, slices.Clone(args))
	if args[0] == "ps" {
		_, _ = io.WriteString(stderr, runtime.diagnostic)
		if runtime.discoveryErr != nil {
			return runtime.discoveryErr
		}
		if runtime.lateID != "" && len(runtime.commands) > 1 {
			runtime.listed = runtime.lateID
		}
		_, err := io.WriteString(stdout, runtime.listed)
		return err
	}
	return runtime.removeErr
}

func TestDiskInspectionCleanupReconcilesUncertainCreation(t *testing.T) {
	for _, tt := range []struct {
		name, lateID string
		want         e.Code
	}{
		{name: "late daemon registration", lateID: strings.Repeat("a", 64)},
		{name: "absence is not creation completion", want: e.CodeInternal},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runtime := &diskInspectionCleanupRuntime{lateID: tt.lateID}
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			if err := removeDiskInspectionContainer(ctx, runtime, "attempt-token", true); code(err) != tt.want {
				t.Fatalf("cleanup outcome: %v, want %s", err, tt.want)
			}
			last := runtime.commands[len(runtime.commands)-1]
			if tt.lateID != "" && !slices.Equal(last, []string{"rm", "--force", tt.lateID}) {
				t.Fatalf("late registration not removed by owned ID: %v", last)
			}
			if tt.lateID == "" && last[0] == "rm" {
				t.Fatal("removed a container without an owned identity")
			}
		})
	}
}

func TestDiskInspectionCleanupRemovesOnlyOwnedIdentity(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, tt := range []struct {
		name, listed string
		wantRemove   bool
		wantCode     e.Code
	}{
		{name: "completed attempt already absent leaves siblings untouched"},
		{name: "canceled create cleans its owned container", listed: id + "\n", wantRemove: true},
		{name: "invalid identity", listed: "nkd-inspect-sibling\n", wantCode: e.CodeVerifyFailed},
		{name: "short identity", listed: id[:12] + "\n", wantCode: e.CodeVerifyFailed},
		{name: "ambiguous ownership", listed: id + "\n" + strings.Repeat("b", 64) + "\n", wantCode: e.CodeVerifyFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runtime := &diskInspectionCleanupRuntime{listed: tt.listed}
			if err := removeDiskInspectionContainer(t.Context(), runtime, "attempt-token", false); code(err) != tt.wantCode {
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

func TestDiskInspectionCanceledFreeLockReleasesMutex(t *testing.T) {
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := lockDiskInspection(ctx, &mu); code(err) != e.CodeTimeout {
		t.Fatalf("canceled free lock: %v", err)
	}
	if !mu.TryLock() {
		t.Fatal("canceled acquisition retained the mutex")
	}
	mu.Unlock()
}

func TestDiskInspectionCleanupDiscoveryFailures(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, uncertain := range []bool{false, true} {
		for _, tt := range []struct {
			name, listed, diagnostic string
			discoveryErr, removeErr  error
			want                     e.Code
			remove                   bool
		}{
			{name: "discovery error", discoveryErr: errors.New("discovery"), want: e.CodeInternal},
			{name: "warning without identity", diagnostic: "warning", want: e.CodeVerifyFailed},
			{name: "warning with owned identity", listed: id, diagnostic: "warning", want: e.CodeVerifyFailed, remove: true},
			{name: "warning with ambiguous identity", listed: id + " " + strings.Repeat("b", 64), diagnostic: "warning", want: e.CodeVerifyFailed},
			{name: "warning joins removal failure", listed: id, diagnostic: "warning", removeErr: errors.New("remove failure"), want: e.CodeVerifyFailed, remove: true},
			{name: "removal failure", listed: id, removeErr: errors.New("remove failure"), want: e.CodeInternal, remove: true},
		} {
			t.Run(tt.name+"/uncertain="+strconv.FormatBool(uncertain), func(t *testing.T) {
				runtime := &diskInspectionCleanupRuntime{listed: tt.listed, diagnostic: tt.diagnostic, discoveryErr: tt.discoveryErr, removeErr: tt.removeErr}
				err := removeDiskInspectionContainer(t.Context(), runtime, "attempt-token", uncertain)
				if code(err) != tt.want || (len(runtime.commands) == 2) != tt.remove {
					t.Fatalf("cleanup=%v commands=%v", err, runtime.commands)
				}
				if tt.remove && !slices.Equal(runtime.commands[1], []string{"rm", "--force", id}) {
					t.Fatalf("wrong removal target: %v", runtime.commands)
				}
				if tt.removeErr != nil && !errors.Is(err, tt.removeErr) {
					t.Fatalf("removal error lost: %v", err)
				}
				if tt.discoveryErr != nil && !errors.Is(err, tt.discoveryErr) {
					t.Fatalf("discovery error lost: %v", err)
				}
			})
		}
	}
}

func TestOpenDiskInspectionContainerAdmitsBeforeRuntimeIO(t *testing.T) {
	for _, tt := range []struct {
		name       string
		nilContext bool
		want       e.Code
	}{
		{name: "nil context", nilContext: true, want: e.CodeUsage},
		{name: "non-native host", want: e.CodeEngineCapability},
	} {
		t.Run(tt.name, func(t *testing.T) {
			selection := testDiskInspectionSelection(t)
			selection.RuntimeExecutable = "/must-not-spawn/podman"
			selection.Platform = "linux/amd64"
			if runtime.GOARCH == "amd64" {
				selection.Platform = "linux/arm64"
			}
			ctx := t.Context()
			if tt.nilContext {
				ctx = nil
			}
			container, err := OpenDiskInspectionContainer(ctx, selection)
			if container != nil || code(err) != tt.want {
				t.Fatalf("container=%v error=%v", container, err)
			}
			if !tt.nilContext && !strings.Contains(err.Error(), "native Linux host architecture") {
				t.Fatalf("rejection reached runtime work instead of native admission: %v", err)
			}
		})
	}
}

func TestDiskInspectionResourceAdmissionIndependentClauses(t *testing.T) {
	for _, before := range []bool{false, true} {
		for _, tt := range []struct {
			name string
			size int64
			edit func(*DiskInspectionSelection, *execution.ObservedHostFacts)
			want e.Code
		}{
			{name: "exact minimum", size: 1 << 30},
			{name: "request one MiB short", size: 1 << 30, edit: func(s *DiskInspectionSelection, _ *execution.ObservedHostFacts) { s.Resources.MemoryMiB-- }, want: e.CodeEngineCapability},
			{name: "8GiB needs more than 16GiB", size: 8 << 30, edit: func(s *DiskInspectionSelection, f *execution.ObservedHostFacts) {
				s.Resources.MemoryMiB = 16384
				f.Resources.MemoryMiB = 32768
			}, want: e.CodeEngineCapability},
			{name: "platform", size: 1 << 30, edit: func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts) {
				f.Platform = execution.HostPlatformLinuxARM64
			}, want: e.CodeEngineCapability},
			{name: "CPU", size: 1 << 30, edit: func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts) { f.Resources.CPUMillis-- }, want: e.CodeEngineCapability},
			{name: "storage", size: 1 << 30, edit: func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts) { f.Resources.EphemeralStorageMiB-- }, want: e.CodeEngineCapability},
			{name: "host memory", size: 1 << 30, edit: func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts) { f.Resources.MemoryMiB-- }, want: e.CodeEngineCapability},
		} {
			t.Run(tt.name+"/before="+strconv.FormatBool(before), func(t *testing.T) {
				selection := testDiskInspectionSelection(t)
				selection.Resources.MemoryMiB = 4096
				memory := uint64(4096)
				if before {
					memory += 1024
				}
				facts := execution.ObservedHostFacts{Platform: execution.HostPlatformLinuxAMD64, Resources: execution.ResourceCapacity{CPUMillis: 1000, MemoryMiB: memory, EphemeralStorageMiB: 1024}}
				if tt.edit != nil {
					tt.edit(&selection, &facts)
				}
				if err := validateDiskInspectionResources(selection, facts, tt.size, before); code(err) != tt.want {
					t.Fatalf("resource error=%v want=%s", err, tt.want)
				}
			})
		}
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

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/buildactivity"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"golang.org/x/sys/unix"
)

func activityTestSink(t *testing.T) (*buildactivity.Sink, *bufio.Reader) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Dup(int(write.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	sink, err := buildactivity.AdoptFD(t.Context(), fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close(); _ = read.Close() })
	if err := read.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return sink, bufio.NewReader(read)
}

func TestPackerActivityCollectorBoundsAndPrivacy(t *testing.T) {
	for _, input := range []struct {
		name, raw string
		available bool
	}{
		{"valid", `{"available":true,"bytes":1024,"files":1,"growth_age_millis":0,"growth_observed":false}` + "\n", true},
		{"unknown field", `{"available":true,"bytes":1024,"files":1,"growth_age_millis":0,"growth_observed":false,"path":"private"}` + "\n", false},
		{"negative bytes", `{"available":true,"bytes":-1,"files":1,"growth_age_millis":0,"growth_observed":false}` + "\n", false},
		{"unavailable with data", `{"available":false,"bytes":1024,"files":1,"growth_age_millis":0,"growth_observed":false}` + "\n", false},
		{"oversized", strings.Repeat("private secret", 1000) + "\n", false},
	} {
		t.Run(input.name, func(t *testing.T) {
			sink, reader := activityTestSink(t)
			writer := &packerActivityWriter{sink: sink, operation: "os/build-disk"}
			// Simulate arbitrary Docker attachment boundaries.
			for _, fragment := range []string{input.raw[:len(input.raw)/2], input.raw[len(input.raw)/2:]} {
				if n, err := writer.Write([]byte(fragment)); err != nil || n != len(fragment) {
					t.Fatal(n, err)
				}
			}
			if len(writer.partial) > 1024 {
				t.Fatal("collector exceeded finite frame bound")
			}
			if !input.available {
				raw, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				record, err := buildactivity.Decode([]byte(strings.TrimSuffix(raw, "\n")))
				if err != nil || record.Available || strings.Contains(raw, "private") {
					t.Fatal(raw, err)
				}
			} else if input.available {
				raw, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				record, err := buildactivity.Decode([]byte(strings.TrimSuffix(raw, "\n")))
				if err != nil || !record.Available || record.Operation != "os/build-disk" || record.Bytes != 1024 {
					t.Fatal(record, err)
				}
			}
		})
	}
}

func TestPackerActivityMetadataSampler(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is needed for the fixed toolchain-script contract")
	}
	prefix := strings.Split(packerActivityScript, "observer = Observer()\n")[0]
	for _, test := range []struct {
		name, action          string
		lastAvailable, growth bool
	}{
		{"baseline", "", true, false},
		{"same inode grows", `open(ROOT+'/private.log','ab').write(b'b')`, true, true},
		{"truncation", `open(ROOT+'/private.log','wb').close()`, false, false},
		{"replacement", `os.rename(ROOT+'/private.log',ROOT+'/old'); open(ROOT+'/private.log','wb').write(b'b')`, false, false},
		{"disappearance", `os.unlink(ROOT+'/private.log')`, false, false},
		{"new file baseline", `open(ROOT+'/new','wb').write(b'new')`, true, false},
		{"symlink", `os.symlink('private.log',ROOT+'/link')`, false, false},
		{"hardlink", `os.link(ROOT+'/private.log',ROOT+'/link')`, false, false},
		{"FIFO", `os.mkfifo(ROOT+'/pipe')`, false, false},
		{"directory replacement", `os.rename(ROOT,ROOT+'-old'); os.mkdir(ROOT)`, false, false},
		{"file byte bound", `open(ROOT+'/large','wb').truncate(16*1024*1024+1)`, false, false},
		{"file count bound", `[open(ROOT+'/'+str(i),'wb').close() for i in range(65)]`, false, false},
		{"entry bound", `[os.mkdir(ROOT+'/'+str(i)) for i in range(129)]`, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir() + "/logs"
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			harness := "\nimport sys\nROOT=sys.argv[1]\nopen(ROOT+'/private.log','wb').write(b'a')\no=Observer()\na=o.sample()\n" + test.action + "\nb=o.sample()\nprint(json.dumps([a,b]))\n"
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			raw, err := exec.CommandContext(ctx, python, "-c", prefix+harness, root).CombinedOutput()
			if err != nil {
				t.Fatal(err, string(raw))
			}
			var samples []packerActivitySample
			if err := json.Unmarshal(raw, &samples); err != nil {
				t.Fatal(err, string(raw))
			}
			if len(samples) != 2 || !samples[0].Available || samples[0].GrowthObserved || samples[1].Available != test.lastAvailable || samples[1].GrowthObserved != test.growth {
				t.Fatalf("metadata=%s", raw)
			}
			if strings.Contains(string(raw), "private") || strings.Contains(string(raw), root) {
				t.Fatal("observer exported a filename/path")
			}
		})
	}
}

type activityPackerRuntime struct {
	mu          sync.Mutex
	created     []string
	started     chan struct{}
	joined      chan struct{}
	release     chan struct{}
	cleanups    int
	cleanupArgs [][]string
	stopRelease chan struct{}
	stopJoined  chan struct{}
	stopFailure bool
}

func (*activityPackerRuntime) Endpoint() string                   { return qualifiedRuntimeEndpoint }
func (*activityPackerRuntime) RuntimeName() string                { return "docker" }
func (*activityPackerRuntime) AdmittedIdentity() (string, string) { return "", "" }
func (r *activityPackerRuntime) Run(ctx context.Context, _ io.Reader, out, _ io.Writer, args ...string) error {
	if args[0] == "create" {
		r.mu.Lock()
		r.created = append([]string(nil), args...)
		r.mu.Unlock()
		_, err := io.WriteString(out, "private-container-id\n")
		return err
	}
	close(r.started)
	if r.release != nil {
		<-r.release
	} else {
		<-ctx.Done()
	}
	close(r.joined)
	return ctx.Err()
}
func (r *activityPackerRuntime) Cleanup(_ context.Context, _ time.Duration, _, _ io.Writer, args ...string) error {
	r.mu.Lock()
	r.cleanups++
	r.cleanupArgs = append(r.cleanupArgs, append([]string(nil), args...))
	r.mu.Unlock()
	if r.stopRelease != nil && strings.HasSuffix(args[len(args)-1], "observer") {
		<-r.stopRelease
		defer close(r.stopJoined)
	}
	if r.stopFailure {
		return errors.New("private runtime failure")
	}
	return nil
}

func TestPackerActivityUnjoinedRetainsDependentResources(t *testing.T) {
	for _, blocked := range []string{"attach", "cleanup"} {
		t.Run(blocked, func(t *testing.T) {
			sink, _ := activityTestSink(t)
			runtime := &activityPackerRuntime{started: make(chan struct{}), joined: make(chan struct{})}
			if blocked == "attach" {
				runtime.release = make(chan struct{})
			} else {
				runtime.stopRelease, runtime.stopJoined = make(chan struct{}), make(chan struct{})
			}
			parent, err := os.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			workspace, err := createPackerWorkspaceRoot(parent, "test-observer")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = workspace.cleanup() })
			staged, err := heldfile.FromBytesContext(t.Context(), []byte("staged inputs"), heldfile.Options{Label: "activity-staging", MaxBytes: 64})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = staged.Close() })
			resources := &packerContainerResources{runtime: runtime, containers: []string{"owned-build", "owned-validate"},
				volumes: []string{"owned-input", "owned-work"}, held: []*heldfile.File{staged}, workspace: workspace}
			request := packerContainerRunRequest{runtime: runtime, activity: sink, activityOperation: "os/build-disk", bindings: packerContainerBindings{buildLogDirectoryVariable: "logs"}, identity: packerContainerIdentity{passwdPath: "private-passwd", groupPath: "private-group"}}
			observer := startPackerActivity(t.Context(), request, resources, "owned-work", "owned-observer", nil)
			if observer == nil {
				t.Fatal("observer was not admitted")
			}
			select {
			case <-runtime.started:
			case <-time.After(time.Second):
				t.Fatal("observer did not start")
			}
			defer func() {
				if runtime.release != nil {
					close(runtime.release)
				}
				if runtime.stopRelease != nil {
					close(runtime.stopRelease)
					<-runtime.stopJoined
				}
				<-observer.done
			}()
			if err := observer.stopWithTimeout(t.Context(), 10*time.Millisecond); code(err) != e.CodeTimeout {
				t.Fatal("unjoined observer did not fail closed", err)
			}
			if err := resources.cleanup(); code(err) != e.CodeTimeout {
				t.Fatal("cleanup ignored retained live observer", err)
			}
			runtime.mu.Lock()
			cleanups := runtime.cleanups
			runtime.mu.Unlock()
			if !reflect.DeepEqual(resources.containers, []string{"owned-observer"}) ||
				!reflect.DeepEqual(resources.volumes, []string{"owned-work"}) || resources.workspace != workspace || cleanups != 4 {
				t.Fatal("live observer's dependent controls were released")
			}
			if staged.OSFile() != nil || len(resources.held) != 0 {
				t.Fatal("completed staging inputs unnecessarily retained")
			}
			wantCleanup := [][]string{{"rm", "-fv", "owned-observer"}, {"rm", "-fv", "owned-validate"},
				{"rm", "-fv", "owned-build"}, {"volume", "rm", "-f", "owned-input"}}
			if !reflect.DeepEqual(runtime.cleanupArgs, wantCleanup) {
				t.Fatalf("unrelated attempt cleanup=%v, want %v", runtime.cleanupArgs, wantCleanup)
			}
			if _, err := workspace.root.Stat(); err != nil {
				t.Fatal("native workspace closed before observer join", err)
			}
		})
	}
}

type podmanActivityRuntime struct{ *activityPackerRuntime }

func (*podmanActivityRuntime) RuntimeName() string { return "podman" }

func TestPackerActivityDoesNotRetainPodmanConfiguration(t *testing.T) {
	sink, _ := activityTestSink(t)
	runtime := &podmanActivityRuntime{&activityPackerRuntime{}}
	resources := &packerContainerResources{runtime: runtime}
	request := packerContainerRunRequest{runtime: runtime, activity: sink, activityOperation: "os/build-disk", bindings: packerContainerBindings{buildLogDirectoryVariable: "logs"}}
	if startPackerActivity(t.Context(), request, resources, "work", "observer", nil) != nil || len(resources.containers) != 0 || len(runtime.created) != 0 {
		t.Fatal("Docker-only telemetry altered ordinary Podman execution")
	}
}

func TestPackerActivityOwnedReadOnlyLifecycle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop joins", true: "stop failure fails closed"}[fail], func(t *testing.T) {
			sink, _ := activityTestSink(t)
			runtime := &activityPackerRuntime{started: make(chan struct{}), joined: make(chan struct{}), stopFailure: fail}
			resources := &packerContainerResources{runtime: runtime}
			request := packerContainerRunRequest{runtime: runtime, activity: sink, activityOperation: "os/build-disk", bindings: packerContainerBindings{buildLogDirectoryVariable: "logs"},
				node: ir.Node{Toolchain: "pinned-image", ToolchainPlatform: "linux/arm64"}, identity: packerContainerIdentity{passwdPath: "private-passwd", groupPath: "private-group"}}
			observer := startPackerActivity(t.Context(), request, resources, "owned-work", "owned-observer", nil)
			if observer == nil {
				t.Fatal("observer not started")
			}
			select {
			case <-runtime.started:
			case <-time.After(time.Second):
				t.Fatal("observer attach not started")
			}
			if err := observer.stop(t.Context()); (err != nil) != fail || fail && code(err) != e.CodeInternal {
				t.Fatal(err)
			}
			select {
			case <-runtime.joined:
			default:
				t.Fatal("stop returned before joining attach")
			}
			args := strings.Join(runtime.created, " ")
			for _, want := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "src=owned-work,dst=/nkd/work,volume-nocopy,readonly", "--entrypoint=python3", "pinned-image -I -u -c"} {
				if !strings.Contains(args, want) {
					t.Fatalf("missing %s", want)
				}
			}
			for _, denied := range []string{"dst=/nkd/input,", "--device", "--cap-add=", "--network=bridge"} {
				if strings.Contains(args, denied) {
					t.Fatalf("observer granted %s", denied)
				}
			}
			if len(resources.containers) != map[bool]int{false: 0, true: 1}[fail] {
				t.Fatal("object ownership not preserved on failure")
			}
			if fail {
				err := resources.cleanup()
				if code(err) != e.CodeInternal || strings.Contains(err.Error(), "private runtime failure") {
					t.Fatal("observer cleanup retry exposed private runtime details", err)
				}
			}
		})
	}
}

func TestPackerActivityDoesNotStartForUnrequestedOrUnboundMetadata(t *testing.T) {
	sink, _ := activityTestSink(t)
	for _, test := range []struct {
		name               string
		activity           *buildactivity.Sink
		operation, binding string
	}{
		{"default execution", nil, "os/build-disk", "logs"},
		{"no bound log destination", sink, "os/build-disk", ""},
		{"no exact operation", sink, "", "logs"},
		{"unsafe operation", sink, "private host/token", "logs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &activityPackerRuntime{}
			resources := &packerContainerResources{runtime: runtime}
			request := packerContainerRunRequest{runtime: runtime, activity: test.activity, activityOperation: test.operation, bindings: packerContainerBindings{buildLogDirectoryVariable: test.binding}}
			if startPackerActivity(t.Context(), request, resources, "owned-work", "owned-observer", nil) != nil || len(runtime.created) != 0 || len(resources.containers) != 0 {
				t.Fatal("metadata opt-out mutated execution")
			}
		})
	}
}

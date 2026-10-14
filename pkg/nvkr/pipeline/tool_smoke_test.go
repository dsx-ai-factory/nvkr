// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func smokeSubject(t *testing.T) *heldfile.File {
	t.Helper()
	raw := smokeELFBytes()
	held, err := heldfile.FromBytesContext(t.Context(), raw, heldfile.Options{Label: "smoke-test", MaxBytes: MaxToolSmokeExecutableBytes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	return held
}
func smokeELFBytes() []byte {
	raw := make([]byte, 256)
	copy(raw, []byte{'\x7f', 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:18], 2)
	machine := uint16(62)
	if runtime.GOARCH == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(raw[18:20], machine)
	binary.LittleEndian.PutUint32(raw[20:24], 1)
	binary.LittleEndian.PutUint16(raw[52:54], 64)
	return raw
}
func smokeSelection() ToolSmokeSelection {
	return ToolSmokeSelection{RuntimeExecutable: "docker", Image: "sha256:" + strings.Repeat("c", 64), Platform: "linux/" + runtime.GOARCH}
}

func TestToolSmokeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ToolSmokeSelection)
		want   e.Code
	}{
		{"valid", func(*ToolSmokeSelection) {}, ""},
		{"tag", func(s *ToolSmokeSelection) { s.Image = "ubuntu:latest" }, e.CodeUnpinnedInput},
		{"missing pin", func(s *ToolSmokeSelection) { s.Image = "" }, e.CodeUnpinnedInput},
		{"other digest", func(s *ToolSmokeSelection) { s.Image = "example.test/tool@sha512:" + strings.Repeat("a", 128) }, e.CodeUnpinnedInput},
		{"podman", func(s *ToolSmokeSelection) { s.RuntimeExecutable = "podman" }, e.CodeEngineCapability},
		{"unspecified runtime", func(s *ToolSmokeSelection) { s.RuntimeExecutable = "" }, e.CodeEngineCapability},
		{"unspecified platform", func(s *ToolSmokeSelection) { s.Platform = "" }, e.CodeEngineCapability},
		{"darwin", func(s *ToolSmokeSelection) { s.Platform = "darwin/arm64" }, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := smokeSelection()
			tc.mutate(&s)
			if got := code(validateToolSmokeSelection(s)); got != tc.want {
				t.Fatalf("code=%s want=%s", got, tc.want)
			}
		})
	}
	if _, err := OpenToolSmokeContainer(nil, smokeSelection()); code(err) != e.CodeUsage { //nolint:staticcheck // Deliberately exercise the coded nil-context boundary.
		t.Fatalf("nil context: %v", err)
	}
	if runtime.GOOS != "linux" {
		if _, err := OpenToolSmokeContainer(t.Context(), smokeSelection()); code(err) != e.CodeEngineCapability {
			t.Fatalf("non-Linux admission: %v", err)
		}
	}
	for _, tc := range []struct {
		profile ToolSmokeProfile
		want    []string
	}{
		{ToolSmokeNVKECTL, []string{"version", "--output", "json"}},
		{ToolSmokeHelm, []string{"version", "--template", "{{.Version}}"}},
		{ToolSmokeKubectl, []string{"version", "--client", "--output", "json"}},
	} {
		got, err := toolSmokeArguments(tc.profile)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("profile %s: %v %v", tc.profile, got, err)
		}
	}
	if _, err := toolSmokeArguments("arbitrary-command"); code(err) != e.CodeEngineCapability {
		t.Fatalf("unknown profile: %v", err)
	}
}

func TestToolSmokeNativeELF(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]byte)
		want   e.Code
	}{
		{"native", func([]byte) {}, ""},
		{"script", func(b []byte) { copy(b, "#!/bin/sh") }, e.CodeEngineCapability},
		{"ELF32", func(b []byte) { b[4] = 1 }, e.CodeEngineCapability},
		{"big endian", func(b []byte) { b[5] = 2 }, e.CodeEngineCapability},
		{"foreign machine", func(b []byte) { binary.LittleEndian.PutUint16(b[18:20], 3) }, e.CodeEngineCapability},
		{"shared relocatable", func(b []byte) { binary.LittleEndian.PutUint16(b[16:18], 1) }, e.CodeEngineCapability},
		{"bad ELF header", func(b []byte) { binary.LittleEndian.PutUint16(b[52:54], 1) }, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := smokeELFBytes()
			tc.mutate(raw)
			f, err := heldfile.FromBytesContext(t.Context(), raw, heldfile.Options{Label: "ELF", MaxBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if got := code(validateToolSmokeSubject(t.Context(), f, smokeSelection().Platform)); got != tc.want {
				t.Fatalf("code=%s want=%s", got, tc.want)
			}
		})
	}
	subject := smokeSubject(t)
	if err := subject.Close(); err != nil {
		t.Fatal(err)
	}
	if got := code(validateToolSmokeSubject(t.Context(), subject, smokeSelection().Platform)); got != e.CodeVerifyFailed {
		t.Fatalf("closed subject: %s", got)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := code(validateToolSmokeSubject(ctx, smokeSubject(t), smokeSelection().Platform)); got != e.CodeTimeout {
		t.Fatalf("canceled subject: %s", got)
	}
}

type smokeRuntimeFixture struct {
	subject                                    *heldfile.File
	profile                                    ToolSmokeProfile
	commands                                   [][]string
	inspection                                 toolSmokeInspection
	createErr, startErr, cleanupErr            error
	createWarning, cleanupWarning, output      string
	mutateInspection                           func(*toolSmokeInspection)
	changeIdentity, changeImage                bool
	started, removed, streamed                 bool
	blockStart, cleanupEntered, cleanupRelease chan struct{}
	absent, ambiguous                          bool
}

func (*smokeRuntimeFixture) Endpoint() string    { return qualifiedRuntimeEndpoint }
func (*smokeRuntimeFixture) RuntimeName() string { return "docker" }
func (f *smokeRuntimeFixture) AdmittedIdentity() (string, string) {
	executable := "sha256:" + strings.Repeat("d", 64)
	identity, err := toolSmokeRuntimeIdentity(executable, qualifiedRuntimeEndpoint, []byte(`{"Version":"fixture"}`), []byte(`"fixture-daemon"`))
	if err != nil {
		panic(err)
	}
	return executable, identity
}
func (f *smokeRuntimeFixture) Run(ctx context.Context, input io.Reader, out, diagnostic io.Writer, args ...string) error {
	f.commands = append(f.commands, slices.Clone(args))
	write := func(raw string) error { _, err := io.WriteString(out, raw); return err }
	switch args[0] {
	case "version":
		if f.started && f.changeIdentity {
			return write(`{"Version":"changed"}`)
		}
		return write(`{"Version":"fixture"}`)
	case "info":
		return write(`"fixture-daemon"`)
	case "image":
		id := smokeSelection().Image
		if f.started && f.changeImage {
			id = "sha256:" + strings.Repeat("f", 64)
		}
		raw, err := json.Marshal(map[string]any{"Id": id, "Os": "linux", "Architecture": runtime.GOARCH, "Config": map[string]any{"Env": []string{"PATH=/image/path"}}})
		if err != nil {
			return err
		}
		return write(string(raw))
	case "create":
		attempt := ""
		for _, arg := range args {
			if strings.HasPrefix(arg, "--label="+toolSmokeLabel+"=") {
				attempt = strings.TrimPrefix(arg, "--label="+toolSmokeLabel+"=")
			}
		}
		f.inspection = smokeContainerInspection(f.subject, f.profile, attempt)
		if f.mutateInspection != nil {
			f.mutateInspection(&f.inspection)
		}
		if _, err := io.WriteString(diagnostic, f.createWarning); err != nil {
			return err
		}
		if f.createErr != nil {
			return f.createErr
		}
		return write(strings.Repeat("a", 64))
	case "container":
		raw, err := json.Marshal(f.inspection)
		if err != nil {
			return err
		}
		return write(string(raw))
	case "start":
		f.started = true
		if f.blockStart != nil {
			close(f.blockStart)
			<-ctx.Done()
			return ctx.Err()
		}
		if f.startErr != nil {
			return f.startErr
		}
		raw, err := io.ReadAll(input)
		if err != nil {
			return err
		}
		expected, err := f.subject.ReadAllContext(ctx, MaxToolSmokeExecutableBytes)
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, expected) {
			return errors.New("streamed bytes differ")
		}
		f.streamed = true
		f.inspection.State.Status = "exited"
		return write(f.output)
	default:
		return errors.New("unexpected runtime command")
	}
}
func (f *smokeRuntimeFixture) Cleanup(ctx context.Context, _ time.Duration, out, diagnostic io.Writer, args ...string) error {
	f.commands = append(f.commands, slices.Clone(args))
	if args[0] == "ps" {
		if _, err := io.WriteString(diagnostic, f.cleanupWarning); err != nil {
			return err
		}
		if f.absent {
			return nil
		}
		ids := strings.Repeat("a", 64)
		if f.ambiguous {
			ids += "\n" + strings.Repeat("b", 64)
		}
		_, err := io.WriteString(out, ids)
		return err
	}
	if f.cleanupEntered != nil {
		close(f.cleanupEntered)
		select {
		case <-f.cleanupRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.cleanupErr != nil {
		return f.cleanupErr
	}
	f.removed = true
	return nil
}
func smokeContainerInspection(subject *heldfile.File, profile ToolSmokeProfile, attempt string) toolSmokeInspection {
	var result toolSmokeInspection
	result.ID = strings.Repeat("a", 64)
	result.Image = smokeSelection().Image
	c, h := &result.Config, &result.HostConfig
	c.Image = smokeSelection().Image
	c.User = "65534:65534"
	c.Hostname = "nvkr-tool-smoke"
	c.WorkingDir = "/work"
	c.Env = toolSmokeEnvironment()
	c.Entrypoint = []string{"/bin/sh"}
	c.Cmd = toolSmokeCommand(subject, profile)
	c.OpenStdin = true
	c.Labels = map[string]string{toolSmokeLabel: attempt}
	c.Healthcheck.Test = []string{"NONE"}
	h.NetworkMode = "none"
	h.IpcMode = "none"
	h.CgroupnsMode = "private"
	h.ReadonlyRootfs = true
	h.Init = new(bool)
	*h.Init = true
	h.CapDrop = []string{"ALL"}
	h.SecurityOpt = []string{"no-new-privileges"}
	h.Tmpfs = map[string]string{"/work": toolSmokeTmpfs}
	h.Memory = 512 << 20
	h.MemorySwap = 512 << 20
	h.CPUPeriod = 100000
	h.CPUQuota = 100000
	h.PidsLimit = new(int64)
	*h.PidsLimit = 64
	h.LogConfig.Type = "none"
	h.RestartPolicy.Name = "no"
	h.Ulimits = append(h.Ulimits, struct {
		Name       string
		Soft, Hard int64
	}{Name: "core"}, struct {
		Name       string
		Soft, Hard int64
	}{Name: "nofile", Soft: 128, Hard: 128})
	result.State.Status = "created"
	return result
}
func smokeOwner(t *testing.T, f *smokeRuntimeFixture) *ToolSmokeContainer {
	t.Helper()
	f.subject = smokeSubject(t)
	f.profile = ToolSmokeHelm
	if f.output == "" {
		f.output = "v3.19.0"
	}
	owner := &ToolSmokeContainer{toolSmokeState: &toolSmokeState{runtime: toolSmokeBoundedRuntime{f}, selection: smokeSelection()}}
	t.Cleanup(func() { _ = owner.Close() })
	return owner
}

func TestToolSmokeRunOwnsStreamingAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*smokeRuntimeFixture)
		want      e.Code
		start     bool
	}{
		{"success", func(*smokeRuntimeFixture) {}, "", true},
		{"create failure", func(f *smokeRuntimeFixture) { f.createErr = errors.New("create") }, e.CodeEngineCapability, false},
		{"ignored sandbox warning", func(f *smokeRuntimeFixture) { f.createWarning = "memory limit ignored" }, e.CodeEngineCapability, false},
		{"startup failure", func(f *smokeRuntimeFixture) { f.startErr = errors.New("start") }, e.CodeVerifyFailed, true},
		{"output bound", func(f *smokeRuntimeFixture) { f.output = strings.Repeat("x", MaxToolSmokeOutputBytes+1) }, e.CodeVerifyFailed, true},
		{"cleanup warning still removes", func(f *smokeRuntimeFixture) { f.cleanupWarning = "warning" }, e.CodeInternal, true},
		{"cleanup failure", func(f *smokeRuntimeFixture) { f.cleanupErr = errors.New("remove") }, e.CodeInternal, true},
		{"ambiguous cleanup", func(f *smokeRuntimeFixture) { f.ambiguous = true }, e.CodeVerifyFailed, true},
		{"runtime replaced", func(f *smokeRuntimeFixture) { f.changeIdentity = true }, e.CodeEngineCapability, true},
		{"image replaced", func(f *smokeRuntimeFixture) { f.changeImage = true }, e.CodePreflightFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := new(smokeRuntimeFixture)
			tc.configure(f)
			owner := smokeOwner(t, f)
			observation, err := owner.RunHeld(t.Context(), f.subject, f.profile)
			if code(err) != tc.want {
				t.Fatalf("error=%v want=%s", err, tc.want)
			}
			if f.started != tc.start {
				t.Fatalf("started=%v", f.started)
			}
			if tc.want != "" {
				if f.cleanupWarning != "" && !f.removed {
					t.Fatal("cleanup warning prevented owned removal")
				}
				if observation != nil {
					t.Fatal("failure returned observation")
				}
				return
			}
			if !f.removed || !f.streamed {
				t.Fatal("success preceded stream/cleanup")
			}
			digest, size := observation.Subject()
			if digest != f.subject.Digest() || size != f.subject.Size() || observation.Platform() != smokeSelection().Platform || observation.Image() != smokeSelection().Image || observation.RuntimeIdentity() == "" || observation.Profile() != f.profile {
				t.Fatal("incorrect observation binding")
			}
			raw := observation.Stdout()
			raw[0] = 'X'
			if string(observation.Stdout()) != f.output {
				t.Fatal("mutable output escaped")
			}
			if err := f.subject.AssertUnchangedContext(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestToolSmokeIsolationRejectsMutationBeforeStart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*toolSmokeInspection)
	}{
		{"supplementary group", func(i *toolSmokeInspection) { i.HostConfig.GroupAdd = []string{"0"} }},
		{"root", func(i *toolSmokeInspection) { i.Config.User = "0:0" }},
		{"environment", func(i *toolSmokeInspection) { i.Config.Env = append(i.Config.Env, "TOKEN=secret") }},
		{"volume", func(i *toolSmokeInspection) {
			i.Config.Volumes = map[string]json.RawMessage{"/secret": json.RawMessage(`{}`)}
		}},
		{"host bind", func(i *toolSmokeInspection) { i.HostConfig.Binds = []string{"/:/host"} }},
		{"network", func(i *toolSmokeInspection) { i.HostConfig.NetworkMode = "host" }},
		{"writable root", func(i *toolSmokeInspection) { i.HostConfig.ReadonlyRootfs = false }},
		{"capabilities", func(i *toolSmokeInspection) { i.HostConfig.CapAdd = []string{"SYS_ADMIN"} }},
		{"privileged", func(i *toolSmokeInspection) { i.HostConfig.Privileged = true }},
		{"privilege escalation", func(i *toolSmokeInspection) { i.HostConfig.SecurityOpt = nil }},
		{"PID limit", func(i *toolSmokeInspection) { i.HostConfig.PidsLimit = nil }},
		{"memory", func(i *toolSmokeInspection) { i.HostConfig.Memory = 0 }},
		{"cpu", func(i *toolSmokeInspection) { i.HostConfig.CPUQuota = 0 }},
		{"tmpfs", func(i *toolSmokeInspection) { i.HostConfig.Tmpfs["/work"] = "rw" }},
		{"image", func(i *toolSmokeInspection) { i.Image = "sha256:" + strings.Repeat("b", 64) }},
		{"entrypoint", func(i *toolSmokeInspection) { i.Config.Entrypoint = []string{"/bin/arbitrary"} }},
		{"profile", func(i *toolSmokeInspection) { i.Config.Cmd = append(i.Config.Cmd, "--arbitrary") }},
		{"device", func(i *toolSmokeInspection) { i.HostConfig.Devices = []json.RawMessage{json.RawMessage(`{}`)} }},
		{"host pid", func(i *toolSmokeInspection) { i.HostConfig.PidMode = "host" }},
		{"restart", func(i *toolSmokeInspection) { i.HostConfig.RestartPolicy.Name = "always" }},
		{"healthcheck", func(i *toolSmokeInspection) { i.Config.Healthcheck.Test = []string{"CMD", "unsafe"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &smokeRuntimeFixture{mutateInspection: tc.mutate}
			owner := smokeOwner(t, f)
			observation, err := owner.RunHeld(t.Context(), f.subject, f.profile)
			if code(err) != e.CodeEngineCapability || observation != nil || f.started || !f.removed {
				t.Fatalf("observation=%v error=%v started=%v removed=%v", observation, err, f.started, f.removed)
			}
		})
	}
}

func TestToolSmokeCloseCancelsAndJoins(t *testing.T) {
	f := &smokeRuntimeFixture{blockStart: make(chan struct{}), cleanupEntered: make(chan struct{}), cleanupRelease: make(chan struct{})}
	owner := smokeOwner(t, f)
	runDone := make(chan error, 1)
	go func() {
		observation, err := owner.RunHeld(t.Context(), f.subject, f.profile)
		if observation != nil {
			runDone <- errors.New("canceled run returned observation")
			return
		}
		runDone <- err
	}()
	select {
	case <-f.blockStart:
	case <-time.After(5 * time.Second):
		t.Fatal("start did not arrive")
	}
	closeDone := make(chan error, 1)
	go func() { copied := *owner; closeDone <- copied.Close() }()
	select {
	case <-f.cleanupEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel run")
	}
	select {
	case err := <-closeDone:
		t.Fatalf("Close preceded cleanup: %v", err)
	default:
	}
	close(f.cleanupRelease)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-runDone; code(err) != e.CodeTimeout {
		t.Fatalf("cancel result=%v", err)
	}
	if !f.removed {
		t.Fatal("cleanup was not joined")
	}
	if _, err := owner.RunHeld(t.Context(), f.subject, f.profile); code(err) != e.CodeEngineCapability {
		t.Fatalf("closed owner admitted run: %v", err)
	}
	if err := f.subject.AssertUnchangedContext(t.Context()); err != nil {
		t.Fatalf("owner closed borrowed subject: %v", err)
	}
}

func TestToolSmokeFailedCreateReconcilesUncertainRegistration(t *testing.T) {
	f := &smokeRuntimeFixture{createErr: errors.New("create"), absent: true}
	owner := smokeOwner(t, f)
	_, _, err := runToolSmokeContainer(t.Context(), owner.runtime, smokeSelection(), smokeSelection().Image, f.subject, f.profile, 20*time.Millisecond)
	if code(err) != e.CodeEngineCapability || !strings.Contains(err.Error(), "outcome remains uncertain") {
		t.Fatalf("uncertain create=%v", err)
	}
}

func TestToolSmokeImageObservationForms(t *testing.T) {
	native := "sha256:" + strings.Repeat("a", 64)
	index := "sha256:" + strings.Repeat("b", 64)
	selection := smokeSelection()
	selection.Image = "example.test/tool@" + index
	for _, tc := range []struct {
		name, image, descriptor, platform string
		want                              bool
	}{
		{"legacy native ID", native, "", "", true},
		{"containerd native descriptor", index, native, selection.Platform, true},
		{"index without native descriptor", index, "", "", false},
		{"wrong descriptor", index, index, selection.Platform, false},
		{"foreign platform", index, native, "linux/386", false},
		{"wrong index", "sha256:" + strings.Repeat("c", 64), native, selection.Platform, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observation := toolSmokeInspection{Image: tc.image}
			if tc.descriptor != "" {
				osName, arch, _ := strings.Cut(tc.platform, "/")
				raw, err := json.Marshal(map[string]any{"ImageManifestDescriptor": map[string]any{"Digest": tc.descriptor, "Platform": map[string]string{"OS": osName, "Architecture": arch}}})
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &observation); err != nil {
					t.Fatal(err)
				}
			}
			if got := toolSmokeImageMatches(observation, selection, native); got != tc.want {
				t.Fatalf("matches=%v want=%v", got, tc.want)
			}
		})
	}
}

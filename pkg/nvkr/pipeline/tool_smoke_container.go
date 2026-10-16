// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const (
	toolSmokeLabel     = "nvkr.dev/tool-smoke-attempt"
	toolSmokeTmpfs     = "rw,exec,nosuid,nodev,size=268435456,mode=0700,uid=65534,gid=65534"
	toolSmokeBootstrap = `umask 077
cat > /work/tool
test "$(wc -c < /work/tool)" -eq "$2"
printf '%s  /work/tool\n' "$1" | sha256sum -c - >/dev/null
chmod 0500 /work/tool
shift 2
exec /usr/bin/env -i PATH=/usr/bin:/bin HOME=/work TMPDIR=/work LC_ALL=C TZ=UTC CLUSTERCTL_DISABLE_VERSIONCHECK=true /work/tool "$@"`
)

func toolSmokeEnvironment() []string {
	// clusterctl performs a network update check after even its version command.
	// Disable that optional check while retaining the version output and all
	// container isolation. This fixed value never comes from the caller.
	return []string{"PATH=/usr/bin:/bin", "HOME=/work", "TMPDIR=/work", "LC_ALL=C", "TZ=UTC", "CLUSTERCTL_DISABLE_VERSIONCHECK=true"}
}

func toolSmokeCreateArgs(selection ToolSmokeSelection, image, attempt string, subject *heldfile.File, profile ToolSmokeProfile) []string {
	args := []string{"create", "--name=nvkr-tool-smoke-" + attempt, "--pull=never", "--platform=" + selection.Platform,
		"--label=" + toolSmokeLabel + "=" + attempt, "--network=none", "--log-driver=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--user=65534:65534", "--hostname=nvkr-tool-smoke", "--no-healthcheck", "--init", "--interactive", "--ipc=none", "--cgroupns=private",
		"--cpu-period=100000", "--cpu-quota=100000", "--memory=512m", "--memory-swap=512m", "--pids-limit=64",
		"--ulimit=core=0:0", "--ulimit=nofile=128:128", "--restart=no", "--tmpfs=/work:" + toolSmokeTmpfs,
		"--workdir=/work", "--entrypoint=/bin/sh"}
	for _, variable := range toolSmokeEnvironment() {
		args = append(args, "--env", variable)
	}
	return append(append(args, image), toolSmokeCommand(subject, profile)...)
}

func toolSmokeCommand(subject *heldfile.File, profile ToolSmokeProfile) []string {
	args, _ := toolSmokeArguments(profile) // The executable boundary admits the profile before creating anything.
	return append([]string{"-eu", "-c", toolSmokeBootstrap, "nvkr-tool-smoke", strings.TrimPrefix(subject.Digest(), "sha256:"), strconv.FormatInt(subject.Size(), 10)}, args...)
}

func runToolSmokeContainer(ctx context.Context, runtime packerContainerRuntime, selection ToolSmokeSelection, image string, subject *heldfile.File, profile ToolSmokeProfile, cleanupTimeout time.Duration) (stdout, stderr []byte, returnErr error) {
	if _, err := toolSmokeArguments(profile); err != nil {
		return nil, nil, err
	}
	attempt, err := packerAttemptID()
	if err != nil {
		return nil, nil, e.Wrap(e.CodeInternal, err, "allocating tool smoke attempt")
	}
	creationUncertain := true
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := removeToolSmokeContainer(cleanupCtx, runtime, attempt, creationUncertain); err != nil {
			stdout, stderr = nil, nil
			returnErr = errors.Join(returnErr, err)
		}
	}()
	var created, diagnostic diskInspectionBuffer
	created.limit, diagnostic.limit = toolSmokeControlBytes, toolSmokeControlBytes
	if err := runtime.Run(ctx, nil, &created, &diagnostic, toolSmokeCreateArgs(selection, selection.Image, attempt, subject, profile)...); err != nil {
		return nil, nil, runtimeCommandError(ctx, err, diskInspectionDiagnostic(diagnostic.Bytes()), e.CodeEngineCapability, "creating tool smoke container")
	}
	creationUncertain = false
	if diagnostic.Len() != 0 {
		return nil, nil, e.New(e.CodeEngineCapability, "tool smoke creation returned diagnostics: %s", diskInspectionDiagnostic(diagnostic.Bytes()))
	}
	ids := strings.Fields(created.String())
	if len(ids) != 1 || !canonical.IsDigest("sha256:"+ids[0]) {
		return nil, nil, e.New(e.CodeVerifyFailed, "tool smoke create returned no unique immutable container ID")
	}
	if err := observeToolSmokeContainer(ctx, runtime, selection, ids[0], image, attempt, subject, profile, false); err != nil {
		return nil, nil, err
	}
	var output, errorOutput diskInspectionBuffer
	output.limit, errorOutput.limit = MaxToolSmokeOutputBytes, MaxToolSmokeOutputBytes
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		copied, err := subject.WriteToContext(ctx, writer)
		if err == nil && copied != subject.Size() {
			err = e.New(e.CodeVerifyFailed, "tool smoke input stream changed size")
		}
		_ = writer.CloseWithError(err)
		done <- err
	}()
	runErr := runtime.Run(ctx, reader, &output, &errorOutput, "start", "--attach", "--interactive", ids[0])
	_ = reader.Close()
	copyErr := <-done
	if err := errors.Join(runErr, copyErr); err != nil {
		return nil, nil, runtimeCommandError(ctx, err, diskInspectionDiagnostic(errorOutput.Bytes()), e.CodeVerifyFailed, "executing delivered tool smoke")
	}
	if err := observeToolSmokeContainer(ctx, runtime, selection, ids[0], image, attempt, subject, profile, true); err != nil {
		return nil, nil, err
	}
	if output.Len() == 0 {
		return nil, nil, e.New(e.CodeVerifyFailed, "delivered tool smoke returned no version output")
	}
	return bytes.Clone(output.Bytes()), bytes.Clone(errorOutput.Bytes()), nil
}

// Docker's ordinary inspect document is trusted daemon observation. Inspect only
// this create's immutable ID and reject inherited or unexpected privileges before
// any subject byte is streamed. The exit-state observation is checked afterward.
type toolSmokeInspection struct {
	ID     string `json:"Id"`
	Image  string
	Config struct {
		Image, User, Hostname, WorkingDir string
		Env, Entrypoint, Cmd              []string
		Tty, OpenStdin                    bool
		Volumes                           map[string]json.RawMessage
		Labels                            map[string]string
		Healthcheck                       struct{ Test []string }
	}
	HostConfig struct {
		NetworkMode, PidMode, IpcMode, UTSMode, CgroupnsMode, UsernsMode                                                             string
		ReadonlyRootfs, Privileged, PublishAllPorts                                                                                  bool
		Init                                                                                                                         *bool
		Binds, VolumesFrom, CapAdd, CapDrop, SecurityOpt, Links, ExtraHosts, Dns, DnsOptions, DnsSearch, DeviceCgroupRules, GroupAdd []string
		Mounts, Devices, DeviceRequests                                                                                              []json.RawMessage
		PortBindings                                                                                                                 map[string]json.RawMessage
		Tmpfs                                                                                                                        map[string]string
		Memory, MemorySwap, CPUPeriod, CPUQuota                                                                                      int64
		PidsLimit                                                                                                                    *int64
		Ulimits                                                                                                                      []struct {
			Name       string
			Soft, Hard int64
		}
		LogConfig struct {
			Type   string
			Config map[string]string
		}
		RestartPolicy struct {
			Name              string
			MaximumRetryCount int
		}
	}
	ImageManifestDescriptor *struct {
		Digest   string
		Platform struct{ OS, Architecture string }
	}
	Mounts []struct {
		Type, Source, Destination string
		RW                        bool
	}
	State struct {
		Status                                       string
		Running, Paused, Restarting, OOMKilled, Dead bool
		ExitCode                                     int
		Error                                        string
	}
}

func observeToolSmokeContainer(ctx context.Context, runtime packerContainerRuntime, selection ToolSmokeSelection, id, image, attempt string, subject *heldfile.File, profile ToolSmokeProfile, exited bool) error {
	var output, diagnostic diskInspectionBuffer
	output.limit, diagnostic.limit = toolSmokeControlBytes, toolSmokeControlBytes
	if err := runtime.Run(ctx, nil, &output, &diagnostic, "container", "inspect", "--format", "{{json .}}", id); err != nil {
		return runtimeCommandError(ctx, err, diskInspectionDiagnostic(diagnostic.Bytes()), e.CodeEngineCapability, "observing tool smoke container")
	}
	if diagnostic.Len() != 0 {
		return e.New(e.CodeEngineCapability, "tool smoke inspection returned diagnostics")
	}
	var observed toolSmokeInspection
	if err := json.Unmarshal(output.Bytes(), &observed); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "decoding tool smoke container observation")
	}
	return validateToolSmokeInspection(observed, selection, id, image, attempt, subject, profile, exited)
}

func validateToolSmokeInspection(observed toolSmokeInspection, selection ToolSmokeSelection, id, image, attempt string, subject *heldfile.File, profile ToolSmokeProfile, exited bool) error {
	c, h, state := observed.Config, observed.HostConfig, observed.State
	denied := func() error {
		return e.New(e.CodeEngineCapability, "tool smoke container does not match the fixed isolation profile")
	}
	if observed.ID != id || !toolSmokeImageMatches(observed, selection, image) || c.Image != selection.Image || c.User != "65534:65534" || c.Hostname != "nvkr-tool-smoke" || c.WorkingDir != "/work" || c.Tty || !c.OpenStdin ||
		!slices.Equal(c.Entrypoint, []string{"/bin/sh"}) || !slices.Equal(c.Cmd, toolSmokeCommand(subject, profile)) || !toolSmokeSameStrings(c.Env, toolSmokeEnvironment()) ||
		len(c.Volumes) != 0 || c.Labels[toolSmokeLabel] != attempt || !slices.Equal(c.Healthcheck.Test, []string{"NONE"}) {
		return denied()
	}
	if h.NetworkMode != "none" || !h.ReadonlyRootfs || h.Privileged || h.PublishAllPorts || h.Init == nil || !*h.Init ||
		h.PidMode != "" || h.IpcMode != "none" || h.UTSMode != "" || h.CgroupnsMode != "private" || h.UsernsMode != "" ||
		len(h.GroupAdd)+len(h.Binds)+len(h.VolumesFrom)+len(h.CapAdd)+len(h.Links)+len(h.ExtraHosts)+len(h.Dns)+len(h.DnsOptions)+len(h.DnsSearch)+len(h.DeviceCgroupRules)+len(h.Mounts)+len(h.Devices)+len(h.DeviceRequests)+len(h.PortBindings) != 0 ||
		!slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(h.SecurityOpt, []string{"no-new-privileges"}) ||
		len(h.Tmpfs) != 1 || h.Tmpfs["/work"] != toolSmokeTmpfs || h.Memory != 512<<20 || h.MemorySwap != 512<<20 || h.CPUPeriod != 100000 || h.CPUQuota != 100000 ||
		h.PidsLimit == nil || *h.PidsLimit != 64 || h.LogConfig.Type != "none" || len(h.LogConfig.Config) != 0 || h.RestartPolicy.Name != "no" || h.RestartPolicy.MaximumRetryCount != 0 {
		return denied()
	}
	if len(h.Ulimits) != 2 {
		return denied()
	}
	limits := make(map[string]bool)
	for _, limit := range h.Ulimits {
		if limits[limit.Name] || (limit.Name != "core" && limit.Name != "nofile") {
			return denied()
		}
		expected := int64(0)
		if limit.Name == "nofile" {
			expected = 128
		}
		if limit.Hard != expected || limit.Soft != expected {
			return denied()
		}
		limits[limit.Name] = true
	}
	// Docker versions either omit tmpfs from Mounts or project its one anonymous
	// mount. A daemon-injected bind/volume/device never passes this boundary.
	if len(observed.Mounts) > 1 {
		return denied()
	}
	for _, mount := range observed.Mounts {
		if mount.Type != "tmpfs" || mount.Source != "" || mount.Destination != "/work" || !mount.RW {
			return denied()
		}
	}
	if state.Running || state.Paused || state.Restarting || state.OOMKilled || state.Dead || state.Error != "" || state.ExitCode != 0 {
		return e.New(e.CodeVerifyFailed, "tool smoke container did not reach a clean stopped state")
	}
	wantStatus := "created"
	if exited {
		wantStatus = "exited"
	}
	if state.Status != wantStatus {
		return e.New(e.CodeVerifyFailed, "tool smoke container has unexpected execution state")
	}
	return nil
}

// Legacy Docker reports its inspected image ID directly. Containerd-backed
// Docker reports the selected index in Image and the resolved native manifest
// separately. Both require the exact admitted native identity; a selected index
// alone cannot stand in for a platform observation.
func toolSmokeImageMatches(observed toolSmokeInspection, selection ToolSmokeSelection, image string) bool {
	descriptor := observed.ImageManifestDescriptor
	if descriptor != nil && (descriptor.Digest != image || descriptor.Platform.OS+"/"+descriptor.Platform.Architecture != selection.Platform) {
		return false
	}
	if observed.Image == image {
		return true
	}
	selected := selection.Image
	if !canonical.IsDigest(selected) {
		var err error
		selected, err = imageManifestDigest(selected)
		if err != nil {
			return false
		}
	}
	return descriptor != nil && observed.Image == selected
}

func toolSmokeSameStrings(actual, expected []string) bool {
	actual, expected = slices.Clone(actual), slices.Clone(expected)
	slices.Sort(actual)
	slices.Sort(expected)
	return slices.Equal(actual, expected)
}

// Failed create can still register late in the trusted daemon. Reconcile the
// unique attempt label until discovered or the independent cleanup budget ends;
// an empty lookup is not proof that a failed create had no side effects.
func removeToolSmokeContainer(ctx context.Context, runtime packerContainerRuntime, attempt string, uncertain bool) error {
	var output, diagnostic diskInspectionBuffer
	output.limit, diagnostic.limit = toolSmokeControlBytes, toolSmokeControlBytes
	var warning error
	for {
		output.Reset()
		diagnostic.Reset()
		if err := runtime.Cleanup(ctx, time.Second, &output, &diagnostic, "ps", "--all", "--no-trunc", "--filter", "label="+toolSmokeLabel+"="+attempt, "--format", "{{.ID}}"); err != nil {
			return errors.Join(warning, e.Wrap(e.CodeInternal, err, "finding owned tool smoke container"))
		}
		if diagnostic.Len() != 0 {
			warning = e.New(e.CodeInternal, "tool smoke cleanup discovery returned diagnostics")
		}
		ids := strings.Fields(output.String())
		if len(ids) == 0 {
			if !uncertain {
				return warning
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errors.Join(warning, e.Wrap(e.CodeInternal, ctx.Err(), "tool smoke creation outcome remains uncertain"))
			case <-timer.C:
				continue
			}
		}
		if len(ids) != 1 || !canonical.IsDigest("sha256:"+ids[0]) {
			return errors.Join(warning, e.New(e.CodeVerifyFailed, "tool smoke cleanup has ambiguous container identity"))
		}
		output.Reset()
		diagnostic.Reset()
		if err := runtime.Cleanup(ctx, time.Second, &output, &diagnostic, "rm", "--force", ids[0]); err != nil {
			return errors.Join(warning, e.Wrap(e.CodeInternal, err, "removing owned tool smoke container"))
		}
		if diagnostic.Len() != 0 {
			return errors.Join(warning, e.New(e.CodeInternal, "tool smoke removal returned diagnostics"))
		}
		return warning
	}
}

func toolSmokeRuntimeIdentity(executable, endpoint string, version, daemon []byte) (string, error) {
	var serverID string
	if err := json.Unmarshal(daemon, &serverID); err != nil || strings.TrimSpace(serverID) == "" {
		return "", e.New(e.CodeEngineCapability, "tool smoke runtime returned no server identity")
	}
	return runtimeIdentityFromOutput(executable, endpoint, serverID, version)
}

// Bound all control responses even when reusing observers that use bytes.Buffer.
// Runtime clients remain context-bound and joined by the held-file runner.
type toolSmokeBoundedRuntime struct{ packerContainerRuntime }

func (r toolSmokeBoundedRuntime) Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	out, diagnostic := toolSmokeLimitWriter{destination: stdout}, toolSmokeLimitWriter{destination: stderr}
	err := r.packerContainerRuntime.Run(ctx, stdin, &out, &diagnostic, args...)
	return errors.Join(err, out.err, diagnostic.err)
}
func (r toolSmokeBoundedRuntime) Cleanup(ctx context.Context, grace time.Duration, stdout, stderr io.Writer, args ...string) error {
	out, diagnostic := toolSmokeLimitWriter{destination: stdout}, toolSmokeLimitWriter{destination: stderr}
	err := r.packerContainerRuntime.Cleanup(ctx, grace, &out, &diagnostic, args...)
	return errors.Join(err, out.err, diagnostic.err)
}

type toolSmokeLimitWriter struct {
	destination io.Writer
	written     int
	err         error
}

func (w *toolSmokeLimitWriter) Write(raw []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if len(raw) > toolSmokeControlBytes-w.written {
		w.err = e.New(e.CodeEngineCapability, "tool smoke runtime output exceeds its bound")
		return 0, w.err
	}
	w.written += len(raw)
	n, err := w.destination.Write(raw)
	w.err = err
	return n, err
}

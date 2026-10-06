package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// CheckContainerPlatform materializes a digest-pinned native toolchain and checks
// the runtime's selected image config before source or build output work. This is
// capability admission, not qualified runtime or reproduction evidence.
func CheckContainerPlatform(ctx context.Context, runtimeBinary, image, platform string) (err error) {
	if err := requireRunContext(ctx, "container platform admission"); err != nil {
		return err
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return e.New(e.CodeEngineCapability, "container execution platform %q is unsupported", platform)
	}
	if _, err := imageManifestDigest(image); err != nil {
		return e.Wrap(e.CodeUnpinnedSource, err, "container platform admission requires an immutable image")
	}
	switch filepath.Base(runtimeBinary) {
	case "docker", "podman":
	default:
		return e.New(e.CodeEngineCapability, "container platform admission requires Docker or Podman")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			err = e.Wrap(e.CodeTimeout, ctx.Err(), "container platform admission canceled")
		}
	}()
	raw, err := containerPlatformOutput(ctx, runtimeBinary, "info", "--format", "{{json .}}")
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "observing container daemon platform")
	}
	if err := checkContainerDaemonPlatform(raw, platform); err != nil {
		return err
	}
	pull := execCmd(RunOptions{Ctx: ctx}, runtimeBinary, "pull", "--platform", platform, image)
	pull.Stdout, pull.Stderr = io.Discard, io.Discard
	if err := runOwnedCommand(ctx, pull); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "materializing %s toolchain", platform)
	}
	raw, err = containerPlatformOutput(ctx, runtimeBinary, "image", "inspect", "--format", "{{json .}}", image)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "inspecting %s toolchain", platform)
	}
	return checkContainerPlatform(raw, image, platform)
}

func checkContainerPlatform(raw []byte, image, platform string) error {
	var inspection struct {
		OS           string   `json:"Os"`
		Architecture string   `json:"Architecture"`
		RepoDigests  []string `json:"RepoDigests"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &inspection); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "decoding toolchain platform")
	}
	if inspection.OS+"/"+inspection.Architecture != platform {
		return e.New(e.CodeEngineCapability, "toolchain provides %s/%s, not requested %s", inspection.OS, inspection.Architecture, platform)
	}
	want, err := imageManifestDigest(image)
	if err != nil {
		return e.Wrap(e.CodeUnpinnedSource, err, "invalid toolchain identity")
	}
	for _, ref := range inspection.RepoDigests {
		digest, err := imageManifestDigest(ref)
		if err == nil && digest == want {
			return nil
		}
	}
	return e.New(e.CodePreflightFailed, "runtime did not report selected toolchain manifest %s", want)
}

// Inspection is small JSON; bound captured bytes as well as subprocess lifetime.
func containerPlatformOutput(ctx context.Context, runtimeBinary string, args ...string) ([]byte, error) {
	cmd := execCmd(RunOptions{Ctx: ctx}, runtimeBinary, args...)
	var output containerPlatformBuffer
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := runOwnedCommand(ctx, cmd); err != nil {
		return nil, err
	}
	if output.overflow {
		return nil, fmt.Errorf("container image inspection exceeds 1 MiB")
	}
	return output.buffer.Bytes(), nil
}

type containerPlatformBuffer struct {
	buffer   bytes.Buffer
	overflow bool
}

func (output *containerPlatformBuffer) Write(raw []byte) (int, error) {
	n := len(raw)
	remaining := (1 << 20) - output.buffer.Len()
	if len(raw) > remaining {
		output.overflow = true
		raw = raw[:remaining]
	}
	_, _ = output.buffer.Write(raw)
	return n, nil
}

func checkContainerDaemonPlatform(raw []byte, platform string) error {
	var information struct {
		OSType       string `json:"OSType"`
		Architecture string `json:"Architecture"`
		Host         struct {
			OS   string `json:"os"`
			Arch string `json:"arch"`
		} `json:"host"`
	}
	if err := json.Unmarshal(raw, &information); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "decoding container daemon platform")
	}
	osName, arch := information.OSType, information.Architecture
	if information.Host.OS != "" || information.Host.Arch != "" {
		if osName != "" || arch != "" {
			return e.New(e.CodeEngineCapability, "ambiguous container daemon platform")
		}
		osName, arch = information.Host.OS, information.Host.Arch
	}
	switch arch {
	case "x86_64":
		arch = "amd64"
	case "aarch64":
		arch = "arm64"
	}
	if osName+"/"+arch != platform {
		return e.New(e.CodeEngineCapability, "container daemon provides %s/%s, not native %s", osName, arch, platform)
	}
	return nil
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// CheckContainerPlatform is the diagnostic facade over image admission. Omitted
// access permits local inspection only; network materialization must be explicit.
func CheckContainerPlatform(ctx context.Context, runtimeBinary, image, platform string, routes ...ImageAccess) error {
	access := ImagePreparedOnly
	if len(routes) > 1 {
		return e.New(e.CodeEngineCapability, "image admission requires one access route")
	}
	if len(routes) == 1 {
		access = routes[0]
	}
	_, err := PrepareContainerImage(ctx, runtimeBinary, image, platform, access)
	return err
}

func containerPullFailure(ctx context.Context, platform string, cause error, diagnostic *containerPlatformBuffer) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "container platform admission canceled")
	}
	return e.Wrap(e.CodeEngineCapability, cause, "materializing %s toolchain (diagnostic: %s)", platform, containerPullDiagnostic(diagnostic))
}

// Only fixed descriptive categories cross the error boundary. Registry messages
// can include credentials, URLs or headers and must never be copied into logs.
// These categories do not authorize retry or change capability admission.
func containerPullDiagnostic(diagnostic *containerPlatformBuffer) string {
	if diagnostic.overflow {
		return "oversized"
	}
	message := strings.ToLower(diagnostic.buffer.String())
	for _, category := range []struct {
		name    string
		phrases []string
	}{
		{"registry-rate-limit", []string{"toomanyrequests:", "too many requests", "you have reached your pull rate limit"}},
		{"registry-auth", []string{"authentication required", "pull access denied", "requested access to the resource is denied", "401 unauthorized", "403 forbidden"}},
		{"registry-unavailable", []string{"500 internal server error", "502 bad gateway", "503 service unavailable", "504 gateway timeout"}},
		{"network-timeout", []string{"context deadline exceeded", "i/o timeout", "tls handshake timeout", "client.timeout exceeded while awaiting headers"}},
		{"network-unavailable", []string{"no such host", "network is unreachable", "connection refused", "connection reset by peer", "temporary failure in name resolution"}},
		{"local-storage", []string{"no space left on device", "read-only file system", "disk quota exceeded"}},
		{"runtime-unavailable", []string{"cannot connect to the docker daemon", "unable to connect to podman socket"}},
	} {
		for _, phrase := range category.phrases {
			if strings.Contains(message, phrase) {
				return category.name
			}
		}
	}
	return "unknown"
}

func checkContainerPlatform(raw []byte, image, platform string) error {
	_, err := inspectAdmittedImage(image, platform, "diagnostic", raw)
	return err
}

// Inspection is small JSON; bound captured bytes as well as subprocess lifetime.
func containerPlatformOutput(ctx context.Context, runtimeBinary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
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

// containerPlatformBuffer drains excess subprocess output while retaining at most
// 1 MiB. Write reports all input consumed; every caller must reject overflow before
// interpreting the captured prefix.
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
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "validating container daemon JSON")
	}
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
			return e.New(e.CodeEngineCapability, "mixed top-level and host container daemon platform fields")
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

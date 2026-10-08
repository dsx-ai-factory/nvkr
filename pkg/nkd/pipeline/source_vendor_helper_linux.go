// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"encoding/json"
	"os"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

// This is source-only host-tool admission, not a pinned or qualified runtime
// closure. It does not change the shared runtime's closed PATH/config defaults.
func admitSourceVendorPodmanHelpers(ctx context.Context, held *heldRuntimeExecutable) error {
	if err := requireRunContext(ctx, "source vendor network helper admission"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "source vendor network helper admission canceled")
	}
	if held == nil || held.name != "podman" {
		return e.New(e.CodeEngineCapability, "source vendor network helper requires its held Podman runtime")
	}
	raw, err := heldExecutionOutput(ctx, held, "info", "--format", "{{json .}}")
	if err != nil {
		return err
	}
	helper, err := sourceVendorPodmanNetworkHelper(raw)
	if err != nil || helper == "" {
		return err // Rootful Podman retains its existing network implementation.
	}
	root, _, err := securepath.OpenDirectory("/")
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "holding source vendor system helper root")
	}
	defer root.Close()
	current := root
	for _, component := range []string{"", "usr", "bin", helper} {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "source vendor network helper admission canceled")
		}
		directory := component != helper
		if component != "" {
			next, _, err := securepath.OpenChild(current, component, directory)
			if err != nil {
				return e.Wrap(e.CodeEngineCapability, err, "holding source vendor system helper component %q", component)
			}
			defer next.Close()
			current = next
		}
		if err := sourceVendorSystemHelperFile(current, directory); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "source vendor network helper admission canceled")
	}
	held.environment = append(held.environment, "CONTAINERS_HELPER_BINARY_DIR=/usr/bin")
	return nil
}

func sourceVendorPodmanNetworkHelper(raw []byte) (string, error) {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "decoding source vendor Podman networking")
	}
	var info struct {
		Host struct {
			ServiceIsRemote    bool   `json:"serviceIsRemote"`
			RootlessNetworkCmd string `json:"rootlessNetworkCmd"`
			Security           struct {
				Rootless *bool `json:"rootless"`
			} `json:"security"`
		} `json:"host"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "decoding source vendor Podman networking")
	}
	if info.Host.ServiceIsRemote {
		return "", nil // The authenticated remote server owns its network helpers.
	}
	if info.Host.Security.Rootless == nil {
		return "", e.New(e.CodeEngineCapability, "source vendor Podman did not report its network privilege mode")
	}
	if !*info.Host.Security.Rootless {
		return "", nil
	}
	switch info.Host.RootlessNetworkCmd {
	case "pasta", "slirp4netns":
		return info.Host.RootlessNetworkCmd, nil
	default:
		return "", e.New(e.CodeEngineCapability, "source vendor Podman rootless network helper %q is unsupported", info.Host.RootlessNetworkCmd)
	}
}

func sourceVendorSystemHelperFile(file *os.File, directory bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "observing source vendor system helper protection")
	}
	return sourceVendorSystemHelperProtection(stat, directory)
}

func sourceVendorSystemHelperProtection(stat unix.Stat_t, directory bool) error {
	wantType := uint32(unix.S_IFREG)
	if directory {
		wantType = unix.S_IFDIR
	}
	if stat.Uid != 0 || stat.Mode&0o022 != 0 || stat.Mode&unix.S_IFMT != wantType || stat.Mode&0o111 == 0 {
		return e.New(e.CodeEngineCapability, "source vendor system helper and ancestors must be root-owned, executable and not group/world writable")
	}
	return nil
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"fmt"
	"sort"
	"strings"
)

// String renders a one-line human summary (used by `catalog list`). It includes
// the digest-pinned toolchain so a reviewer sees the full trust chain — pinned
// source ref AND the toolchain that builds it — at a glance (NVKR-FR-INT-042).
func (c Component) String() string {
	if c.Prebuilt != nil {
		ref := c.Prebuilt.URL
		if ref == "" {
			ref = c.Prebuilt.Image
		}
		archs := make([]string, 0, len(c.Prebuilt.Digests))
		for a := range c.Prebuilt.Digests {
			archs = append(archs, a)
		}
		sort.Strings(archs)
		return fmt.Sprintf("prebuilt %s [%s] pinned %v", ref, c.OutputType, archs)
	}
	if !c.FromSource {
		return fmt.Sprintf("excluded (%s)", c.ExclusionReason)
	}
	return fmt.Sprintf("%s @ %s [%s] toolchain %s", c.Repo, c.Commit[:12], c.OutputType, abbrevDigest(c.ToolchainImage))
}

// abbrevDigest shortens a digest-pinned image for display, keeping the name (if
// any) and the first 12 hex of the sha256 (e.g. golang@sha256:fe5d57d3b718).
func abbrevDigest(img string) string {
	i := strings.Index(img, "sha256:")
	if i < 0 {
		return img
	}
	hexStart := i + len("sha256:")
	if len(img) >= hexStart+12 {
		return img[:hexStart+12]
	}
	return img
}

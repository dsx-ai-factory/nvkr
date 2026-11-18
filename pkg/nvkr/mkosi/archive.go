// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mkosi

import _ "embed"

// ArchiveToolsDirectory is a fixed read-only namespace projection. The recipe
// pins this versioned policy; callers cannot supply an arbitrary search path.
// Change its version when the adapter's byte-producing policy changes.
const ArchiveToolsDirectory = "/inputs/nvkr-tar-v1"

//go:embed tar-wrapper.sh
var archiveWrapper string

// ArchiveWrapper returns the fixed GNU tar adapter selected through mkosi's own
// ExtraSearchPaths support. It does not modify the authenticated mkosi source.
func ArchiveWrapper() string { return archiveWrapper }

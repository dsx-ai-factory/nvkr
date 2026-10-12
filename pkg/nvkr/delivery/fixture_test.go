// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"path/filepath"
	"strings"
)

func fixtureBlobPath(root, pin string) string {
	return filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(pin, "sha256:"))
}

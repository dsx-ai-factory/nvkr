// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package securepath

import "fmt"

func observeDiskSpace(int) (FilesystemSpace, error) {
	return FilesystemSpace{}, fmt.Errorf("disk-backed scratch observation is unsupported on this platform")
}

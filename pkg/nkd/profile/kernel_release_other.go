// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package profile

import "os/exec"

func kernelRelease() string { return "" }

func findExecutable(name string) (string, error) { return exec.LookPath(name) }

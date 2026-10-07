// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin && !linux

package transportarchive

func publishTransportNoReplace(_, _ string) error {
	return errTransportNoReplaceUnsupported
}

// PublishAtNoReplace rejects publication when atomic no-replace rename is
// unavailable. It never falls back to replacing an existing destination.
func PublishAtNoReplace(_ int, _ string, _ int, _ string) error {
	return errTransportNoReplaceUnsupported
}

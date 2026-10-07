// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package versionpolicy owns version syntax admission. Validation never returns
// a normalized version: callers retain the original text for lookup and identity.
package versionpolicy

import (
	"strings"

	"github.com/Masterminds/semver/v3"
)

// PinSet requires all three numeric segments and permits one leading v.
func PinSet(value string) bool {
	if len(value) > 256 {
		return false
	}
	_, err := semver.StrictNewVersion(strings.TrimPrefix(value, "v"))
	return err == nil
}

// Helm follows Helm metadata admission, including leading v and abbreviated
// versions. Coercion is used only to validate; its normalized result is discarded.
func Helm(value string) bool {
	_, err := semver.NewVersion(value)
	return err == nil
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package sourceenv owns the reserved environment namespace for opt-in source
// metadata. Catalog resolution owns values; executable IR admits these names.
package sourceenv

import (
	"regexp"
	"strings"
)

var name = regexp.MustCompile(`^NVKR_SOURCE_[A-Z][A-Z0-9_]{0,63}$`)

func ValidName(value string) bool { return name.MatchString(value) }
func Reserved(value string) bool  { return strings.HasPrefix(value, "NVKR_SOURCE_") }

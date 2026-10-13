// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package target

import (
	"strings"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	DefaultDebianDistribution = "ubuntu-24.04"
	DefaultDebianPlatform     = "linux/amd64"
)

// Debian describes an exact source-owned distribution/ABI profile and artifact
// platform. Distribution is an opaque profile key, not an APT publication suite
// or a parsed Debian version. A description grants no toolchain or build authority.
type Debian struct {
	Distribution string `json:"distribution"`
	Platform     string `json:"platform"`
}

// Validate requires a fully resolved description. It does not establish that a
// package or toolchain supports the named distribution/ABI profile.
func (target Debian) Validate() error {
	if target.Distribution == "" || !utf8.ValidString(target.Distribution) || target.Distribution != strings.TrimSpace(target.Distribution) ||
		strings.ContainsAny(target.Distribution, "\x00\r\n\t*?") {
		return e.New(e.CodeInvalidCatalog, "Debian distribution profile must be a nonempty canonical exact key")
	}
	return ValidatePlatform(target.Platform)
}

// ResolveDebian resolves omissions before producer lowering. Standalone requests
// use the documented default profile/platform; a dependency inherits both from
// its complete consuming OS/image target and rejects explicit conflicts. The
// complete sorted source-declared route set is checked before selection. Callers
// must separately authenticate source/toolchain pins and consumer binding.
func ResolveDebian(request Debian, consumer *Debian, supported []Debian) (Debian, error) {
	if err := ValidateDebianRoutes(supported); err != nil {
		return Debian{}, err
	}
	base := Debian{Distribution: DefaultDebianDistribution, Platform: DefaultDebianPlatform}
	if consumer != nil {
		if err := consumer.Validate(); err != nil {
			return Debian{}, err
		}
		base = *consumer
		if (request.Distribution != "" && request.Distribution != base.Distribution) ||
			(request.Platform != "" && request.Platform != base.Platform) {
			return Debian{}, e.New(e.CodeInvalidCatalog, "Debian dependency target conflicts with its consuming OS/image target")
		}
	}
	if request.Distribution == "" {
		request.Distribution = base.Distribution
	}
	if request.Platform == "" {
		request.Platform = base.Platform
	}
	if err := request.Validate(); err != nil {
		return Debian{}, err
	}
	for _, route := range supported {
		if route == request {
			return request, nil
		}
	}
	return Debian{}, e.New(e.CodeInvalidCatalog, "Debian source does not declare target %q on %q", request.Distribution, request.Platform)
}

// ValidateDebianRoutes admits a complete finite route declaration independently
// of which target is selected; a source need not support the standalone default.
func ValidateDebianRoutes(supported []Debian) error {
	previous := Debian{}
	for i, route := range supported {
		if err := route.Validate(); err != nil {
			return err
		}
		if i > 0 && (route.Distribution < previous.Distribution ||
			(route.Distribution == previous.Distribution && route.Platform <= previous.Platform)) {
			return e.New(e.CodeInvalidCatalog, "Debian source routes must be sorted and unique by distribution and platform")
		}
		previous = route
	}
	if len(supported) == 0 {
		return e.New(e.CodeInvalidCatalog, "Debian source has no declared target routes")
	}
	return nil
}

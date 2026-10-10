// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// RequiredBuilders returns the distinct from-source builder types a resolved output
// needs (ADR-07 §5), so a builder profile can be capability-matched against them
// (profile.RequireBuilders). Prebuilt/exclusion components require no builder.
func RequiredBuilders(res *Resolved) []string {
	seen := map[string]bool{}
	for _, nc := range res.Components {
		if !nc.FromSource {
			continue
		}
		bt := nc.BuilderType
		if bt == "" {
			bt = ir.BuilderOCIHermetic
		}
		seen[bt] = true
	}
	out := make([]string, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

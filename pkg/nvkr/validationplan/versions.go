// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validationplan

import (
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

// outputVersion is the tag of the one source that built the output's
// components. Components built from no source are ignored. It is empty when no
// component has a tagged source or when the tags differ, because the output
// then has no single version.
func outputVersion(components []string, sources []lifecycle.PlanSource) string {
	tags := map[string]bool{}
	for _, name := range components {
		for _, source := range sources {
			if slices.Contains(source.Components, name) {
				tags[source.Tag] = true
			}
		}
	}
	if len(tags) != 1 {
		return ""
	}
	for tag := range tags {
		return tag
	}
	return ""
}

// componentVersions maps each component built from a tagged source to that
// source's tag. A component built from no source, or from an untagged one, is
// absent.
func componentVersions(components []string, sources []lifecycle.PlanSource) map[string]string {
	versions := map[string]string{}
	for _, name := range components {
		for _, source := range sources {
			if source.Tag != "" && slices.Contains(source.Components, name) {
				versions[name] = source.Tag
			}
		}
	}
	if len(versions) == 0 {
		return nil
	}
	return versions
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

type githubActionRefs pipeline.GitHubActionRefs

func resolveGitHubActions(pins map[string]catalog.ActionPin) (githubActionRefs, error) {
	refs, err := pipeline.ResolveGitHubActions(pins)
	return githubActionRefs(refs), err
}

func cloneActionPins(pins map[string]catalog.ActionPin) map[string]catalog.ActionPin {
	if len(pins) == 0 {
		return nil
	}
	out := make(map[string]catalog.ActionPin, len(pins))
	for name, pin := range pins {
		out[name] = pin
	}
	return out
}

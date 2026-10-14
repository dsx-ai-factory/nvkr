// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Leave room in the provider's job-output budget for other action invocations.
// The bound includes the output name and newline, before UTF-16 accounting.
const maxGitHubActionDataOutputBytes = 200_000

// WriteGitHubActionData publishes the selected shared action's descriptive data
// from the admitted Plan. The checked-in data file is never execution authority:
// its entries are regenerated, and neither credentials nor expressions are
// resolved from the process environment. Complete validation precedes output.
func WriteGitHubActionData(ctx context.Context, plan *Plan, selector, outputPath string) error {
	if err := admitGitHubPlan(ctx, plan); err != nil {
		return err
	}
	if selector == "" || outputPath == "" {
		return e.New(e.CodeUsage, "GitHub action description requires an action selector and GITHUB_OUTPUT path")
	}
	bundle, err := renderGitHub(ctx, plan, false)
	if err != nil {
		return err
	}
	raw, ok := bundle.Files[githubActionDataPathFor(plan)]
	if !ok {
		return e.New(e.CodePipelineDrift, "generated GitHub bundle omits shared action data")
	}
	var document githubActionDataDocument
	if err := canonical.UnmarshalExact(raw, &document); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decode generated GitHub action data")
	}
	entry, ok := document.Entries[selector]
	if !ok || entry.Template == "" || entry.Values == nil {
		return e.New(e.CodePipelineDrift, "GitHub action selector %q has no admitted shared action data", selector)
	}
	return writeGitHubActionValues(ctx, entry.Values, outputPath)
}

func writeGitHubActionValues(ctx context.Context, values map[string]string, outputPath string) error {
	data, err := canonical.Marshal(values)
	if err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "encode generated GitHub action values")
	}
	const prefix = "data="
	if len(prefix)+len(data)+1 > maxGitHubActionDataOutputBytes {
		return e.New(e.CodeEngineCapability, "GitHub action description exceeds the bounded provider output size")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "GitHub action description canceled")
	}
	if err := appendGitHubSummary(outputPath, prefix+string(data)+"\n"); err != nil {
		return e.Wrap(e.CodeInternal, err, "publish GitHub action description")
	}
	return nil
}

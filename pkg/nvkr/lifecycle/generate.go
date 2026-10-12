// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// GenerateFiles renders or checks one hosted Run from authored lifecycle
// inputs. The generated Plan remains an inspectable internal record.
func GenerateFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, engine, root string, check bool) (string, error) {
	if err := factory.ValidateGenerationEngine(engine); err != nil {
		return "", err
	}
	plan, err := CompileFiles(ctx, definitionPath, environmentPath, generatorVersion)
	if err != nil {
		return "", err
	}
	bundle, err := factory.Render(ctx, plan, engine)
	if err != nil {
		return "", err
	}
	if check {
		if err := factory.CheckGeneratedBundle(ctx, root, bundle); err != nil {
			return "", err
		}
	} else if err := factory.WriteGeneratedBundle(ctx, root, bundle); err != nil {
		return "", err
	}
	return bundle.PlanDigest, nil
}

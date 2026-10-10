// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"sigs.k8s.io/yaml"
)

const maxDocumentBytes = int64(4 << 20)

func LoadDefinition(ctx context.Context, path string) (Definition, error) {
	var definition Definition
	if err := loadStrict(ctx, path, "definition", &definition); err != nil {
		return Definition{}, err
	}
	if err := definition.Validate(); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

func LoadEnvironment(ctx context.Context, path string) (Environment, error) {
	var environment Environment
	if err := loadStrict(ctx, path, "environment", &environment); err != nil {
		return Environment{}, err
	}
	if err := environment.Validate(); err != nil {
		return Environment{}, err
	}
	return environment, nil
}

func loadStrict(ctx context.Context, path, label string, output any) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s load requires a context", label)
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: label, MaxBytes: maxDocumentBytes})
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "%s load canceled", label)
		}
		return e.Wrap(e.CodeInvalidCatalog, err, "loading %s %s", label, path)
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxDocumentBytes)
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "%s read canceled", label)
		}
		return e.Wrap(e.CodeInvalidCatalog, err, "reading %s %s", label, path)
	}
	if err := yaml.UnmarshalStrict(raw, output); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "parsing %s %s", label, path)
	}
	return nil
}

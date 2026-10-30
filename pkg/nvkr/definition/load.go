// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

const maxDocumentBytes = int64(4 << 20)

// LoadDefinition admits a plain Definition. A composite is refused with
// engine-capability: callers that admit composites dispatch through LoadInput.
func LoadDefinition(ctx context.Context, path string) (Definition, error) {
	raw, err := readBounded(ctx, path, "definition")
	if err != nil {
		return Definition{}, err
	}
	if kind, err := DocumentKind(raw); err == nil && kind == CompositeKind {
		return Definition{}, e.New(e.CodeEngineCapability, "%s is a composite Definition; this use admits only a plain Definition", path)
	}
	var definition Definition
	if err := decodeStrict(raw, "definition", &definition); err != nil {
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
	raw, err := readBounded(ctx, path, label)
	if err != nil {
		return err
	}
	return decodeStrict(raw, label, output)
}

// DecodeDefinition admits bounded saved authored bytes through the same strict
// schema and validation as LoadDefinition. It performs no filesystem reads.
func DecodeDefinition(raw []byte) (Definition, error) {
	var definition Definition
	if err := decodeStrict(raw, "definition", &definition); err != nil {
		return Definition{}, err
	}
	if err := definition.Validate(); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

// DecodeEnvironment admits bounded saved authored bytes without filesystem reads.
func DecodeEnvironment(raw []byte) (Environment, error) {
	var environment Environment
	if err := decodeStrict(raw, "environment", &environment); err != nil {
		return Environment{}, err
	}
	if err := environment.Validate(); err != nil {
		return Environment{}, err
	}
	return environment, nil
}

func decodeStrict(raw []byte, label string, output any) error {
	if int64(len(raw)) > maxDocumentBytes {
		return e.New(e.CodeInvalidCatalog, "%s document exceeds its byte bound", label)
	}
	if err := yaml.UnmarshalStrict(raw, output); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "parsing %s", label)
	}
	return nil
}

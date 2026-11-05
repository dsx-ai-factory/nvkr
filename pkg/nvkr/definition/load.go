// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"bytes"
	"context"
	"io"
	"strings"

	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
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
	if err := requireSingleDocument(raw, label); err != nil {
		return err
	}
	if err := yaml.UnmarshalStrict(raw, output); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "parsing %s", label)
	}
	// Explicit null/empty values must not extend the old version's vocabulary.
	if environment, ok := output.(*Environment); ok && environment.APIVersion == EnvironmentAPIVersion {
		var fields map[string]any
		if err := yaml.UnmarshalStrict(raw, &fields); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "reading Environment fields")
		}
		for name, value := range fields {
			if !strings.EqualFold(name, "publication") {
				continue
			}
			if publication, ok := value.(map[string]any); ok {
				for field := range publication {
					if strings.EqualFold(field, "objectStores") {
						return e.New(e.CodeInvalidCatalog, "objectStores requires Environment v2")
					}
				}
			}
		}
	}
	return nil
}

// requireSingleDocument rejects trailing YAML documents that UnmarshalStrict
// would otherwise ignore, so concatenated authored files cannot be admitted
// under the identity of their first document.
func requireSingleDocument(raw []byte, label string) error {
	decoder := yamlv3.NewDecoder(bytes.NewReader(raw))
	// Decode into a Node rather than any: yaml.v3 builds a Node tree
	// without the pairwise duplicate-key comparison it does when filling
	// an interface, which is quadratic for large flat mappings. Only the
	// EOF check is needed here; UnmarshalStrict still validates the first
	// document from the raw bytes.
	var document yamlv3.Node
	if err := decoder.Decode(&document); err != nil {
		if err == io.EOF {
			return nil
		}
		return e.Wrap(e.CodeInvalidCatalog, err, "parsing %s", label)
	}
	if err := decoder.Decode(&document); err != io.EOF {
		return e.New(e.CodeInvalidCatalog, "%s must contain exactly one YAML document", label)
	}
	return nil
}

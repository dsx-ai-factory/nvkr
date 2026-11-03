// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"context"
	"os"
	"path/filepath"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// Input is one admitted --definition document: a plain Definition, or a
// composite whose Definition is its derived effective Definition.
type Input struct {
	// Kind is DefinitionKind or CompositeKind; the digest family follows it.
	Kind string
	// Digest is nvkr.definition for a plain Definition and
	// nvkr.composite-definition for a composite.
	Digest      string
	Definition  Definition
	Composition *Composition
}

// ReadDocumentKind reads only enough bounded bytes to dispatch a --definition
// path by authored kind. It admits nothing.
func ReadDocumentKind(ctx context.Context, path string) (string, error) {
	raw, err := readBounded(ctx, path, "definition")
	if err != nil {
		return "", err
	}
	return DocumentKind(raw)
}

// LoadInput admits a --definition path of either kind. Composite members and
// catalogs are confined to the working directory, which is the admitted input
// root, so a composite path must lie beneath it.
func LoadInput(ctx context.Context, path string) (Input, error) {
	kind, err := ReadDocumentKind(ctx, path)
	if err != nil {
		return Input{}, err
	}
	if kind == DefinitionKind {
		definition, err := LoadDefinition(ctx, path)
		if err != nil {
			return Input{}, err
		}
		digest, err := definition.Digest()
		if err != nil {
			return Input{}, err
		}
		return Input{Kind: kind, Digest: digest, Definition: definition}, nil
	}
	relative, err := WorkingRelativePath(path)
	if err != nil {
		return Input{}, err
	}
	composition, err := LoadComposite(ctx, ".", relative)
	if err != nil {
		return Input{}, err
	}
	return Input{Kind: kind, Digest: composition.Digest(), Definition: composition.Effective(), Composition: &composition}, nil
}

// WorkingRelativePath returns path as a clean slash path beneath the working
// directory, the input root used for composites and compile-input capture.
func WorkingRelativePath(path string) (string, error) {
	relative := filepath.Clean(path)
	if filepath.IsAbs(relative) {
		working, err := os.Getwd()
		if err != nil {
			return "", e.Wrap(e.CodeInvalidCatalog, err, "resolving the working directory")
		}
		if relative, err = filepath.Rel(working, relative); err != nil {
			return "", e.Wrap(e.CodeInvalidCatalog, err, "resolving %s beneath the working directory", path)
		}
	}
	if !filepath.IsLocal(relative) {
		return "", e.New(e.CodeInvalidCatalog, "composite input %q must be beneath the working directory", path)
	}
	relative = filepath.ToSlash(relative)
	if err := validateRootPath(relative); err != nil {
		return "", err
	}
	return relative, nil
}

func readBounded(ctx context.Context, path, label string) ([]byte, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "%s load requires a context", label)
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: label, MaxBytes: maxDocumentBytes})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "%s load canceled", label)
		}
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "loading %s %s", label, path)
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxDocumentBytes)
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "%s read canceled", label)
		}
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "reading %s %s", label, path)
	}
	return raw, nil
}

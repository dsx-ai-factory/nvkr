// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

func blobDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func readDocument(ctx context.Context, path string, target any, documents map[string]string, budget *int) error {
	root, err := openCatalogRoot(ctx, filepath.Dir(path))
	if err != nil {
		return configurationError(ctx, err, "opening configuration root")
	}
	defer func() { _ = root.Close() }()
	return readDocumentAt(ctx, root, path, target, documents, budget)
}

// readDocumentAt retains decoding, byte accounting and exact document integrity for a held root.
func readDocumentAt(ctx context.Context, root *catalogRoot, path string, target any, documents map[string]string, budget *int) error {
	raw, err := root.read(ctx, filepath.Base(path), 1<<20)
	if err != nil {
		return configurationError(ctx, err, "reading configuration")
	}
	*budget -= len(raw)
	if *budget < 0 {
		return usage("configuration byte budget exceeded")
	}
	if err := decodeDocument(raw, target); err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "resolving configuration path")
	}
	documents[absolute] = blobDigest(raw)
	return nil
}
func decodeDocument(raw []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return e.Wrap(e.CodeUsage, err, "decoding validation document")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return usage("validation requires exactly one document")
	}
	var walk func(*yaml.Node, int) error
	walk = func(n *yaml.Node, depth int) error {
		if depth > 64 || n.Kind == yaml.AliasNode || n.Anchor != "" || n.Tag == "!!null" {
			return usage("aliases, anchors, nulls and excessive nesting are forbidden")
		}
		if n.Kind == yaml.MappingNode {
			keys := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i]
				if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || keys[k.Value] {
					return usage("duplicate or invalid document key")
				}
				keys[k.Value] = true
			}
		}
		for _, child := range n.Content {
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(&node, 0); err != nil {
		return err
	}
	var value any
	if err := node.Decode(&value); err != nil {
		return e.Wrap(e.CodeUsage, err, "decoding validation values")
	}
	if err := exactDocumentFields(value, reflect.TypeOf(target).Elem()); err != nil {
		return err
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return e.Wrap(e.CodeUsage, err, "normalizing validation document")
	}
	strict := json.NewDecoder(bytes.NewReader(normalized))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		return e.Wrap(e.CodeUsage, err, "invalid validation fields")
	}
	return nil
}
func safeEntryPath(path string) bool {
	if !pathText(path) || filepath.IsAbs(path) || strings.Contains(path, "\\") || filepath.Clean(path) != path {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) > 17 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func readRegular(ctx context.Context, file *os.File, limit int) ([]byte, error) {
	if ctx == nil {
		return nil, usage("document read requires context")
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, usage("document must be a bounded regular file")
	}
	var out bytes.Buffer
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := file.Read(buffer)
		if out.Len()+n > limit {
			return nil, usage("document exceeds byte limit")
		}
		out.Write(buffer[:n])
		if err == io.EOF {
			return out.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// encoding/json accepts case-insensitive field aliases; authored schemas do not.
func exactDocumentFields(value any, schema reflect.Type) error {
	if value == nil {
		return usage("null document value")
	}
	switch schema.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return usage("expected document object")
		}
		fields := map[string]reflect.Type{}
		var collect func(reflect.Type)
		collect = func(t reflect.Type) {
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				if f.Anonymous {
					collect(f.Type)
					continue
				}
				name := strings.Split(f.Tag.Get("json"), ",")[0]
				if name != "" && name != "-" {
					fields[name] = f.Type
				}
			}
		}
		collect(schema)
		for name, child := range object {
			field, ok := fields[name]
			if !ok {
				return usage("unknown document field: " + name)
			}
			if err := exactDocumentFields(child, field); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return usage("expected role object")
		}
		for _, child := range object {
			if err := exactDocumentFields(child, schema.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice:
		values, ok := value.([]any)
		if !ok {
			return usage("expected document list")
		}
		for _, child := range values {
			if err := exactDocumentFields(child, schema.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package documentpath provides bounded YAML/JSON traversal with literal named selectors.
package documentpath

import (
	"bytes"
	"io"
	"regexp"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

const (
	MaxBytes        = 4 << 20
	MaxDepth        = 64
	MaxNodes        = 65536
	MaxMembers      = 1024
	MaxPathBytes    = 4096
	MaxPathSegments = 32
)

var (
	fieldPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	valuePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+%-]*$`)
)

type segment struct {
	field string
	key   string
	value string
}

// collection records the complete membership of one selector-addressed
// sequence. Multiple paths through that sequence must use the same identity key.
type collection struct {
	path      string
	key       string
	items     map[string]*yaml.Node
	accounted map[string]bool
}

// Select resolves all paths together. When complete is true, every member of each
// selected sequence must be accounted for. Failures return no partial selection.
func Select(root *yaml.Node, paths []string, complete bool) (map[string]*yaml.Node, error) {
	collections := make(map[*yaml.Node]*collection)
	result := make(map[string]*yaml.Node, len(paths))
	for _, name := range paths {
		path, err := parse(name)
		if err != nil {
			return nil, err
		}
		value, err := resolve(root, path, collections)
		if err != nil {
			return nil, err
		}
		result[name] = value
	}
	if complete {
		var unknown []string
		for _, c := range collections {
			for name := range c.items {
				if !c.accounted[name] {
					unknown = append(unknown, c.path+"["+c.key+"="+name+"]")
				}
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, e.New(e.CodeInvalidCatalog, "document has unaccounted member %q; declare every member of each selected collection", unknown[0])
		}
	}
	return result, nil
}

// Validate checks the shared path grammar without reading a document.
func Validate(path string) error {
	_, err := parse(path)
	return err
}

func parse(path string) ([]segment, error) {
	invalid := func() ([]segment, error) {
		return nil, e.New(e.CodeInvalidCatalog, "invalid document value path %q; use dotted fields and literal [key=value] selectors", path)
	}
	if len(path) == 0 || len(path) > MaxPathBytes {
		return invalid()
	}
	var segments []segment
	for rest := path; rest != ""; {
		end := strings.IndexAny(rest, ".[ ")
		if end == -1 {
			end = len(rest)
		}
		segment := segment{field: rest[:end]}
		if !fieldPattern.MatchString(segment.field) {
			return invalid()
		}
		rest = rest[end:]
		if strings.HasPrefix(rest, "[") {
			closeIndex := strings.IndexByte(rest, ']')
			if closeIndex < 0 {
				return invalid()
			}
			key, value, ok := strings.Cut(rest[1:closeIndex], "=")
			if !ok || !fieldPattern.MatchString(key) || !valuePattern.MatchString(value) {
				return invalid()
			}
			segment.key, segment.value = key, value
			rest = rest[closeIndex+1:]
		}
		segments = append(segments, segment)
		if len(segments) > MaxPathSegments {
			return invalid()
		}
		if rest == "" {
			break
		}
		if rest[0] != '.' || len(rest) == 1 {
			return invalid()
		}
		rest = rest[1:]
	}
	return segments, nil
}

func Decode(raw []byte) (*yaml.Node, error) {
	if len(raw) == 0 || int64(len(raw)) > MaxBytes {
		return nil, e.New(e.CodeInvalidCatalog, "document must contain 1 to %d bytes", MaxBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing document")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, e.New(e.CodeInvalidCatalog, "document must be an object")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, e.New(e.CodeInvalidCatalog, "document must contain exactly one YAML or JSON document")
	}
	count := 0
	if err := validateNode(document.Content[0], 0, &count); err != nil {
		return nil, err
	}
	return document.Content[0], nil
}

func validateNode(node *yaml.Node, depth int, count *int) error {
	*count++
	if depth > MaxDepth || *count > MaxNodes {
		return e.New(e.CodeInvalidCatalog, "document exceeds depth or node limit")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return e.New(e.CodeInvalidCatalog, "document cannot contain YAML anchors or aliases")
	}
	switch node.Kind {
	case yaml.MappingNode:
		if node.Tag != "!!map" {
			return e.New(e.CodeInvalidCatalog, "document has unsupported mapping tag %q", node.Tag)
		}
		seen := make(map[string]bool, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return e.New(e.CodeInvalidCatalog, "document requires string object keys")
			}
			if seen[key.Value] {
				return e.New(e.CodeInvalidCatalog, "document has duplicate key %q", key.Value)
			}
			seen[key.Value] = true
		}
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return e.New(e.CodeInvalidCatalog, "document has unsupported sequence tag %q", node.Tag)
		}
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!str", "!!bool", "!!int", "!!float", "!!null", "!!timestamp":
		default:
			return e.New(e.CodeInvalidCatalog, "document has unsupported scalar tag %q", node.Tag)
		}
	default:
		return e.New(e.CodeInvalidCatalog, "document has unsupported YAML node")
	}
	for _, child := range node.Content {
		if err := validateNode(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func resolve(root *yaml.Node, path []segment, collections map[*yaml.Node]*collection) (*yaml.Node, error) {
	current := root
	var traversed string
	for _, segment := range path {
		if traversed != "" {
			traversed += "."
		}
		traversed += segment.field
		field := Field(current, segment.field)
		if field == nil {
			return nil, e.New(e.CodeInvalidCatalog, "missing object field %q", traversed)
		}
		current = field
		if segment.key == "" {
			continue
		}
		if current.Kind != yaml.SequenceNode {
			return nil, e.New(e.CodeInvalidCatalog, "selector at %q requires a sequence", traversed)
		}
		collection := collections[current]
		if collection == nil {
			var err error
			collection, err = readCollection(current, traversed, segment.key)
			if err != nil {
				return nil, err
			}
			collections[current] = collection
		} else if collection.key != segment.key {
			return nil, e.New(e.CodeInvalidCatalog, "selectors at %q use inconsistent identity keys %q and %q", traversed, collection.key, segment.key)
		}
		current = collection.items[segment.value]
		if current == nil {
			return nil, e.New(e.CodeInvalidCatalog, "selector at %q has no member %q", traversed, segment.value)
		}
		collection.accounted[segment.value] = true
		traversed += "[" + segment.key + "=" + segment.value + "]"
	}
	return current, nil
}

func readCollection(node *yaml.Node, path, key string) (*collection, error) {
	collection := &collection{path: path, key: key, items: make(map[string]*yaml.Node), accounted: make(map[string]bool)}
	for _, item := range node.Content {
		identity := Field(item, key)
		if identity == nil || identity.Kind != yaml.ScalarNode || identity.Tag != "!!str" || identity.Value == "" || strings.TrimSpace(identity.Value) != identity.Value {
			return nil, e.New(e.CodeInvalidCatalog, "each member at %q requires a nonempty string identity %q without surrounding whitespace", path, key)
		}
		if collection.items[identity.Value] != nil {
			return nil, e.New(e.CodeInvalidCatalog, "collection at %q has duplicate %s %q", path, key, identity.Value)
		}
		collection.items[identity.Value] = item
	}
	return collection, nil
}

func Field(node *yaml.Node, field string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == field {
			return node.Content[index+1]
		}
	}
	return nil
}

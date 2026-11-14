// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

// expandCommandFiles lowers authoring shorthand before the existing strict typed
// decode. Resolved catalogs and IR retain only argv strings, never file paths.
// JSON here is parser transport, not identity encoding. Duplicate YAML keys are
// rejected before expansion; unknown fields remain for the strict typed decoder.
func expandCommandFiles(raw []byte, declaring string, providers []DataProvider) ([]byte, error) {
	converted, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing command declarations")
	}
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(converted))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "parsing command document")
	}
	changed := false
	for _, section := range []string{"sources", "components"} {
		members, _ := document[section].(map[string]any)
		for _, name := range commandKeys(members) {
			member, _ := members[name].(map[string]any)
			fields := []string{"buildCommand", "buildCommandByPlatform"}
			if section == "sources" {
				fields = []string{"vendorCommand"}
			}
			for _, field := range fields {
				commands := []any{member[field]}
				if field == "buildCommandByPlatform" {
					commands = nil
					platforms, _ := member[field].(map[string]any)
					for _, platform := range commandKeys(platforms) {
						commands = append(commands, platforms[platform])
					}
				}
				for _, command := range commands {
					argv, _ := command.([]any)
					for i, operand := range argv {
						if _, ok := operand.(string); ok {
							continue
						}
						reference, ok := operand.(map[string]any)
						if !ok {
							return nil, e.New(e.CodeInvalidCatalog, "%s %q %s operand must be a string or {file: path}", section, name, field)
						}
						file, ok := reference["file"].(string)
						if !ok || len(reference) != 1 {
							return nil, e.New(e.CodeInvalidCatalog, "%s %q %s operand must be a string or {file: path}", section, name, field)
						}
						body, err := readCommandFile(declaring, file, providers)
						if err != nil {
							return nil, err
						}
						argv[i] = string(body)
						changed = true
					}
				}
			}
		}
	}
	if !changed {
		return raw, nil
	}
	result, err := json.Marshal(document)
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "expanding command files")
	}
	return result, nil
}

func commandKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func readCommandFile(declaring, name string, providers []DataProvider) ([]byte, error) {
	// Validate the operand BEFORE joining: fragment imports admit leading ../,
	// but an operand must stay beneath its declaring document's directory.
	if err := validateCatalogPath(name); err != nil {
		return nil, err
	}
	if strings.ContainsAny(name, ":\x00") {
		return nil, e.New(e.CodeInvalidCatalog, "unsafe command file path %q", name)
	}
	if len(providers) != 1 {
		return nil, e.New(e.CodeEngineCapability, "command files require a context-bound provider")
	}
	reader, ok := providers[0].(*boundContextProvider)
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "command files require a context-bound provider")
	}
	resolved := path.Join(path.Dir(declaring), name)
	var raw []byte
	var err error
	if strings.HasPrefix(resolved, "../") {
		raw, err = reader.readFragment(resolved)
	} else {
		raw, err = reader.ReadFile(resolved)
	}
	if err != nil {
		return nil, normalizeCatalogContextError(reader.ctx,
			e.Wrap(e.CodeInvalidCatalog, err, "reading command file %q", resolved), "command file read")
	}
	if !utf8.Valid(raw) {
		return nil, e.New(e.CodeInvalidCatalog, "command file %q must contain UTF-8 bytes", resolved)
	}
	return raw, nil
}

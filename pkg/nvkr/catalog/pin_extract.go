// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"sort"

	"github.com/dsx-ai-factory/nvkr/internal/documentpath"
	"github.com/dsx-ai-factory/nvkr/internal/versionpolicy"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

const (
	maxPinSetMembers       = documentpath.MaxMembers
	maxPinSetDocumentDepth = documentpath.MaxDepth
	maxPinSetDocumentNodes = documentpath.MaxNodes
	maxPinSetPathBytes     = documentpath.MaxPathBytes
	maxPinSetPathSegments  = documentpath.MaxPathSegments
)

func validatePinSetValuePath(path string) error           { return documentpath.Validate(path) }
func decodePinSetDocument(raw []byte) (*yaml.Node, error) { return documentpath.Decode(raw) }
func pinSetObjectField(node *yaml.Node, field string) *yaml.Node {
	return documentpath.Field(node, field)
}
func exactPinSetVersion(version string) bool { return versionpolicy.PinSet(version) }
func extractPinSetVersions(raw []byte, members map[string]PinSetMember) (map[string]string, error) {
	if len(members) == 0 || len(members) > maxPinSetMembers {
		return nil, e.New(e.CodeInvalidCatalog, "pin set must declare between 1 and %d members", maxPinSetMembers)
	}
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, members[name].Path)
	}
	root, err := documentpath.Decode(raw)
	if err != nil {
		return nil, err
	}
	selected, err := documentpath.Select(root, paths, true)
	if err != nil {
		return nil, err
	}
	versions := make(map[string]string, len(members))
	for _, name := range names {
		value := selected[members[name].Path]
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || !exactPinSetVersion(value.Value) {
			return nil, e.New(e.CodeInvalidCatalog, "pin set member %q at %q must contain an exact string version", name, members[name].Path)
		}
		versions[name] = value.Value
	}
	return versions, nil
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskinspect

import (
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type Component struct {
	BOMRef     string     `json:"bom-ref,omitempty"`
	Type       string     `json:"type"`
	Name       string     `json:"name"`
	Version    string     `json:"version,omitempty"`
	PURL       string     `json:"purl,omitempty"`
	Hashes     []Hash     `json:"hashes,omitempty"`
	Properties []Property `json:"properties,omitempty"`
}

type Hash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

type Property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func ValidateComponent(field string, component Component) error {
	if (component.Type != "application" && component.Type != "library" && component.Type != "container" && component.Type != "operating-system" && component.Type != "file") || !canonicalText(component.Name) {
		return e.New(e.CodeInvalidCatalog, "%s is not a canonical supported component", field)
	}
	if component.Type == "file" && len(component.Hashes) != 1 {
		return e.New(e.CodeUnverifiedRef, "%s file has no exact content hash", field)
	}
	if component.Version != "" && !canonicalText(component.Version) {
		return e.New(e.CodeInvalidCatalog, "%s has a non-canonical version", field)
	}
	if component.PURL != "" && (!canonicalText(component.PURL) || strings.ContainsAny(component.PURL, " \t\r\n")) {
		return e.New(e.CodeInvalidCatalog, "%s has a non-canonical purl", field)
	}
	if len(component.Hashes) > 1 {
		return e.New(e.CodeInvalidCatalog, "%s has ambiguous duplicate hash identities", field)
	}
	for _, hash := range component.Hashes {
		if hash.Alg != "SHA-256" || !canonical.IsDigest("sha256:"+hash.Content) {
			return e.New(e.CodeUnverifiedRef, "%s has a non-canonical SHA-256 identity", field)
		}
	}
	return ValidateProperties(field+".properties", component.Properties)
}

func ValidateProperties(field string, properties []Property) error {
	previous := ""
	for i, property := range properties {
		if !canonicalText(property.Name) || !canonicalText(property.Value) {
			return e.New(e.CodeInvalidCatalog, "%s[%d] has a non-canonical name or value", field, i)
		}
		if i > 0 && property.Name <= previous {
			return e.New(e.CodeInvalidCatalog, "%s must be strictly sorted and duplicate-free", field)
		}
		previous = property.Name
	}
	return nil
}

// Scanner describes the executable selection; opening it authenticates its bytes.
type Scanner struct{ Path, Digest string }

type DependencyInventory struct {
	Components    []Component
	Method        string
	ScannerDigest string
}

const MaxContentReportBytes = 16 << 20

func cloneComponent(component Component) Component {
	copy := component
	copy.Hashes = append([]Hash(nil), component.Hashes...)
	copy.Properties = append([]Property(nil), component.Properties...)
	return copy
}

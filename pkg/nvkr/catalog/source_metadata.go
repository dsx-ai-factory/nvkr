// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template"

	"github.com/Masterminds/semver/v3"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/sourceenv"
)

// SourceMetadataEnv renders opt-in environment bindings from this producer's
// source pin. The declarations survive resolved overlays and exact-source
// replacement; rendered values enter logical Build identity as environment.
func (c Component) SourceMetadataEnv() (map[string]string, error) {
	for _, name := range slices.Sorted(maps.Keys(c.VersionStamp)) {
		if sourceenv.Reserved(name) {
			return nil, e.New(e.CodeInvalidCatalog, "versionStamp cannot override reserved source metadata binding %q", name)
		}
	}
	if len(c.SourceMetadata) == 0 {
		return nil, nil
	}
	if !c.FromSource || c.Prebuilt != nil || (c.BuilderType != "" && c.BuilderType != BuilderOCIHermetic && c.BuilderType != BuilderBuildKit && c.BuilderType != BuilderNativeGo) {
		return nil, e.New(e.CodeInvalidCatalog, "sourceMetadata requires a source command producer with a build environment")
	}
	if len(c.SourceMetadata) > 32 {
		return nil, e.New(e.CodeInvalidCatalog, "sourceMetadata admits at most 32 bindings")
	}
	data := sourceMetadataData{stampData: stampContext(Pin{Tag: c.Tag, Commit: c.Commit, TreeHash: c.TreeHash, SourceDateEpoch: c.SourceDate})}
	result := make(map[string]string, len(c.SourceMetadata))
	for _, name := range slices.Sorted(maps.Keys(c.SourceMetadata)) {
		value := c.SourceMetadata[name]
		if !sourceenv.ValidName(name) || len(value) > 4096 {
			return nil, e.New(e.CodeInvalidCatalog, "invalid or oversized source metadata binding %q", name)
		}
		t, err := template.New(name).Option("missingkey=error").Parse(value)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "source metadata %q", name)
		}
		var out metadataBuffer
		if err := t.Execute(&out, data); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "source metadata %q", name)
		}
		if strings.ContainsRune(out.String(), '\x00') {
			return nil, e.New(e.CodeInvalidCatalog, "source metadata %q contains NUL", name)
		}
		result[name] = out.String()
	}
	return result, nil
}

type sourceMetadataData struct{ stampData }

// ReleaseVersion deliberately differs from legacy Version: a commit-only pin
// cannot be interpreted as a release. Such a producer must declare its stream
// explicitly and check it against its pinned chart during packaging.
func (d sourceMetadataData) ReleaseVersion() (string, error) {
	if d.Tag == d.Commit || fullSHA.MatchString(d.Tag) {
		return "", fmt.Errorf("commit-only source requires explicit stream metadata")
	}
	if _, err := semver.StrictNewVersion(d.Version); err != nil {
		return "", fmt.Errorf("source tag has no exact semantic release version: %w", err)
	}
	return d.Version, nil
}

type metadataBuffer struct{ strings.Builder }

func (b *metadataBuffer) Write(p []byte) (int, error) {
	if len(p) > 4096-b.Len() {
		return 0, fmt.Errorf("source metadata value exceeds 4096 bytes")
	}
	return b.Builder.Write(p)
}

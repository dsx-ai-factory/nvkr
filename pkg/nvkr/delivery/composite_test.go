// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func compositeReleaseRecord(t *testing.T) (*definition.CompositionRecord, string) {
	t.Helper()
	record := &definition.CompositionRecord{Kind: definition.CompositeKind, Metadata: definition.Metadata{Name: "product"},
		Members:                   []definition.MemberIdentity{{Name: "charts", Digest: "sha256:" + strings.Repeat("1", 64)}},
		EffectiveDefinitionDigest: "sha256:" + strings.Repeat("2", 64),
		Outputs:                   []definition.OutputAttribution{{Output: "chart", Member: "charts"}}}
	digest, err := definition.CompositeDigest(record.Metadata, record.Members)
	if err != nil {
		t.Fatal(err)
	}
	return record, digest
}

func TestCompositePublicationRootContract(t *testing.T) {
	plainLayout, _ := helmPublicationFixture(t, "", "")
	plain, err := Inspect(t.Context(), plainLayout)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(plain.Root)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Root.SchemaVersion != PublicationRootSchema || bytes.Contains(raw, []byte(`"composition"`)) {
		t.Fatal("plain Release root changed its v4 schema or bytes")
	}

	record, compositeDigest := compositeReleaseRecord(t)
	layout, _ := helmPublicationFixtureWith(t, "", "", func(release *ReleaseMetadata) {
		release.DefinitionDigest, release.Composition = compositeDigest, record
	})
	composite, err := Inspect(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	if composite.Root.SchemaVersion != PublicationRootCompositeSchema || !composite.Root.Release.Composition.Equal(record) {
		t.Fatalf("composite root = %s %+v", composite.Root.SchemaVersion, composite.Root.Release.Composition)
	}
	description, err := InspectMetadataDescription(t.Context(), layout, "registry.example/product/releases@"+composite.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	if member, ok := description.Metadata.Composition.MemberOf("chart"); !ok || member != "charts" {
		t.Fatal("release description export omits per-output member attribution")
	}

	for _, tc := range []struct {
		name   string
		mutate func(root *PublicationRoot)
	}{
		{"v4 reader rejects composite major", func(root *PublicationRoot) { root.SchemaVersion = PublicationRootSchema }},
		{"composite major without record", func(root *PublicationRoot) { root.Release.Composition = nil }},
		{"attribution to unlisted member", func(root *PublicationRoot) { root.Release.Composition.Outputs[0].Member = "other" }},
		{"missing attribution", func(root *PublicationRoot) { root.Release.Composition.Outputs = nil }},
		{"extra member", func(root *PublicationRoot) {
			root.Release.Composition.Members = append(root.Release.Composition.Members, definition.MemberIdentity{Name: "extra", Digest: "sha256:" + strings.Repeat("3", 64)})
		}},
		{"tampered effective digest", func(root *PublicationRoot) { root.Release.Composition.EffectiveDefinitionDigest = "sha256:short" }},
		{"plain definition digest", func(root *PublicationRoot) { root.Release.DefinitionDigest = record.EffectiveDefinitionDigest }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := composite.Root
			release := *root.Release
			release.Composition = record.Clone()
			root.Release = &release
			tc.mutate(&root)
			var coded *e.Error
			if err := root.Validate(); !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("want %s; got %v", e.CodeVerifyFailed, err)
			}
		})
	}
}

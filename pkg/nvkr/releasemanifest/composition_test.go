// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestManifestBindsCompleteComposition(t *testing.T) {
	for _, mode := range []string{"complete", "missing attribution", "changed digest", "unknown member"} {
		t.Run(mode, func(t *testing.T) {
			evidence := evidenceFixture(t)
			record := &definition.CompositionRecord{Kind: definition.CompositeKind, Metadata: definition.Metadata{Name: "composite"},
				EffectiveDefinitionDigest: fixtureDigest, Members: []definition.MemberIdentity{{Name: "family", Digest: fixtureDigest}}}
			for _, output := range evidence.ArtifactChecks.Outputs {
				record.Outputs = append(record.Outputs, definition.OutputAttribution{Output: output.Name, Member: "family"})
			}
			digest, err := definition.CompositeDigest(record.Metadata, record.Members)
			if err != nil {
				t.Fatal(err)
			}
			evidence.ArtifactChecks.Inputs.DefinitionDigest = digest
			evidence.ArtifactChecks.Composition = record
			evidence.ToolSmoke.ArtifactChecks = evidence.ArtifactChecks
			for i := range evidence.ToolSmoke.Workers {
				evidence.ToolSmoke.Workers[i].ArtifactChecks = evidence.ArtifactChecks
			}
			manifest, err := fromEvidence(evidence, membersFixture(evidence))
			if err != nil {
				t.Fatal(err)
			}
			if !manifest.Composition.Equal(record) {
				t.Fatal("manifest lost composition")
			}
			record.Members[0].Name = "mutated"
			if manifest.Composition.Members[0].Name != "family" {
				t.Fatal("manifest aliases input record")
			}
			switch mode {
			case "missing attribution":
				manifest.Composition.Outputs = manifest.Composition.Outputs[1:]
			case "changed digest":
				manifest.Inputs.DefinitionDigest = fixtureDigest
			case "unknown member":
				manifest.Composition.Outputs[0].Member = "other"
			}
			raw, err := manifest.Canonical()
			if mode != "complete" {
				requireCode(t, err, e.CodeUnverifiedRef)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(raw)
			if err != nil || !decoded.Composition.Equal(manifest.Composition) {
				t.Fatalf("composition round trip: %v", err)
			}
		})
	}
}

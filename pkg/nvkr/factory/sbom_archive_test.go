// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func archiveAssetPlan(t *testing.T) *Plan {
	t.Helper()
	maintained, err := LoadPlanContext(t.Context(), "../../../.github/nvkr-run-nvke-nightly-arm64/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	var base catalog.Catalog
	for _, build := range maintained.Builds {
		if build.Name == "nicosim-tools-arm64" {
			base = *build.CatalogSnapshot
		}
	}
	base.Tools = map[string]catalog.ToolPin{"syft": maintained.CatalogSnapshot.Tools["syft"]}
	stack, err := catalog.ApplyStack(&base)
	if err != nil {
		t.Fatal(err)
	}
	definition := authored.Definition{APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind, Metadata: authored.Metadata{Name: "archive-assets"}, Catalog: authored.Catalog{Base: "catalog"}, SBOMTool: "syft", Outputs: []authored.DefinitionOutput{{Name: "assets", Platform: "linux/arm64", Format: authored.OutputExecutable, Components: []string{"nico-sim-runtime-assets"}}}}
	environment := authored.Environment{APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind, Metadata: authored.Metadata{Name: "local"}, UtilityContainer: fixtureUtilityContainer, UtilityPlatform: "linux/amd64"}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestArchiveSBOMSelectionRequiresExactSourceExecutableMember(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Plan, *Artifact)
		kind   string
		code   e.Code
	}{
		{"source executable asset", func(*Plan, *Artifact) {}, "file-archive", ""},
		{"public Helm", func(p *Plan, _ *Artifact) { p.Definition.Outputs[0].Format = authored.OutputHelm }, "tarball", ""},
		{"OCI wrapper", func(p *Plan, _ *Artifact) { p.Definition.Outputs[0].Format = authored.OutputOCI }, "tarball", ""},
		{"disk wrapper", func(p *Plan, _ *Artifact) { p.Definition.Outputs[0].Format = authored.OutputDiskImage }, "tarball", ""},
		{"declared embedded Helm", func(p *Plan, _ *Artifact) {
			p.Builds[0].ResolvedComponents[0].Helm = &catalog.HelmPackaging{Chart: "."}
		}, "tarball", ""},
		{"acquired tarball", func(p *Plan, _ *Artifact) { p.Builds[0].ResolvedComponents[0].FromSource = false }, "", e.CodeEngineCapability},
		{"wrong resolved type", func(p *Plan, _ *Artifact) { p.Builds[0].ResolvedComponents[0].OutputType = "host-package" }, "", e.CodeEngineCapability},
		{"different producer", func(_ *Plan, a *Artifact) { a.Producer = "other" }, "", e.CodeEngineCapability},
		{"different artifact", func(_ *Plan, a *Artifact) { a.ID = "other" }, "", e.CodeEngineCapability},
		{"resolved component mismatch", func(p *Plan, _ *Artifact) { p.Builds[0].ResolvedComponents[0].Name = "other" }, "", e.CodeEngineCapability},
		{"producer target mismatch", func(p *Plan, a *Artifact) {
			for i := range p.Graph.Nodes {
				if p.Graph.Nodes[i].ID == a.Producer {
					p.Graph.Nodes[i].BuildTarget = "other"
				}
			}
		}, "", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := archiveAssetPlan(t)
			_, artifacts := planIndex(plan)
			payload := artifacts["artifact/assets/nico-sim-runtime-assets"]
			test.change(plan, &payload)
			kind, err := plan.sbomPayloadKind(payload)
			if test.code != "" {
				assertFactoryErrorCode(t, err, test.code)
			} else if err != nil || kind != test.kind {
				t.Fatalf("kind=%s error=%v", kind, err)
			}
		})
	}
}

func archiveAssetBytes(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	for _, entry := range []struct{ name, body string }{{"internal/seed/default-seed.yaml", "nodes: []\n"}, {"ui/app.js", "compiled UI\n"}} {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Size: int64(len(entry.body)), Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestArchiveSBOMExecutorAndTerminalPublicationKeepMethodsBound(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Plan, *sbom.BOM)
		code   e.Code
	}{
		{"complete", func(*Plan, *sbom.BOM) {}, ""},
		{"Helm substitution", func(_ *Plan, b *sbom.BOM) {
			b.Components = []sbom.Component{{Type: "application", Name: "invented-chart", Version: "1", PURL: "pkg:helm/invented-chart@1", BOMRef: "pkg:helm/invented-chart@1"}}
			b.Dependencies[0].DependsOn = []string{"pkg:helm/invented-chart@1"}
			for i := range b.Metadata.Props {
				if b.Metadata.Props[i].Name == "nvkr:dependencyInspection" {
					b.Metadata.Props[i].Value = "helm-manifests/v1"
				}
			}
		}, e.CodeVerifyFailed},
		{"Syft substitution", func(p *Plan, b *sbom.BOM) {
			b.Components = []sbom.Component{{Type: "library", Name: "invented-package", Version: "1", PURL: "pkg:npm/invented-package@1", BOMRef: "pkg:npm/invented-package@1"}}
			b.Dependencies[0].DependsOn = []string{"pkg:npm/invented-package@1"}
			for i := range b.Metadata.Props {
				if b.Metadata.Props[i].Name == "nvkr:dependencyInspection" {
					b.Metadata.Props[i].Value = "syft-cyclonedx/v1"
				}
			}
			b.Metadata.Props = append(b.Metadata.Props, sbom.Property{Name: "nvkr:dependencyScannerDigest", Value: toolExecutableDigest(p, p.Definition.SBOMTool)})
		}, e.CodeVerifyFailed},
		{"archive for expected Helm", func(p *Plan, _ *sbom.BOM) { p.Definition.Outputs[0].Format = authored.OutputHelm }, e.CodeVerifyFailed},
		{"scanner substitution", func(_ *Plan, b *sbom.BOM) {
			b.Metadata.Props = append(b.Metadata.Props, sbom.Property{Name: "nvkr:dependencyScannerDigest", Value: "sha256:" + strings.Repeat("a", 64)})
		}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := archiveAssetPlan(t)
			t.Chdir(t.TempDir())
			_, artifacts := planIndex(plan)
			node := planNode(plan, "assets/sbom")
			body := archiveAssetBytes(t)
			payload := artifacts["artifact/assets/nico-sim-runtime-assets"]
			if err := writeFactoryFile(t.Context(), payload.Path, body, 0o644); err != nil {
				t.Fatal(err)
			}
			result := pipeline.ArtifactResult{Component: payload.Component, SHA256: pipeline.FileDigest(body), Size: int64(len(body))}
			encoded, err := canonical.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFactoryFile(t.Context(), artifacts["result/assets/nico-sim-runtime-assets"].Path, encoded, 0o644); err != nil {
				t.Fatal(err)
			}
			// Syft remains a declared graph input; this reader never opens or
			// executes it and never claims a scanner digest or package discovery.
			if err := executeSBOM(t.Context(), plan, node, artifacts, io.Discard); err != nil {
				t.Fatal(err)
			}
			set := artifacts[node.Outputs[0]]
			local := func(filename string) (string, error) { return filename, nil }
			objects, err := sbomPublicationObjects(t.Context(), plan, set, artifacts, local)
			if err != nil || len(objects) != 2 {
				t.Fatalf("complete archive finalization: objects=%d error=%v", len(objects), err)
			}
			var bom sbom.BOM
			if err := canonical.UnmarshalExact(bytes.TrimSpace(objects[1].Bytes), &bom); err != nil {
				t.Fatal(err)
			}
			if len(bom.Components) != 2 || bom.Metadata.Component.Type != "file" || "sha256:"+bom.Metadata.Component.Hashes[0].Content != result.SHA256 {
				t.Fatalf("wrong archive inventory or subject: %+v", bom)
			}
			test.change(plan, &bom)
			if test.name == "scanner substitution" {
				assertFactoryErrorCode(t, bom.Validate(), test.code)
			} else if err := bom.Validate(); err != nil {
				t.Fatalf("method-substitution fixture must pass descriptive validation: %v", err)
			}
			// Persist even caller-invented descriptions with a matching typed
			// index digest. Terminal admission must independently reject them.
			encoded, err = canonical.Marshal(bom)
			if err != nil {
				t.Fatal(err)
			}
			var index sbom.Index
			if err := canonical.UnmarshalExact(bytes.TrimSpace(objects[0].Bytes), &index); err != nil {
				t.Fatal(err)
			}
			index.Entries[0].SBOMDigest, err = canonical.Digest(canonical.KindSBOM, bom)
			if err != nil {
				t.Fatal(err)
			}
			indexRaw, err := canonical.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(set.Path, payload.Component+".cdx.json"), append(encoded, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(set.Path, "index.json"), append(indexRaw, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = sbomPublicationObjects(t.Context(), plan, set, artifacts, local)
			if test.code != "" {
				assertFactoryErrorCode(t, err, test.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package sbom emits a deterministic CycloneDX SBOM for a resolved build: a single
// document covering every component's source provenance (repo, pinned commit, tree
// hash, toolchain digest, version) and, when a build has run, the produced artifact
// digests (SRD §6.7). It contains no timestamps or random values, so the same build
// always yields byte-identical bytes.
package sbom

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

const (
	bomFormat   = "CycloneDX"
	specVersion = "1.5"
)

type BOM struct {
	BOMFormat    string   `json:"bomFormat"`
	SpecVersion  string   `json:"specVersion"`
	SerialNumber string   `json:"serialNumber"`
	Version      int      `json:"version"`
	Metadata     Metadata `json:"metadata"`
	// omitempty so a per-artifact SBOM (subject only in metadata.component) omits the
	// key entirely rather than emitting an empty array — and so a nil vs. len-0 slice
	// (e.g. from cloneBOM) canonicalizes identically.
	Components   []Component  `json:"components,omitempty"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
}

type Metadata struct {
	Component Component  `json:"component"`
	Tools     []ToolRef  `json:"tools"`
	Props     []Property `json:"properties,omitempty"`
}

type ToolRef struct {
	Vendor string `json:"vendor"`
	Name   string `json:"name"`
}

type Dependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn"`
}

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

func fromBuild(res *buildgraph.Resolved, buildDigest string, artifactHashes map[string]string,
	transportIndependent bool,
) *BOM {
	bom := &BOM{
		BOMFormat:    bomFormat,
		SpecVersion:  specVersion,
		SerialNumber: deriveURN(buildDigest),
		Version:      1,
		Metadata: Metadata{
			Component: Component{Type: "application", Name: "nvkr-build", Version: buildDigest},
			Tools:     []ToolRef{{Vendor: "NVIDIA", Name: "nvkr"}},
			Props:     []Property{{Name: "nvkr:buildDigest", Value: buildDigest}},
		},
	}

	comps := append([]catalog.NamedComponent(nil), res.Components...)
	sort.Slice(comps, func(i, j int) bool { return comps[i].Name < comps[j].Name })
	for _, nc := range comps {
		bom.Components = append(bom.Components,
			componentForEvidence(nc, artifactHashes[nc.Name], target.Arch(res.Platform),
				externalEvidenceDeclarationProps(nc.Prebuilt), transportIndependent))
	}
	return bom
}

// fromComponentForArch builds a per-artifact SBOM bound to one component's artifact digest
// (ADR-13 §1 / FR-15): a CycloneDX document whose subject is that single artifact.
// Its serial number is derived from buildDigest + the component name, so it is unique
// per artifact yet reproducible (no random UUID, no timestamp).
func fromComponentForArch(nc catalog.NamedComponent, buildDigest, artifactHash, arch string) *BOM {
	metadataProps := []Property{
		{Name: "nvkr:buildDigest", Value: buildDigest},
		{Name: "nvkr:artifact", Value: nc.Name},
	}
	sort.Slice(metadataProps, func(i, j int) bool { return metadataProps[i].Name < metadataProps[j].Name })
	return &BOM{
		BOMFormat:    bomFormat,
		SpecVersion:  specVersion,
		SerialNumber: deriveURN(buildDigest + "/" + nc.Name),
		Version:      1,
		Metadata: Metadata{
			Component: componentForArch(nc, artifactHash, arch),
			Tools:     []ToolRef{{Vendor: "NVIDIA", Name: "nvkr"}},
			Props:     metadataProps,
		},
		// A per-artifact SBOM's subject lives only in metadata.component (idiomatic
		// CycloneDX). components[] is reserved for the subject's constituents/
		// dependencies, populated separately by artifact inspection. The declaration-only
		// form stays empty rather than restating the subject as its own component.
	}
}

// IndexEntry links one per-artifact SBOM to its subject by digest.
type IndexEntry struct {
	Artifact    string `json:"artifact"`
	SBOMDigest  string `json:"sbomDigest"`
	ArtifactSHA string `json:"artifactSha256,omitempty"`
}

// Index is the aggregate index over per-artifact SBOMs (ADR-13 §1). Its own digest is
// an aggregate evidence digest.
type Index struct {
	SchemaVersion string       `json:"schemaVersion"`
	BuildDigest   string       `json:"buildDigest"`
	Entries       []IndexEntry `json:"entries"`
}

const IndexSchemaVersion = "nvkr.dev/sbom-index/v2"

// PerArtifact builds one SBOM per from-source/prebuilt component (bound to its
// artifact digest) plus the aggregate index linking them, all deterministic.
// These documents describe observed composition. Publisher-qualified flows use
// BuildVerifiedFromReport and its PerArtifact method to obtain evidence receipts.
func PerArtifact(res *buildgraph.Resolved, buildDigest string, artifactHashes map[string]string) (map[string]*BOM, *Index, error) {
	comps := append([]catalog.NamedComponent(nil), res.Components...)
	sort.Slice(comps, func(i, j int) bool { return comps[i].Name < comps[j].Name })
	boms := make(map[string]*BOM, len(comps))
	idx := &Index{SchemaVersion: IndexSchemaVersion, BuildDigest: buildDigest}
	for _, nc := range comps {
		if !nc.FromSource && nc.Prebuilt == nil {
			continue // an exclusion has no artifact SBOM
		}
		b := fromComponentForArch(nc, buildDigest, artifactHashes[nc.Name], target.Arch(res.Platform))
		d, err := b.Digest()
		if err != nil {
			return nil, nil, err
		}
		boms[nc.Name] = b
		idx.Entries = append(idx.Entries, IndexEntry{Artifact: nc.Name, SBOMDigest: d, ArtifactSHA: artifactHashes[nc.Name]})
	}
	return boms, idx, nil
}

// Validate enforces the semantic identity of a per-artifact SBOM index. An index
// without exact subject and evidence digests is a declaration, not release evidence.
func (ix *Index) Validate() error {
	if ix == nil {
		return e.New(e.CodeProvenanceMissing, "SBOM index is required")
	}
	if ix.SchemaVersion != IndexSchemaVersion {
		return e.New(e.CodeInvalidCatalog, "SBOM index schemaVersion %q is unsupported", ix.SchemaVersion)
	}
	if !canonical.IsDigest(ix.BuildDigest) {
		return e.New(e.CodeUnverifiedRef, "SBOM index buildDigest %q is not an exact lowercase sha256 identity", ix.BuildDigest)
	}
	if len(ix.Entries) == 0 {
		return e.New(e.CodeProvenanceMissing, "SBOM index has no artifact entries")
	}
	previous := ""
	for i, entry := range ix.Entries {
		if !canonicalText(entry.Artifact) {
			return e.New(e.CodeInvalidCatalog, "SBOM index entry %d has a non-canonical artifact name", i)
		}
		if i > 0 && entry.Artifact <= previous {
			return e.New(e.CodeInvalidCatalog, "SBOM index artifacts must be strictly sorted and duplicate-free")
		}
		previous = entry.Artifact
		if !canonical.IsDigest(entry.SBOMDigest) || !canonical.IsDigest(entry.ArtifactSHA) {
			return e.New(e.CodeUnverifiedRef,
				"SBOM index artifact %q does not bind exact SBOM and artifact sha256 identities", entry.Artifact)
		}
	}
	return nil
}

// Digest is the SBOM index's canonical, semantically validated content digest.
func (ix *Index) Digest() (string, error) {
	if err := ix.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindSBOMIndex, ix)
}

func componentForArch(nc catalog.NamedComponent, artifactHash, arch string) Component {
	return componentForEvidence(nc, artifactHash, arch,
		externalEvidenceDeclarationProps(nc.Prebuilt), false)
}

func componentForWithExternalEvidence(nc catalog.NamedComponent, artifactHash, arch string, evidenceProps []Property) Component {
	return componentForEvidence(nc, artifactHash, arch, evidenceProps, false)
}

// componentForPublisherEvidence removes source/toolchain transport locators from
// locally generated publisher evidence. Build identity deliberately ignores those
// locators, so retaining them would let an online-to-mirror relocation perturb an
// SBOM and the publisher-evidence aggregate for identical content.
func componentForPublisherEvidence(nc catalog.NamedComponent, artifactHash, arch string,
	evidenceProps []Property,
) Component {
	return componentForEvidence(nc, artifactHash, arch, evidenceProps, true)
}

func componentForEvidence(nc catalog.NamedComponent, artifactHash, arch string,
	evidenceProps []Property, transportIndependent bool,
) Component {
	// A prebuilt (no-compile) component is recorded honestly as acquired, carrying its
	// externally verified evidence state or an explicit evidence:none (ADR-13 §2) —
	// never claimed as from-source and never conflated with a Tier-4 exclusion.
	if nc.Prebuilt != nil {
		props := []Property{
			{Name: "nvkr:fromSource", Value: "false"},
			{Name: "nvkr:lane", Value: "prebuilt"},
			{Name: "nvkr:outputType", Value: nc.OutputType},
		}
		props = append(props, evidenceProps...)
		sort.Slice(props, func(i, j int) bool { return props[i].Name < props[j].Name })
		c := Component{Type: "application", Name: nc.Name, Version: catalog.NormalizeVersion(nc.Tag), Properties: props}
		if artifactHash != "" {
			c.Hashes = []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(artifactHash, "sha256:")}}
		}
		return c
	}
	if !nc.FromSource {
		// A declared Tier-4 exclusion is recorded, never claimed as from-source.
		component := Component{
			Type: "application", Name: nc.Name,
			Properties: []Property{
				{Name: "nvkr:fromSource", Value: "false"},
				{Name: "nvkr:exclusionReason", Value: nc.ExclusionReason},
			},
		}
		sort.Slice(component.Properties, func(i, j int) bool { return component.Properties[i].Name < component.Properties[j].Name })
		return component
	}
	sourceProperty := Property{Name: "nvkr:repo", Value: nc.Repo}
	toolchain := selectedToolchain(nc, arch)
	purlValue := purl(nc.Repo, nc.Commit)
	if transportIndependent {
		sourceProperty = Property{Name: "nvkr:source", Value: "nvkr:source/" + nc.Name}
		toolchain = contentDigestIdentity(toolchain)
		purlValue = ""
	}
	props := []Property{
		{Name: "nvkr:fromSource", Value: "true"},
		sourceProperty,
		{Name: "nvkr:commit", Value: nc.Commit},
		{Name: "nvkr:treeHash", Value: nc.TreeHash},
		{Name: "nvkr:toolchain", Value: toolchain},
		{Name: "nvkr:outputType", Value: nc.OutputType},
	}
	sort.Slice(props, func(i, j int) bool { return props[i].Name < props[j].Name })
	c := Component{
		Type:       "application",
		Name:       nc.Name,
		Version:    catalog.NormalizeVersion(nc.Tag),
		PURL:       purlValue,
		Properties: props,
	}
	if artifactHash != "" {
		c.Hashes = []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(artifactHash, "sha256:")}}
	}
	return c
}

func selectedToolchain(nc catalog.NamedComponent, arch string) string {
	if nc.BuilderType == ir.BuilderNix && nc.Flake != nil && nc.Flake.Image != "" {
		return nc.Flake.Image
	}
	if arch != "" {
		if toolchain := nc.ToolchainImages[arch]; toolchain != "" {
			return toolchain
		}
	}
	if nc.ToolchainImage != "" {
		return nc.ToolchainImage
	}
	if len(nc.ToolchainImages) == 1 {
		for _, toolchain := range nc.ToolchainImages {
			return toolchain
		}
	}
	return ""
}

func contentDigestIdentity(reference string) string {
	if separator := strings.LastIndex(reference, "@sha256:"); separator >= 0 {
		return reference[separator+1:]
	}
	return reference
}

// externalEvidenceDeclarationProps deliberately does not copy a catalog reference
// into an SBOM as if it had been verified. The release-qualified constructor reads
// and hashes those bytes first, then supplies verified properties through
// componentForWithExternalEvidence. Legacy callers therefore produce an honest
// non-authorizing state instead of a forged evidence receipt.
func externalEvidenceDeclarationProps(prebuilt *catalog.Prebuilt) []Property {
	if prebuilt == nil {
		return nil
	}
	ev := prebuilt.Evidence
	if ev != nil && ev.None {
		return []Property{{Name: "nvkr:externalEvidence", Value: "none"}}
	}
	return []Property{{Name: "nvkr:externalEvidence", Value: "unverified-declaration"}}
}

// Validate enforces the deterministic NVKR CycloneDX profile before bytes acquire a
// digest. It deliberately accepts an aggregate diagnostic SBOM with missing artifact
// hashes, but a per-artifact document must bind its exact subject hash.
func (b *BOM) Validate() error {
	if b == nil {
		return e.New(e.CodeProvenanceMissing, "SBOM is required")
	}
	if b.BOMFormat != bomFormat || b.SpecVersion != specVersion || b.Version != 1 {
		return e.New(e.CodeInvalidCatalog, "SBOM does not use the supported CycloneDX %s profile", specVersion)
	}
	if !validSerialNumber(b.SerialNumber) {
		return e.New(e.CodeInvalidCatalog, "SBOM serialNumber %q is not a canonical deterministic UUID URN", b.SerialNumber)
	}
	if len(b.Metadata.Tools) != 1 || b.Metadata.Tools[0] != (ToolRef{Vendor: "NVIDIA", Name: "nvkr"}) {
		return e.New(e.CodeInvalidCatalog, "SBOM must identify exactly the nvkr producer")
	}
	if err := validateSBOMComponent("metadata.component", b.Metadata.Component); err != nil {
		return err
	}
	if err := validateProperties("metadata.properties", b.Metadata.Props); err != nil {
		return err
	}
	buildDigest, artifact, err := metadataBindings(b.Metadata.Props)
	if err != nil {
		return err
	}
	if err := validateInventorySemantics(b, artifact); err != nil {
		return err
	}
	serialSeed := buildDigest
	if artifact != "" {
		serialSeed += "/" + artifact
	}
	expectedSerial := deriveURN(serialSeed)
	for _, prop := range b.Metadata.Props {
		if prop.Name == "nvkr:evidenceStage" {
			if !validEvidenceStage(prop.Value) || artifact == "" || len(b.Metadata.Component.Hashes) != 1 {
				return e.New(e.CodeVerifyFailed, "SBOM has an invalid stage binding")
			}
			expectedSerial, err = stageSerialNumber(buildDigest, artifact, prop.Value, "sha256:"+b.Metadata.Component.Hashes[0].Content)
			if err != nil {
				return err
			}
		}
		if prop.Name == "nvkr:inspectionInputDigest" && !canonical.IsDigest(prop.Value) {
			return e.New(e.CodeVerifyFailed, "SBOM inspection input is not exact")
		}
	}
	if b.SerialNumber != expectedSerial {
		return e.New(e.CodeUnverifiedRef, "SBOM serialNumber is not bound to its exact build/artifact identity")
	}
	previous := ""
	for i, component := range b.Components {
		if err := validateSBOMComponent(fmt.Sprintf("components[%d]", i), component); err != nil {
			return err
		}
		key := component.Name
		if component.BOMRef != "" {
			key = component.BOMRef
		}
		if i > 0 && key <= previous {
			return e.New(e.CodeInvalidCatalog, "SBOM components must be strictly sorted and duplicate-free")
		}
		previous = key
	}
	if artifact != "" {
		if err := validateDependencies(b); err != nil {
			return err
		}
		if b.Metadata.Component.Name != artifact {
			return e.New(e.CodeUnverifiedRef, "per-artifact SBOM subject does not equal its metadata artifact binding")
		}
		if len(b.Metadata.Component.Hashes) != 1 {
			return e.New(e.CodeProvenanceMissing, "per-artifact SBOM %q has no exact subject hash", artifact)
		}
		return nil
	}
	// An aggregate SBOM must inventory the build's components.
	if len(b.Components) == 0 {
		return e.New(e.CodeProvenanceMissing, "SBOM has no components")
	}
	return nil
}

func metadataBindings(properties []Property) (string, string, error) {
	buildDigest := ""
	artifact := ""
	for _, property := range properties {
		switch property.Name {
		case "nvkr:buildDigest":
			buildDigest = property.Value
		case "nvkr:artifact":
			artifact = property.Value
		}
	}
	if !canonical.IsDigest(buildDigest) {
		return "", "", e.New(e.CodeUnverifiedRef, "SBOM metadata has no exact build digest")
	}
	if artifact != "" && !canonicalText(artifact) {
		return "", "", e.New(e.CodeInvalidCatalog, "SBOM metadata has a non-canonical artifact binding")
	}
	return buildDigest, artifact, nil
}

func validateSBOMComponent(field string, component Component) error {
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
	return validateProperties(field+".properties", component.Properties)
}

func validateProperties(field string, properties []Property) error {
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

func validSerialNumber(value string) bool {
	if len(value) != len("urn:uuid:00000000-0000-0000-0000-000000000000") || !strings.HasPrefix(value, "urn:uuid:") {
		return false
	}
	for i, r := range value[len("urn:uuid:"):] {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Marshal renders the semantically validated SBOM deterministically.
func (b *BOM) Marshal() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(b) // canonical on-disk form (ADR-01 §4)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxDocumentBytes {
		return nil, e.New(e.CodeVerifyFailed, "complete SBOM exceeds its bounded document policy")
	}
	return raw, nil
}

// Digest is the SBOM's domain-separated content digest (ADR-01 codec), the value the
// ValidationVector's evidence block references (ADR-05 §1).
func (b *BOM) Digest() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindSBOM, b)
}

// purl builds a Package URL for a github source, falling back to a generic purl.
func purl(repo, commit string) string {
	if path, ok := strings.CutPrefix(repo, "https://github.com/"); ok {
		return "pkg:github/" + strings.TrimSuffix(path, ".git") + "@" + commit
	}
	name := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		name = repo[i+1:]
	}
	return fmt.Sprintf("pkg:generic/%s@%s?download_url=%s", strings.TrimSuffix(name, ".git"), commit, repo)
}

// deriveURN turns the buildDigest into a stable urn:uuid so the serial number is
// unique per build yet reproducible (no random UUID).
func deriveURN(buildDigest string) string {
	h := strings.TrimPrefix(buildDigest, "sha256:")
	for len(h) < 32 {
		h += "0"
	}
	return fmt.Sprintf("urn:uuid:%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

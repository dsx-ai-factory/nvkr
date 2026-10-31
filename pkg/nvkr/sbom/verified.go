// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// EvidenceReader is the only external-evidence acquisition seam. Implementations
// may read https, OCI, or an activated mirror, but must honor ctx. BuildVerified
// computes the digest itself, so this adapter cannot assert that bytes verified.
type EvidenceReader interface {
	ReadEvidence(ctx context.Context, ref string) ([]byte, error)
}

// EvidenceReaderFunc adapts a function to EvidenceReader.
type EvidenceReaderFunc func(context.Context, string) ([]byte, error)

func (fn EvidenceReaderFunc) ReadEvidence(ctx context.Context, ref string) ([]byte, error) {
	return fn(ctx, ref)
}

// SLSAProvenanceV1PredicateType is the only accepted upstream provenance predicate.
const SLSAProvenanceV1PredicateType = "https://slsa.dev/provenance/v1"

// EvidenceBundlePolicy is the exact keyless signer and subject an upstream
// Sigstore provenance bundle must satisfy.
type EvidenceBundlePolicy struct {
	SubjectDigest string
	PredicateType string
	Issuer        string
	Identity      string
}

// EvidenceBundleVerifier authenticates one upstream Sigstore bundle: signature,
// certificate identity and issuer, and transparency evidence. It returns no
// claims; this module separately decodes and checks the statement it accepts.
type EvidenceBundleVerifier interface {
	VerifyEvidenceBundle(ctx context.Context, bundle []byte, policy EvidenceBundlePolicy) error
}

// LocalEvidenceReceipt is the artifact-scoped capability for an NVKR-generated
// CycloneDX document.  EvidenceDigest is its domain-separated semantic identity;
// BlobDigest is the ordinary sha256 of the exact canonical bytes that may be
// transported as an OCI referrer.  Both are retained because an OCI descriptor
// digest and an NVKR object identity intentionally use different domains.
//
// The receipt cannot be populated through JSON.  It is issued only by
// VerifiedSet.LocalReceipts after BuildVerifiedFromReport has bound every generated
// SBOM to the exact qualified producer build and artifact set.
type LocalEvidenceReceipt struct {
	valid            bool
	component        string
	buildDigest      string
	artifactDigest   string
	evidenceDigest   string
	blobDigest       string
	evidenceSchema   string
	canonicalPayload string
}

// LocalEvidenceBinding is a defensive, read-only projection of a qualified local
// SBOM receipt.  Possessing this value is not authority; every call to Binding
// revalidates the receipt's private exact-byte binding first.
type LocalEvidenceBinding struct {
	Component      string
	BuildDigest    string
	ArtifactDigest string
	EvidenceDigest string
	BlobDigest     string
	EvidenceSchema string
}

func (receipt LocalEvidenceReceipt) Binding() (LocalEvidenceBinding, error) {
	if !receipt.valid || !canonicalText(receipt.component) || !canonical.IsDigest(receipt.buildDigest) ||
		!canonical.IsDigest(receipt.artifactDigest) || !canonical.IsDigest(receipt.evidenceDigest) ||
		!canonical.IsDigest(receipt.blobDigest) || receipt.evidenceSchema != "cyclonedx-json/1.5" ||
		receipt.canonicalPayload == "" {
		return LocalEvidenceBinding{}, e.New(e.CodeUnverifiedRef,
			"local SBOM receipt is absent or lost its exact identity binding")
	}
	var document BOM
	if err := canonical.UnmarshalExact([]byte(receipt.canonicalPayload), &document); err != nil {
		return LocalEvidenceBinding{}, err
	}
	identity, err := document.Digest()
	if err != nil {
		return LocalEvidenceBinding{}, err
	}
	documentBuild, documentArtifact, err := metadataBindings(document.Metadata.Props)
	if err != nil {
		return LocalEvidenceBinding{}, err
	}
	if identity != receipt.evidenceDigest || rawDigest([]byte(receipt.canonicalPayload)) != receipt.blobDigest {
		return LocalEvidenceBinding{}, e.New(e.CodeUnverifiedRef,
			"local SBOM receipt no longer matches its canonical payload")
	}
	subject := document.Metadata.Component
	if documentBuild != receipt.buildDigest || documentArtifact != receipt.component ||
		len(document.Components) != 0 || subject.Name != receipt.component ||
		len(subject.Hashes) != 1 || subject.Hashes[0].Alg != "SHA-256" ||
		"sha256:"+subject.Hashes[0].Content != receipt.artifactDigest {
		return LocalEvidenceBinding{}, e.New(e.CodeUnverifiedRef,
			"local SBOM receipt no longer binds its exact artifact subject")
	}
	return LocalEvidenceBinding{Component: receipt.component, BuildDigest: receipt.buildDigest,
		ArtifactDigest: receipt.artifactDigest, EvidenceDigest: receipt.evidenceDigest,
		BlobDigest: receipt.blobDigest, EvidenceSchema: receipt.evidenceSchema}, nil
}

// EvidenceGap is an explicit waiver-required signal. It has no accepted/passing
// bit and cannot satisfy evidence requirements.
type EvidenceGap struct {
	Component      string   `json:"component"`
	ArtifactDigest string   `json:"artifactDigest"`
	MissingTypes   []string `json:"missingTypes"`
	Reason         string   `json:"reason"`
}

// VerifiedSet groups deterministic SBOMs with verified external-evidence bindings
// and explicit gaps. The validity marker is package-private.
type VerifiedSet struct {
	aggregate          *BOM
	perArtifact        map[string]*BOM
	index              *Index
	gaps               []EvidenceGap
	valid              bool
	publisherQualified bool
}

// BuildVerified verifies declared external evidence and emits deterministic SBOM
// documents from caller-supplied artifact hashes. External evidence properties
// bind bytes that this function reads, re-hashes and checks against the subject. The
// locally generated SBOMs are diagnostic only: caller hash strings do not prove a
// producer observed those artifact bytes, so this path cannot issue LocalReceipts
// or qualify publisher evidence. Use BuildVerifiedFromReport for publisher output.
func BuildVerified(ctx context.Context, res *buildgraph.Resolved, buildDigest string,
	artifactHashes map[string]string, reader EvidenceReader, verifier EvidenceBundleVerifier,
) (*VerifiedSet, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	snapshot, err := requireResolvedSnapshot(res)
	if err != nil {
		return nil, err
	}
	if !canonical.IsDigest(buildDigest) {
		return nil, e.New(e.CodeUnverifiedRef, "verified SBOM has unpinned build digest %q", buildDigest)
	}
	hashes := cloneHashes(artifactHashes)
	components, err := declarationArtifactSelection(snapshot, hashes)
	if err != nil {
		return nil, err
	}
	graph, err := requireResolvedBuildIdentity(snapshot, buildDigest)
	if err != nil {
		return nil, err
	}
	if err := bindDeclaredPrebuiltSubjects(graph, hashes); err != nil {
		return nil, err
	}
	return buildVerifiedSet(ctx, snapshot, buildDigest, components, hashes, reader, verifier, false)
}

// BuildDiagnosticSelected verifies the full resolved build identity, then emits a
// deterministic diagnostic SBOM for exactly the caller-observed artifact subset.
// It exists for development tools such as the local bake-off: a one-component
// observation must not need fabricated hashes for every other build component.
//
// Like BuildVerified, this caller-hash path can independently verify external
// evidence bytes, but it cannot issue LocalEvidenceReceipts or publisher authority.
// Qualified publisher output must use BuildVerifiedFromReport instead.
func BuildDiagnosticSelected(ctx context.Context, res *buildgraph.Resolved, buildDigest string,
	artifactHashes map[string]string, reader EvidenceReader, verifier EvidenceBundleVerifier,
) (*VerifiedSet, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	snapshot, err := requireResolvedSnapshot(res)
	if err != nil {
		return nil, err
	}
	if !canonical.IsDigest(buildDigest) {
		return nil, e.New(e.CodeUnverifiedRef,
			"diagnostic SBOM has unpinned build digest %q", buildDigest)
	}
	hashes := cloneHashes(artifactHashes)
	graph, err := requireResolvedBuildIdentity(snapshot, buildDigest)
	if err != nil {
		return nil, err
	}
	components, err := diagnosticArtifactSelection(snapshot, graph, hashes)
	if err != nil {
		return nil, err
	}
	return buildVerifiedSet(ctx, snapshot, buildDigest, components, hashes, reader, verifier, false)
}

// BuildVerifiedFromReport is the sole local producer-to-SBOM authority seam. It
// preserves the full resolved build identity while emitting only the exact
// artifact subset authorized by the opaque qualified pipeline report. Artifact
// names and hashes are derived from that report, never accepted as caller input.
// External evidence remains independently verified by reading its exact bytes.
func BuildVerifiedFromReport(ctx context.Context, res *buildgraph.Resolved, report *pipeline.Report,
	reader EvidenceReader, verifier EvidenceBundleVerifier,
) (*VerifiedSet, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	if report == nil {
		return nil, e.New(e.CodePreflightFailed,
			"publisher SBOM construction requires a qualified pipeline report")
	}
	if err := report.RequireQualified(ctx); err != nil {
		return nil, err
	}
	snapshot, err := requireResolvedSnapshot(res)
	if err != nil {
		return nil, err
	}
	graph, err := requireResolvedBuildIdentity(snapshot, report.BuildDigest)
	if err != nil {
		return nil, err
	}
	components, hashes, err := qualifiedReportArtifactSelection(snapshot, graph, report)
	if err != nil {
		return nil, err
	}
	set, err := buildVerifiedSet(ctx, snapshot, report.BuildDigest, components, hashes, reader, verifier, true)
	if err != nil {
		return nil, err
	}
	// Evidence readers and verifiers are untrusted callbacks. Revalidate the report and its
	// live terminal artifact bytes after all external I/O before issuing authority.
	if err := report.RequireQualified(ctx); err != nil {
		return nil, err
	}
	return set, nil
}

func validateSBOMContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "verified SBOM construction requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "building verified SBOM evidence")
	}
	return nil
}

func requireResolvedSnapshot(res *buildgraph.Resolved) (*buildgraph.Resolved, error) {
	if res == nil {
		return nil, e.New(e.CodeProvenanceMissing,
			"verified SBOM construction requires a resolved build")
	}
	return snapshotResolved(res)
}

func cloneHashes(artifactHashes map[string]string) map[string]string {
	hashes := make(map[string]string, len(artifactHashes))
	for name, digest := range artifactHashes {
		hashes[name] = digest
	}
	return hashes
}

func validatedResolvedComponents(snapshot *buildgraph.Resolved) ([]catalog.NamedComponent, error) {
	components := append([]catalog.NamedComponent(nil), snapshot.Components...)
	sort.Slice(components, func(i, j int) bool { return components[i].Name < components[j].Name })
	previousName := ""
	for index, component := range components {
		if !canonicalText(component.Name) {
			return nil, e.New(e.CodeInvalidCatalog,
				"verified SBOM component %q has a non-canonical name", component.Name)
		}
		if index > 0 && component.Name == previousName {
			return nil, e.New(e.CodeInvalidCatalog,
				"verified SBOM resolved build repeats component %q", component.Name)
		}
		previousName = component.Name
		if component.FromSource && len(component.Commit) < 12 {
			return nil, e.New(e.CodeUnpinnedSource,
				"verified SBOM from-source component %q has an invalid commit identity", component.Name)
		}
	}
	return components, nil
}

func declarationArtifactSelection(snapshot *buildgraph.Resolved, hashes map[string]string) ([]catalog.NamedComponent, error) {
	components, err := validatedResolvedComponents(snapshot)
	if err != nil {
		return nil, err
	}
	selected := make([]catalog.NamedComponent, 0, len(components))
	expected := make(map[string]bool)
	for _, component := range components {
		if !component.FromSource && component.Prebuilt == nil {
			continue
		}
		expected[component.Name] = true
		selected = append(selected, component)
	}
	if len(selected) == 0 {
		return nil, e.New(e.CodeProvenanceMissing,
			"verified SBOM resolved build has no produced or acquired artifacts")
	}
	for name := range expected {
		if !canonical.IsDigest(hashes[name]) {
			return nil, e.New(e.CodeProvenanceMissing,
				"artifact %q requires an exact sha256 digest before SBOM evidence can qualify", name)
		}
	}
	for name, digest := range hashes {
		if !expected[name] {
			return nil, e.New(e.CodeUnverifiedRef,
				"artifact digest map contains unselected component %q", name)
		}
		if !canonical.IsDigest(digest) {
			return nil, e.New(e.CodeUnverifiedRef,
				"artifact %q has non-canonical digest %q", name, digest)
		}
	}
	// Preserve the legacy aggregate's inventory semantics, including declared
	// exclusions. Only producer components create per-artifact documents; unlike
	// BuildVerifiedFromReport this diagnostic path is not a selected publication.
	return components, nil
}

func diagnosticArtifactSelection(snapshot *buildgraph.Resolved, graph *ir.Graph,
	hashes map[string]string,
) ([]catalog.NamedComponent, error) {
	components, err := validatedResolvedComponents(snapshot)
	if err != nil {
		return nil, err
	}
	if len(hashes) == 0 {
		return nil, e.New(e.CodeProvenanceMissing,
			"diagnostic SBOM requires at least one observed artifact digest")
	}
	byName := make(map[string]catalog.NamedComponent, len(components))
	for _, component := range components {
		byName[component.Name] = component
	}
	producerByComponent := make(map[string]ir.Node)
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindBuild || node.Kind == ir.KindPull {
			producerByComponent[node.Component] = node
		}
	}
	selected := make([]catalog.NamedComponent, 0, len(hashes))
	for name, digest := range hashes {
		component, resolved := byName[name]
		producer, produced := producerByComponent[name]
		if !resolved || !produced || (!component.FromSource && component.Prebuilt == nil) {
			return nil, e.New(e.CodeUnverifiedRef,
				"diagnostic SBOM artifact %q is not a resolved build producer", name)
		}
		if !canonical.IsDigest(digest) {
			return nil, e.New(e.CodeUnverifiedRef,
				"diagnostic SBOM artifact %q has non-canonical digest %q", name, digest)
		}
		if producer.Kind == ir.KindPull && digest != producer.Digest {
			return nil, e.New(e.CodeUnverifiedRef,
				"diagnostic prebuilt SBOM subject %q is %s, want build-pinned %s",
				name, digest, producer.Digest)
		}
		selected = append(selected, component)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return selected, nil
}

func requireResolvedBuildIdentity(snapshot *buildgraph.Resolved, buildDigest string) (*ir.Graph, error) {
	graph, err := buildgraph.Compile(snapshot)
	if err != nil {
		return nil, err
	}
	computed, err := graph.Digest()
	if err != nil {
		return nil, err
	}
	if computed != buildDigest {
		return nil, e.New(e.CodeUnverifiedRef,
			"verified SBOM build digest %s does not match resolved build %s", buildDigest, computed)
	}
	return graph, nil
}

func bindDeclaredPrebuiltSubjects(graph *ir.Graph, hashes map[string]string) error {
	for _, node := range graph.Nodes {
		if node.Kind != ir.KindPull {
			continue
		}
		if hashes[node.Component] != node.Digest {
			return e.New(e.CodeUnverifiedRef,
				"declared prebuilt SBOM subject %q is %s, want build-pinned %s",
				node.Component, hashes[node.Component], node.Digest)
		}
	}
	return nil
}

func qualifiedReportArtifactSelection(snapshot *buildgraph.Resolved, graph *ir.Graph,
	report *pipeline.Report,
) ([]catalog.NamedComponent, map[string]string, error) {
	resolvedComponents, err := validatedResolvedComponents(snapshot)
	if err != nil {
		return nil, nil, err
	}
	byComponent := make(map[string]catalog.NamedComponent, len(resolvedComponents))
	for _, component := range resolvedComponents {
		byComponent[component.Name] = component
	}
	producers := make(map[string]ir.Node)
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindBuild || node.Kind == ir.KindPull {
			producers[node.Component] = node
		}
	}
	profileDigest := report.QualificationProfileDigest()
	executionBinding := report.ExecutionBinding()
	selected := make([]catalog.NamedComponent, 0, len(report.Artifacts))
	hashes := make(map[string]string, len(report.Artifacts))
	for _, artifact := range report.Artifacts {
		if _, duplicate := hashes[artifact.Component]; duplicate {
			return nil, nil, e.New(e.CodeVerifyFailed,
				"qualified report repeats artifact component %q", artifact.Component)
		}
		component, knownComponent := byComponent[artifact.Component]
		node, produced := producers[artifact.Component]
		if !knownComponent || !produced || artifact.OutputType != node.OutputType ||
			!canonical.IsDigest(artifact.SHA256) {
			return nil, nil, e.New(e.CodeVerifyFailed,
				"qualified report artifact %q is not an exact resolved build producer", artifact.Component)
		}
		switch node.Kind {
		case ir.KindBuild:
			if !component.FromSource || component.Prebuilt != nil ||
				!canonical.IsDigest(profileDigest) ||
				artifact.Qualification != pipeline.ArtifactQualifiedExecution ||
				artifact.BuilderType != node.BuilderType || artifact.EngineClass != node.BuilderType ||
				artifact.Toolchain != node.Toolchain || !canonical.IsDigest(artifact.InputDigest) ||
				artifact.ProfileDigest != profileDigest || artifact.ExecutionBinding != executionBinding {
				return nil, nil, e.New(e.CodeVerifyFailed,
					"qualified build artifact %q lost its exact build/execution binding", artifact.Component)
			}
		case ir.KindPull:
			if component.FromSource || component.Prebuilt == nil ||
				artifact.Qualification != pipeline.ArtifactProfileFreeAcquisition ||
				artifact.BuilderType != "" || artifact.EngineClass != ir.EnginePrebuiltAcquire ||
				artifact.Toolchain != "" || artifact.InputDigest != "" ||
				artifact.ProfileDigest != "" || artifact.ExecutionBinding != "" ||
				artifact.SHA256 != node.Digest {
				return nil, nil, e.New(e.CodeVerifyFailed,
					"qualified prebuilt artifact %q lost its exact acquisition binding", artifact.Component)
			}
		}
		selected = append(selected, component)
		hashes[artifact.Component] = artifact.SHA256
	}
	if len(selected) == 0 {
		return nil, nil, e.New(e.CodeProvenanceMissing,
			"qualified report contains no selected artifact SBOM subjects")
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return selected, hashes, nil
}

func buildVerifiedSet(ctx context.Context, snapshot *buildgraph.Resolved, buildDigest string,
	components []catalog.NamedComponent, hashes map[string]string, reader EvidenceReader,
	verifier EvidenceBundleVerifier, publisherQualified bool,
) (*VerifiedSet, error) {
	verifiedProps := make(map[string][]Property)
	gaps := make([]EvidenceGap, 0)
	for _, component := range components {
		if !component.FromSource && component.Prebuilt == nil {
			continue
		}
		artifactDigest := hashes[component.Name]
		if component.Prebuilt == nil {
			continue
		}
		props, componentGaps, err := verifyExternalEvidence(ctx, component, artifactDigest, reader, verifier)
		if err != nil {
			return nil, err
		}
		verifiedProps[component.Name] = props
		gaps = append(gaps, componentGaps...)
	}

	scoped := *snapshot
	scoped.Components = append([]catalog.NamedComponent(nil), components...)
	aggregate := fromBuild(&scoped, buildDigest, hashes, publisherQualified)
	for i := range aggregate.Components {
		for _, component := range components {
			if aggregate.Components[i].Name == component.Name && component.Prebuilt != nil {
				if publisherQualified {
					aggregate.Components[i] = componentForPublisherEvidence(component,
						hashes[component.Name], target.ExecutableArch(snapshot.Platform), verifiedProps[component.Name])
				} else {
					aggregate.Components[i] = componentForWithExternalEvidence(component,
						hashes[component.Name], target.ExecutableArch(snapshot.Platform), verifiedProps[component.Name])
				}
				break
			}
		}
	}
	perArtifact := make(map[string]*BOM)
	index := &Index{SchemaVersion: IndexSchemaVersion, BuildDigest: buildDigest}
	for _, component := range components {
		if !component.FromSource && component.Prebuilt == nil {
			continue
		}
		bom := fromComponentForArch(component, buildDigest, hashes[component.Name], target.ExecutableArch(snapshot.Platform))
		if publisherQualified {
			publisherComponent := componentForPublisherEvidence(component,
				hashes[component.Name], target.ExecutableArch(snapshot.Platform),
				externalEvidenceDeclarationProps(component.Prebuilt))
			bom.Metadata.Component = publisherComponent
		}
		if component.Prebuilt != nil {
			verifiedComponent := componentForWithExternalEvidence(component,
				hashes[component.Name], target.ExecutableArch(snapshot.Platform), verifiedProps[component.Name])
			if publisherQualified {
				verifiedComponent = componentForPublisherEvidence(component,
					hashes[component.Name], target.ExecutableArch(snapshot.Platform), verifiedProps[component.Name])
			}
			bom.Metadata.Component = verifiedComponent
		}
		digest, err := bom.Digest()
		if err != nil {
			return nil, err
		}
		perArtifact[component.Name] = bom
		index.Entries = append(index.Entries, IndexEntry{
			Artifact: component.Name, SBOMDigest: digest, ArtifactSHA: hashes[component.Name],
		})
	}
	if err := aggregate.Validate(); err != nil {
		return nil, err
	}
	if err := index.Validate(); err != nil {
		return nil, err
	}
	return &VerifiedSet{aggregate: aggregate, perArtifact: perArtifact, index: index,
		gaps: gaps, valid: true,
		publisherQualified: publisherQualified}, nil
}

func verifyExternalEvidence(ctx context.Context, component catalog.NamedComponent, artifactDigest string,
	reader EvidenceReader, verifier EvidenceBundleVerifier,
) ([]Property, []EvidenceGap, error) {
	ev := component.Prebuilt.Evidence
	if ev == nil {
		return nil, nil, e.New(e.CodeProvenanceMissing,
			"prebuilt component %q has no explicit external-evidence declaration", component.Name)
	}
	if ev.None {
		if ev.SBOMRef != "" || ev.SBOMDigest != "" || ev.ProvenanceRef != "" || ev.ProvenanceDigest != "" || ev.Signer != nil {
			return nil, nil, e.New(e.CodeUnverifiedRef,
				"prebuilt component %q mixes evidence:none with external references", component.Name)
		}
		return []Property{{Name: "nvkr:externalEvidence", Value: "none"}},
			[]EvidenceGap{{Component: component.Name, ArtifactDigest: artifactDigest,
				MissingTypes: []string{"provenance", "sbom"}, Reason: "evidence:none"}}, nil
	}
	if ev.SBOMRef == "" && ev.ProvenanceRef == "" {
		return nil, nil, e.New(e.CodeProvenanceMissing,
			"prebuilt component %q declares neither references nor evidence:none", component.Name)
	}
	if reader == nil {
		return nil, nil, e.New(e.CodeUnavailable,
			"external evidence for prebuilt component %q requires a reader adapter", component.Name)
	}

	props := []Property{{Name: "nvkr:externalEvidence", Value: "verified"}}
	gaps := make([]EvidenceGap, 0, 1)
	missing := make([]string, 0, 2)
	for _, item := range []struct {
		typeName string
		ref      string
		digest   string
		refProp  string
		digProp  string
	}{
		{"sbom", ev.SBOMRef, ev.SBOMDigest, "nvkr:externalSbomRef", "nvkr:externalSbomDigest"},
		{"provenance", ev.ProvenanceRef, ev.ProvenanceDigest, "nvkr:externalProvenanceRef", "nvkr:externalProvenanceDigest"},
	} {
		if item.ref == "" {
			if item.digest != "" {
				return nil, nil, e.New(e.CodeUnverifiedRef,
					"prebuilt component %q has %s digest without a reference", component.Name, item.typeName)
			}
			missing = append(missing, item.typeName)
			continue
		}
		if !canonicalText(item.ref) {
			return nil, nil, e.New(e.CodeUnverifiedRef,
				"prebuilt component %q has a non-canonical external %s reference", component.Name, item.typeName)
		}
		if !canonical.IsDigest(item.digest) {
			return nil, nil, e.New(e.CodeUnpinnedInput,
				"prebuilt component %q has unpinned external %s digest %q", component.Name, item.typeName, item.digest)
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, e.Wrap(e.CodeTimeout, err,
				"reading external %s for %s", item.typeName, component.Name)
		}
		bytes, err := readExternalEvidence(ctx, component, item.typeName, item.ref, reader)
		if err != nil {
			return nil, nil, err
		}
		if len(bytes) == 0 {
			return nil, nil, e.New(e.CodeProvenanceMissing,
				"external %s for prebuilt component %q is empty", item.typeName, component.Name)
		}
		got := rawDigest(bytes)
		if got != item.digest {
			return nil, nil, e.New(e.CodeUnverifiedRef,
				"external %s for prebuilt component %q hashes to %s, not declared %s",
				item.typeName, component.Name, got, item.digest)
		}
		err = validateExternalEvidence(ctx, item.typeName, bytes, evidenceSubjectName(component), artifactDigest,
			ev.Signer, verifier)
		if err != nil {
			return nil, nil, err
		}
		props = append(props, Property{Name: item.refProp, Value: item.ref}, Property{Name: item.digProp, Value: got})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		props[0].Value = "partial-waiver-required"
		gaps = append(gaps, EvidenceGap{Component: component.Name, ArtifactDigest: artifactDigest,
			MissingTypes: missing, Reason: "not-declared"})
	}
	sort.Slice(props, func(i, j int) bool { return props[i].Name < props[j].Name })
	return props, gaps, nil
}

// readExternalEvidence reads one declared reference. An archive: reference is a
// member of the component's own pinned release archive; the catalog ToolPin
// authenticates the archive bytes before the shared release-asset selector reads it.
func readExternalEvidence(ctx context.Context, component catalog.NamedComponent, evidenceType, ref string,
	reader EvidenceReader,
) ([]byte, error) {
	location := ref
	member, archived := catalog.ArchiveMemberRef(ref)
	tool := component.Prebuilt.Executable
	if archived {
		if tool == nil {
			return nil, e.New(e.CodeInvalidCatalog,
				"external %s %q for %s requires a pinned release archive", evidenceType, ref, component.Name)
		}
		location = tool.URL
	}
	raw, err := reader.ReadEvidence(ctx, location)
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "reading external %s for %s", evidenceType, component.Name)
		}
		return nil, e.Wrap(e.CodeUnavailable, err, "reading external %s for %s", evidenceType, component.Name)
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "reading external %s for %s", evidenceType, component.Name)
	}
	raw = append([]byte(nil), raw...)
	if !archived {
		return raw, nil
	}
	return tool.ArchiveMember(ctx, raw, member, MaxBundleBytes)
}

// evidenceSubjectName is the upstream artifact name its evidence describes: a
// release executable's own file name, otherwise the component name.
func evidenceSubjectName(component catalog.NamedComponent) string {
	if tool := component.Prebuilt.Executable; tool != nil {
		return path.Base(tool.ExecutablePath)
	}
	return component.Name
}

type externalCycloneDX struct {
	BOMFormat   string `json:"bomFormat"`
	SpecVersion string `json:"specVersion"`
	Metadata    struct {
		Component externalCycloneDXComponent `json:"component"`
	} `json:"metadata"`
	Components []externalCycloneDXComponent `json:"components"`
}

type externalCycloneDXComponent struct {
	Name   string `json:"name"`
	Hashes []struct {
		Alg     string `json:"alg"`
		Content string `json:"content"`
	} `json:"hashes"`
}

type externalSPDX struct {
	SPDXID            string                `json:"SPDXID"`
	DocumentDescribes []string              `json:"documentDescribes"`
	Packages          []externalSPDXElement `json:"packages"`
	Files             []externalSPDXElement `json:"files"`
	Relationships     []struct {
		Element string `json:"spdxElementId"`
		Related string `json:"relatedSpdxElement"`
		Type    string `json:"relationshipType"`
	} `json:"relationships"`
}

type externalSPDXElement struct {
	SPDXID    string `json:"SPDXID"`
	Name      string `json:"name"`
	FileName  string `json:"fileName"`
	Checksums []struct {
		Algorithm string `json:"algorithm"`
		Value     string `json:"checksumValue"`
	} `json:"checksums"`
}

type externalSLSAStatement struct {
	Type          string `json:"_type"`
	PredicateType string `json:"predicateType"`
	Subject       []struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
	Predicate struct {
		BuildDefinition struct {
			BuildType          string          `json:"buildType"`
			ExternalParameters json.RawMessage `json:"externalParameters"`
		} `json:"buildDefinition"`
		RunDetails struct {
			Builder struct {
				ID string `json:"id"`
			} `json:"builder"`
		} `json:"runDetails"`
	} `json:"predicate"`
}

func validateExternalEvidence(ctx context.Context, evidenceType string, raw []byte, subject, artifactDigest string,
	signer *catalog.EvidenceSigner, verifier EvidenceBundleVerifier,
) error {
	switch evidenceType {
	case "sbom":
		return validateExternalSBOM(raw, subject, artifactDigest)
	case "provenance":
		return validateExternalProvenance(ctx, raw, subject, artifactDigest, signer, verifier)
	default:
		return e.New(e.CodeUnverifiedRef, "unsupported external evidence type %q", evidenceType)
	}
}

// validateExternalSBOM accepts CycloneDX 1.4-1.6 JSON or SPDX 2.3 JSON. NVKR never
// merges or converts the document; it only checks that it describes the artifact.
func validateExternalSBOM(raw []byte, subject, artifactDigest string) error {
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("SPDXVersion:")) {
		return e.New(e.CodeUnverifiedRef,
			"external SBOM for %s is SPDX tag-value; only SPDX 2.3 JSON and CycloneDX JSON are supported", subject)
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "external sbom for %s is not an unambiguous JSON document", subject)
	}
	var format struct {
		BOMFormat   string          `json:"bomFormat"`
		SPDXVersion string          `json:"spdxVersion"`
		Context     json.RawMessage `json:"@context"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "decoding external SBOM for %s", subject)
	}
	switch {
	case format.BOMFormat != "" && format.SPDXVersion == "" && format.Context == nil:
		return validateExternalCycloneDX(raw, subject, artifactDigest)
	case format.SPDXVersion == "SPDX-2.3" && format.BOMFormat == "" && format.Context == nil:
		return validateExternalSPDX(raw, subject, artifactDigest)
	default:
		return e.New(e.CodeUnverifiedRef,
			"external SBOM for %s is not a supported CycloneDX 1.4-1.6 or SPDX 2.3 JSON document", subject)
	}
}

func validateExternalCycloneDX(raw []byte, component, artifactDigest string) error {
	var document externalCycloneDX
	if err := json.Unmarshal(raw, &document); err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "decoding external CycloneDX SBOM for %s", component)
	}
	if document.BOMFormat != "CycloneDX" ||
		(document.SpecVersion != "1.4" && document.SpecVersion != "1.5" && document.SpecVersion != "1.6") {
		return e.New(e.CodeUnverifiedRef,
			"external SBOM for %s is not a supported CycloneDX JSON document", component)
	}
	subjects := append([]externalCycloneDXComponent{document.Metadata.Component}, document.Components...)
	matches := 0
	for _, subject := range subjects {
		if subject.Name != component {
			continue
		}
		matches++
		sha256Rows := 0
		for _, hash := range subject.Hashes {
			if hash.Alg != "SHA-256" {
				continue
			}
			sha256Rows++
			candidate := "sha256:" + hash.Content
			if !canonical.IsDigest(candidate) || candidate != artifactDigest {
				return e.New(e.CodeUnverifiedRef,
					"external CycloneDX subject %s contains a non-canonical or conflicting SHA-256 row",
					component)
			}
		}
		if sha256Rows != 1 {
			return e.New(e.CodeUnverifiedRef,
				"external CycloneDX subject %s must contain exactly one canonical SHA-256 row for artifact %s (got %d)",
				component, artifactDigest, sha256Rows)
		}
	}
	if matches != 1 {
		return e.New(e.CodeUnverifiedRef,
			"external CycloneDX SBOM must contain exactly one subject named %s (got %d)", component, matches)
	}
	return nil
}

// validateExternalSPDX requires the document to describe exactly one package or
// file, named after the subject, with exactly one SHA256 checksum equal to it.
func validateExternalSPDX(raw []byte, subject, artifactDigest string) error {
	var document externalSPDX
	if err := json.Unmarshal(raw, &document); err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "decoding external SPDX SBOM for %s", subject)
	}
	if document.SPDXID == "" {
		return e.New(e.CodeUnverifiedRef, "external SPDX SBOM for %s has no document SPDXID", subject)
	}
	described := make(map[string]bool)
	for _, id := range document.DocumentDescribes {
		described[id] = true
	}
	for _, relationship := range document.Relationships {
		switch {
		case relationship.Type == "DESCRIBES" && relationship.Element == document.SPDXID:
			described[relationship.Related] = true
		case relationship.Type == "DESCRIBED_BY" && relationship.Related == document.SPDXID:
			described[relationship.Element] = true
		}
	}
	if len(described) != 1 {
		return e.New(e.CodeUnverifiedRef,
			"external SPDX SBOM for %s must describe exactly one element (got %d)", subject, len(described))
	}
	var root *externalSPDXElement
	for _, element := range append(document.Packages, document.Files...) {
		if !described[element.SPDXID] {
			continue
		}
		if root != nil {
			return e.New(e.CodeUnverifiedRef, "external SPDX SBOM for %s repeats its described SPDXID", subject)
		}
		root = &element
	}
	if root == nil || (root.Name != subject && (root.Name != "" || root.FileName != subject)) {
		return e.New(e.CodeUnverifiedRef, "external SPDX SBOM must describe an element named %s", subject)
	}
	sha256Rows := 0
	for _, checksum := range root.Checksums {
		if checksum.Algorithm != "SHA256" {
			continue
		}
		sha256Rows++
		if candidate := "sha256:" + checksum.Value; !canonical.IsDigest(candidate) || candidate != artifactDigest {
			return e.New(e.CodeUnverifiedRef,
				"external SPDX subject %s contains a non-canonical or conflicting SHA256 checksum", subject)
		}
	}
	if sha256Rows != 1 {
		return e.New(e.CodeUnverifiedRef,
			"external SPDX subject %s must contain exactly one SHA256 checksum for artifact %s (got %d)",
			subject, artifactDigest, sha256Rows)
	}
	return nil
}

// validateExternalProvenance accepts an unsigned in-toto statement, or a Sigstore
// v0.3 DSSE bundle whose statement passes the same subject checks and whose
// signature the injected verifier authenticates under the catalog signer policy.
func validateExternalProvenance(ctx context.Context, raw []byte, subject, artifactDigest string,
	signer *catalog.EvidenceSigner, verifier EvidenceBundleVerifier,
) error {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "external provenance for %s is not an unambiguous JSON document", subject)
	}
	var bundle struct {
		MediaType string `json:"mediaType"`
		DSSE      *struct {
			PayloadType string `json:"payloadType"`
			Payload     string `json:"payload"`
		} `json:"dsseEnvelope"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "decoding external provenance for %s", subject)
	}
	if bundle.MediaType == "" && bundle.DSSE == nil {
		if signer != nil {
			return e.New(e.CodeSignatureInvalid,
				"external provenance for %s declares a signer but is an unsigned statement", subject)
		}
		return validateExternalSLSA(raw, subject, artifactDigest)
	}
	if bundle.MediaType != BundleMediaType || bundle.DSSE == nil || bundle.DSSE.PayloadType != "application/vnd.in-toto+json" {
		return e.New(e.CodeUnverifiedRef,
			"external provenance for %s is not a Sigstore v0.3 in-toto DSSE bundle", subject)
	}
	if signer == nil {
		return e.New(e.CodeTrustPolicyMissing,
			"signed external provenance for %s requires a prebuilt.evidence.signer policy", subject)
	}
	statement, err := base64.StdEncoding.DecodeString(bundle.DSSE.Payload)
	if err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "decoding external provenance statement for %s", subject)
	}
	if err := canonical.ValidateJSONDocument(statement); err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "external provenance statement for %s is not unambiguous JSON", subject)
	}
	if err := validateExternalSLSA(statement, subject, artifactDigest); err != nil {
		return err
	}
	if verifier == nil {
		return e.New(e.CodeUnavailable,
			"signed external provenance for %s requires a Sigstore bundle verifier", subject)
	}
	err = verifier.VerifyEvidenceBundle(ctx, append([]byte(nil), raw...), EvidenceBundlePolicy{
		SubjectDigest: artifactDigest, PredicateType: SLSAProvenanceV1PredicateType,
		Issuer: signer.Issuer, Identity: signer.Identity,
	})
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "verifying external provenance for %s", subject)
	}
	if err != nil {
		var coded *e.Error
		if errors.As(err, &coded) {
			return err
		}
		return e.Wrap(e.CodeSignatureInvalid, err, "verifying external provenance signature for %s", subject)
	}
	return nil
}

func validateExternalSLSA(raw []byte, component, artifactDigest string) error {
	var statement externalSLSAStatement
	if err := json.Unmarshal(raw, &statement); err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "decoding external SLSA provenance for %s", component)
	}
	if (statement.Type != "https://in-toto.io/Statement/v1" && statement.Type != "https://in-toto.io/Statement/v0.1") ||
		statement.PredicateType != SLSAProvenanceV1PredicateType ||
		strings.TrimSpace(statement.Predicate.BuildDefinition.BuildType) == "" ||
		len(statement.Predicate.BuildDefinition.ExternalParameters) == 0 ||
		strings.TrimSpace(statement.Predicate.RunDetails.Builder.ID) == "" {
		return e.New(e.CodeUnverifiedRef,
			"external provenance for %s is not a complete in-toto/SLSA v1 statement", component)
	}
	matches := 0
	for _, subject := range statement.Subject {
		if subject.Name != component {
			continue
		}
		matches++
		if "sha256:"+subject.Digest["sha256"] != artifactDigest {
			return e.New(e.CodeUnverifiedRef,
				"external provenance subject %s does not bind artifact %s", component, artifactDigest)
		}
	}
	if matches != 1 {
		return e.New(e.CodeUnverifiedRef,
			"external provenance must contain exactly one subject named %s (got %d)", component, matches)
	}
	return nil
}

// Aggregate returns a defensive copy of the aggregate SBOM.
func (set *VerifiedSet) Aggregate() (*BOM, error) {
	if set == nil || !set.valid {
		return nil, e.New(e.CodeUnverifiedRef, "SBOM set was not built through verified evidence acquisition")
	}
	return cloneBOM(set.aggregate), nil
}

// PerArtifact returns defensive copies of all artifact-bound SBOMs and their index.
func (set *VerifiedSet) PerArtifact() (map[string]*BOM, *Index, error) {
	if set == nil || !set.valid {
		return nil, nil, e.New(e.CodeUnverifiedRef, "SBOM set was not built through verified evidence acquisition")
	}
	boms := make(map[string]*BOM, len(set.perArtifact))
	for name, bom := range set.perArtifact {
		boms[name] = cloneBOM(bom)
	}
	index := *set.index
	index.Entries = append([]IndexEntry(nil), set.index.Entries...)
	return boms, &index, nil
}

// LocalReceipts returns one immutable, exact-byte receipt per locally generated
// artifact SBOM. Only BuildVerifiedFromReport may issue these capabilities; the
// declaration-only BuildVerified path remains usable for verified external
// evidence and diagnostic documents but cannot mint publisher authority.
func (set *VerifiedSet) LocalReceipts() ([]LocalEvidenceReceipt, error) {
	if set == nil || !set.valid || !set.publisherQualified || set.index == nil {
		return nil, e.New(e.CodePreflightFailed,
			"local SBOM receipts require BuildVerifiedFromReport producer authority")
	}
	byArtifact := make(map[string]IndexEntry, len(set.index.Entries))
	for _, entry := range set.index.Entries {
		byArtifact[entry.Artifact] = entry
	}
	components := make([]string, 0, len(set.perArtifact))
	for component := range set.perArtifact {
		components = append(components, component)
	}
	sort.Strings(components)
	receipts := make([]LocalEvidenceReceipt, 0, len(components))
	for _, component := range components {
		document := cloneBOM(set.perArtifact[component])
		entry, ok := byArtifact[component]
		if !ok || entry.ArtifactSHA == "" {
			return nil, e.New(e.CodeUnverifiedRef,
				"verified SBOM set lost index binding for artifact %q", component)
		}
		identity, err := document.Digest()
		if err != nil {
			return nil, err
		}
		if identity != entry.SBOMDigest {
			return nil, e.New(e.CodeUnverifiedRef,
				"verified SBOM index identity for %q changed after construction", component)
		}
		payload, err := canonical.Marshal(document)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, LocalEvidenceReceipt{
			valid: true, component: component, buildDigest: set.index.BuildDigest,
			artifactDigest: entry.ArtifactSHA, evidenceDigest: identity,
			blobDigest: rawDigest(payload), evidenceSchema: "cyclonedx-json/1.5",
			canonicalPayload: string(payload),
		})
	}
	return receipts, nil
}

// EvidenceGaps returns explicit missing-evidence signals. The returned values are
// informational inputs to a waiver flow, never passing evidence.
func (set *VerifiedSet) EvidenceGaps() ([]EvidenceGap, error) {
	if set == nil || !set.valid {
		return nil, e.New(e.CodeUnverifiedRef, "SBOM set was not built through verified evidence acquisition")
	}
	out := append([]EvidenceGap(nil), set.gaps...)
	for i := range out {
		out[i].MissingTypes = append([]string(nil), set.gaps[i].MissingTypes...)
	}
	return out, nil
}

func rawDigest(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func snapshotResolved(resolved *buildgraph.Resolved) (*buildgraph.Resolved, error) {
	raw, err := json.Marshal(resolved)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "snapshotting resolved build for verified SBOM")
	}
	var snapshot buildgraph.Resolved
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "restoring resolved build snapshot for verified SBOM")
	}
	return &snapshot, nil
}

func canonicalText(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func cloneBOM(bom *BOM) *BOM {
	if bom == nil {
		return nil
	}
	copy := *bom
	copy.Metadata = cloneMetadata(bom.Metadata)
	copy.Components = make([]Component, len(bom.Components))
	for i := range bom.Components {
		copy.Components[i] = cloneComponent(bom.Components[i])
	}
	copy.Dependencies = make([]Dependency, len(bom.Dependencies))
	for i, dependency := range bom.Dependencies {
		copy.Dependencies[i] = Dependency{Ref: dependency.Ref, DependsOn: append([]string(nil), dependency.DependsOn...)}
	}
	return &copy
}

func cloneMetadata(metadata Metadata) Metadata {
	copy := metadata
	copy.Component = cloneComponent(metadata.Component)
	copy.Tools = append([]ToolRef(nil), metadata.Tools...)
	copy.Props = append([]Property(nil), metadata.Props...)
	return copy
}

func cloneComponent(component Component) Component {
	copy := component
	copy.Hashes = append([]Hash(nil), component.Hashes...)
	copy.Properties = append([]Property(nil), component.Properties...)
	return copy
}

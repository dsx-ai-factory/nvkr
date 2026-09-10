package sbom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/recipe"
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

// ExternalEvidenceReceipt is created only after the referenced bytes have been
// read and re-hashed. Its critical binding is private; callers can inspect but not
// construct a receipt that release code should trust.
type ExternalEvidenceReceipt struct {
	component      string
	artifactDigest string
	receiptType    string
	ref            string
	evidenceHash   string
	evidenceSchema string
}

func (r ExternalEvidenceReceipt) Component() string      { return r.component }
func (r ExternalEvidenceReceipt) ArtifactDigest() string { return r.artifactDigest }
func (r ExternalEvidenceReceipt) ReceiptType() string    { return r.receiptType }
func (r ExternalEvidenceReceipt) Ref() string            { return r.ref }
func (r ExternalEvidenceReceipt) EvidenceDigest() string { return r.evidenceHash }
func (r ExternalEvidenceReceipt) EvidenceSchema() string { return r.evidenceSchema }

// LocalEvidenceReceipt is the artifact-scoped capability for an NKD-generated
// CycloneDX document.  EvidenceDigest is its domain-separated semantic identity;
// BlobDigest is the ordinary sha256 of the exact canonical bytes that may be
// transported as an OCI referrer.  Both are retained because an OCI descriptor
// digest and an NKD object identity intentionally use different domains.
//
// The receipt cannot be populated through JSON.  It is issued only by
// VerifiedSet.LocalReceipts after BuildVerifiedFromReport has bound every generated
// SBOM to the exact qualified producer recipe and artifact set.
type LocalEvidenceReceipt struct {
	valid            bool
	component        string
	recipeDigest     string
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
	RecipeDigest   string
	ArtifactDigest string
	EvidenceDigest string
	BlobDigest     string
	EvidenceSchema string
}

func (receipt LocalEvidenceReceipt) Binding() (LocalEvidenceBinding, error) {
	if !receipt.valid || !canonicalText(receipt.component) || !canonical.IsDigest(receipt.recipeDigest) ||
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
	documentRecipe, documentArtifact, err := metadataBindings(document.Metadata.Props)
	if err != nil {
		return LocalEvidenceBinding{}, err
	}
	if identity != receipt.evidenceDigest || rawDigest([]byte(receipt.canonicalPayload)) != receipt.blobDigest {
		return LocalEvidenceBinding{}, e.New(e.CodeUnverifiedRef,
			"local SBOM receipt no longer matches its canonical payload")
	}
	subject := document.Metadata.Component
	if documentRecipe != receipt.recipeDigest || documentArtifact != receipt.component ||
		len(document.Components) != 0 || subject.Name != receipt.component ||
		len(subject.Hashes) != 1 || subject.Hashes[0].Alg != "SHA-256" ||
		"sha256:"+subject.Hashes[0].Content != receipt.artifactDigest {
		return LocalEvidenceBinding{}, e.New(e.CodeUnverifiedRef,
			"local SBOM receipt no longer binds its exact artifact subject")
	}
	return LocalEvidenceBinding{Component: receipt.component, RecipeDigest: receipt.recipeDigest,
		ArtifactDigest: receipt.artifactDigest, EvidenceDigest: receipt.evidenceDigest,
		BlobDigest: receipt.blobDigest, EvidenceSchema: receipt.evidenceSchema}, nil
}

// ExactBytes returns a defensive copy of the canonical SBOM document bound by the
// receipt.  It is intended for publisher storage; release authorization consumes
// only Binding.
func (receipt LocalEvidenceReceipt) ExactBytes() ([]byte, error) {
	if _, err := receipt.Binding(); err != nil {
		return nil, err
	}
	return append([]byte(nil), receipt.canonicalPayload...), nil
}

// EvidenceGap is an explicit waiver-required signal. It has no accepted/passing
// bit and cannot be converted into an ExternalEvidenceReceipt.
type EvidenceGap struct {
	Component      string   `json:"component"`
	ArtifactDigest string   `json:"artifactDigest"`
	MissingTypes   []string `json:"missingTypes"`
	Reason         string   `json:"reason"`
}

// VerifiedSet groups deterministic SBOMs with the external-evidence receipts and
// explicit gaps used to construct them. The validity marker is package-private.
type VerifiedSet struct {
	aggregate          *BOM
	perArtifact        map[string]*BOM
	index              *Index
	receipts           []ExternalEvidenceReceipt
	gaps               []EvidenceGap
	valid              bool
	publisherQualified bool
}

// BuildVerified verifies declared external evidence and emits deterministic SBOM
// documents from caller-supplied artifact hashes. The external receipts are
// authoritative because this function reads and re-hashes their exact bytes. The
// locally generated SBOMs are diagnostic only: caller hash strings do not prove a
// producer observed those artifact bytes, so this path cannot issue LocalReceipts
// or qualify publisher evidence. Use BuildVerifiedFromReport for publisher output.
func BuildVerified(ctx context.Context, res *recipe.Resolved, recipeDigest string,
	artifactHashes map[string]string, reader EvidenceReader,
) (*VerifiedSet, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	snapshot, err := requireResolvedSnapshot(res)
	if err != nil {
		return nil, err
	}
	if !canonical.IsDigest(recipeDigest) {
		return nil, e.New(e.CodeUnverifiedRef, "verified SBOM has unpinned recipe digest %q", recipeDigest)
	}
	hashes := cloneHashes(artifactHashes)
	components, err := declarationArtifactSelection(snapshot, hashes)
	if err != nil {
		return nil, err
	}
	graph, err := requireResolvedRecipeIdentity(snapshot, recipeDigest)
	if err != nil {
		return nil, err
	}
	if err := bindDeclaredPrebuiltSubjects(graph, hashes); err != nil {
		return nil, err
	}
	return buildVerifiedSet(ctx, snapshot, recipeDigest, components, hashes, reader, false)
}

// BuildDiagnosticSelected verifies the full resolved recipe identity, then emits a
// deterministic diagnostic SBOM for exactly the caller-observed artifact subset.
// It exists for development tools such as the local bake-off: a one-component
// observation must not need fabricated hashes for every other recipe component.
//
// Like BuildVerified, this caller-hash path can independently verify external
// evidence bytes, but it cannot issue LocalEvidenceReceipts or publisher authority.
// Qualified publisher output must use BuildVerifiedFromReport instead.
func BuildDiagnosticSelected(ctx context.Context, res *recipe.Resolved, recipeDigest string,
	artifactHashes map[string]string, reader EvidenceReader,
) (*VerifiedSet, error) {
	if err := validateSBOMContext(ctx); err != nil {
		return nil, err
	}
	snapshot, err := requireResolvedSnapshot(res)
	if err != nil {
		return nil, err
	}
	if !canonical.IsDigest(recipeDigest) {
		return nil, e.New(e.CodeUnverifiedRef,
			"diagnostic SBOM has unpinned recipe digest %q", recipeDigest)
	}
	hashes := cloneHashes(artifactHashes)
	graph, err := requireResolvedRecipeIdentity(snapshot, recipeDigest)
	if err != nil {
		return nil, err
	}
	components, err := diagnosticArtifactSelection(snapshot, graph, hashes)
	if err != nil {
		return nil, err
	}
	return buildVerifiedSet(ctx, snapshot, recipeDigest, components, hashes, reader, false)
}

// BuildVerifiedFromReport is the sole local producer-to-SBOM authority seam. It
// preserves the full resolved recipe identity while emitting only the exact
// artifact subset authorized by the opaque qualified pipeline report. Artifact
// names and hashes are derived from that report, never accepted as caller input.
// External evidence remains independently verified by reading its exact bytes.
func BuildVerifiedFromReport(ctx context.Context, res *recipe.Resolved, report *pipeline.Report,
	reader EvidenceReader,
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
	graph, err := requireResolvedRecipeIdentity(snapshot, report.RecipeDigest)
	if err != nil {
		return nil, err
	}
	if snapshot.RecipeID != report.RecipeID || graph.RecipeID != report.RecipeID {
		return nil, e.New(e.CodeVerifyFailed,
			"resolved SBOM recipe %q does not match qualified report recipe %q",
			snapshot.RecipeID, report.RecipeID)
	}
	components, hashes, err := qualifiedReportArtifactSelection(snapshot, graph, report)
	if err != nil {
		return nil, err
	}
	set, err := buildVerifiedSet(ctx, snapshot, report.RecipeDigest, components, hashes, reader, true)
	if err != nil {
		return nil, err
	}
	// An evidence reader is an untrusted callback. Revalidate the report and its
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

func requireResolvedSnapshot(res *recipe.Resolved) (*recipe.Resolved, error) {
	if res == nil {
		return nil, e.New(e.CodeProvenanceMissing,
			"verified SBOM construction requires a resolved recipe")
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

func validatedResolvedComponents(snapshot *recipe.Resolved) ([]catalog.NamedComponent, error) {
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
				"verified SBOM resolved recipe repeats component %q", component.Name)
		}
		previousName = component.Name
		if component.FromSource && len(component.Commit) < 12 {
			return nil, e.New(e.CodeUnpinnedSource,
				"verified SBOM from-source component %q has an invalid commit identity", component.Name)
		}
	}
	return components, nil
}

func declarationArtifactSelection(snapshot *recipe.Resolved, hashes map[string]string) ([]catalog.NamedComponent, error) {
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
			"verified SBOM resolved recipe has no produced or acquired artifacts")
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

func diagnosticArtifactSelection(snapshot *recipe.Resolved, graph *ir.Graph,
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
				"diagnostic SBOM artifact %q is not a resolved recipe producer", name)
		}
		if !canonical.IsDigest(digest) {
			return nil, e.New(e.CodeUnverifiedRef,
				"diagnostic SBOM artifact %q has non-canonical digest %q", name, digest)
		}
		if producer.Kind == ir.KindPull && digest != producer.Digest {
			return nil, e.New(e.CodeUnverifiedRef,
				"diagnostic prebuilt SBOM subject %q is %s, want recipe-pinned %s",
				name, digest, producer.Digest)
		}
		selected = append(selected, component)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return selected, nil
}

func requireResolvedRecipeIdentity(snapshot *recipe.Resolved, recipeDigest string) (*ir.Graph, error) {
	graph, err := recipe.Compile(snapshot)
	if err != nil {
		return nil, err
	}
	computed, err := graph.Digest()
	if err != nil {
		return nil, err
	}
	if computed != recipeDigest {
		return nil, e.New(e.CodeUnverifiedRef,
			"verified SBOM recipe digest %s does not match resolved recipe %s", recipeDigest, computed)
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
				"declared prebuilt SBOM subject %q is %s, want recipe-pinned %s",
				node.Component, hashes[node.Component], node.Digest)
		}
	}
	return nil
}

func qualifiedReportArtifactSelection(snapshot *recipe.Resolved, graph *ir.Graph,
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
				"qualified report artifact %q is not an exact resolved recipe producer", artifact.Component)
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
					"qualified build artifact %q lost its exact recipe/execution binding", artifact.Component)
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

func buildVerifiedSet(ctx context.Context, snapshot *recipe.Resolved, recipeDigest string,
	components []catalog.NamedComponent, hashes map[string]string, reader EvidenceReader,
	publisherQualified bool,
) (*VerifiedSet, error) {
	verifiedProps := make(map[string][]Property)
	receipts := make([]ExternalEvidenceReceipt, 0)
	gaps := make([]EvidenceGap, 0)
	for _, component := range components {
		if !component.FromSource && component.Prebuilt == nil {
			continue
		}
		artifactDigest := hashes[component.Name]
		if component.Prebuilt == nil {
			continue
		}
		props, componentReceipts, componentGaps, err := verifyExternalEvidence(ctx, component, artifactDigest, reader)
		if err != nil {
			return nil, err
		}
		verifiedProps[component.Name] = props
		receipts = append(receipts, componentReceipts...)
		gaps = append(gaps, componentGaps...)
	}

	scoped := *snapshot
	scoped.Components = append([]catalog.NamedComponent(nil), components...)
	aggregate := fromRecipe(&scoped, recipeDigest, hashes, publisherQualified)
	for i := range aggregate.Components {
		for _, component := range components {
			if aggregate.Components[i].Name == component.Name && component.Prebuilt != nil {
				if publisherQualified {
					aggregate.Components[i] = componentForPublisherEvidence(component,
						hashes[component.Name], snapshot.Criteria.Arch, verifiedProps[component.Name])
				} else {
					aggregate.Components[i] = componentForWithExternalEvidence(component,
						hashes[component.Name], snapshot.Criteria.Arch, verifiedProps[component.Name])
				}
				break
			}
		}
	}
	perArtifact := make(map[string]*BOM)
	index := &Index{SchemaVersion: IndexSchemaVersion, RecipeDigest: recipeDigest}
	for _, component := range components {
		if !component.FromSource && component.Prebuilt == nil {
			continue
		}
		bom := fromComponentForArch(component, recipeDigest, hashes[component.Name], snapshot.Criteria.Arch)
		if publisherQualified {
			publisherComponent := componentForPublisherEvidence(component,
				hashes[component.Name], snapshot.Criteria.Arch,
				externalEvidenceDeclarationProps(component.Prebuilt))
			bom.Metadata.Component = publisherComponent
		}
		if component.Prebuilt != nil {
			verifiedComponent := componentForWithExternalEvidence(component,
				hashes[component.Name], snapshot.Criteria.Arch, verifiedProps[component.Name])
			if publisherQualified {
				verifiedComponent = componentForPublisherEvidence(component,
					hashes[component.Name], snapshot.Criteria.Arch, verifiedProps[component.Name])
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
		receipts: receipts, gaps: gaps, valid: true,
		publisherQualified: publisherQualified}, nil
}

func verifyExternalEvidence(ctx context.Context, component catalog.NamedComponent, artifactDigest string,
	reader EvidenceReader,
) ([]Property, []ExternalEvidenceReceipt, []EvidenceGap, error) {
	ev := component.Prebuilt.Evidence
	if ev == nil {
		return nil, nil, nil, e.New(e.CodeProvenanceMissing,
			"prebuilt component %q has no explicit external-evidence declaration", component.Name)
	}
	if ev.None {
		if ev.SBOMRef != "" || ev.SBOMDigest != "" || ev.ProvenanceRef != "" || ev.ProvenanceDigest != "" {
			return nil, nil, nil, e.New(e.CodeUnverifiedRef,
				"prebuilt component %q mixes evidence:none with external references", component.Name)
		}
		return []Property{{Name: "nkd:externalEvidence", Value: "none"}}, nil,
			[]EvidenceGap{{Component: component.Name, ArtifactDigest: artifactDigest,
				MissingTypes: []string{"provenance", "sbom"}, Reason: "evidence:none"}}, nil
	}
	if ev.SBOMRef == "" && ev.ProvenanceRef == "" {
		return nil, nil, nil, e.New(e.CodeProvenanceMissing,
			"prebuilt component %q declares neither references nor evidence:none", component.Name)
	}
	if reader == nil {
		return nil, nil, nil, e.New(e.CodeUnavailable,
			"external evidence for prebuilt component %q requires a reader adapter", component.Name)
	}

	props := []Property{{Name: "nkd:externalEvidence", Value: "verified"}}
	receipts := make([]ExternalEvidenceReceipt, 0, 2)
	gaps := make([]EvidenceGap, 0, 1)
	missing := make([]string, 0, 2)
	for _, item := range []struct {
		typeName string
		ref      string
		digest   string
		refProp  string
		digProp  string
	}{
		{"sbom", ev.SBOMRef, ev.SBOMDigest, "nkd:externalSbomRef", "nkd:externalSbomDigest"},
		{"provenance", ev.ProvenanceRef, ev.ProvenanceDigest, "nkd:externalProvenanceRef", "nkd:externalProvenanceDigest"},
	} {
		if item.ref == "" {
			if item.digest != "" {
				return nil, nil, nil, e.New(e.CodeUnverifiedRef,
					"prebuilt component %q has %s digest without a reference", component.Name, item.typeName)
			}
			missing = append(missing, item.typeName)
			continue
		}
		if !canonicalText(item.ref) {
			return nil, nil, nil, e.New(e.CodeUnverifiedRef,
				"prebuilt component %q has a non-canonical external %s reference", component.Name, item.typeName)
		}
		if !canonical.IsDigest(item.digest) {
			return nil, nil, nil, e.New(e.CodeUnpinnedInput,
				"prebuilt component %q has unpinned external %s digest %q", component.Name, item.typeName, item.digest)
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, e.Wrap(e.CodeTimeout, err,
				"reading external %s for %s", item.typeName, component.Name)
		}
		bytes, err := reader.ReadEvidence(ctx, item.ref)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, nil, e.Wrap(e.CodeTimeout, ctx.Err(), "reading external %s for %s", item.typeName, component.Name)
			}
			return nil, nil, nil, e.Wrap(e.CodeUnavailable, err, "reading external %s for %s", item.typeName, component.Name)
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, e.Wrap(e.CodeTimeout, err,
				"reading external %s for %s", item.typeName, component.Name)
		}
		bytes = append([]byte(nil), bytes...)
		if len(bytes) == 0 {
			return nil, nil, nil, e.New(e.CodeProvenanceMissing,
				"external %s for prebuilt component %q is empty", item.typeName, component.Name)
		}
		got := rawDigest(bytes)
		if got != item.digest {
			return nil, nil, nil, e.New(e.CodeUnverifiedRef,
				"external %s for prebuilt component %q hashes to %s, not declared %s",
				item.typeName, component.Name, got, item.digest)
		}
		evidenceSchema, err := validateExternalEvidence(item.typeName, bytes, component.Name, artifactDigest)
		if err != nil {
			return nil, nil, nil, err
		}
		props = append(props, Property{Name: item.refProp, Value: item.ref}, Property{Name: item.digProp, Value: got})
		receipts = append(receipts, ExternalEvidenceReceipt{
			component: component.Name, artifactDigest: artifactDigest,
			receiptType: item.typeName, ref: item.ref, evidenceHash: got, evidenceSchema: evidenceSchema,
		})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		props[0].Value = "partial-waiver-required"
		gaps = append(gaps, EvidenceGap{Component: component.Name, ArtifactDigest: artifactDigest,
			MissingTypes: missing, Reason: "not-declared"})
	}
	sort.Slice(props, func(i, j int) bool { return props[i].Name < props[j].Name })
	return props, receipts, gaps, nil
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

func validateExternalEvidence(evidenceType string, raw []byte, component, artifactDigest string) (string, error) {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return "", e.Wrap(e.CodeUnverifiedRef, err, "external %s for %s is not an unambiguous JSON document", evidenceType, component)
	}
	switch evidenceType {
	case "sbom":
		return validateExternalCycloneDX(raw, component, artifactDigest)
	case "provenance":
		return validateExternalSLSA(raw, component, artifactDigest)
	default:
		return "", e.New(e.CodeUnverifiedRef, "unsupported external evidence type %q", evidenceType)
	}
}

func validateExternalCycloneDX(raw []byte, component, artifactDigest string) (string, error) {
	var document externalCycloneDX
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", e.Wrap(e.CodeUnverifiedRef, err, "decoding external CycloneDX SBOM for %s", component)
	}
	if document.BOMFormat != "CycloneDX" ||
		(document.SpecVersion != "1.4" && document.SpecVersion != "1.5" && document.SpecVersion != "1.6") {
		return "", e.New(e.CodeUnverifiedRef,
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
				return "", e.New(e.CodeUnverifiedRef,
					"external CycloneDX subject %s contains a non-canonical or conflicting SHA-256 row",
					component)
			}
		}
		if sha256Rows != 1 {
			return "", e.New(e.CodeUnverifiedRef,
				"external CycloneDX subject %s must contain exactly one canonical SHA-256 row for artifact %s (got %d)",
				component, artifactDigest, sha256Rows)
		}
	}
	if matches != 1 {
		return "", e.New(e.CodeUnverifiedRef,
			"external CycloneDX SBOM must contain exactly one subject named %s (got %d)", component, matches)
	}
	return "cyclonedx-json/" + document.SpecVersion, nil
}

func validateExternalSLSA(raw []byte, component, artifactDigest string) (string, error) {
	var statement externalSLSAStatement
	if err := json.Unmarshal(raw, &statement); err != nil {
		return "", e.Wrap(e.CodeUnverifiedRef, err, "decoding external SLSA provenance for %s", component)
	}
	if statement.Type != "https://in-toto.io/Statement/v1" ||
		statement.PredicateType != "https://slsa.dev/provenance/v1" ||
		strings.TrimSpace(statement.Predicate.BuildDefinition.BuildType) == "" ||
		len(statement.Predicate.BuildDefinition.ExternalParameters) == 0 ||
		strings.TrimSpace(statement.Predicate.RunDetails.Builder.ID) == "" {
		return "", e.New(e.CodeUnverifiedRef,
			"external provenance for %s is not a complete in-toto/SLSA v1 statement", component)
	}
	matches := 0
	for _, subject := range statement.Subject {
		if subject.Name != component {
			continue
		}
		matches++
		if "sha256:"+subject.Digest["sha256"] != artifactDigest {
			return "", e.New(e.CodeUnverifiedRef,
				"external provenance subject %s does not bind artifact %s", component, artifactDigest)
		}
	}
	if matches != 1 {
		return "", e.New(e.CodeUnverifiedRef,
			"external provenance must contain exactly one subject named %s (got %d)", component, matches)
	}
	return "in-toto-slsa-provenance/v1", nil
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
			valid: true, component: component, recipeDigest: set.index.RecipeDigest,
			artifactDigest: entry.ArtifactSHA, evidenceDigest: identity,
			blobDigest: rawDigest(payload), evidenceSchema: "cyclonedx-json/1.5",
			canonicalPayload: string(payload),
		})
	}
	return receipts, nil
}

// ExternalReceipts returns verified byte bindings. An evidence:none declaration
// never produces a receipt.
func (set *VerifiedSet) ExternalReceipts() ([]ExternalEvidenceReceipt, error) {
	if set == nil || !set.valid {
		return nil, e.New(e.CodeUnverifiedRef, "SBOM set was not built through verified evidence acquisition")
	}
	return append([]ExternalEvidenceReceipt(nil), set.receipts...), nil
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

// RequireCompleteExternalEvidence fails when a distinct waiver receipt is needed.
// It never interprets evidence:none as success.
func (set *VerifiedSet) RequireCompleteExternalEvidence() error {
	if set == nil || !set.valid {
		return e.New(e.CodeUnverifiedRef, "SBOM set was not built through verified evidence acquisition")
	}
	if len(set.gaps) == 0 {
		return nil
	}
	parts := make([]string, 0, len(set.gaps))
	for _, gap := range set.gaps {
		parts = append(parts, gap.Component+":"+strings.Join(gap.MissingTypes, ","))
	}
	sort.Strings(parts)
	return e.New(e.CodeProvenanceMissing,
		"external evidence is incomplete (%s); an exact authenticated waiver receipt is required", strings.Join(parts, "; "))
}

func rawDigest(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func snapshotResolved(resolved *recipe.Resolved) (*recipe.Resolved, error) {
	raw, err := json.Marshal(resolved)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "snapshotting resolved recipe for verified SBOM")
	}
	var snapshot recipe.Resolved
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "restoring resolved recipe snapshot for verified SBOM")
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

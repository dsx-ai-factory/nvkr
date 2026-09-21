package provenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// QualifiedSet is a validated, mutation-isolated provenance snapshot. Only
// BuildQualifiedFromReport can issue release or publisher receipts.
type QualifiedSet struct {
	valid              bool
	publisherQualified bool
	index              Index
}

// LocalEvidenceReceipt binds one locally generated statement to its exact build
// and artifact subject. EvidenceDigest is the domain-separated statement identity;
// BlobDigest is the ordinary sha256 used by content transports.
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

// LocalEvidenceBinding is a defensive view, not an independently constructible
// authority. Binding rechecks the private canonical-payload snapshot.
type LocalEvidenceBinding struct {
	Component      string
	BuildDigest    string
	ArtifactDigest string
	EvidenceDigest string
	BlobDigest     string
	EvidenceSchema string
}

// BuildQualifiedFromReport is the sole local producer-to-provenance authority
// seam. The opaque pipeline report first revalidates its terminal artifact bytes
// and execution receipt. The supplied graph is then snapshotted, semantically
// validated, and required to have the report's exact build identity. All
// provenance subjects, builder fields, and dependencies are derived from those two
// authorities; callers cannot supply an Artifact projection.
func BuildQualifiedFromReport(ctx context.Context, report *pipeline.Report, graph *ir.Graph) (*QualifiedSet, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "qualified provenance requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "building qualified provenance")
	}
	if report == nil {
		return nil, e.New(e.CodePreflightFailed, "qualified provenance requires a pipeline report")
	}
	if err := report.RequireQualified(ctx); err != nil {
		return nil, err
	}

	graphSnapshot, err := snapshotGraph(graph)
	if err != nil {
		return nil, err
	}
	buildDigest, err := graphSnapshot.Digest()
	if err != nil {
		return nil, err
	}
	if buildDigest != report.BuildDigest {
		return nil, e.New(e.CodeVerifyFailed,
			"provenance graph %s does not match qualified report %s",
			buildDigest, report.BuildDigest)
	}

	artifacts, err := projectQualifiedArtifacts(report, graphSnapshot)
	if err != nil {
		return nil, err
	}
	index := BuildIndex(report.BuildDigest, report.QualificationProfileDigest(), artifacts)
	snapshot, err := snapshotValidatedIndex(index)
	if err != nil {
		return nil, err
	}
	// Revalidate the opaque producer after projection so report mutation cannot
	// silently turn a previously observed declaration into publisher authority.
	if err := report.RequireQualified(ctx); err != nil {
		return nil, err
	}
	return &QualifiedSet{valid: true, publisherQualified: true, index: snapshot}, nil
}

func snapshotGraph(graph *ir.Graph) (*ir.Graph, error) {
	if graph == nil {
		return nil, e.New(e.CodeInvalidCatalog, "qualified provenance requires an IR graph")
	}
	payload, err := graph.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	var snapshot ir.Graph
	if err := canonical.UnmarshalExact(payload, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func snapshotValidatedIndex(index *Index) (Index, error) {
	if err := index.Validate(); err != nil {
		return Index{}, err
	}
	payload, err := canonical.Marshal(index)
	if err != nil {
		return Index{}, err
	}
	var snapshot Index
	if err := canonical.UnmarshalExact(payload, &snapshot); err != nil {
		return Index{}, err
	}
	return snapshot, nil
}

func projectQualifiedArtifacts(report *pipeline.Report, graph *ir.Graph) ([]Artifact, error) {
	byComponent := make(map[string]pipeline.ArtifactResult, len(report.Artifacts))
	for _, result := range report.Artifacts {
		if _, duplicate := byComponent[result.Component]; duplicate {
			return nil, e.New(e.CodeVerifyFailed,
				"qualified report repeats artifact component %q", result.Component)
		}
		byComponent[result.Component] = result
	}
	byID := make(map[string]ir.Node, len(graph.Nodes))
	for _, node := range graph.Nodes {
		byID[node.ID] = node
	}

	profileDigest := report.QualificationProfileDigest()
	executionBinding := report.ExecutionBinding()
	artifacts := make([]Artifact, 0, len(byComponent))
	for _, node := range graph.Nodes {
		if node.Kind != ir.KindBuild && node.Kind != ir.KindPull {
			continue
		}
		result, ok := byComponent[node.Component]
		if !ok {
			// A qualified pipeline report may represent an exact --only
			// selection from the full build graph. Its opaque terminal receipt
			// already proves that exact selected producer set; unselected graph
			// producers must not acquire provenance subjects.
			continue
		}
		if result.OutputType != node.OutputType || !canonical.IsDigest(result.SHA256) {
			return nil, e.New(e.CodeVerifyFailed,
				"qualified artifact %q lost its exact output type or digest", node.Component)
		}

		artifact := Artifact{Component: result.Component, SHA256: result.SHA256}
		switch node.Kind {
		case ir.KindBuild:
			if !canonical.IsDigest(profileDigest) ||
				result.Qualification != pipeline.ArtifactQualifiedExecution ||
				result.BuilderType != node.BuilderType || result.EngineClass != node.BuilderType ||
				result.Toolchain != node.Toolchain || !canonical.IsDigest(result.InputDigest) ||
				result.ProfileDigest != profileDigest || result.ExecutionBinding != executionBinding {
				return nil, e.New(e.CodeVerifyFailed,
					"qualified build artifact %q does not match its exact builder/input/toolchain authority",
					node.Component)
			}
			fetch, err := qualifiedSourceDependency(node, byID)
			if err != nil {
				return nil, err
			}
			artifact.InputDigest = result.InputDigest
			artifact.BuilderType = result.BuilderType
			artifact.Deps = qualifiedBuildDependencies(result, node, fetch)
		case ir.KindPull:
			if result.Qualification != pipeline.ArtifactProfileFreeAcquisition ||
				result.BuilderType != "" || result.EngineClass != ir.EnginePrebuiltAcquire ||
				result.Toolchain != "" || result.InputDigest != "" ||
				result.ProfileDigest != "" || result.ExecutionBinding != "" ||
				result.SHA256 != node.Digest {
				return nil, e.New(e.CodeVerifyFailed,
					"qualified prebuilt artifact %q does not match its exact acquisition authority",
					node.Component)
			}
			artifact.Deps = []Dependency{{
				URI:    "nkd:prebuilt/" + node.Component,
				Digest: map[string]string{"sha256": strings.TrimPrefix(node.Digest, "sha256:")},
			}}
		}
		artifacts = append(artifacts, artifact)
		delete(byComponent, node.Component)
	}
	if len(byComponent) != 0 {
		return nil, e.New(e.CodeVerifyFailed,
			"qualified report contains artifacts outside the graph producer set")
	}
	if len(artifacts) == 0 {
		return nil, e.New(e.CodeProvenanceMissing,
			"qualified report contains no selected artifact provenance subjects")
	}
	return artifacts, nil
}

func qualifiedSourceDependency(node ir.Node, byID map[string]ir.Node) (ir.Node, error) {
	var fetch ir.Node
	for _, dependency := range node.Needs {
		candidate, ok := byID[dependency]
		if !ok || candidate.Kind != ir.KindFetch {
			continue
		}
		if fetch.ID != "" {
			return ir.Node{}, e.New(e.CodeInvalidCatalog,
				"build artifact %q has more than one pinned source dependency", node.Component)
		}
		fetch = candidate
	}
	if fetch.ID == "" {
		return ir.Node{}, e.New(e.CodeInvalidCatalog,
			"build artifact %q lost its pinned source dependency", node.Component)
	}
	return fetch, nil
}

func qualifiedBuildDependencies(result pipeline.ArtifactResult, build, fetch ir.Node) []Dependency {
	dependencies := []Dependency{
		{URI: "nkd:resolved-input/" + build.Component,
			Digest: map[string]string{"sha256": strings.TrimPrefix(result.InputDigest, "sha256:")}},
		{URI: "nkd:source-commit/" + build.Component,
			Digest: map[string]string{"gitCommit": fetch.Commit}},
		{URI: "nkd:source-tree/" + build.Component,
			Digest: map[string]string{"gitTree": fetch.TreeHash}},
		{URI: "nkd:vendor-spec/" + build.Component,
			Digest: map[string]string{"sha256": strings.TrimPrefix(fetch.VendorSpecDigest, "sha256:")}},
	}
	// A registry/repository name is a transport locator, not build identity. The
	// graph validator already requires a bare sha256 or repo@sha256 toolchain pin;
	// project only that stable content address so an online/mirror relocation cannot
	// perturb deterministic provenance or publisher evidence.
	toolchainDigest := result.Toolchain
	if separator := strings.LastIndex(toolchainDigest, "@sha256:"); separator >= 0 {
		toolchainDigest = toolchainDigest[separator+1:]
	}
	dependencies = append(dependencies, Dependency{
		URI: "nkd:toolchain/" + build.Component,
		Digest: map[string]string{
			"sha256": strings.TrimPrefix(toolchainDigest, "sha256:"),
		},
	})
	for index, patch := range build.Patches {
		dependencies = append(dependencies, Dependency{
			URI:    fmt.Sprintf("nkd:patch/%s/%08d/%s", build.Component, index, patch.Name),
			Digest: map[string]string{"sha256": strings.TrimPrefix(patch.SHA256, "sha256:")},
		})
	}
	if build.BuilderType == ir.BuilderNix {
		dependencies = append(dependencies, Dependency{
			URI:    "nkd:nix-flake-lock/" + build.Component + "/" + build.FlakeOutput,
			Digest: map[string]string{"sha256": strings.TrimPrefix(build.FlakeLockDigest, "sha256:")},
		})
	}
	return dependencies
}

// Index returns a defensive copy of the complete provenance index.
func (set *QualifiedSet) Index() (*Index, error) {
	if set == nil || !set.valid {
		return nil, e.New(e.CodeUnverifiedRef,
			"provenance index was not built through qualified local generation")
	}
	payload, err := canonical.Marshal(set.index)
	if err != nil {
		return nil, err
	}
	var snapshot Index
	if err := canonical.UnmarshalExact(payload, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// LocalReceipts returns one exact-byte capability per artifact statement.
func (set *QualifiedSet) LocalReceipts() ([]LocalEvidenceReceipt, error) {
	if set == nil || !set.valid || !set.publisherQualified {
		return nil, e.New(e.CodePreflightFailed,
			"declaration-only provenance cannot authorize publisher evidence; use BuildQualifiedFromReport")
	}
	index, err := set.Index()
	if err != nil {
		return nil, err
	}
	receipts := make([]LocalEvidenceReceipt, 0, len(index.Statements))
	for _, statement := range index.Statements {
		if len(statement.Subject) != 1 {
			return nil, e.New(e.CodeProvenanceMissing,
				"qualified provenance statement lost its exact subject")
		}
		payload, err := canonical.Marshal(statement)
		if err != nil {
			return nil, err
		}
		identity, err := canonical.Digest(canonical.KindProvenanceStatement, statement)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, LocalEvidenceReceipt{
			valid: true, component: statement.Subject[0].Name,
			buildDigest:    index.BuildDigest,
			artifactDigest: "sha256:" + statement.Subject[0].Digest["sha256"],
			evidenceDigest: identity, blobDigest: rawDigest(payload),
			evidenceSchema: "in-toto-slsa-provenance/v1", canonicalPayload: string(payload),
		})
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].component < receipts[j].component })
	return receipts, nil
}

func (receipt LocalEvidenceReceipt) Binding() (LocalEvidenceBinding, error) {
	if !receipt.valid || !canonical.IsDigest(receipt.buildDigest) ||
		!canonical.IsDigest(receipt.artifactDigest) || !canonical.IsDigest(receipt.evidenceDigest) ||
		!canonical.IsDigest(receipt.blobDigest) || receipt.evidenceSchema != "in-toto-slsa-provenance/v1" ||
		receipt.canonicalPayload == "" {
		return LocalEvidenceBinding{}, e.New(e.CodeUnverifiedRef,
			"local provenance receipt is absent or lost its exact identity binding")
	}
	var statement Statement
	if err := canonical.UnmarshalExact([]byte(receipt.canonicalPayload), &statement); err != nil {
		return LocalEvidenceBinding{}, err
	}
	if err := (&Index{SchemaVersion: IndexSchemaVersion, BuildDigest: receipt.buildDigest,
		Statements: []Statement{statement}}).Validate(); err != nil {
		return LocalEvidenceBinding{}, err
	}
	identity, err := canonical.Digest(canonical.KindProvenanceStatement, statement)
	if err != nil {
		return LocalEvidenceBinding{}, err
	}
	if identity != receipt.evidenceDigest || rawDigest([]byte(receipt.canonicalPayload)) != receipt.blobDigest ||
		len(statement.Subject) != 1 || statement.Subject[0].Name != receipt.component ||
		"sha256:"+statement.Subject[0].Digest["sha256"] != receipt.artifactDigest ||
		statement.Predicate.BuildDefinition.ExternalParameters["buildDigest"] != receipt.buildDigest {
		return LocalEvidenceBinding{}, e.New(e.CodeUnverifiedRef,
			"local provenance receipt no longer matches its canonical artifact binding")
	}
	return LocalEvidenceBinding{Component: receipt.component, BuildDigest: receipt.buildDigest,
		ArtifactDigest: receipt.artifactDigest, EvidenceDigest: receipt.evidenceDigest,
		BlobDigest: receipt.blobDigest, EvidenceSchema: receipt.evidenceSchema}, nil
}

// ExactBytes returns a defensive copy of the canonical statement.
func (receipt LocalEvidenceReceipt) ExactBytes() ([]byte, error) {
	if _, err := receipt.Binding(); err != nil {
		return nil, err
	}
	return append([]byte(nil), receipt.canonicalPayload...), nil
}

func rawDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

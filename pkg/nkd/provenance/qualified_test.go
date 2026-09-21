package provenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestBuildQualifiedFromReportDerivesPublisherAuthority(t *testing.T) {
	report, graph := qualifiedPrebuiltReport(t, []byte("verified artifact"))

	qualified, err := BuildQualifiedFromReport(context.Background(), report, graph)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := qualified.LocalReceipts()
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 {
		t.Fatalf("got %d provenance receipts, want 1", len(receipts))
	}
	binding, err := receipts[0].Binding()
	if err != nil {
		t.Fatal(err)
	}
	if binding.Component != "agent" || binding.BuildDigest != report.BuildDigest ||
		binding.ArtifactDigest != report.Artifacts[0].SHA256 {
		t.Fatalf("receipt lost qualified subject binding: %+v", binding)
	}
	statementBytes, err := receipts[0].ExactBytes()
	if err != nil {
		t.Fatal(err)
	}
	var statement Statement
	if err := canonical.UnmarshalExact(statementBytes, &statement); err != nil {
		t.Fatal(err)
	}
	statementIdentity, err := canonical.Digest(canonical.KindProvenanceStatement, statement)
	if err != nil || statementIdentity != binding.EvidenceDigest {
		t.Fatalf("receipt did not use the artifact-scoped provenance domain: %s, %v", statementIdentity, err)
	}
	wrongFamily, err := canonical.Digest(canonical.KindProvenance, statement)
	if err != nil || wrongFamily == binding.EvidenceDigest {
		t.Fatalf("provenance statement/index domains collapsed: %s, %v", wrongFamily, err)
	}

	index, err := qualified.Index()
	if err != nil {
		t.Fatal(err)
	}
	dependencies := index.Statements[0].Predicate.BuildDefinition.ResolvedDependencies
	if len(dependencies) != 1 || dependencies[0].URI != "nkd:prebuilt/agent" ||
		dependencies[0].Digest["sha256"] != strings.TrimPrefix(report.Artifacts[0].SHA256, "sha256:") {
		t.Fatalf("prebuilt dependency was not derived from the qualified graph: %+v", dependencies)
	}
}

func TestCallerDeclaredProvenanceCannotMintPublisherAuthority(t *testing.T) {
	index := BuildIndex(provenanceDigest('f'), "", []Artifact{{
		Component: "agent", SHA256: provenanceDigest('a'),
		Deps: []Dependency{{URI: "nkd:prebuilt/agent", Digest: map[string]string{
			"sha256": strings.Repeat("a", 64),
		}}},
	}})
	fabricated := &QualifiedSet{valid: true, index: *index}
	if _, err := fabricated.Index(); err != nil {
		t.Fatalf("declaration-only provenance should remain available for diagnostics: %v", err)
	}
	if _, err := fabricated.LocalReceipts(); errorCode(err) != nkderr.CodePreflightFailed {
		t.Fatalf("fabricated provenance minted publisher authority: %v", err)
	}

	report, graph := qualifiedPrebuiltReport(t, []byte("verified artifact"))
	forged := *graph
	forged.Nodes = append([]ir.Node(nil), graph.Nodes...)
	for index := range forged.Nodes {
		if forged.Nodes[index].Kind == ir.KindPull {
			forged.Nodes[index].Digest = provenanceDigest('9')
		}
	}
	if _, err := BuildQualifiedFromReport(context.Background(), report, &forged); errorCode(err) != nkderr.CodeVerifyFailed {
		t.Fatalf("graph/caller substitution did not fail closed: %v", err)
	}

	assembled := &pipeline.Report{
		BuildDigest: report.BuildDigest,
		Artifacts:   append([]pipeline.ArtifactResult(nil), report.Artifacts...),
	}
	if _, err := BuildQualifiedFromReport(context.Background(), assembled, graph); errorCode(err) != nkderr.CodePreflightFailed {
		t.Fatalf("caller-assembled report minted provenance authority: %v", err)
	}
}

func TestQualifiedDependencyProjectionSupportsBareToolchainIdentity(t *testing.T) {
	component := "agent"
	input := provenanceDigest('4')
	toolchain := provenanceDigest('5')
	dependencies := qualifiedBuildDependencies(pipeline.ArtifactResult{
		Component: component, Toolchain: toolchain, InputDigest: input,
	}, ir.Node{Component: component, BuilderType: ir.BuilderOCIHermetic}, ir.Node{
		Commit: strings.Repeat("6", 40), TreeHash: strings.Repeat("7", 40),
		VendorSpecDigest: provenanceDigest('8'),
	})
	index := BuildIndex(provenanceDigest('9'), provenanceDigest('a'), []Artifact{{
		Component: component, SHA256: provenanceDigest('b'), InputDigest: input,
		BuilderType: ir.BuilderOCIHermetic, Deps: dependencies,
	}})
	if err := index.Validate(); err != nil {
		t.Fatalf("valid bare toolchain content identity was not projected canonically: %v\n%+v", err, dependencies)
	}
	found := false
	for _, dependency := range index.Statements[0].Predicate.BuildDefinition.ResolvedDependencies {
		found = found || (dependency.URI == "nkd:toolchain/agent" &&
			dependency.Digest["sha256"] == strings.TrimPrefix(toolchain, "sha256:"))
	}
	if !found {
		t.Fatalf("bare toolchain digest missing from dependency projection: %+v", dependencies)
	}
}

func TestQualifiedSourceDependencyDoesNotAssumeNeedsOrder(t *testing.T) {
	fetch := ir.Node{ID: "fetch-image", Kind: ir.KindFetch}
	material := ir.Node{ID: "build-agent", Kind: ir.KindBuild}
	byID := map[string]ir.Node{fetch.ID: fetch, material.ID: material}
	got, err := qualifiedSourceDependency(ir.Node{
		Component: "image", Needs: []string{material.ID, fetch.ID},
	}, byID)
	if err != nil || got.ID != fetch.ID {
		t.Fatalf("qualified source dependency = %#v, %v; want %q", got, err, fetch.ID)
	}
	for _, test := range []struct {
		name  string
		needs []string
		code  nkderr.Code
	}{
		{name: "missing fetch", needs: []string{material.ID}, code: nkderr.CodeInvalidCatalog},
		{name: "multiple fetches", needs: []string{fetch.ID, "fetch-other"}, code: nkderr.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := qualifiedSourceDependency(ir.Node{Component: "image", Needs: test.needs}, map[string]ir.Node{
				fetch.ID: fetch, "fetch-other": {ID: "fetch-other", Kind: ir.KindFetch}, material.ID: material,
			})
			if errorCode(err) != test.code {
				t.Fatalf("qualified source dependency error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestQualifiedDependencyProjectionIsMirrorInvariant(t *testing.T) {
	component := "agent"
	input := provenanceDigest('4')
	toolchainDigest := provenanceDigest('5')
	artifact := provenanceDigest('b')
	profile := provenanceDigest('a')
	buildDigest := provenanceDigest('9')
	fetch := ir.Node{
		Commit: strings.Repeat("6", 40), TreeHash: strings.Repeat("7", 40),
		VendorSpecDigest: provenanceDigest('8'),
	}

	buildIndex := func(toolchain string) *Index {
		result := pipeline.ArtifactResult{
			Component: component, Toolchain: toolchain, InputDigest: input,
		}
		build := ir.Node{
			Component: component, BuilderType: ir.BuilderOCIHermetic,
			Toolchain: toolchain,
		}
		return BuildIndex(buildDigest, profile, []Artifact{{
			Component: component, SHA256: artifact, InputDigest: input,
			BuilderType: ir.BuilderOCIHermetic,
			Deps:        qualifiedBuildDependencies(result, build, fetch),
		}})
	}

	online := buildIndex("registry.online.example/toolchain@" + toolchainDigest)
	mirror := buildIndex("registry.airgap.example/mirror/toolchain@" + toolchainDigest)
	onlineDigest, err := online.Digest()
	if err != nil {
		t.Fatal(err)
	}
	mirrorDigest, err := mirror.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if onlineDigest != mirrorDigest {
		t.Fatalf("toolchain registry relocation changed provenance identity: %s != %s", onlineDigest, mirrorDigest)
	}
	dependencies := online.Statements[0].Predicate.BuildDefinition.ResolvedDependencies
	for _, dependency := range dependencies {
		if strings.Contains(dependency.URI, "registry.online.example") ||
			strings.Contains(dependency.URI, "registry.airgap.example") {
			t.Fatalf("provenance retained a transport locator: %+v", dependency)
		}
	}
}

func qualifiedPrebuiltReport(t *testing.T, payload []byte) (*pipeline.Report, *ir.Graph) {
	t.Helper()
	sum := sha256.Sum256(payload)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	graph := &ir.Graph{
		SchemaVersion: ir.SchemaVersion,
		Platform:      "linux/amd64",
		Nodes: []ir.Node{
			{
				ID: "pull-agent", Kind: ir.KindPull, Component: "agent",
				Artifact: "https://artifacts.example.test/agent", Digest: digest,
				Platform: "linux/amd64", Network: ir.NetworkDefault, OutputType: "raw",
				ArtifactPath: "agent", Version: "1.0.0",
			},
			{ID: "verify", Kind: ir.KindVerify, Needs: []string{"pull-agent"}},
		},
	}
	graph.Normalize()
	buildDigest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}

	work := filepath.Join(t.TempDir(), "work")
	sourceRoot := filepath.Join(work, "src")
	artifactPath := filepath.Join(work, "fetch", "agent", "agent")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{
		Component: "agent", OutputType: "raw",
		Qualification: pipeline.ArtifactProfileFreeAcquisition,
		EngineClass:   ir.EnginePrebuiltAcquire,
		Path:          artifactPath, SHA256: digest, Size: int64(len(payload)),
	}
	resultPayload, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	resultDirectory := filepath.Join(filepath.Dir(sourceRoot), "results")
	if err := os.MkdirAll(resultDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resultDirectory, "pull-agent.json"), resultPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := (pipeline.Local{}).VerifyResults(graph, buildDigest, resultDirectory,
		pipeline.RunOptions{Ctx: context.Background(), SourceRoot: sourceRoot})
	if err != nil {
		t.Fatal(err)
	}
	return report, graph
}

func errorCode(err error) nkderr.Code {
	if coded, ok := err.(*nkderr.Error); ok {
		return coded.Code
	}
	return ""
}

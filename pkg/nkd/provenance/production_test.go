package provenance

import (
	"errors"
	"strings"
	"testing"
	"time"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

func requireCode(t *testing.T, err error, want nkderr.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	var coded *nkderr.Error
	if !errors.As(err, &coded) {
		t.Fatalf("error %v is not coded", err)
	}
	if coded.Code != want {
		t.Fatalf("error code = %s, want %s (%v)", coded.Code, want, err)
	}
}

const (
	testPlanDigest       = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testDefinitionDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	testProfileDigest    = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	testCatalogDigest    = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	testCommit           = "ed9fb09b04fd40bf331d25df2222206ffaca593e"
	testTree             = "309887e8cdb8ee24a39785b864390eda3afce5ec"
	testToolchain        = "docker.io/library/golang@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6"
)

func testRunTimes() (time.Time, time.Time) {
	started := time.Date(2026, 9, 4, 17, 5, 3, 0, time.UTC)
	return started, started.Add(92 * time.Second)
}

func testProductionInputs() ProductionInputs {
	return ProductionInputs{
		BuildType:               BuildTypeWrappedBuild,
		PlanDigest:              testPlanDigest,
		FactoryReference:        "nke-v1@0.1.0",
		FactoryDefinitionDigest: testDefinitionDigest,
		DeliveryProfileDigest:   testProfileDigest,
		CatalogDigest:           testCatalogDigest,
		GeneratorVersion:        "nkdctl/0.1.0",
		SigningMode:             SigningModeDryRun,
		ResolvedDependencies: []Dependency{
			ToolchainDependency(testToolchain),
			SourceDependency("https://github.com/NVIDIA/nke", testCommit, testTree),
		},
	}
}

func testLocalRunContext(t *testing.T) RunContext {
	t.Helper()
	started, finished := testRunTimes()
	run, err := NewLocalRunContext(testPlanDigest, started, finished)
	if err != nil {
		t.Fatalf("NewLocalRunContext: %v", err)
	}
	return run
}

// The exact canonical predicate bytes are what a signer hashes and what cosign
// attest consumes, so they are pinned here byte for byte.
const goldenProductionPredicate = `{"buildDefinition":{"buildType":"https://nkd.dev/buildtypes/wrapped-build/v1","externalParameters":{"catalogDigest":"sha256:4444444444444444444444444444444444444444444444444444444444444444","deliveryProfileDigest":"sha256:3333333333333333333333333333333333333333333333333333333333333333","factoryDefinitionDigest":"sha256:2222222222222222222222222222222222222222222222222222222222222222","factoryReference":"nke-v1@0.1.0","planDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"},"internalParameters":{"generatorVersion":"nkdctl/0.1.0","signingMode":"dry-run"},"resolvedDependencies":[{"uri":"docker.io/library/golang@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6"},{"digest":{"gitCommit":"ed9fb09b04fd40bf331d25df2222206ffaca593e","gitTree":"309887e8cdb8ee24a39785b864390eda3afce5ec"},"uri":"https://github.com/NVIDIA/nke@ed9fb09b04fd40bf331d25df2222206ffaca593e"},{"digest":{"sha256":"1111111111111111111111111111111111111111111111111111111111111111"},"uri":"nkd:factory-plan"}]},"runDetails":{"builder":{"id":"https://nkd.dev/builders/local-factory-run/v1"},"metadata":{"finishedOn":"2026-09-04T17:06:35Z","invocationId":"sha256:1111111111111111111111111111111111111111111111111111111111111111/2026-09-04T17:05:03Z","startedOn":"2026-09-04T17:05:03Z"}}}`

func TestProductionPredicateGolden(t *testing.T) {
	predicate := ProductionPredicate(testLocalRunContext(t), testProductionInputs())
	raw, err := MarshalProduction(predicate)
	if err != nil {
		t.Fatalf("MarshalProduction: %v", err)
	}
	if string(raw) != goldenProductionPredicate {
		t.Fatalf("production predicate bytes diverged:\n got: %s\nwant: %s", raw, goldenProductionPredicate)
	}
}

func TestProductionPredicateIsDeterministicAndSorted(t *testing.T) {
	inputs := testProductionInputs()
	first, err := MarshalProduction(ProductionPredicate(testLocalRunContext(t), inputs))
	if err != nil {
		t.Fatalf("MarshalProduction: %v", err)
	}
	// Reorder the caller-supplied dependencies: the emitter must normalize.
	inputs.ResolvedDependencies = []Dependency{
		SourceDependency("https://github.com/NVIDIA/nke", testCommit, testTree),
		ToolchainDependency(testToolchain),
	}
	second, err := MarshalProduction(ProductionPredicate(testLocalRunContext(t), inputs))
	if err != nil {
		t.Fatalf("MarshalProduction: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("production predicate is not order-independent:\n%s\n%s", first, second)
	}
}

func TestProductionPredicateIsNotAStatement(t *testing.T) {
	raw, err := MarshalProduction(ProductionPredicate(testLocalRunContext(t), testProductionInputs()))
	if err != nil {
		t.Fatalf("MarshalProduction: %v", err)
	}
	for _, forbidden := range []string{`"_type"`, `"subject"`, `"predicateType"`, `"predicate"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("predicate must not be wrapped in an in-toto statement (found %s): %s", forbidden, raw)
		}
	}
}

func TestProductionBuildTypes(t *testing.T) {
	for _, buildType := range []string{BuildTypeHermeticBuild, BuildTypeWrappedBuild, BuildTypeAcquired} {
		inputs := testProductionInputs()
		inputs.BuildType = buildType
		if err := ValidateProduction(ProductionPredicate(testLocalRunContext(t), inputs)); err != nil {
			t.Fatalf("build type %s rejected: %v", buildType, err)
		}
	}
	inputs := testProductionInputs()
	inputs.BuildType = BuildTypeOCIHermetic // a reproduction build type is not a production one
	requireCode(t, ValidateProduction(ProductionPredicate(testLocalRunContext(t), inputs)), nkderr.CodeInvalidCatalog)
}

func TestProductionPredicateRejectsMissingBuilderID(t *testing.T) {
	predicate := ProductionPredicate(testLocalRunContext(t), testProductionInputs())
	predicate.RunDetails.Builder.ID = ""
	requireCode(t, ValidateProduction(predicate), nkderr.CodeProvenanceMissing)

	predicate = ProductionPredicate(nil, testProductionInputs())
	requireCode(t, ValidateProduction(predicate), nkderr.CodeProvenanceMissing)
}

func TestProductionPredicateRejectsMissingInvocation(t *testing.T) {
	predicate := ProductionPredicate(testLocalRunContext(t), testProductionInputs())
	predicate.RunDetails.Metadata["invocationId"] = ""
	requireCode(t, ValidateProduction(predicate), nkderr.CodeProvenanceMissing)

	predicate = ProductionPredicate(testLocalRunContext(t), testProductionInputs())
	delete(predicate.RunDetails.Metadata, "invocationId")
	requireCode(t, ValidateProduction(predicate), nkderr.CodeProvenanceMissing)
}

func TestProductionPredicateRejectsMissingOrInvertedTimestamps(t *testing.T) {
	predicate := ProductionPredicate(testLocalRunContext(t), testProductionInputs())
	delete(predicate.RunDetails.Metadata, "startedOn")
	requireCode(t, ValidateProduction(predicate), nkderr.CodeProvenanceMissing)

	predicate = ProductionPredicate(testLocalRunContext(t), testProductionInputs())
	predicate.RunDetails.Metadata["finishedOn"] = ""
	requireCode(t, ValidateProduction(predicate), nkderr.CodeProvenanceMissing)

	predicate = ProductionPredicate(testLocalRunContext(t), testProductionInputs())
	predicate.RunDetails.Metadata["finishedOn"] = "2026-09-04T17:00:00Z"
	requireCode(t, ValidateProduction(predicate), nkderr.CodeUnverifiedRef)
}

func TestProductionPredicateRejectsUnpinnedInputs(t *testing.T) {
	inputs := testProductionInputs()
	inputs.PlanDigest = "not-a-digest"
	requireCode(t, ValidateProduction(ProductionPredicate(testLocalRunContext(t), inputs)), nkderr.CodeUnverifiedRef)

	inputs = testProductionInputs()
	inputs.FactoryReference = ""
	requireCode(t, ValidateProduction(ProductionPredicate(testLocalRunContext(t), inputs)), nkderr.CodeProvenanceMissing)

	inputs = testProductionInputs()
	inputs.ResolvedDependencies = append(inputs.ResolvedDependencies, Dependency{URI: "docker.io/library/golang:1.26.6"})
	requireCode(t, ValidateProduction(ProductionPredicate(testLocalRunContext(t), inputs)), nkderr.CodeUnpinnedInput)

	inputs = testProductionInputs()
	inputs.SigningMode = "off"
	requireCode(t, ValidateProduction(ProductionPredicate(testLocalRunContext(t), inputs)), nkderr.CodeInvalidCatalog)

	inputs = testProductionInputs()
	inputs.GeneratorVersion = ""
	requireCode(t, ValidateProduction(ProductionPredicate(testLocalRunContext(t), inputs)), nkderr.CodeProvenanceMissing)
}

func TestProductionPredicateRequiresThePlanDependency(t *testing.T) {
	predicate := ProductionPredicate(testLocalRunContext(t), testProductionInputs())
	kept := predicate.BuildDefinition.ResolvedDependencies[:0]
	for _, dependency := range predicate.BuildDefinition.ResolvedDependencies {
		if dependency.URI != PlanDependencyURI {
			kept = append(kept, dependency)
		}
	}
	predicate.BuildDefinition.ResolvedDependencies = kept
	requireCode(t, ValidateProduction(predicate), nkderr.CodeProvenanceMissing)

	predicate = ProductionPredicate(testLocalRunContext(t), testProductionInputs())
	for i := range predicate.BuildDefinition.ResolvedDependencies {
		if predicate.BuildDefinition.ResolvedDependencies[i].URI == PlanDependencyURI {
			predicate.BuildDefinition.ResolvedDependencies[i].Digest = map[string]string{
				"sha256": strings.Repeat("9", 64),
			}
		}
	}
	requireCode(t, ValidateProduction(predicate), nkderr.CodeUnverifiedRef)
}

func TestLocalRunContext(t *testing.T) {
	started, finished := testRunTimes()
	run, err := NewLocalRunContext(testPlanDigest, started, finished)
	if err != nil {
		t.Fatalf("NewLocalRunContext: %v", err)
	}
	if run.BuilderID() != LocalBuilderID {
		t.Fatalf("builder id = %q", run.BuilderID())
	}
	if run.InvocationID() != testPlanDigest+"/2026-09-04T17:05:03Z" {
		t.Fatalf("invocation id = %q", run.InvocationID())
	}
	if !run.StartedOn().Equal(started) || !run.FinishedOn().Equal(finished) {
		t.Fatalf("run window = %v..%v", run.StartedOn(), run.FinishedOn())
	}

	_, err = NewLocalRunContext("plan", started, finished)
	requireCode(t, err, nkderr.CodeUnverifiedRef)
	_, err = NewLocalRunContext(testPlanDigest, time.Time{}, finished)
	requireCode(t, err, nkderr.CodeProvenanceMissing)
	_, err = NewLocalRunContext(testPlanDigest, started, time.Time{})
	requireCode(t, err, nkderr.CodeProvenanceMissing)
	_, err = NewLocalRunContext(testPlanDigest, finished, started)
	requireCode(t, err, nkderr.CodeUnverifiedRef)
}

func githubEnv() map[string]string {
	return map[string]string{
		"GITHUB_SERVER_URL":   "https://github.com/",
		"GITHUB_REPOSITORY":   "NVIDIA/nke",
		"GITHUB_WORKFLOW_REF": "NVIDIA/nke/.github/workflows/nkd-factory.yml@refs/heads/main",
		"GITHUB_RUN_ID":       "17654321",
		"GITHUB_RUN_ATTEMPT":  "2",
	}
}

func TestGitHubRunContext(t *testing.T) {
	started, finished := testRunTimes()
	env := githubEnv()
	run, err := NewGitHubRunContext(func(name string) string { return env[name] }, started, finished)
	if err != nil {
		t.Fatalf("NewGitHubRunContext: %v", err)
	}
	wantBuilder := "https://github.com/NVIDIA/nke/.github/workflows/nkd-factory.yml@refs/heads/main"
	if run.BuilderID() != wantBuilder {
		t.Fatalf("builder id = %q, want %q", run.BuilderID(), wantBuilder)
	}
	wantInvocation := "https://github.com/NVIDIA/nke/actions/runs/17654321/attempts/2"
	if run.InvocationID() != wantInvocation {
		t.Fatalf("invocation id = %q, want %q", run.InvocationID(), wantInvocation)
	}

	predicate := ProductionPredicate(run, testProductionInputs())
	if err := ValidateProduction(predicate); err != nil {
		t.Fatalf("GitHub production predicate rejected: %v", err)
	}
}

func TestGitHubRunContextRequiresEveryRunVariable(t *testing.T) {
	started, finished := testRunTimes()
	for _, missing := range []string{
		"GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_WORKFLOW_REF",
		"GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT",
	} {
		env := githubEnv()
		delete(env, missing)
		_, err := NewGitHubRunContext(func(name string) string { return env[name] }, started, finished)
		requireCode(t, err, nkderr.CodeProvenanceMissing)
	}
	_, err := NewGitHubRunContext(nil, started, finished)
	requireCode(t, err, nkderr.CodeUsage)

	env := githubEnv()
	_, err = NewGitHubRunContext(func(name string) string { return env[name] }, time.Time{}, finished)
	requireCode(t, err, nkderr.CodeProvenanceMissing)
}

// The reproduction validator governs a different shape and must never accept a
// production predicate, however well formed that predicate is.
func TestReproductionValidatorRejectsTheProductionShape(t *testing.T) {
	predicate := ProductionPredicate(testLocalRunContext(t), testProductionInputs())
	if err := ValidateProduction(predicate); err != nil {
		t.Fatalf("production predicate must be valid before this test means anything: %v", err)
	}
	index := &Index{
		SchemaVersion: IndexSchemaVersion,
		RecipeDigest:  testDefinitionDigest,
		Statements: []Statement{{
			Type:          statementType,
			PredicateType: predicateType,
			Subject:       []Subject{{Name: "nkectl", Digest: map[string]string{"sha256": strings.Repeat("a", 64)}}},
			Predicate:     predicate,
		}},
	}
	if err := index.Validate(); err == nil {
		t.Fatal("reproduction validator accepted a production predicate")
	}

	// Even after the production build type is swapped for a reproduction one,
	// the build-time internal parameters keep it out of the reproduction lane.
	index.Statements[0].Predicate.BuildDefinition.BuildType = BuildTypeOCIHermetic
	index.Statements[0].Predicate.BuildDefinition.ExternalParameters = map[string]string{
		"recipeDigest": testDefinitionDigest,
		"component":    "nkectl",
		"inputDigest":  testPlanDigest,
	}
	index.Statements[0].Predicate.RunDetails.Builder.ID = testProfileDigest
	index.Statements[0].Predicate.RunDetails.Metadata = map[string]string{
		"invocationId": testDefinitionDigest + "/nkectl",
	}
	requireCode(t, index.Validate(), nkderr.CodeUnverifiedRef)

	index.Statements[0].Predicate.BuildDefinition.InternalParameters = nil
	index.Statements[0].Predicate.BuildDefinition.ResolvedDependencies = nil
	if err := index.Validate(); err != nil {
		t.Fatalf("reproduction index must still validate once the production fields are gone: %v", err)
	}
}

// Reproduction statements never emit internalParameters, so existing
// reproduction bytes and digests are unchanged by the production emitter.
func TestReproductionStatementsOmitInternalParameters(t *testing.T) {
	index := BuildIndex(provenanceDigest('f'), provenanceDigest('3'), sampleArtifacts())
	raw, err := index.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if raw == "" {
		t.Fatal("empty reproduction digest")
	}
	for _, statement := range index.Statements {
		if statement.Predicate.BuildDefinition.InternalParameters != nil {
			t.Fatal("reproduction statements must not carry internal parameters")
		}
	}
}

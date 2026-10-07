// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package provenance

// production.go emits the PRODUCTION SLSA v1 predicate for one executed factory
// run. It is deliberately separate from the reproduction emitter in
// provenance.go: a reproduction statement OMITS every build-time fact so its
// digest is a stable expected-evidence identity, while a production predicate
// exists precisely to record who ran the build, when, and against which exact
// plan. The two shapes must never be confused, so each has its own validator and
// Index.Validate (the reproduction validator) rejects a production predicate.
//
// This emitter returns the PREDICATE only. `cosign attest` wraps the predicate
// into the in-toto Statement and supplies the subject from the artifact being
// attested, so writing a Statement here would produce a doubly-wrapped
// attestation.

import (
	"os"
	"sort"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Production build types. They are distinct from the reproduction build types in
// provenance.go because they describe how a factory node produced bytes, not how
// a build's expected digests were derived.
const (
	// BuildTypeHermeticBuild is a build executed from pinned source inside a
	// pinned toolchain container with no unpinned network input.
	BuildTypeHermeticBuild = "https://nkd.dev/buildtypes/hermetic-build/v2"
	// BuildTypeWrappedBuild is a build that shells an upstream project's own
	// build entrypoint (its Makefile or script) inside a pinned toolchain.
	BuildTypeWrappedBuild = "https://nkd.dev/buildtypes/wrapped-build/v2"
	// BuildTypeAcquired is a prebuilt artifact acquired by exact digest; NKD
	// verified its identity but did not compile it.
	BuildTypeAcquired = "https://nkd.dev/buildtypes/acquired/v2"
)

// LocalBuilderID is the builder identity for a `factory run` executed on an
// operator workstation. It is deliberately NOT a qualified release builder: it
// names an unattested local execution so a consumer can refuse it by identity.
const LocalBuilderID = "https://nkd.dev/builders/local-factory-run/v1"

// PlanDependencyURI is the resolved-dependency URI under which the exact factory
// plan identity is recorded.
const PlanDependencyURI = "nkd:run-plan"

// Signing modes recorded as an internal parameter. They mirror the delivery
// profile's signing mode: only `cosign` can carry pipeline-attested authority.
const (
	SigningModeDryRun = "dry-run"
	SigningModeCosign = "cosign"
)

// timestampLayout preserves the existing local invocation identity rendering.
const timestampLayout = time.RFC3339Nano

// productionTimestamp matches the protobuf JSON representation used by Cosign's
// SLSA v1 encoder: UTC with 0, 3, 6 or 9 fractional digits. RFC3339Nano alone
// trims individual zeros, so Cosign can otherwise change the exact predicate.
func productionTimestamp(value time.Time) string {
	layout := "2006-01-02T15:04:05.000000000Z"
	switch nanos := value.Nanosecond(); {
	case nanos == 0:
		layout = "2006-01-02T15:04:05Z"
	case nanos%1000000 == 0:
		layout = "2006-01-02T15:04:05.000Z"
	case nanos%1000 == 0:
		layout = "2006-01-02T15:04:05.000000Z"
	}
	return value.UTC().Format(layout)
}

// RunContext is the authenticated-enough description of one build execution:
// which builder ran it, under which invocation identity, and between which
// instants. Implementations are constructed from an environment the caller
// trusts; the predicate never invents any of these facts.
type RunContext interface {
	BuilderID() string
	InvocationID() string
	StartedOn() time.Time
	FinishedOn() time.Time
}

// EnvLookup reads one environment variable. It exists so a run context can be
// constructed from a captured environment in tests without mutating the process.
type EnvLookup func(name string) string

// OSEnvLookup reads the live process environment.
func OSEnvLookup(name string) string { return os.Getenv(name) }

// runTimes is the shared, validated time window of a run context.
type runTimes struct {
	startedOn  time.Time
	finishedOn time.Time
}

func (r runTimes) StartedOn() time.Time  { return r.startedOn }
func (r runTimes) FinishedOn() time.Time { return r.finishedOn }

func newRunTimes(startedOn, finishedOn time.Time) (runTimes, error) {
	if startedOn.IsZero() || finishedOn.IsZero() {
		return runTimes{}, e.New(e.CodeProvenanceMissing,
			"production provenance requires both a start and a finish timestamp")
	}
	if finishedOn.Before(startedOn) {
		return runTimes{}, e.New(e.CodeUnverifiedRef,
			"production provenance finish timestamp precedes its start timestamp")
	}
	return runTimes{startedOn: startedOn.UTC(), finishedOn: finishedOn.UTC()}, nil
}

// GitHubRunContext is the run context of a GitHub Actions job. Every field comes
// from the runner-provided GITHUB_* environment; a missing variable fails closed
// rather than degrading to an anonymous builder.
type GitHubRunContext struct {
	runTimes
	builderID    string
	invocationID string
}

// NewGitHubRunContext binds the exact GitHub Actions run identity.
//
// builder.id is the fully qualified workflow reference (server URL + workflow
// ref), which is what a verifier pins with cosign's --certificate-identity;
// invocationId is the run/attempt URL, which is unique per execution.
func NewGitHubRunContext(lookup EnvLookup, startedOn, finishedOn time.Time) (*GitHubRunContext, error) {
	if lookup == nil {
		return nil, e.New(e.CodeUsage, "GitHub run context requires an environment lookup")
	}
	times, err := newRunTimes(startedOn, finishedOn)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, name := range []string{
		"GITHUB_SERVER_URL", "GITHUB_REPOSITORY", "GITHUB_WORKFLOW_REF",
		"GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT",
	} {
		value := strings.TrimSpace(lookup(name))
		if value == "" {
			return nil, e.New(e.CodeProvenanceMissing,
				"GitHub run context requires %s", name)
		}
		values[name] = value
	}
	server := strings.TrimSuffix(values["GITHUB_SERVER_URL"], "/")
	return &GitHubRunContext{
		runTimes:  times,
		builderID: server + "/" + values["GITHUB_WORKFLOW_REF"],
		invocationID: server + "/" + values["GITHUB_REPOSITORY"] +
			"/actions/runs/" + values["GITHUB_RUN_ID"] +
			"/attempts/" + values["GITHUB_RUN_ATTEMPT"],
	}, nil
}

func (g *GitHubRunContext) BuilderID() string    { return g.builderID }
func (g *GitHubRunContext) InvocationID() string { return g.invocationID }

// LocalRunContext is the run context of an operator's local `factory run`. Its
// invocation identity is the exact plan digest plus the run's start instant,
// which is the only thing that distinguishes two local runs of the same plan.
type LocalRunContext struct {
	runTimes
	invocationID string
}

// NewLocalRunContext binds a local execution to the exact plan it executed.
func NewLocalRunContext(planDigest string, startedOn, finishedOn time.Time) (*LocalRunContext, error) {
	if !canonical.IsDigest(planDigest) {
		return nil, e.New(e.CodeUnverifiedRef,
			"local run context requires an exact lowercase sha256 plan digest")
	}
	times, err := newRunTimes(startedOn, finishedOn)
	if err != nil {
		return nil, err
	}
	return &LocalRunContext{
		runTimes:     times,
		invocationID: planDigest + "/" + times.startedOn.Format(timestampLayout),
	}, nil
}

func (l *LocalRunContext) BuilderID() string    { return LocalBuilderID }
func (l *LocalRunContext) InvocationID() string { return l.invocationID }

// ProductionInputs are the exact identities a production predicate binds. Every
// digest field is a canonical `sha256:<hex>` identity produced by the owning
// module; this struct never derives one.
type ProductionInputs struct {
	BuildType         string
	PlanDigest        string
	RunPlanReference  string
	DefinitionDigest  string
	EnvironmentDigest string
	CatalogDigest     string
	GeneratorVersion  string
	SigningMode       string
	// ResolvedDependencies are the pinned source and toolchain inputs. The plan
	// dependency is appended by ProductionPredicate and must not be supplied here.
	ResolvedDependencies []Dependency
}

// SourceDependency is the canonical resolved-dependency form for a pinned git
// source: the repository URI at its exact commit, annotated with the exact tree
// hash that commit resolves to.
func SourceDependency(repo, commit, tree string) Dependency {
	digest := map[string]string{}
	if commit != "" {
		digest["gitCommit"] = commit
	}
	if tree != "" {
		digest["gitTree"] = tree
	}
	uri := repo
	if commit != "" {
		uri = repo + "@" + commit
	}
	return Dependency{URI: uri, Digest: digest}
}

// ToolchainDependency is the canonical resolved-dependency form for a pinned OCI
// toolchain image reference (`image@sha256:<hex>`).
func ToolchainDependency(imageRef string) Dependency {
	return Dependency{URI: imageRef}
}

// ProductionPredicate renders the https://slsa.dev/provenance/v1 predicate for
// one executed run. It performs no validation of its own — call
// ValidateProduction (or MarshalProduction, which validates) before treating the
// result as evidence.
func ProductionPredicate(run RunContext, in ProductionInputs) Predicate {
	dependencies := append([]Dependency(nil), in.ResolvedDependencies...)
	if in.PlanDigest != "" {
		dependencies = append(dependencies, Dependency{
			URI:    PlanDependencyURI,
			Digest: map[string]string{"sha256": hexOf(in.PlanDigest)},
		})
	}
	sort.SliceStable(dependencies, func(i, j int) bool { return dependencies[i].URI < dependencies[j].URI })

	builderID, invocationID := "", ""
	metadata := map[string]string{}
	if run != nil {
		builderID = run.BuilderID()
		invocationID = run.InvocationID()
		metadata["invocationId"] = invocationID
		metadata["startedOn"] = productionTimestamp(run.StartedOn())
		metadata["finishedOn"] = productionTimestamp(run.FinishedOn())
	}
	return Predicate{
		BuildDefinition: BuildDefinition{
			BuildType: in.BuildType,
			ExternalParameters: map[string]string{
				"planDigest":        in.PlanDigest,
				"runPlanReference":  in.RunPlanReference,
				"definitionDigest":  in.DefinitionDigest,
				"environmentDigest": in.EnvironmentDigest,
				"catalogDigest":     in.CatalogDigest,
			},
			InternalParameters: map[string]string{
				"generatorVersion": in.GeneratorVersion,
				"signingMode":      in.SigningMode,
			},
			ResolvedDependencies: dependencies,
		},
		RunDetails: RunDetails{
			Builder:  Builder{ID: builderID},
			Metadata: metadata,
		},
	}
}

// MarshalProduction validates the predicate and renders its exact canonical JSON
// bytes. These are the bytes a signer hashes and `cosign attest --predicate`
// consumes, so they must be deterministic and independent of Go map ordering.
func MarshalProduction(p Predicate) ([]byte, error) {
	if err := ValidateProduction(p); err != nil {
		return nil, err
	}
	return canonical.Marshal(p)
}

var productionBuildTypes = map[string]bool{
	BuildTypeHermeticBuild: true,
	BuildTypeWrappedBuild:  true,
	BuildTypeAcquired:      true,
}

var productionExternalParameters = []string{
	"catalogDigest", "definitionDigest", "environmentDigest",
	"planDigest", "runPlanReference",
}

// productionExternalDigests are the external parameters that must be exact
// content identities. runPlanReference is a human/logical reference and is only
// required to be non-empty and canonical whitespace.
var productionExternalDigests = []string{
	"catalogDigest", "definitionDigest", "environmentDigest", "planDigest",
}

// ValidateProduction enforces the production predicate contract: a known build
// type, exact run-plan/definition/environment/catalog identities, a declared generator
// and signing mode, canonically sorted and content-addressed dependencies
// including the plan itself, a non-empty builder identity, and a complete,
// ordered invocation/time triple. It is INDEPENDENT of Index.Validate, which
// governs the reproduction shape.
func ValidateProduction(p Predicate) error {
	definition := p.BuildDefinition
	if !productionBuildTypes[definition.BuildType] {
		return e.New(e.CodeInvalidCatalog,
			"production provenance buildType %q is not a supported NKD production build type",
			definition.BuildType)
	}
	if err := validateProductionParameters(definition); err != nil {
		return err
	}
	if err := validateProductionDependencies(definition, definition.ExternalParameters["planDigest"]); err != nil {
		return err
	}
	return validateProductionRunDetails(p.RunDetails)
}

func validateProductionParameters(definition BuildDefinition) error {
	parameters := definition.ExternalParameters
	if len(parameters) != len(productionExternalParameters) {
		return e.New(e.CodeInvalidCatalog,
			"production provenance externalParameters must contain exactly %v", productionExternalParameters)
	}
	for _, name := range productionExternalParameters {
		value, ok := parameters[name]
		if !ok || strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return e.New(e.CodeProvenanceMissing,
				"production provenance externalParameters.%s is missing or non-canonical", name)
		}
	}
	for _, name := range productionExternalDigests {
		if !canonical.IsDigest(parameters[name]) {
			return e.New(e.CodeUnverifiedRef,
				"production provenance externalParameters.%s %q is not an exact lowercase sha256 identity",
				name, parameters[name])
		}
	}
	internal := definition.InternalParameters
	if len(internal) != 2 {
		return e.New(e.CodeInvalidCatalog,
			"production provenance internalParameters must contain exactly [generatorVersion signingMode]")
	}
	if strings.TrimSpace(internal["generatorVersion"]) == "" {
		return e.New(e.CodeProvenanceMissing,
			"production provenance internalParameters.generatorVersion is required")
	}
	switch internal["signingMode"] {
	case SigningModeDryRun, SigningModeCosign:
	default:
		return e.New(e.CodeInvalidCatalog,
			"production provenance internalParameters.signingMode %q is not %q or %q",
			internal["signingMode"], SigningModeDryRun, SigningModeCosign)
	}
	return nil
}

func validateProductionDependencies(definition BuildDefinition, planDigest string) error {
	previous := ""
	planBound := false
	for _, dependency := range definition.ResolvedDependencies {
		uri := dependency.URI
		if strings.TrimSpace(uri) == "" || strings.TrimSpace(uri) != uri ||
			(previous != "" && uri <= previous) {
			return e.New(e.CodeUnpinnedInput,
				"production provenance dependencies must have canonical, strictly sorted, unique URIs")
		}
		previous = uri
		if !validProductionDependencyIdentity(dependency) {
			return e.New(e.CodeUnpinnedInput,
				"production provenance dependency %q has no supported exact content identity", uri)
		}
		if uri == PlanDependencyURI {
			if dependency.Digest["sha256"] != hexOf(planDigest) {
				return e.New(e.CodeUnverifiedRef,
					"production provenance plan dependency does not bind the exact plan digest")
			}
			planBound = true
		}
	}
	if !planBound {
		return e.New(e.CodeProvenanceMissing,
			"production provenance must resolve the exact factory plan as a dependency")
	}
	return nil
}

func validateProductionRunDetails(details RunDetails) error {
	builderID := details.Builder.ID
	if strings.TrimSpace(builderID) == "" || strings.TrimSpace(builderID) != builderID ||
		strings.ContainsAny(builderID, " \t\n") {
		return e.New(e.CodeProvenanceMissing,
			"production provenance requires a canonical runDetails.builder.id")
	}
	metadata := details.Metadata
	if len(metadata) != 3 {
		return e.New(e.CodeProvenanceMissing,
			"production provenance metadata must contain exactly [finishedOn invocationId startedOn]")
	}
	if strings.TrimSpace(metadata["invocationId"]) == "" {
		return e.New(e.CodeProvenanceMissing,
			"production provenance requires a non-empty metadata.invocationId")
	}
	startedOn, err := time.Parse(time.RFC3339, metadata["startedOn"])
	if err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err,
			"production provenance metadata.startedOn is not an RFC 3339 timestamp")
	}
	finishedOn, err := time.Parse(time.RFC3339, metadata["finishedOn"])
	if err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err,
			"production provenance metadata.finishedOn is not an RFC 3339 timestamp")
	}
	if finishedOn.Before(startedOn) {
		return e.New(e.CodeUnverifiedRef,
			"production provenance metadata.finishedOn precedes metadata.startedOn")
	}
	return nil
}

// validProductionDependencyIdentity accepts a digest-pinned URI (an OCI image
// reference) or a digest set whose every entry is a supported exact identity. A
// git source may legitimately carry both gitCommit and gitTree.
func validProductionDependencyIdentity(dependency Dependency) bool {
	if len(dependency.Digest) == 0 {
		return strings.Contains(dependency.URI, "@sha256:") &&
			canonical.IsDigest(dependency.URI[strings.LastIndex(dependency.URI, "@")+1:])
	}
	for algorithm, value := range dependency.Digest {
		switch algorithm {
		case "sha256":
			if !bareSHA256.MatchString(value) {
				return false
			}
		case "gitCommit", "gitTree":
			if !gitCommit.MatchString(value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package factory compiles and executes complete Run plans.
// CompileDocuments resolves catalogs and derives a Plan from Definition and
// Environment; Compile accepts an already resolved catalog. RunLocal executes the
// same artifact graph that Render schedules for hosted execution. This package
// owns producer references, typed payload observation, packaging, and evidence joins;
// a compiled Plan describes required work and carries no runtime authority.
package factory

import (
	"context"
	"regexp"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const (
	MaxPlanBytes        = int64(4 << 20)
	PlanSchemaVersion   = "nvkr.dev/run-plan/v5"
	ArtifactGraphSchema = "nvkr.dev/artifact-graph/v5"
	// DefaultRunsOn is the runner an unset Environment.runsOn falls back to. ADR-18 §8
	// runs on a GENERIC hosted runner — the helper orchestrates the pinned container itself —
	// so no bespoke self-hosted runner label is required. Ordinary native ARM64
	// producers use ubuntu-24.04-arm when runsOn is unset in the ordinary hosted
	// route; explicit routes (including the complete ARC projection) remain overrides.
	DefaultRunsOn = "ubuntu-latest"
)

const (
	NodeFetch   = "fetch"
	NodeBuild   = "build"
	NodeAcquire = "acquire"
	NodeVerify  = "terminal-observe"
	// NodeSBOM emits a deterministic CycloneDX SBOM per built artifact plus an
	// aggregate index for one build target (ADR-13 §1). It runs after that
	// target's terminal observation because it binds
	// each component's SBOM to the runner-observed artifact digest that observation
	// gates. It asserts composition only, never signature or reproduction.
	NodeSBOM      = "sbom"
	NodePackage   = "package"
	NodePublish   = "publish"
	NodeInventory = "inventory"
	// NodeSign binds a signature and an SLSA production attestation to the exact
	// published OCI digest of one route. It runs after that route's publication
	// because it consumes the published reference as data.
	NodeSign = "sign"
	// NodeVerifyPublication re-resolves the published reference at the registry and
	// checks the signature record against it, so a run never ends on the claim that
	// signing "probably" bound the right bytes.
	NodeVerifyPublication = "verify-publication"
)

// Signing modes. dry-run executes nothing and contacts no registry; it records the
// exact argv a real signer would run. cosign runs the pinned cosign capability.
const (
	SigningModeDryRun = "dry-run"
	SigningModeCosign = "cosign"
)

const (
	PackageBinaryBundle = "binary-bundle"
	PackageAPT          = "apt-repository"
	PackageOCI          = "oci-root"
	PackageHelm         = "helm-oci"
	PackageImage        = "container-image"
	PackageDiskImage    = "disk-image"
)

const (
	TargetOCI    = "oci"
	TargetAPT    = "apt"
	TargetObject = "object"
)

const (
	ToolFormatRaw     = "raw"
	ToolFormatTarGzip = "tar-gzip"
)

// packageSpec describes dataflow, not ordering. Inputs are artifact selectors:
// target/component, target/*, or package/name.
type packageSpec struct {
	ImageIndex   *ImageIndexPlan   `json:"imageIndex,omitempty"`
	HelmImage    *HelmImageBinding `json:"helmImage,omitempty"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Inputs       []string          `json:"inputs"`
	Distribution string            `json:"distribution,omitempty"`
	Component    string            `json:"component,omitempty"`
}

// HelmImage joins one output dependency with its catalog-owned chart mapping.
type HelmImage struct {
	From   string                  `json:"from"`
	Values catalog.HelmImageValues `json:"values"`
}

// HelmImageBinding is compiler-owned package dataflow, not a publication route.
type HelmImageBinding struct {
	Chart  string               `json:"chart"`
	Images map[string]HelmImage `json:"images"`
}

type Target struct {
	ImageTags     map[string]string `json:"imageTags,omitempty"`
	Kind          string            `json:"kind"`
	Ref           string            `json:"ref"`
	CredentialRef string            `json:"credentialRef,omitempty"`
	Anonymous     bool              `json:"anonymous,omitempty"`
	PlainHTTP     bool              `json:"plainHTTP,omitempty"`
}

type Route struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Target string `json:"target"`
}

type Plan struct {
	Actions           map[string]catalog.ActionPin `json:"actions,omitempty"`
	SchemaVersion     string                       `json:"schemaVersion"`
	Name              string                       `json:"name"`
	GeneratorVersion  string                       `json:"generatorVersion"`
	DefinitionPath    string                       `json:"definitionPath,omitempty"`
	DefinitionDigest  string                       `json:"definitionDigest,omitempty"`
	EnvironmentPath   string                       `json:"environmentPath,omitempty"`
	Qualification     EvidenceTier                 `json:"qualification"`
	CatalogDigest     string                       `json:"catalogDigest"`
	CatalogSnapshot   *catalog.Catalog             `json:"catalogSnapshot"`
	AppliedOverlays   []catalog.AppliedOverlay     `json:"appliedOverlays,omitempty"`
	EnvironmentDigest string                       `json:"environmentDigest,omitempty"`
	Definition        definition.Definition        `json:"definition"`
	Environment       definition.Environment       `json:"environment"`
	Targets           map[string]Target            `json:"targets,omitempty"`
	Routes            []Route                      `json:"routes,omitempty"`
	Builds            []BuildPlan                  `json:"builds"`
	LifecycleOutputs  []LifecycleOutputPlan        `json:"lifecycleOutputs,omitempty"`
	Graph             ArtifactGraph                `json:"graph"`

	// identityDigest is the digest of this plan AS COMPILED, carried across the local
	// runner's workspace relocation. It is deliberately unexported: it never reaches the
	// wire, so it cannot perturb Digest() or the bytes of a generated CI bundle.
	//
	// Relocation rewrites every artifact path from the compiled `.nvkr/factory` prefix
	// into the caller's `--workspace`, which changes the plan's canonical bytes. That is
	// correct for execution and wrong for identity: which local directory a developer
	// happened to build in is not part of what was built. Attestation must quote the
	// compiled identity, so the same release attests the same planDigest from any
	// workspace and agrees with `factory validate` and the CI run.
	identityDigest string
	// workspace is set only by local relocation after validating the compiled
	// plan. Serialized plans cannot use it to substitute artifact paths.
	workspace string
}

type LifecycleOutputPlan struct {
	ImageIndex  *ImageIndexPlan `json:"imageIndex,omitempty"`
	Name        string          `json:"name"`
	Kind        string          `json:"kind"`
	ArtifactID  string          `json:"artifactId"`
	BuildDigest string          `json:"buildDigest"`
	Components  []string        `json:"components"`
}

type LifecycleBoundOutput = delivery.Output

type BuildPlan struct {
	Name            string                   `json:"name"`
	CatalogDigest   string                   `json:"catalogDigest"`
	AppliedOverlays []catalog.AppliedOverlay `json:"appliedOverlays,omitempty"`
	BuildDigest     string                   `json:"buildDigest"`
	Platform        string                   `json:"platform"`
	CatalogSnapshot *catalog.Catalog         `json:"catalogSnapshot"`
	Components      []string                 `json:"components,omitempty"`
	// ResolvedComponents is the exact resolved catalog component set this build
	// compiled from. It is persisted so the SBOM node (ADR-13) can build a
	// faithful CycloneDX document at node-execution time, where the catalog is not
	// reachable — the container-isolated executor only ever sees this plan.
	ResolvedComponents []catalog.NamedComponent `json:"resolvedComponents,omitempty"`
	Graph              *ir.Graph                `json:"executionGraph"`
}

type ArtifactGraph struct {
	SchemaVersion string     `json:"schemaVersion"`
	Nodes         []Node     `json:"nodes"`
	Artifacts     []Artifact `json:"artifacts"`
}

type Node struct {
	ImageIndex       *ImageIndexPlan   `json:"imageIndex,omitempty"`
	HelmImage        *HelmImageBinding `json:"helmImage,omitempty"`
	ID               string            `json:"id"`
	Kind             string            `json:"kind"`
	BuildTarget      string            `json:"buildTarget,omitempty"`
	IRNode           string            `json:"irNode,omitempty"`
	PackageKind      string            `json:"packageKind,omitempty"`
	Distribution     string            `json:"distribution,omitempty"`
	PackageComponent string            `json:"packageComponent,omitempty"`
	Target           string            `json:"target,omitempty"`
	RunsOn           string            `json:"runsOn,omitempty"`
	Network          string            `json:"network,omitempty"`
	Container        string            `json:"container,omitempty"`
	Platform         string            `json:"platform,omitempty"`
	URL              string            `json:"url,omitempty"`
	Digest           string            `json:"digest,omitempty"`
	Executable       string            `json:"executable,omitempty"`
	ExecutableDigest string            `json:"executableDigest,omitempty"`
	ToolFormat       string            `json:"toolFormat,omitempty"`
	Inputs           []string          `json:"inputs,omitempty"`
	Outputs          []string          `json:"outputs,omitempty"`

	Requirements     *execution.Requirements `json:"requirements,omitempty"`
	ExecutionBinding *execution.Binding      `json:"executionBinding,omitempty"`
}

type Artifact struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	MediaType   string `json:"mediaType,omitempty"`
	Path        string `json:"path,omitempty"`
	Producer    string `json:"producer"`
	BuildTarget string `json:"buildTarget,omitempty"`
	Component   string `json:"component,omitempty"`
	Target      string `json:"target,omitempty"`
}

var canonicalName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
var containerPin = regexp.MustCompile(`(?:@|^)sha256:[0-9a-f]{64}$`)

func (graph ArtifactGraph) Digest() (string, error) {
	if err := graph.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindArtifactGraph, graph)
}

func (plan Plan) Digest() (string, error) {
	if err := plan.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindRunPlan, plan)
}

// IdentityDigest returns the digest that names WHAT this plan builds, as opposed to the
// digest of the bytes currently in hand. The two differ only after the local runner has
// relocated artifact paths into a caller-chosen workspace; for a plan straight from the
// compiler they are the same value. Evidence that outlives the run — an attestation
// predicate, a signing record — must quote this one.
func (plan Plan) IdentityDigest() (string, error) {
	if plan.identityDigest != "" {
		return plan.identityDigest, nil
	}
	return plan.Digest()
}

func classifyContext(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "factory operation canceled")
	}
	return err
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

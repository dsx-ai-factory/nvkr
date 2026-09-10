// Package factory compiles operator intent into a static, inspectable AI-factory
// plan. It joins catalog resolution, build IR, artifact dataflow, packaging, and
// environment-specific delivery routes without minting runtime authority.
package factory

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/cluster"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

const (
	CompileSpecAPIVersion       = "nkd.dev/compile-spec/v1"
	CompileSpecKind             = "CompileSpec"
	ExecutionSettingsAPIVersion = "nkd.dev/execution-settings/v1"
	ExecutionSettingsKind       = "ExecutionSettings"
	PlanSchemaVersion           = "nkd.dev/run-plan/v1"
	ArtifactGraphSchema         = "nkd.dev/artifact-graph/v1"
	MaxDefinitionBytes          = int64(4 << 20)
	// DefaultRunsOn is the runner an unset definition.runsOn falls back to. ADR-18 §8
	// runs on a GENERIC hosted runner — the helper orchestrates the pinned container itself —
	// so no bespoke self-hosted runner label is required; operators override runsOn only when
	// they genuinely need a specific runner (e.g. a native arm64 host).
	DefaultRunsOn = "ubuntu-latest"
)

const (
	NodeFetch   = "fetch"
	NodeBuild   = "build"
	NodeAcquire = "acquire"
	NodeAICR    = "aicr-render"
	NodeVerify  = "terminal-observe"
	// NodeSBOM emits a deterministic CycloneDX SBOM per built artifact plus an
	// aggregate index for one recipe target (ADR-13 §1, re-entered into v1 by
	// ADR-13). It runs after that target's terminal observation because it binds
	// each component's SBOM to the runner-observed artifact digest that observation
	// gates. It asserts composition only, never signature or reproduction.
	NodeSBOM        = "sbom"
	NodePackage     = "package"
	NodePublish     = "publish"
	NodeComposition = "composition"
	NodeInventory   = "inventory"
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

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CompileSpec is the compiler-owned handoff derived from a public Definition.
// Publication destinations stay in ExecutionSettings so changing registries cannot
// perturb recipeDigest.
type CompileSpec struct {
	APIVersion       string          `json:"apiVersion"`
	Kind             string          `json:"kind"`
	Metadata         Metadata        `json:"metadata"`
	RunsOn           string          `json:"runsOn"`           // default GitHub runner for packaging/publication and recipe targets
	UtilityContainer string          `json:"utilityContainer"` // exact image for offline non-build nodes
	UtilityPlatform  string          `json:"utilityPlatform"`  // linux/amd64 or linux/arm64
	Catalog          CatalogStack    `json:"catalog"`
	Tools            map[string]Tool `json:"tools,omitempty"`
	Recipes          []RecipeTarget  `json:"recipes"`
	Clusters         []ClusterSpec   `json:"clusters,omitempty"`
	Packages         []PackageSpec   `json:"packages,omitempty"`
}

// Tool is a content-addressed pipeline executable distributed independently of
// release components. ArchiveDigest authenticates transport bytes while
// ExecutableDigest authenticates the exact member that is executed after extraction.
// Keeping tools outside the catalog prevents the factory implementation itself from
// becoming an accidental member of an NKE ReleaseSet.
type Tool struct {
	Format           string `json:"format"`
	URL              string `json:"url"`
	ArchiveDigest    string `json:"archiveDigest"`
	ExecutablePath   string `json:"executablePath"`
	ExecutableDigest string `json:"executableDigest"`
}

type CatalogStack struct {
	Base     string   `json:"base"` // explicit catalog directory
	Overlays []string `json:"overlays,omitempty"`
}

// RecipeTarget gives one resolved recipe/SKU a stable operator name. Reserved holds
// exact AICR axes (service, accelerator, intent, platform).
type RecipeTarget struct {
	Name          string            `json:"name"`
	Catalog       *CatalogStack     `json:"catalog,omitempty"` // optional recipe-specific catalog stack
	Criteria      criteria.Criteria `json:"criteria"`
	Reserved      map[string]string `json:"reserved,omitempty"`
	AICRGenerator string            `json:"aicrGenerator,omitempty"` // exact pipeline tool providing the pinned generator executable
	RunsOn        string            `json:"runsOn"`                  // operator-selected CI runner label/tag
	// LifecycleComponents is the compiler handoff from public Definition outputs.
	// It is never authored or serialized: Definition identity already binds the
	// requested output members, while the full resolved Recipe and its digest stay
	// unchanged. A non-nil value limits execution to those exact members and their
	// transitive source dependencies.
	LifecycleComponents []string `json:"-"`
}

// PackageSpec describes dataflow, not ordering. Inputs are artifact selectors:
// target/component, target/*, target/aicr-bundle, or package/name.
type PackageSpec struct {
	HelmImage    *HelmImageBinding `json:"helmImage,omitempty"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	Inputs       []string          `json:"inputs"`
	Distribution string            `json:"distribution,omitempty"`
	Component    string            `json:"component,omitempty"`
}

// HelmImageBinding is compiler-owned package dataflow, not a publication route.
type HelmImageBinding struct {
	Image string `json:"image"`
	Chart string `json:"chart"`
}

// ClusterSpec joins independently resolved recipe targets into one pre-release
// tenant/operator definition. It produces ClusterComposition rather than inventing
// a ReleaseSet before runtime artifact and evidence digests exist.
type ClusterSpec struct {
	Name                   string                    `json:"name"`
	Type                   string                    `json:"type"`
	CNI                    string                    `json:"cni,omitempty"`
	RecipeTargets          []string                  `json:"recipeTargets"`
	RoleBindings           []cluster.RoleBinding     `json:"roleBindings"`
	ClusterCategories      []string                  `json:"clusterCategories,omitempty"`
	AICRBundle             *cluster.AICRBundleInputs `json:"aicrBundle,omitempty"`
	ValidationRequirements []string                  `json:"validationRequirements,omitempty"`
}

// ExecutionSettings is the compiler-owned handoff derived from a public Environment.
// Routes bind artifact/package selectors to named targets; credentials remain references.
// Runtime is the operator's container-runtime environment for the generated pipeline —
// which runtime binary the helper orchestrates with and the pinned base/helper images.
type ExecutionSettings struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	// SourceAccess is transport-only source authentication and mirroring keyed by
	// the canonical repository URL carried by fetch nodes.
	SourceAccess map[string]SourceAccess `json:"sourceAccess,omitempty"`
	// RunsOn optionally overrides the definition's default runner for THIS environment (e.g. a
	// GitLab tag such as "generic"). The runner is environment config, not recipe identity, so
	// it may be set per-delivery without duplicating the definition or perturbing recipeDigest.
	RunsOn string `json:"runsOn,omitempty"`
	// Targets and Routes are OPTIONAL: a delivery with no routes is a valid build-only profile
	// that publishes nothing (the generated pipeline builds and packages, but pushes nowhere).
	Targets map[string]Target `json:"targets,omitempty"`
	Routes  []Route           `json:"routes,omitempty"`
	Runtime RuntimeProfile    `json:"runtime,omitempty"`
	// Signing is the OPTIONAL publication signing policy for this environment. It is
	// delivery configuration, never recipe identity: which identity signs, and where
	// its certificates come from, is a property of the environment that publishes,
	// so turning signing on cannot perturb recipeDigest. Absent (the zero value)
	// means the plan emits no sign or verify-publication node at all.
	Signing *SigningProfile `json:"signing,omitempty"`
}

type SourceAccess struct {
	Mirror        string `json:"mirror"`
	CredentialRef string `json:"credentialRef"`
}

// SigningProfile is the exact publication signing policy of one environment.
// Identity and Issuer are mandatory whenever signing is configured — including in
// dry-run mode — because a dry run that does not name the trust policy it stands
// in for describes nothing that could later be enforced.
type SigningProfile struct {
	// Mode is dry-run or cosign. dry-run records the exact invocations a real
	// signer would run and touches no registry; cosign executes the pinned tool.
	Mode string `json:"mode"`
	// Identity is the certificate-identity REGEXP a verifier must require
	// (cosign --certificate-identity-regexp).
	Identity string `json:"identity"`
	// Issuer is the exact OIDC issuer a verifier must require
	// (cosign --certificate-oidc-issuer).
	Issuer string `json:"issuer"`
	// CosignTool names a pinned tool in the compile spec holding the exact
	// pinned cosign executable. It is mandatory in cosign mode: an ambient `cosign`
	// on the runner's PATH is not an authenticated capability.
	CosignTool string `json:"cosignTool,omitempty"`
}

// Validate enforces the signing contract that is checkable from the profile
// alone. Whether CosignTool actually names a defined tool is a definition/profile
// join and is checked by the compiler, which holds both documents.
func (signing *SigningProfile) Validate(targets map[string]Target, routes []Route) error {
	if signing == nil {
		return nil
	}
	switch signing.Mode {
	case SigningModeDryRun, SigningModeCosign:
	default:
		return e.New(e.CodeInvalidCatalog,
			"delivery signing mode %q is unsupported (%s|%s)", signing.Mode, SigningModeDryRun, SigningModeCosign)
	}
	if strings.TrimSpace(signing.Identity) == "" || strings.TrimSpace(signing.Issuer) == "" ||
		strings.TrimSpace(signing.Identity) != signing.Identity || strings.TrimSpace(signing.Issuer) != signing.Issuer {
		return e.New(e.CodeTrustPolicyMissing,
			"delivery signing requires a canonical certificate identity and OIDC issuer")
	}
	if strings.ContainsAny(signing.Identity, "\r\n\x00") || strings.ContainsAny(signing.Issuer, "\r\n\x00") {
		return e.New(e.CodeInvalidCatalog, "delivery signing identity/issuer must not contain control characters")
	}
	if _, err := regexp.Compile(signing.Identity); err != nil {
		return e.Wrap(e.CodeTrustPolicyMissing, err, "delivery signing identity %q is not a valid regexp", signing.Identity)
	}
	if signing.Mode == SigningModeCosign && signing.CosignTool == "" {
		return e.New(e.CodeTrustPolicyMissing,
			"delivery signing mode %s requires cosignTool naming an exact pinned tools: entry", SigningModeCosign)
	}
	if signing.CosignTool != "" && !canonicalName.MatchString(signing.CosignTool) {
		return e.New(e.CodeInvalidCatalog, "delivery signing cosignTool %q is not a canonical tool name", signing.CosignTool)
	}
	if len(routes) == 0 {
		return e.New(e.CodeInvalidCatalog, "delivery signing is configured but no route publishes anything to sign")
	}
	for _, route := range routes {
		if targets[route.Target].Kind != TargetOCI {
			return e.New(e.CodeEngineCapability,
				"delivery signing requires every route target to be an OCI registry; route %q targets %q of kind %q",
				route.Name, route.Target, targets[route.Target].Kind)
		}
	}
	return nil
}

// RuntimeProfile is the ADR-18 §8 runtime environment. It is delivery/environment config, not
// recipe identity: it lives in Environment execution settings, so
// changing the runtime or repinning an image cannot perturb recipeDigest. It is the home
// for the values that were previously a hardcoded emitter constant, so `run generate`
// sources them from the operator's config.
type RuntimeProfile struct {
	// Runtime is the container runtime binary the helper orchestrates with
	// (podman | docker). Empty defaults to rootless podman (ADR-18 §8).
	Runtime string `json:"runtime,omitempty"`
	// RuntimeImage is the digest-pinned base image (carrying the container runtime) that
	// fetch/build jobs run on so the helper can launch the toolchain container. Empty in
	// the from-source bootstrap default; required for pinned-image delivery.
	RuntimeImage string `json:"runtimeImage,omitempty"`
	// HelperImage is the operator's digest-pinned nkd-exec image (ADR-18 §8): when set,
	// jobs run on it with nkd-exec on PATH and the from-source bootstrap is omitted.
	HelperImage string `json:"helperImage,omitempty"`
	// BuilderImage is the digest-pinned image the from-source bootstrap job runs on to `go
	// build` nkd-exec — it must carry the Go toolchain (GitLab has no setup-go action, and a
	// bare docker/dind runner has no Go). Empty leaves the bootstrap on the runner's default
	// image.
	BuilderImage string `json:"builderImage,omitempty"`
	// Services are digest-pinned CI service images attached to every node job (GitLab), e.g. a
	// docker:dind daemon so the helper's `docker run` has a daemon to reach. Environment config.
	Services []string `json:"services,omitempty"`
	// Variables are CI job variables set on every node job (e.g. DOCKER_HOST, DOCKER_TLS_CERTDIR).
	// Environment config, not recipe identity.
	Variables map[string]string `json:"variables,omitempty"`
	// Setup is a before_script run on every node job before the helper — e.g. `docker login`
	// so the dind daemon can pull a private, digest-pinned toolchain from the operator registry.
	// Environment config, never recipe identity (it authenticates transport, it does not build).
	Setup []string `json:"setup,omitempty"`
	// ToolchainRegistry is the operator registry that materialized the custom toolchains (those
	// the recipe pins registry-agnostically as `nkd-toolchain-*@sha256:…`). At compile time it
	// is prepended to those refs (digest preserved, so recipeDigest is unaffected — the same
	// transport-vs-identity split as airgap relocation). Engine/operator environment config.
	ToolchainRegistry string `json:"toolchainRegistry,omitempty"`
}

// RuntimeBinary is the configured container runtime binary, defaulting to podman.
func (profile ExecutionSettings) RuntimeBinary() string {
	if r := strings.TrimSpace(profile.Runtime.Runtime); r != "" {
		return r
	}
	return "podman"
}

func (profile RuntimeProfile) validate() error {
	if profile.Runtime != "" && profile.Runtime != "podman" && profile.Runtime != "docker" {
		return e.New(e.CodeInvalidCatalog, "delivery runtime %q is unsupported (podman|docker)", profile.Runtime)
	}
	for label, image := range map[string]string{"runtimeImage": profile.RuntimeImage, "helperImage": profile.HelperImage, "builderImage": profile.BuilderImage} {
		if image != "" && !strings.Contains(image, "@sha256:") {
			return e.New(e.CodeUnpinnedSource, "delivery %s %q must be digest-pinned (name@sha256:...)", label, image)
		}
	}
	for _, service := range profile.Services {
		if !strings.Contains(service, "@sha256:") {
			return e.New(e.CodeUnpinnedSource, "delivery service %q must be digest-pinned (name@sha256:...)", service)
		}
	}
	for name, value := range profile.Variables {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\r\n\x00 ") || strings.ContainsAny(value, "\r\n\x00") {
			return e.New(e.CodeInvalidCatalog, "delivery variable %q has an unsafe name or value", name)
		}
	}
	for _, line := range profile.Setup {
		if strings.ContainsAny(line, "\r\n\x00") {
			return e.New(e.CodeInvalidCatalog, "delivery setup line must not contain control characters")
		}
	}
	if strings.ContainsAny(profile.ToolchainRegistry, "\r\n\x00 ") {
		return e.New(e.CodeInvalidCatalog, "delivery toolchainRegistry %q is unsafe", profile.ToolchainRegistry)
	}
	return nil
}

type Target struct {
	Kind          string `json:"kind"`
	Ref           string `json:"ref"`
	CredentialRef string `json:"credentialRef,omitempty"`
	Anonymous     bool   `json:"anonymous,omitempty"`
	PlainHTTP     bool   `json:"plainHTTP,omitempty"`
}

type Route struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Target string `json:"target"`
}

type Plan struct {
	SchemaVersion           string                   `json:"schemaVersion"`
	Name                    string                   `json:"name"`
	GeneratorVersion        string                   `json:"generatorVersion"`
	DefinitionPath          string                   `json:"definitionPath,omitempty"`
	CompileSpecDigest       string                   `json:"compileSpecDigest,omitempty"`
	DefinitionDigest        string                   `json:"definitionDigest,omitempty"`
	EnvironmentPath         string                   `json:"environmentPath,omitempty"`
	Qualification           EvidenceTier             `json:"qualification"`
	CatalogDigest           string                   `json:"catalogDigest"`
	AppliedOverlays         []catalog.AppliedOverlay `json:"appliedOverlays,omitempty"`
	ExecutionSettingsDigest string                   `json:"executionSettingsDigest"`
	EnvironmentDigest       string                   `json:"environmentDigest,omitempty"`
	ExecutionSettings       ExecutionSettings        `json:"executionSettings"`
	Recipes                 []RecipePlan             `json:"recipes"`
	Compositions            []CompositionPlan        `json:"compositions,omitempty"`
	LifecycleOutputs        []LifecycleOutputPlan    `json:"lifecycleOutputs,omitempty"`
	LifecycleClusters       []LifecycleClusterPlan   `json:"lifecycleClusters,omitempty"`
	Graph                   ArtifactGraph            `json:"graph"`

	// identityDigest is the digest of this plan AS COMPILED, carried across the local
	// runner's workspace relocation. It is deliberately unexported: it never reaches the
	// wire, so it cannot perturb Digest() or the bytes of a generated CI bundle.
	//
	// Relocation rewrites every artifact path from the compiled `.nkd/factory` prefix
	// into the caller's `--workspace`, which changes the plan's canonical bytes. That is
	// correct for execution and wrong for identity: which local directory a developer
	// happened to build in is not part of what was built. Attestation must quote the
	// compiled identity, so the same release attests the same planDigest from any
	// workspace and agrees with `factory validate` and the CI run.
	identityDigest string
}

type LifecycleOutputPlan struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	ArtifactID   string   `json:"artifactId"`
	RecipeDigest string   `json:"recipeDigest"`
	Components   []string `json:"components"`
}

type LifecycleClusterPlan struct {
	Name              string                   `json:"name"`
	CompositionDigest string                   `json:"compositionDigest"`
	Members           []LifecycleClusterMember `json:"members"`
}

type LifecycleClusterMember struct {
	Output string `json:"output"`
	Role   string `json:"role,omitempty"`
}

const LifecycleBindingSchema = "nkd.dev/lifecycle-binding/v1"

// LifecycleBinding is generated content carried inside a signed publication
// root. It preserves authored output names and exact cluster membership without
// exposing compiler graph joins to consumers.
type LifecycleBinding struct {
	SchemaVersion     string                  `json:"schemaVersion"`
	DefinitionDigest  string                  `json:"definitionDigest"`
	EnvironmentDigest string                  `json:"environmentDigest"`
	ContentSubject    string                  `json:"contentSubject"`
	Outputs           []LifecycleBoundOutput  `json:"outputs"`
	Clusters          []LifecycleBoundCluster `json:"clusters,omitempty"`
}

type LifecycleBoundOutput struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	Digest        string   `json:"digest"`
	RecipeDigest  string   `json:"recipeDigest"`
	Components    []string `json:"components"`
	TransportPath string   `json:"transportPath,omitempty"`
}

type LifecycleBoundCluster struct {
	SchemaVersion     string                        `json:"schemaVersion"`
	Name              string                        `json:"name"`
	CompositionDigest string                        `json:"compositionDigest"`
	ContentSubject    string                        `json:"contentSubject"`
	Members           []LifecycleBoundClusterMember `json:"members"`
}

type LifecycleBoundClusterMember struct {
	Output         string   `json:"output"`
	ArtifactDigest string   `json:"artifactDigest"`
	RecipeDigest   string   `json:"recipeDigest"`
	Components     []string `json:"components"`
	Role           string   `json:"role,omitempty"`
}

type RecipePlan struct {
	Name                string                   `json:"name"`
	CatalogDigest       string                   `json:"catalogDigest"`
	AppliedOverlays     []catalog.AppliedOverlay `json:"appliedOverlays,omitempty"`
	RecipeID            string                   `json:"recipeId"`
	RecipeDigest        string                   `json:"recipeDigest"`
	Criteria            criteria.Criteria        `json:"criteria"`
	RunsOn              string                   `json:"runsOn"`
	Components          []string                 `json:"components,omitempty"`
	AllComponents       []string                 `json:"allComponents,omitempty"`
	AICRProfileIdentity string                   `json:"aicrProfileIdentity,omitempty"`
	// ResolvedComponents is the exact resolved catalog component set this recipe
	// compiled from. It is persisted so the SBOM node (ADR-13) can build a
	// faithful CycloneDX document at node-execution time, where the catalog is not
	// reachable — the container-isolated executor only ever sees this plan. Adding
	// it is the reason the plan identity digest moved when SBOM re-entered v1.
	ResolvedComponents []catalog.NamedComponent `json:"resolvedComponents,omitempty"`
	Graph              *ir.Graph                `json:"executionGraph"`
}

type CompositionPlan struct {
	Name        string                     `json:"name"`
	Digest      string                     `json:"digest"`
	Composition cluster.ClusterComposition `json:"composition"`
}

type ArtifactGraph struct {
	SchemaVersion string     `json:"schemaVersion"`
	Nodes         []Node     `json:"nodes"`
	Artifacts     []Artifact `json:"artifacts"`
}

type Node struct {
	HelmImage        *HelmImageBinding `json:"helmImage,omitempty"`
	ID               string            `json:"id"`
	Kind             string            `json:"kind"`
	RecipeTarget     string            `json:"recipeTarget,omitempty"`
	IRNode           string            `json:"irNode,omitempty"`
	PackageKind      string            `json:"packageKind,omitempty"`
	Distribution     string            `json:"distribution,omitempty"`
	PackageComponent string            `json:"packageComponent,omitempty"`
	Target           string            `json:"target,omitempty"`
	RunsOn           string            `json:"runsOn"`
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
}

type Artifact struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	MediaType    string `json:"mediaType,omitempty"`
	Path         string `json:"path,omitempty"`
	Producer     string `json:"producer"`
	RecipeTarget string `json:"recipeTarget,omitempty"`
	Component    string `json:"component,omitempty"`
	Target       string `json:"target,omitempty"`
}

var canonicalName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
var containerPin = regexp.MustCompile(`(?:@|^)sha256:[0-9a-f]{64}$`)
var githubCredentialRef = regexp.MustCompile(`^github:[A-Za-z_][A-Za-z0-9_]*$`)

func (definition CompileSpec) Validate() error {
	if definition.APIVersion != CompileSpecAPIVersion || definition.Kind != CompileSpecKind {
		return e.New(e.CodeInvalidCatalog, "compile spec has unsupported apiVersion/kind %q/%q", definition.APIVersion, definition.Kind)
	}
	if !canonicalName.MatchString(definition.Metadata.Name) {
		return e.New(e.CodeInvalidCatalog, "compile spec name %q is not canonical", definition.Metadata.Name)
	}
	if definition.Catalog.Base == "" {
		return e.New(e.CodeInvalidCatalog, "compile spec requires an explicit catalog directory")
	}
	// runsOn is optional: an unset label defaults to the generic hosted runner (DefaultRunsOn,
	// applied in compileWithTargetStacks), since ADR-18 §8 needs no bespoke runner. Only a
	// label with control characters is rejected here.
	if strings.ContainsAny(definition.RunsOn, "\r\n\x00") {
		return e.New(e.CodeInvalidCatalog, "compile spec runsOn label must not contain control characters")
	}
	if !containerPin.MatchString(definition.UtilityContainer) ||
		(definition.UtilityPlatform != "linux/amd64" && definition.UtilityPlatform != "linux/arm64") {
		return e.New(e.CodeUnpinnedSource, "compile spec requires a digest-pinned utilityContainer and linux/amd64|linux/arm64 utilityPlatform")
	}
	if len(definition.Recipes) == 0 {
		return e.New(e.CodeInvalidCatalog, "compile spec requires at least one recipe target")
	}
	for name, tool := range definition.Tools {
		if tool.Format != ToolFormatRaw && tool.Format != ToolFormatTarGzip {
			return e.New(e.CodeInvalidCatalog, "factory tool %q has unsupported format %q", name, tool.Format)
		}
		if !canonicalName.MatchString(name) || !strings.HasPrefix(tool.URL, "https://") ||
			!canonical.IsDigest(tool.ArchiveDigest) || !canonical.IsDigest(tool.ExecutableDigest) ||
			tool.ExecutablePath == "" || strings.HasPrefix(tool.ExecutablePath, "/") ||
			strings.Contains(tool.ExecutablePath, "\\") || strings.ContainsAny(tool.ExecutablePath, "\x00\r\n") {
			return e.New(e.CodeInvalidCatalog, "factory tool %q must bind an https archive, exact archive/executable digests, and a safe member path", name)
		}
		if tool.Format == ToolFormatRaw && tool.ArchiveDigest != tool.ExecutableDigest {
			return e.New(e.CodeInvalidCatalog, "factory raw tool %q must bind identical transport and executable digests", name)
		}
		for _, part := range strings.Split(tool.ExecutablePath, "/") {
			if part == "" || part == "." || part == ".." {
				return e.New(e.CodeInvalidCatalog, "factory tool %q has unsafe executablePath %q", name, tool.ExecutablePath)
			}
		}
	}
	seen := map[string]bool{}
	for _, target := range definition.Recipes {
		if !canonicalName.MatchString(target.Name) || seen[target.Name] {
			return e.New(e.CodeInvalidCatalog, "factory recipe target name %q is invalid or repeated", target.Name)
		}
		seen[target.Name] = true
		if err := target.Criteria.ValidateActive(); err != nil {
			return err
		}
		if target.Catalog != nil && target.Catalog.Base == "" {
			return e.New(e.CodeInvalidCatalog, "factory recipe target %q has an empty catalog override", target.Name)
		}
		if strings.ContainsAny(target.RunsOn, "\r\n\x00") {
			return e.New(e.CodeInvalidCatalog, "factory recipe target %q has an unsafe runsOn label", target.Name)
		}
		if target.AICRGenerator != "" {
			if _, ok := definition.Tools[target.AICRGenerator]; !ok {
				return e.New(e.CodeInvalidCatalog, "factory recipe target %q references unknown AICR tool %q", target.Name, target.AICRGenerator)
			}
		}
	}
	recipeTargets := seen
	seen = map[string]bool{}
	for _, clusterSpec := range definition.Clusters {
		if !canonicalName.MatchString(clusterSpec.Name) || seen[clusterSpec.Name] || len(clusterSpec.RecipeTargets) == 0 {
			return e.New(e.CodeInvalidCatalog, "factory cluster name %q is invalid/repeated or has no recipe targets", clusterSpec.Name)
		}
		seen[clusterSpec.Name] = true
		for _, target := range clusterSpec.RecipeTargets {
			if !recipeTargets[target] {
				return e.New(e.CodeInvalidCatalog, "factory cluster %q references unknown recipe target %q", clusterSpec.Name, target)
			}
		}
	}
	seen = map[string]bool{}
	for _, spec := range definition.Packages {
		if !canonicalName.MatchString(spec.Name) || seen[spec.Name] {
			return e.New(e.CodeInvalidCatalog, "factory package name %q is invalid or repeated", spec.Name)
		}
		seen[spec.Name] = true
		switch spec.Kind {
		case PackageBinaryBundle, PackageAPT, PackageOCI, PackageHelm, PackageImage:
		default:
			return e.New(e.CodeInvalidCatalog, "factory package %q has unsupported kind %q", spec.Name, spec.Kind)
		}
		if len(spec.Inputs) == 0 {
			return e.New(e.CodeInvalidCatalog, "factory package %q has no artifact inputs", spec.Name)
		}
		if spec.Kind == PackageAPT && (spec.Distribution == "" || spec.Component == "") {
			return e.New(e.CodeInvalidCatalog, "APT package %q requires distribution and component", spec.Name)
		}
		if spec.HelmImage != nil && (spec.Kind != PackageHelm || !canonicalName.MatchString(spec.HelmImage.Chart) || !strings.HasPrefix(spec.HelmImage.Image, "package/")) {
			return e.New(e.CodeInvalidCatalog, "package %q has invalid Helm image binding", spec.Name)
		}
	}
	return nil
}

func (profile ExecutionSettings) Validate() error {
	if profile.APIVersion != ExecutionSettingsAPIVersion || profile.Kind != ExecutionSettingsKind {
		return e.New(e.CodeInvalidCatalog, "execution settings have unsupported apiVersion/kind %q/%q", profile.APIVersion, profile.Kind)
	}
	if !canonicalName.MatchString(profile.Metadata.Name) {
		return e.New(e.CodeInvalidCatalog, "execution settings require a canonical name")
	}
	if strings.ContainsAny(profile.RunsOn, "\r\n\x00") {
		return e.New(e.CodeInvalidCatalog, "execution settings runsOn label must not contain control characters")
	}
	for source, access := range profile.SourceAccess {
		if err := validateSourceAccessURL("source", source); err != nil {
			return err
		}
		if err := validateSourceAccessURL("mirror", access.Mirror); err != nil {
			return err
		}
		if !githubCredentialRef.MatchString(access.CredentialRef) {
			return e.New(e.CodeInvalidCatalog,
				"execution settings sourceAccess %q credentialRef must be github:SECRET_NAME", source)
		}
	}
	// Build-only is a valid mode: a profile with no routes publishes nothing. Routes still
	// require targets to publish to (each route's target is verified below).
	if len(profile.Routes) > 0 && len(profile.Targets) == 0 {
		return e.New(e.CodeInvalidCatalog, "execution settings have routes but no targets to publish to")
	}
	for name, target := range profile.Targets {
		if !canonicalName.MatchString(name) || strings.TrimSpace(target.Ref) == "" {
			return e.New(e.CodeInvalidCatalog, "delivery target %q has an invalid name or empty ref", name)
		}
		switch target.Kind {
		case TargetOCI, TargetAPT, TargetObject:
		default:
			return e.New(e.CodeInvalidCatalog, "delivery target %q has unsupported kind %q", name, target.Kind)
		}
		if target.Anonymous == (target.CredentialRef != "") {
			return e.New(e.CodeInvalidCatalog, "delivery target %q requires exactly one of anonymous or credentialRef", name)
		}
		if strings.ContainsAny(target.CredentialRef, "\r\n\x00") {
			return e.New(e.CodeInvalidCatalog, "delivery target %q credentialRef is unsafe", name)
		}
	}
	seen := map[string]bool{}
	for _, route := range profile.Routes {
		if !canonicalName.MatchString(route.Name) || seen[route.Name] || strings.TrimSpace(route.Input) == "" {
			return e.New(e.CodeInvalidCatalog, "delivery route name/input %q/%q is invalid or repeated", route.Name, route.Input)
		}
		seen[route.Name] = true
		if _, ok := profile.Targets[route.Target]; !ok {
			return e.New(e.CodeInvalidCatalog, "delivery route %q references unknown target %q", route.Name, route.Target)
		}
	}
	if err := profile.Runtime.validate(); err != nil {
		return err
	}
	return profile.Signing.Validate(profile.Targets, profile.Routes)
}

func validateSourceAccessURL(label, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.ContainsAny(value, "?#") ||
		parsed.Opaque != "" || parsed.String() != value {
		return e.New(e.CodeInvalidCatalog,
			"execution settings sourceAccess %s %q must be a canonical HTTPS URL without userinfo, query, or fragment", label, value)
	}
	return nil
}

func (profile ExecutionSettings) Digest() (string, error) {
	if err := profile.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindExecutionSettings, profile)
}

// Digest identifies the exact validated compiler input independently of catalog
// location, environment selection, generator version, or its resolved run plan.
func (definition CompileSpec) Digest() (string, error) {
	if err := definition.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindCompileSpec, definition)
}

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

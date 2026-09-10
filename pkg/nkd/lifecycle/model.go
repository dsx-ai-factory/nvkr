// Package lifecycle owns NKD's small public product model. Compiler plans,
// execution graphs, inventories, clearances, and publication envelopes remain
// generated records owned by their specialist packages.
package lifecycle

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/cluster"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

const (
	DefinitionAPIVersion  = "nkd.dev/definition/v2"
	DefinitionKind        = "Definition"
	EnvironmentAPIVersion = "nkd.dev/environment/v1"
	EnvironmentKind       = "Environment"
	RunSchemaVersion      = "nkd.dev/run/v1"
	ReleaseSchemaVersion  = "nkd.dev/release/v1"
)

const (
	OutputExecutable         = "executable"
	OutputOCI                = "oci"
	OutputAPT                = "apt-repository"
	OutputHelm               = "helm-oci"
	LaneQualified            = "qualified"
	LanePipeline             = "pipeline-attested"
	LaneUnqualified          = "static-unqualified"
	ScopeArtifactPublication = "artifact-publication"
	ScopeRelease             = "release"
)

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
var containerPinPattern = regexp.MustCompile(`(?:@|^)sha256:[0-9a-f]{64}$`)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Definition declares reusable recipes and named outputs once. Its compiler
// derives package envelopes, routes, plans, and execution graphs.
type Definition struct {
	APIVersion string              `json:"apiVersion"`
	Kind       string              `json:"kind"`
	Metadata   Metadata            `json:"metadata"`
	Catalog    Catalog             `json:"catalog"`
	Tools      map[string]Tool     `json:"tools,omitempty"`
	Recipes    []RecipeSelection   `json:"recipes"`
	Outputs    []DefinitionOutput  `json:"outputs"`
	Clusters   []DefinitionCluster `json:"clusters,omitempty"`
}

// Recipe is the reusable catalog-owned component recipe. Definition refers to
// one through RecipeSelection; selection is not a second kind of Recipe.
type Recipe = catalog.Recipe

// RecipeSelection names one use of a reusable Recipe under an explicit frame.
// Resolution and its exact logical identity are compiler products.
type RecipeSelection struct {
	Name          string            `json:"name"`
	Catalog       *Catalog          `json:"catalog,omitempty"`
	Criteria      criteria.Criteria `json:"criteria"`
	Parameters    map[string]string `json:"parameters,omitempty"`
	AICRGenerator string            `json:"aicrGenerator,omitempty"`
}

type Catalog struct {
	Base     string   `json:"base"`
	Overlays []string `json:"overlays,omitempty"`
}

type Tool struct {
	Format           string `json:"format"`
	URL              string `json:"url"`
	ArchiveDigest    string `json:"archiveDigest"`
	ExecutablePath   string `json:"executablePath"`
	ExecutableDigest string `json:"executableDigest"`
}

// DefinitionOutput is a named artifact slot frozen before a run. Components
// selects exact outputs of one recipe; format selects the standard envelope.
type DefinitionOutput struct {
	Name       string      `json:"name"`
	Recipe     string      `json:"recipe"`
	Components []string    `json:"components,omitempty"`
	Format     string      `json:"format"`
	APT        *APTOutput  `json:"apt,omitempty"`
	Helm       *HelmOutput `json:"helm,omitempty"`
}

// HelmOutput binds a rendered controller chart to another output's native image.
// Chart names a direct child of the source archive's helm directory.
type HelmOutput struct {
	ImageOutput string `json:"imageOutput"`
	Chart       string `json:"chart"`
}

type APTOutput struct {
	Distribution string `json:"distribution"`
	Component    string `json:"component"`
}

// DefinitionCluster is the sole authored cluster view. Exact recipe and later
// release-artifact bindings are compiler products.
type DefinitionCluster struct {
	Name                   string                    `json:"name"`
	Type                   string                    `json:"type"`
	CNI                    string                    `json:"cni,omitempty"`
	Members                []ClusterMember           `json:"members"`
	AICRBundle             *cluster.AICRBundleInputs `json:"aicrBundle,omitempty"`
	ValidationRequirements []string                  `json:"validationRequirements,omitempty"`
}

// ClusterMember selects one named output. Role is required for node-level
// recipe categories and omitted for cluster-level categories.
type ClusterMember struct {
	Output string `json:"output"`
	Role   string `json:"role,omitempty"`
}

// Environment contains settings independently selected for one run. Publish
// maps a Definition output name to a destination name; routine routes are derived.
type Environment struct {
	APIVersion       string                  `json:"apiVersion"`
	Kind             string                  `json:"kind"`
	Metadata         Metadata                `json:"metadata"`
	RunsOn           string                  `json:"runsOn,omitempty"`
	UtilityContainer string                  `json:"utilityContainer"`
	UtilityPlatform  string                  `json:"utilityPlatform"`
	Tools            map[string]Tool         `json:"tools,omitempty"`
	SourceAccess     map[string]SourceAccess `json:"sourceAccess,omitempty"`
	Destinations     map[string]Destination  `json:"destinations,omitempty"`
	Publish          map[string]string       `json:"publish,omitempty"`
	Runtime          Runtime                 `json:"runtime,omitempty"`
	Signing          *Signing                `json:"signing,omitempty"`
}

// SourceAccess configures transport-only authentication and mirroring for one
// canonical catalog source URL. It cannot alter source commits, tree hashes, or
// Recipe identity.
type SourceAccess struct {
	Mirror        string `json:"mirror"`
	CredentialRef string `json:"credentialRef"`
}

type Destination struct {
	Kind          string `json:"kind"`
	Ref           string `json:"ref"`
	CredentialRef string `json:"credentialRef,omitempty"`
	Anonymous     bool   `json:"anonymous,omitempty"`
	PlainHTTP     bool   `json:"plainHTTP,omitempty"`
}

type Runtime struct {
	Runtime           string            `json:"runtime,omitempty"`
	RuntimeImage      string            `json:"runtimeImage,omitempty"`
	HelperImage       string            `json:"helperImage,omitempty"`
	BuilderImage      string            `json:"builderImage,omitempty"`
	Services          []string          `json:"services,omitempty"`
	Variables         map[string]string `json:"variables,omitempty"`
	Setup             []string          `json:"setup,omitempty"`
	ToolchainRegistry string            `json:"toolchainRegistry,omitempty"`
}

type Signing struct {
	Mode       string `json:"mode"`
	Identity   string `json:"identity"`
	Issuer     string `json:"issuer"`
	CosignTool string `json:"cosignTool,omitempty"`
}

// Artifact is an immutable public output or evidence reference. It always names
// exact observed bytes; required output slots use a separate internal record.
type Artifact struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Digest       string   `json:"digest"`
	Reference    string   `json:"reference,omitempty"`
	RecipeDigest string   `json:"recipeDigest"`
	Components   []string `json:"components,omitempty"`
	Role         string   `json:"role,omitempty"`
}

// Run joins frozen expectations to observations without deriving requirements
// from success. Plans and node reports are inspectable internal records.
type Run struct {
	SchemaVersion     string               `json:"schemaVersion"`
	Attempt           string               `json:"attempt"`
	DefinitionDigest  string               `json:"definitionDigest"`
	EnvironmentDigest string               `json:"environmentDigest"`
	PlanDigest        string               `json:"planDigest"`
	Lane              string               `json:"lane"`
	Status            string               `json:"status"`
	Expected          []requiredOutput     `json:"expected"`
	Observed          []Artifact           `json:"observed,omitempty"`
	Failure           *runFailure          `json:"failure,omitempty"`
	ContentSubject    string               `json:"contentSubject,omitempty"`
	Clusters          []clusterRealization `json:"clusters,omitempty"`
}

type requiredOutput struct {
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	ArtifactID       string   `json:"artifactId"`
	MemberArtifactID string   `json:"memberArtifactId"`
	RecipeDigest     string   `json:"recipeDigest"`
	Role             string   `json:"role,omitempty"`
	Components       []string `json:"components"`
	ComponentResults []string `json:"componentResults"`
}

type runFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Release is the public, immutable projection returned after lane-specific
// closure verification. Wire manifests and qualified receipts retain their own
// schemas and identities behind this view.
type Release struct {
	SchemaVersion  string               `json:"schemaVersion"`
	Subject        string               `json:"subject"`
	Lane           string               `json:"lane"`
	Scope          string               `json:"scope"`
	Artifacts      []Artifact           `json:"artifacts"`
	ContentSubject string               `json:"contentSubject"`
	Clusters       []clusterRealization `json:"clusters,omitempty"`
	PolicyDigest   string               `json:"policyDigest"`
}

// VerificationPolicy identifies the exact lane and trust requirements a
// consumer accepts. Empty signer policy never authorizes pipeline evidence.
type VerificationPolicy struct {
	Lane           string `json:"lane"`
	Scope          string `json:"scope"`
	IdentityRegexp string `json:"identityRegexp,omitempty"`
	Issuer         string `json:"issuer,omitempty"`
}

func (definition Definition) Validate() error {
	if definition.APIVersion != DefinitionAPIVersion || definition.Kind != DefinitionKind {
		return e.New(e.CodeInvalidCatalog, "definition has unsupported apiVersion/kind %q/%q", definition.APIVersion, definition.Kind)
	}
	if !namePattern.MatchString(definition.Metadata.Name) || definition.Catalog.Base == "" || len(definition.Recipes) == 0 || len(definition.Outputs) == 0 {
		return e.New(e.CodeInvalidCatalog, "definition requires a canonical name, catalog, recipes, and outputs")
	}
	for name, tool := range definition.Tools {
		if err := validateTool(name, tool); err != nil {
			return err
		}
	}
	recipes := make(map[string]bool, len(definition.Recipes))
	selections := make(map[string]RecipeSelection, len(definition.Recipes))
	previous := ""
	for _, recipe := range definition.Recipes {
		if !namePattern.MatchString(recipe.Name) || recipe.Name <= previous || recipes[recipe.Name] {
			return e.New(e.CodeInvalidCatalog, "definition recipe name %q is invalid or repeated", recipe.Name)
		}
		if err := recipe.Criteria.ValidateActive(); err != nil {
			return err
		}
		if recipe.Criteria.Role != "" && recipe.Criteria.Role != "any" {
			return e.New(e.CodeInvalidCatalog, "recipe selection %q must leave role to its cluster member", recipe.Name)
		}
		if recipe.Criteria.ClusterType != "" && recipe.Criteria.ClusterType != "any" {
			return e.New(e.CodeInvalidCatalog, "recipe selection %q must leave cluster type to its cluster view", recipe.Name)
		}
		recipes[recipe.Name] = true
		selections[recipe.Name] = recipe
		previous = recipe.Name
		if recipe.AICRGenerator != "" {
			if _, ok := definition.Tools[recipe.AICRGenerator]; !ok {
				return e.New(e.CodeInvalidCatalog, "recipe selection %q references unknown output tool %q", recipe.Name, recipe.AICRGenerator)
			}
		}
	}
	outputs := make(map[string]bool, len(definition.Outputs))
	previous = ""
	for _, output := range definition.Outputs {
		if !namePattern.MatchString(output.Name) || output.Name <= previous || outputs[output.Name] || !recipes[output.Recipe] {
			return e.New(e.CodeInvalidCatalog, "definition output %q is invalid, repeated, empty, or references an unknown recipe", output.Name)
		}
		if output.Format != OutputExecutable && output.Format != OutputOCI && output.Format != OutputAPT && output.Format != OutputHelm {
			return e.New(e.CodeInvalidCatalog, "definition output %q has unsupported format %q", output.Name, output.Format)
		}
		if output.Format == OutputHelm {
			generated := selections[output.Recipe].AICRGenerator != ""
			if (generated && len(output.Components) != 0) || (!generated && len(output.Components) != 1) {
				return e.New(e.CodeInvalidCatalog, "definition Helm output %q requires either an AICR recipe without components or one source archive component", output.Name)
			}
		} else if len(output.Components) == 0 {
			return e.New(e.CodeInvalidCatalog, "definition output %q requires components", output.Name)
		}
		if output.Format == OutputOCI && len(output.Components) != 1 {
			return e.New(e.CodeInvalidCatalog, "definition OCI output %q requires exactly one image component", output.Name)
		}
		if output.Format == OutputAPT {
			if output.APT == nil || !namePattern.MatchString(output.APT.Distribution) || !namePattern.MatchString(output.APT.Component) {
				return e.New(e.CodeInvalidCatalog, "definition APT output %q requires canonical distribution and component", output.Name)
			}
		} else if output.APT != nil {
			return e.New(e.CodeInvalidCatalog, "definition output %q has APT-only settings", output.Name)
		}
		if output.Helm != nil {
			if output.Format != OutputHelm || len(output.Components) != 1 || !namePattern.MatchString(output.Helm.Chart) {
				return e.New(e.CodeInvalidCatalog, "definition output %q requires a source Helm archive and chart for image binding", output.Name)
			}
			found := false
			for _, candidate := range definition.Outputs {
				if candidate.Name == output.Helm.ImageOutput && candidate.Format == OutputOCI {
					found = true
				}
			}
			if !found {
				return e.New(e.CodeInvalidCatalog, "definition Helm output %q references an unknown image output", output.Name)
			}
		}
		if err := sortedUnique("output components", output.Components); err != nil {
			return err
		}
		outputs[output.Name] = true
		previous = output.Name
	}
	clusters := map[string]bool{}
	previous = ""
	for _, view := range definition.Clusters {
		if !namePattern.MatchString(view.Name) || view.Name <= previous || clusters[view.Name] || len(view.Members) == 0 {
			return e.New(e.CodeInvalidCatalog, "definition cluster %q is invalid, repeated, or has no members", view.Name)
		}
		if view.Type != cluster.TypeTenant && view.Type != cluster.TypeOperator {
			return e.New(e.CodeUnsupported, "definition cluster %q has unsupported type %q", view.Name, view.Type)
		}
		frame := criteria.Criteria{}
		previousMember := ""
		for index, member := range view.Members {
			if !outputs[member.Output] || member.Output <= previousMember {
				return e.New(e.CodeInvalidCatalog, "definition cluster %q members must select sorted unique outputs", view.Name)
			}
			var selectedOutput DefinitionOutput
			for _, output := range definition.Outputs {
				if output.Name == member.Output {
					selectedOutput = output
					break
				}
			}
			candidate := selections[selectedOutput.Recipe].Criteria
			if criteria.IsNodeCategory(candidate.Category) {
				if !criteria.ValidRole(member.Role) {
					return e.New(e.CodeInvalidCatalog, "definition cluster %q output %q must select a supported node role", view.Name, member.Output)
				}
			} else if !criteria.IsClusterCategory(candidate.Category) || member.Role != "" {
				return e.New(e.CodeInvalidCatalog, "definition cluster %q output %q has invalid category/role membership", view.Name, member.Output)
			}
			if index == 0 {
				frame = candidate
			} else if candidate.K8sVersion != frame.K8sVersion || candidate.OS != frame.OS || candidate.Arch != frame.Arch {
				return e.New(e.CodeInvalidCatalog, "definition cluster %q recipe selections do not share one concrete frame", view.Name)
			}
			previousMember = member.Output
		}
		if err := sortedUnique("cluster validation requirements", view.ValidationRequirements); err != nil {
			return err
		}
		clusters[view.Name] = true
		previous = view.Name
	}
	return nil
}

func (definition Definition) Digest() (string, error) {
	if err := definition.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindDefinition, definition)
}

func (environment Environment) Validate() error {
	if environment.APIVersion != EnvironmentAPIVersion || environment.Kind != EnvironmentKind || !namePattern.MatchString(environment.Metadata.Name) {
		return e.New(e.CodeInvalidCatalog, "environment has unsupported apiVersion/kind or invalid name")
	}
	if environment.UtilityContainer == "" || (environment.UtilityPlatform != "linux/amd64" && environment.UtilityPlatform != "linux/arm64") {
		return e.New(e.CodeUnpinnedSource, "environment requires a digest-pinned utilityContainer and linux/amd64|linux/arm64 utilityPlatform")
	}
	if !containerPinPattern.MatchString(environment.UtilityContainer) {
		return e.New(e.CodeUnpinnedSource, "environment utilityContainer must be digest-pinned")
	}
	if strings.ContainsAny(environment.RunsOn, "\r\n\x00") {
		return e.New(e.CodeInvalidCatalog, "environment runner contains control characters")
	}
	for name, tool := range environment.Tools {
		if err := validateTool(name, tool); err != nil {
			return err
		}
	}
	for source, access := range environment.SourceAccess {
		if err := validateHTTPSURL("environment sourceAccess source", source); err != nil {
			return err
		}
		if err := validateHTTPSURL("environment sourceAccess mirror", access.Mirror); err != nil {
			return err
		}
		if !githubCredentialRefPattern.MatchString(access.CredentialRef) {
			return e.New(e.CodeInvalidCatalog,
				"environment sourceAccess %q credentialRef must be github:SECRET_NAME", source)
		}
	}
	for name, destination := range environment.Destinations {
		if !namePattern.MatchString(name) || strings.TrimSpace(destination.Ref) == "" || destination.Anonymous == (destination.CredentialRef != "") {
			return e.New(e.CodeInvalidCatalog, "environment destination %q is invalid or has ambiguous credentials", name)
		}
		switch destination.Kind {
		case "oci", "apt", "object":
		default:
			return e.New(e.CodeUnsupported, "environment destination %q has unsupported kind %q", name, destination.Kind)
		}
	}
	if environment.Signing != nil {
		if environment.Signing.Mode != "dry-run" && environment.Signing.Mode != "cosign" {
			return e.New(e.CodeUnsupported, "environment signing mode %q is unsupported", environment.Signing.Mode)
		}
		if strings.TrimSpace(environment.Signing.Identity) == "" || strings.TrimSpace(environment.Signing.Issuer) == "" {
			return e.New(e.CodeTrustPolicyMissing, "environment signing requires identity and issuer")
		}
		if environment.Signing.Mode == "cosign" {
			if _, ok := environment.Tools[environment.Signing.CosignTool]; !ok {
				return e.New(e.CodeTrustPolicyMissing, "environment signing tool %q is not pinned in Environment.tools", environment.Signing.CosignTool)
			}
		}
	}
	for output, target := range environment.Publish {
		if !namePattern.MatchString(output) || environment.Destinations[target].Ref == "" {
			return e.New(e.CodeInvalidCatalog, "environment publication %q references unknown destination %q", output, target)
		}
	}
	return nil
}

var githubCredentialRefPattern = regexp.MustCompile(`^github:[A-Za-z_][A-Za-z0-9_]*$`)

func validateHTTPSURL(label, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.ContainsAny(value, "?#") ||
		parsed.Opaque != "" || parsed.String() != value {
		return e.New(e.CodeInvalidCatalog, "%s %q must be a canonical HTTPS URL without userinfo, query, or fragment", label, value)
	}
	return nil
}

func validateTool(name string, tool Tool) error {
	if tool.Format != factoryToolFormatRaw && tool.Format != factoryToolFormatTarGzip {
		return e.New(e.CodeInvalidCatalog, "tool %q has unsupported format %q", name, tool.Format)
	}
	if !namePattern.MatchString(name) || !strings.HasPrefix(tool.URL, "https://") || !canonical.IsDigest(tool.ArchiveDigest) ||
		!canonical.IsDigest(tool.ExecutableDigest) || tool.ExecutablePath == "" || strings.HasPrefix(tool.ExecutablePath, "/") || strings.ContainsAny(tool.ExecutablePath, "\\\x00\r\n") {
		return e.New(e.CodeInvalidCatalog, "tool %q does not bind a safe HTTPS archive and exact executable", name)
	}
	if tool.Format == factoryToolFormatRaw && tool.ArchiveDigest != tool.ExecutableDigest {
		return e.New(e.CodeInvalidCatalog, "raw tool %q must bind identical transport and executable digests", name)
	}
	for _, part := range strings.Split(tool.ExecutablePath, "/") {
		if part == "" || part == "." || part == ".." {
			return e.New(e.CodeInvalidCatalog, "tool %q has unsafe executablePath", name)
		}
	}
	return nil
}

const (
	factoryToolFormatRaw     = "raw"
	factoryToolFormatTarGzip = "tar-gzip"
)

func (environment Environment) Digest() (string, error) {
	if err := environment.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindEnvironment, environment)
}

func (policy VerificationPolicy) Validate() error {
	switch policy.Lane {
	case LaneQualified:
		if policy.Scope != ScopeRelease {
			return e.New(e.CodeTrustPolicyMissing, "qualified verification requires full release scope")
		}
		return nil
	case LanePipeline:
		if policy.Scope != ScopeArtifactPublication && policy.Scope != ScopeRelease {
			return e.New(e.CodeTrustPolicyMissing, "pipeline-attested verification requires an explicit artifact-publication or release scope")
		}
		if strings.TrimSpace(policy.IdentityRegexp) == "" || strings.TrimSpace(policy.Issuer) == "" {
			return e.New(e.CodeTrustPolicyMissing, "pipeline-attested verification requires identity and issuer")
		}
		if _, err := regexp.Compile(policy.IdentityRegexp); err != nil {
			return e.Wrap(e.CodeTrustPolicyMissing, err, "compiling verification identity policy")
		}
		return nil
	default:
		return e.New(e.CodeTrustPolicyMissing, "verification policy lane %q is unsupported", policy.Lane)
	}
}

func (policy VerificationPolicy) Digest() (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindVerificationPolicy, policy)
}

func sortedUnique(label string, values []string) error {
	copyValues := append([]string(nil), values...)
	sort.Strings(copyValues)
	for i, value := range values {
		if !namePattern.MatchString(value) || value != copyValues[i] || (i > 0 && value == values[i-1]) {
			return e.New(e.CodeInvalidCatalog, "%s must be canonical, sorted, and duplicate-free", label)
		}
	}
	return nil
}

// Package lifecycle owns NKD's small public product model. Compiler plans,
// execution graphs, inventories, clearances, and publication envelopes remain
// generated records owned by their specialist packages.
package lifecycle

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
	"github.com/google/go-containerregistry/pkg/name"
)

const (
	DefinitionAPIVersion  = "nkd.dev/definition/v4"
	DefinitionKind        = "Definition"
	EnvironmentAPIVersion = "nkd.dev/environment/v1"
	EnvironmentKind       = "Environment"
	RunSchemaVersion      = "nkd.dev/run/v3"
	ReleaseSchemaVersion  = "nkd.dev/release/v3"
)

const (
	OutputExecutable         = "executable"
	OutputOCI                = "oci"
	OutputAPT                = "apt-repository"
	OutputHelm               = "helm-oci"
	OutputDiskImage          = "disk-image"
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

// Definition declares named outputs and their exact producer selections. Its compiler
// derives package envelopes, routes, plans, and execution graphs.
type Definition struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	Metadata   Metadata           `json:"metadata"`
	Catalog    Catalog            `json:"catalog"`
	SBOMTool   string             `json:"sbomTool,omitempty"`
	CosignTool string             `json:"cosignTool,omitempty"`
	Outputs    []DefinitionOutput `json:"outputs"`
}

type Catalog struct {
	Base     string   `json:"base"`
	Overlays []string `json:"overlays,omitempty"`
}

// DefinitionOutput is a named artifact slot frozen before a run. Components
// selects exact outputs of one build; format selects the standard envelope.
type DefinitionOutput struct {
	Name       string      `json:"name"`
	Platform   string      `json:"platform"`
	Catalog    *Catalog    `json:"catalog,omitempty"`
	AICR       *AICROutput `json:"aicr,omitempty"`
	Components []string    `json:"components,omitempty"`
	Format     string      `json:"format"`
	APT        *APTOutput  `json:"apt,omitempty"`
	Helm       *HelmOutput `json:"helm,omitempty"`
}

// AICROutput explicitly selects an imported profile and its pinned generator.
type AICROutput struct {
	Bundle    *aicr.BundleInputs `json:"bundle,omitempty"`
	Profile   string             `json:"profile"`
	Generator string             `json:"generator"`
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

// Environment contains settings independently selected for one run. Publish
// uses a registry namespace; output repositories and routes are derived.
type Environment struct {
	APIVersion       string            `json:"apiVersion"`
	Kind             string            `json:"kind"`
	Metadata         Metadata          `json:"metadata"`
	RunsOn           string            `json:"runsOn,omitempty"`
	ExecutionClasses []execution.Class `json:"executionClasses,omitempty"`
	UtilityContainer string            `json:"utilityContainer"`
	UtilityPlatform  string            `json:"utilityPlatform"`

	SourceAccess map[string]SourceAccess `json:"sourceAccess,omitempty"`
	Publication  *Publication            `json:"publication,omitempty"`
	Runtime      Runtime                 `json:"runtime,omitempty"`
	Signing      *Signing                `json:"signing,omitempty"`
}

// SourceAccess configures transport-only authentication and mirroring for one
// canonical catalog source URL. It cannot alter source commits, tree hashes, or
// Build identity.
type SourceAccess struct {
	Mirror        string `json:"mirror"`
	CredentialRef string `json:"credentialRef"`
}

// Publication selects one OCI namespace. Release metadata/evidence use /releases; outputs use named sibling repositories.
type Publication struct {
	RepositoryPrefix string `json:"repositoryPrefix"`
	CredentialRef    string `json:"credentialRef,omitempty"`
	Anonymous        bool   `json:"anonymous,omitempty"`
	PlainHTTP        bool   `json:"plainHTTP,omitempty"`
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
	Mode     string `json:"mode"`
	Identity string `json:"identity"`
	Issuer   string `json:"issuer"`
}

// Artifact is an immutable public output or evidence reference. It always names
// exact observed bytes; required output slots use a separate internal record.
type Artifact struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Digest      string   `json:"digest"`
	Reference   string   `json:"reference,omitempty"`
	BuildDigest string   `json:"buildDigest"`
	Components  []string `json:"components,omitempty"`
}

// Run joins frozen expectations to observations without deriving requirements
// from success. Plans and node reports are inspectable internal records.
type Run struct {
	SchemaVersion     string           `json:"schemaVersion"`
	Attempt           string           `json:"attempt"`
	DefinitionDigest  string           `json:"definitionDigest"`
	EnvironmentDigest string           `json:"environmentDigest"`
	PlanDigest        string           `json:"planDigest"`
	Lane              string           `json:"lane"`
	Status            string           `json:"status"`
	Expected          []requiredOutput `json:"expected"`
	Observed          []Artifact       `json:"observed,omitempty"`
	Failure           *runFailure      `json:"failure,omitempty"`
	ContentSubject    string           `json:"contentSubject,omitempty"`
}

type requiredOutput struct {
	Name             string   `json:"name"`
	Kind             string   `json:"kind"`
	ArtifactID       string   `json:"artifactId"`
	MemberArtifactID string   `json:"memberArtifactId"`
	BuildDigest      string   `json:"buildDigest"`
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
	SchemaVersion  string     `json:"schemaVersion"`
	Subject        string     `json:"subject"`
	Lane           string     `json:"lane"`
	Scope          string     `json:"scope"`
	Artifacts      []Artifact `json:"artifacts"`
	ContentSubject string     `json:"contentSubject"`
	PolicyDigest   string     `json:"policyDigest"`
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
	if !namePattern.MatchString(definition.Metadata.Name) || definition.Catalog.Base == "" || len(definition.Outputs) == 0 {
		return e.New(e.CodeInvalidCatalog, "definition requires a canonical name, catalog and outputs")
	}
	for _, tool := range []string{definition.SBOMTool, definition.CosignTool} {
		if tool != "" && !namePattern.MatchString(tool) {
			return e.New(e.CodeInvalidCatalog, "invalid catalog tool selector %q", tool)
		}
	}
	previous := ""
	outputs := make(map[string]bool, len(definition.Outputs))
	for _, output := range definition.Outputs {
		if !namePattern.MatchString(output.Name) || output.Name <= previous || outputs[output.Name] {
			return e.New(e.CodeInvalidCatalog, "definition output %q is invalid, repeated, empty, or has an invalid platform", output.Name)
		}
		if err := target.ValidatePlatform(output.Platform); err != nil {
			return err
		}
		if output.Catalog != nil && output.Catalog.Base == "" {
			return e.New(e.CodeInvalidCatalog, "output %q has an empty catalog override", output.Name)
		}
		if output.AICR != nil && (output.Format != OutputHelm || len(output.Components) != 0 || !namePattern.MatchString(output.AICR.Profile) || !namePattern.MatchString(output.AICR.Generator)) {
			return e.New(e.CodeInvalidCatalog, "output %q has invalid AICR producer settings", output.Name)
		}
		if output.AICR != nil {
			if _, err := aicr.NormalizeBundleInputs(output.AICR.Bundle); err != nil {
				return err
			}
		}
		if output.Format != OutputExecutable && output.Format != OutputOCI && output.Format != OutputAPT && output.Format != OutputHelm && output.Format != OutputDiskImage {
			return e.New(e.CodeInvalidCatalog, "definition output %q has unsupported format %q", output.Name, output.Format)
		}
		if output.Format == OutputHelm {
			generated := output.AICR != nil
			if (generated && len(output.Components) != 0) || (!generated && len(output.Components) != 1) {
				return e.New(e.CodeInvalidCatalog, "definition Helm output %q requires either an AICR build without components or one source archive component", output.Name)
			}
		} else if len(output.Components) == 0 {
			return e.New(e.CodeInvalidCatalog, "definition output %q requires components", output.Name)
		}
		if output.Format == OutputOCI && len(output.Components) != 1 {
			return e.New(e.CodeInvalidCatalog, "definition OCI output %q requires exactly one image component", output.Name)
		}
		if output.Format == OutputDiskImage && (len(output.Components) != 1) {
			return e.New(e.CodeInvalidCatalog, "definition disk-image output %q requires exactly one component", output.Name)
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
	if err := execution.ValidateClasses(environment.ExecutionClasses); err != nil {
		return err
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
	if publication := environment.Publication; publication != nil {
		prefix := publication.RepositoryPrefix
		repository, err := name.NewRepository(prefix, name.StrictValidation)
		if err != nil || repository.Name() != prefix || strings.ContainsAny(prefix[strings.LastIndex(prefix, "/")+1:], ":@") || strings.Contains(prefix, "@") || publication.Anonymous == (publication.CredentialRef != "") {
			return e.New(e.CodeInvalidCatalog, "publication requires a canonical untagged repositoryPrefix and exactly one credential mode")
		}
	}
	if environment.Signing != nil {
		if environment.Signing.Mode != "dry-run" && environment.Signing.Mode != "cosign" {
			return e.New(e.CodeUnsupported, "environment signing mode %q is unsupported", environment.Signing.Mode)
		}
		if strings.TrimSpace(environment.Signing.Identity) == "" || strings.TrimSpace(environment.Signing.Issuer) == "" {
			return e.New(e.CodeTrustPolicyMissing, "environment signing requires identity and issuer")
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

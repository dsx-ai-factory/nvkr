// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package definition owns authored Definition and Environment admission and identity.
// Start with LoadDefinition and LoadEnvironment for strict, bounded document reads;
// Validate and Digest apply the same schema and canonical identity to in-memory inputs.
// Compilation and execution belong to factory.
package definition

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
	"github.com/google/go-containerregistry/pkg/name"
)

const (
	DefinitionAPIVersion         = "nkd.dev/definition/v4"
	DefinitionKind               = "Definition"
	EnvironmentAPIVersion        = "nkd.dev/environment/v1"
	EnvironmentKind              = "Environment"
	OutputExecutable             = "executable"
	OutputOCI                    = "oci"
	OutputAPT                    = "apt-repository"
	OutputHelm                   = "helm-oci"
	OutputDiskImage              = "disk-image"
	BuildNetworkOfflineOnly      = "offline-only"
	BuildNetworkProducerDeclared = "producer-declared"
)

var dispatchBranchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

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
	Components []string    `json:"components,omitempty"`
	Format     string      `json:"format"`
	APT        *APTOutput  `json:"apt,omitempty"`
	Helm       *HelmOutput `json:"helm,omitempty"`
}

// HelmOutput maps chart image slots to named OCI outputs. Repository locations
// are supplied at deployment time and are never embedded from the Environment.
type HelmOutput struct {
	Images map[string]string `json:"images"`
}

type APTOutput struct {
	Distribution string `json:"distribution"`
	Component    string `json:"component"`
}

// Environment contains settings independently selected for one run. Publish
// uses a registry namespace; output repositories and routes are derived.
type Environment struct {
	// GitHubARC explicitly selects the trusted native KVM pool policy. It is
	// configuration, not provider isolation or runtime-capacity evidence.
	GitHubARC string `json:"githubARC,omitempty"`
	// DiskEvidence selects the M2 producer-evidence contract; empty requires deep inspection.
	DiskEvidence string `json:"diskEvidence,omitempty"`
	// GitHubDispatchBranches adds exact branch names to default-branch manual execution.
	GitHubDispatchBranches []string                         `json:"githubDispatchBranches,omitempty"`
	APIVersion             string                           `json:"apiVersion"`
	Kind                   string                           `json:"kind"`
	Metadata               Metadata                         `json:"metadata"`
	RunsOn                 string                           `json:"runsOn,omitempty"`
	ExecutionClasses       []execution.Class                `json:"executionClasses,omitempty"`
	DiskInspectionProfiles map[string]DiskInspectionProfile `json:"diskInspectionProfiles,omitempty"`
	UtilityContainer       string                           `json:"utilityContainer"`
	UtilityPlatform        string                           `json:"utilityPlatform"`

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
	// Mode selects output-local publication and metadata-only release assembly.
	Mode             string `json:"mode,omitempty"`
	RepositoryPrefix string `json:"repositoryPrefix"`
	CredentialRef    string `json:"credentialRef,omitempty"`
	Anonymous        bool   `json:"anonymous,omitempty"`
	PlainHTTP        bool   `json:"plainHTTP,omitempty"`
}

type Runtime struct {
	PodmanStorageDriver PodmanStorageDriver `json:"podmanStorageDriver,omitempty"`
	Runtime             string              `json:"runtime,omitempty"`
	RuntimeImage        string              `json:"runtimeImage,omitempty"`
	HelperImage         string              `json:"helperImage,omitempty"`
	// BuilderImage retains the retired GitLab bootstrap schema allocation.
	// Nonempty values are unsupported by maintained execution targets.
	BuilderImage      string            `json:"builderImage,omitempty"`
	Services          []string          `json:"services,omitempty"`
	Variables         map[string]string `json:"variables,omitempty"`
	Setup             []string          `json:"setup,omitempty"`
	ToolchainRegistry string            `json:"toolchainRegistry,omitempty"`
	// BuildNetworkPolicy independently controls whether a producer declaration
	// may request network access during its build. Omission is offline-only.
	BuildNetworkPolicy string `json:"buildNetworkPolicy,omitempty"`
}

type Signing struct {
	Mode     string `json:"mode"`
	Identity string `json:"identity"`
	Issuer   string `json:"issuer"`
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
		if output.Format != OutputExecutable && output.Format != OutputOCI && output.Format != OutputAPT && output.Format != OutputHelm && output.Format != OutputDiskImage {
			return e.New(e.CodeInvalidCatalog, "definition output %q has unsupported format %q", output.Name, output.Format)
		}
		if output.Format == OutputHelm {
			if len(output.Components) != 1 {
				return e.New(e.CodeInvalidCatalog, "definition Helm output %q requires exactly one source archive or acquired chart component", output.Name)
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
			if output.Format != OutputHelm || len(output.Components) != 1 {
				return e.New(e.CodeInvalidCatalog, "definition output %q requires a source Helm archive for image binding", output.Name)
			}

			if err := output.Helm.ValidateBindings(); err != nil {
				return err
			}
			for _, slot := range sortedImageSlots(output.Helm.Images) {
				binding := output.Helm.Images[slot]
				found := false
				for _, candidate := range definition.Outputs {
					if candidate.Name == binding && candidate.Format == OutputOCI && candidate.Platform == output.Platform {
						found = true
					}
				}
				if !found {
					return e.New(e.CodeInvalidCatalog, "Helm output %q slot %q references a missing or incompatible OCI output %q", output.Name, slot, binding)
				}
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
	if len(environment.GitHubDispatchBranches) > 32 {
		return e.New(e.CodeInvalidCatalog, "too many GitHub dispatch branches")
	}
	if environment.GitHubARC != "" && environment.GitHubARC != "native-kvm-v1" {
		return e.New(e.CodeEngineCapability, "unsupported GitHub ARC policy %q", environment.GitHubARC)
	}
	if environment.DiskEvidence != "" && (environment.DiskEvidence != "build-provenance-v1" || environment.GitHubARC != "native-kvm-v1" || environment.Publication == nil) {
		return e.New(e.CodeEngineCapability, "disk evidence mode requires the explicit native ARC publication route")
	}
	previousBranch := ""
	for _, branch := range environment.GitHubDispatchBranches {
		if !dispatchBranchPattern.MatchString(branch) || branch <= previousBranch || strings.HasPrefix(branch, "refs/") || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") {
			return e.New(e.CodeInvalidCatalog, "GitHub dispatch branches must be sorted, unique exact branch names")
		}
		for _, part := range strings.Split(branch, "/") {
			if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
				return e.New(e.CodeInvalidCatalog, "invalid GitHub dispatch branch %q", branch)
			}
		}
		previousBranch = branch
	}
	if err := execution.ValidateClasses(environment.ExecutionClasses); err != nil {
		return err
	}
	profilePlatforms := make([]string, 0, len(environment.DiskInspectionProfiles))
	for platform := range environment.DiskInspectionProfiles {
		profilePlatforms = append(profilePlatforms, platform)
	}
	sort.Strings(profilePlatforms)
	for _, platform := range profilePlatforms {
		profile := environment.DiskInspectionProfiles[platform]
		if err := profile.Validate(platform); err != nil {
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
	if publication := environment.Publication; publication != nil {
		if publication.Mode != "" && (publication.Mode != "per-output-v1" || environment.Signing == nil || environment.Signing.Mode != "cosign") {
			return e.New(e.CodeEngineCapability, "per-output publication requires production cosign signing")
		}
		if strings.ContainsAny(publication.CredentialRef, "\r\n\x00") {
			return e.New(e.CodeInvalidCatalog, "publication credentialRef must not contain control characters")
		}
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

	if err := environment.Runtime.validate(); err != nil {
		return err
	}
	if signing := environment.Signing; signing != nil {
		if environment.Publication == nil {
			return e.New(e.CodeInvalidCatalog, "environment signing requires publication")
		}
		if strings.TrimSpace(signing.Identity) != signing.Identity || strings.TrimSpace(signing.Issuer) != signing.Issuer || strings.ContainsAny(signing.Identity+signing.Issuer, "\r\n\x00") {
			return e.New(e.CodeTrustPolicyMissing, "environment signing requires a canonical identity and issuer")
		}
		if _, err := regexp.Compile(signing.Identity); err != nil {
			return e.Wrap(e.CodeTrustPolicyMissing, err, "compiling signing identity policy")
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

func (profile Environment) RuntimeBinary() string {
	if r := strings.TrimSpace(profile.Runtime.Runtime); r != "" {
		return r
	}
	return "podman"
}

func (profile Runtime) validate() error {
	if err := profile.PodmanStorageDriver.Validate(profile.Runtime); err != nil {
		return err
	}
	if profile.BuilderImage != "" {
		return e.New(e.CodeEngineCapability, "runtime builderImage requires the retired GitLab bootstrap adapter")
	}
	if profile.Runtime != "" && profile.Runtime != "podman" && profile.Runtime != "docker" {
		return e.New(e.CodeInvalidCatalog, "delivery runtime %q is unsupported (podman|docker)", profile.Runtime)
	}
	for label, image := range map[string]string{"runtimeImage": profile.RuntimeImage, "helperImage": profile.HelperImage} {
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
	if profile.BuildNetworkPolicy != "" && profile.BuildNetworkPolicy != BuildNetworkOfflineOnly &&
		profile.BuildNetworkPolicy != BuildNetworkProducerDeclared {
		return e.New(e.CodeInvalidCatalog,
			"delivery buildNetworkPolicy %q is unsupported (offline-only|producer-declared)",
			profile.BuildNetworkPolicy)
	}
	return nil
}

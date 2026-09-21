package catalog

import (
	"strings"
	"unicode"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/packertransport"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/safetext"
)

func validateVMImageBuildRequirements(name string, requirements *execution.Requirements) error {
	if requirements == nil {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image builder requires generic buildRequirements", name)
	}
	resources := requirements.Resources
	if resources == nil {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require a resource request", name)
	}
	if resources.CPUMillis == 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require positive CPU", name)
	}
	if resources.MemoryMiB == 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require positive memory", name)
	}
	if resources.EphemeralStorageMiB == 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require positive ephemeral storage", name)
	}
	if len(requirements.AllowedHostPlatforms) == 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require an allowed host platform", name)
	}
	if len(requirements.Capabilities) != 1 || requirements.Capabilities[0].Name != execution.CapabilityKVM {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require an explicit KVM capability policy", name)
	}
	if requirements.TimeoutSeconds == 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: vm-image buildRequirements require a positive executor timeout", name)
	}
	return nil
}

const (
	maxPackerProjectPathBytes  = 4096
	maxPackerArtifactPathBytes = 4096
	maxPackerBuildNameBytes    = 256
	maxMaterialComponentBytes  = 256
)

func validatePackerConfig(name string, config *PackerConfig) error {
	if !validPackerTransportValue(config.ProjectPath, maxPackerProjectPathBytes) ||
		!validSourceRelativePath(config.ProjectPath) || strings.ContainsAny(config.ProjectPath, "{}") {
		return e.New(e.CodeInvalidCatalog,
			"component %q: packer.projectPath %q is not a clean source-relative path", name, config.ProjectPath)
	}
	if !validPackerTransportValue(config.BuildName, maxPackerBuildNameBytes) ||
		strings.ContainsAny(config.BuildName, ",*?[]\\") {
		return e.New(e.CodeInvalidCatalog,
			"component %q: packer.buildName %q must be a bounded exact Packer build name without list or glob syntax",
			name, config.BuildName)
	}
	if !validPackerTransportValue(config.PackerVersion, 64) ||
		strings.TrimSpace(config.PackerVersion) != config.PackerVersion ||
		strings.IndexFunc(config.PackerVersion, unicode.IsSpace) >= 0 {
		return e.New(e.CodeInvalidCatalog,
			"component %q: packer.packerVersion must be an exact bounded version", name)
	}
	variableNames := sortedStringKeys(config.Variables)
	publicVariables := make([]packertransport.Variable, 0, len(variableNames))
	for _, variableName := range variableNames {
		publicVariables = append(publicVariables, packertransport.Variable{
			Name: variableName, Value: config.Variables[variableName],
		})
	}
	reserved := []string{
		config.ArchitectureVariable,
		config.AcceleratorVariable,
		config.MaterialManifestVariable,
		config.OutputDirectoryVariable,
	}
	if (config.SSHPrivateKeyVariable == "") != (config.SSHPublicKeyVariable == "") {
		return e.New(e.CodeInvalidCatalog,
			"component %q must declare both Packer SSH key variables or neither", name)
	}
	if config.SSHPrivateKeyVariable != "" {
		reserved = append(reserved, config.SSHPrivateKeyVariable, config.SSHPublicKeyVariable)
	}
	for _, optional := range []string{
		config.BuildLogDirectoryVariable,
		config.HTTPDirectoryVariable,
	} {
		if optional != "" {
			reserved = append(reserved, optional)
		}
	}
	if err := packertransport.ValidateVariables(reserved, publicVariables); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err,
			"component %q has invalid Packer variable bindings", name)
	}
	return nil
}

func clonePackerConfig(config *PackerConfig) *PackerConfig {
	if config == nil {
		return nil
	}
	cloned := *config
	cloned.Variables = cloneStringMap(config.Variables)
	return &cloned
}

// validPackerTransportValue enforces only transport-safety properties NKD owns. The
// pinned Packer subprocess remains authoritative for project, build-name, and
// variable semantics; catalog loading deliberately does not reimplement HCL.
func validPackerTransportValue(value string, maxBytes int) bool {
	return safetext.Valid(value, maxBytes, false)
}

// ValidateVMImageHostRoute closes the guest/host relationship for a concrete
// component target architecture. KVM acceleration cannot cross
// architectures; preferred and forbidden policies retain their declared TCG route.
func ValidateVMImageHostRoute(name string, component Component, guestArch string) error {
	if component.BuilderType != BuilderVMImage {
		// TODO(execution-class admission): Before any adapter admits non-nil
		// execution requirements, define in ADR-18 whether an ordinary builder's
		// target architecture constrains its runner host, then enforce required-KVM
		// host matching here or in the authenticated Environment matcher. Today only
		// vm-image defines that architecture as a guest/host relationship, and every
		// current adapter rejects non-nil requirements before work or output.
		return nil
	}
	if component.BuildRequirements == nil {
		return e.New(e.CodeInvalidCatalog,
			"vm-image component %q has no build requirements", name)
	}
	policy := execution.CapabilityPolicy("")
	for _, capability := range component.BuildRequirements.Capabilities {
		if capability.Name == execution.CapabilityKVM {
			policy = capability.Policy
			break
		}
	}
	if policy != execution.CapabilityRequired {
		return nil
	}
	expected := execution.HostPlatform("linux/" + guestArch)
	for _, platform := range component.BuildRequirements.AllowedHostPlatforms {
		if platform == expected {
			return nil
		}
	}
	return e.New(e.CodeInvalidCatalog,
		"vm-image component %q requires KVM but has no same-architecture host platform for guest architecture %q",
		name, guestArch)
}

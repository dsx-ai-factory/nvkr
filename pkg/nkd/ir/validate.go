package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/packertransport"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/safetext"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
)

var (
	canonicalNodeID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	exactGitSHA     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	flakeOutput     = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._+-]*[A-Za-z0-9])?$`)
)

var allowedBuildEnv = map[string]bool{
	"SOURCE_DATE_EPOCH":   true,
	"TZ":                  true,
	"LC_ALL":              true,
	"GOTOOLCHAIN":         true,
	"GOOS":                true,
	"GOARCH":              true,
	"GOAMD64":             true,
	"GOARM64":             true,
	"CGO_ENABLED":         true,
	"GOFLAGS":             true,
	"CC":                  true,
	"KUBE_GIT_VERSION":    true,
	"KUBE_GIT_COMMIT":     true,
	"KUBE_GIT_TREE_STATE": true,
	"KUBE_GIT_MAJOR":      true,
	"KUBE_GIT_MINOR":      true,
	"KUBE_OPERATOR_MODE":  true,
	// NKE CLI (github.com/NVIDIA/nke) version stamps. Like the KUBE_GIT_* set above these
	// are named explicitly rather than pattern-matched: the allowlist is the guarantee that
	// nothing unreviewed reaches a hermetic build container's environment, so an upstream
	// that wants a stamp adds it here and it is reviewed once.
	"NKECTL_VERSION":      true,
	"NKECTL_COMMIT":       true,
	"NKECTL_DATE":         true,
	"NKECTL_CAPI_VERSION": true,
	// Upstream CWE Dockerfile build arguments, rendered from the source lock.
	"BUILD_VERSION": true,
	"GIT_VERSION":   true,
}

var knownOutputTypes = map[string]bool{
	"host-package": true,
	"oci-image":    true,
	"disk-image":   true,
	"raw":          true,
	"tarball":      true,
}

// Validate is the single semantic authority for executable IR. It deliberately
// validates the full transport-bearing graph before identity code removes transport
// locators: an unsafe caller-composed graph can therefore neither execute nor gain a
// buildDigest merely because its reduced DigestView looks harmless.
func (g *Graph) Validate() error {
	if g == nil {
		return e.New(e.CodeInvalidCatalog, "IR graph is nil")
	}
	if g.SchemaVersion != SchemaVersion {
		return e.New(e.CodeUnsupported,
			"IR graph schemaVersion %q is unsupported; want %q", g.SchemaVersion, SchemaVersion)
	}
	if err := target.ValidatePlatform(g.Platform); err != nil {
		return err
	}
	if g.AICR != nil {
		if err := g.AICR.Validate(); err != nil {
			return err
		}
	}
	if len(g.Nodes) == 0 {
		return e.New(e.CodeInvalidCatalog, "IR graph has no terminal verify node")
	}

	byID := make(map[string]Node, len(g.Nodes))
	producerIDs := make([]string, 0, len(g.Nodes))
	components := make(map[string]string)
	fetchConsumers := make(map[string]int)
	var terminal *Node
	previousID := ""
	for i := range g.Nodes {
		node := g.Nodes[i]
		if i > 0 && node.ID <= previousID {
			return e.New(e.CodeInvalidCatalog,
				"IR nodes must be strictly sorted by unique ID: %q follows %q", node.ID, previousID)
		}
		previousID = node.ID
		if err := node.Validate(); err != nil {
			return err
		}
		if _, duplicate := byID[node.ID]; duplicate {
			return e.New(e.CodeInvalidCatalog, "IR graph repeats node ID %q", node.ID)
		}
		byID[node.ID] = node
		switch node.Kind {
		case KindBuild, KindPull:
			if node.Platform != g.Platform {
				return e.New(e.CodeInvalidCatalog,
					"node %q platform %q does not match build target %q",
					node.ID, node.Platform, g.Platform)
			}
			if prior, duplicate := components[node.Component]; duplicate {
				return e.New(e.CodeInvalidCatalog,
					"artifact component %q is produced by both %q and %q", node.Component, prior, node.ID)
			}
			components[node.Component] = node.ID
			producerIDs = append(producerIDs, node.ID)
		case KindVerify:
			if terminal != nil {
				return e.New(e.CodeInvalidCatalog,
					"IR graph has more than one terminal verify node (%q and %q)", terminal.ID, node.ID)
			}
			copyNode := node
			terminal = &copyNode
		}
	}
	if terminal == nil {
		return e.New(e.CodeInvalidCatalog, "IR graph has no terminal verify node")
	}
	if g.AICR != nil && len(producerIDs) != 0 {
		return e.New(e.CodeInvalidCatalog, "AICR build graph must not contain ordinary artifact producers")
	}
	if g.AICR == nil && len(producerIDs) == 0 {
		return e.New(e.CodeInvalidCatalog, "build graph requires at least one artifact producer or an AICR profile")
	}

	for _, node := range g.Nodes {
		for _, dependency := range node.Needs {
			if _, ok := byID[dependency]; !ok {
				return e.New(e.CodeInvalidCatalog,
					"node %q depends on unknown node %q", node.ID, dependency)
			}
		}
		if node.Kind != KindBuild {
			continue
		}
		fetch, err := sourceDependency(node, byID)
		if err != nil {
			return err
		}
		fetchConsumers[fetch.ID]++
		if node.SourceDest != fetch.Dest || node.SourceCommit != fetch.Commit ||
			node.SourceTreeHash != fetch.TreeHash || node.VendorSpecDigest != fetch.VendorSpecDigest {
			return e.New(e.CodeInvalidCatalog,
				"build node %q source pins do not exactly match dependency %q", node.ID, fetch.ID)
		}
		if node.BuilderType == BuilderVMImage {
			if err := validateVMMaterialDependencies(node, byID, fetch.ID); err != nil {
				return err
			}
		}
	}
	for _, node := range g.Nodes {
		if node.Kind == KindFetch && fetchConsumers[node.ID] == 0 {
			return e.New(e.CodeInvalidCatalog,
				"fetch node %q is not consumed by an artifact-producing build", node.ID)
		}
	}
	if len(terminal.Needs) != len(producerIDs) {
		return e.New(e.CodeInvalidCatalog,
			"terminal verify covers %d artifact producers, want %d", len(terminal.Needs), len(producerIDs))
	}
	for i := range producerIDs {
		if terminal.Needs[i] != producerIDs[i] {
			return e.New(e.CodeInvalidCatalog,
				"terminal verify dependency %d is %q, want artifact producer %q",
				i, terminal.Needs[i], producerIDs[i])
		}
	}
	if err := validateAcyclic(g.Nodes, byID); err != nil {
		return err
	}
	return nil
}

// Validate checks the kind-specific semantics that remain meaningful when a CI
// adapter executes one node in isolation. Graph.Validate adds topology, target platform,
// and cross-node pin consistency; both interfaces share these same field checks.
func (n Node) Validate() error {
	if !canonicalNodeID.MatchString(n.ID) {
		return e.New(e.CodeInvalidCatalog,
			"IR node ID %q is not a canonical lowercase identifier", n.ID)
	}
	if err := validateNeeds(n); err != nil {
		return err
	}
	if err := n.Requirements.Validate(); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err,
			"IR node %q has invalid execution requirements", n.ID)
	}
	switch n.Kind {
	case KindFetch:
		return validateFetchNode(n)
	case KindBuild:
		return validateBuildNode(n)
	case KindPull:
		return validatePullNode(n)
	case KindVerify:
		return validateVerifyNode(n)
	default:
		return e.New(e.CodeInvalidCatalog, "IR node %q has unknown kind %q", n.ID, n.Kind)
	}
}

func validateNeeds(n Node) error {
	previous := ""
	for i, dependency := range n.Needs {
		if !canonicalNodeID.MatchString(dependency) {
			return e.New(e.CodeInvalidCatalog,
				"node %q has non-canonical dependency ID %q", n.ID, dependency)
		}
		if dependency == n.ID {
			return e.New(e.CodeInvalidCatalog, "node %q depends on itself", n.ID)
		}
		if i > 0 && dependency <= previous {
			return e.New(e.CodeInvalidCatalog,
				"node %q dependencies must be strictly sorted and unique", n.ID)
		}
		previous = dependency
	}
	return nil
}

func validateFetchNode(n Node) error {
	if err := rejectUnexpectedNodeFields(n, allowFields(
		"repo", "commit", "treeHash", "dest", "vendorCmd", "vendorImage",
		"vendorSpecDigest", "network")); err != nil {
		return err
	}
	if len(n.Needs) != 0 {
		return e.New(e.CodeInvalidCatalog, "fetch node %q must not have dependencies", n.ID)
	}
	if !safeReference(n.Repo) {
		return e.New(e.CodeUnpinnedSource, "fetch node %q has no canonical source transport", n.ID)
	}
	if !exactGitSHA.MatchString(n.Commit) || !exactGitSHA.MatchString(n.TreeHash) {
		return e.New(e.CodeUnpinnedSource,
			"fetch node %q requires exact lowercase 40-hex commit and tree pins", n.ID)
	}
	if !safeRelativePath(n.Dest) {
		return e.New(e.CodeInvalidCatalog, "fetch node %q destination %q is not a bounded relative path", n.ID, n.Dest)
	}
	if n.Network != NetworkDefault {
		return e.New(e.CodeInvalidCatalog,
			"fetch node %q network mode %q is invalid; fetch requires %q", n.ID, n.Network, NetworkDefault)
	}
	if !canonical.IsDigest(n.VendorSpecDigest) {
		return e.New(e.CodeUnpinnedSource,
			"fetch node %q vendor specification is not pinned by an exact sha256", n.ID)
	}
	wantVendorSpec, err := VendorSpecIdentity(n.VendorCmd, n.VendorImage)
	if err != nil {
		return err
	}
	if n.VendorSpecDigest != wantVendorSpec {
		return e.New(e.CodeUnpinnedSource,
			"fetch node %q vendor specification digest does not match its command and image content", n.ID)
	}
	if len(n.VendorCmd) > 0 {
		if err := validateCommand(n.ID+" vendor", n.VendorCmd); err != nil {
			return err
		}
		if n.VendorImage == "" {
			return e.New(e.CodeTagToolchain,
				"fetch node %q has a vendor command without a pinned vendor image", n.ID)
		}
	}
	if n.VendorImage != "" && !exactOCIIdentity(n.VendorImage) {
		return e.New(e.CodeTagToolchain,
			"fetch node %q vendor image %q is not an exact OCI digest pin", n.ID, n.VendorImage)
	}
	return nil
}

// VendorSpecIdentity is the one transport-independent identity for the networked
// dependency materialization step. Registry names are transport; only the selected
// image's manifest digest and exact argv affect the source closure identity.
func VendorSpecIdentity(command []string, image string) (string, error) {
	imageDigest := ""
	if image != "" {
		if !exactOCIIdentity(image) {
			return "", e.New(e.CodeTagToolchain,
				"vendor image %q is not an exact OCI digest pin", image)
		}
		imageDigest = imageDigestID(image)
	}
	return canonical.Digest(canonical.KindVendorSpec, struct {
		Command []string `json:"command,omitempty"`
		Image   string   `json:"imageDigest,omitempty"`
	}{Command: command, Image: imageDigest})
}

func validateBuildNode(n Node) error {
	if n.BuilderType == BuilderVMImage {
		return validateVMImageNode(n)
	}
	if err := rejectUnexpectedNodeFields(n, allowFields(
		"requirements", "executionBinding",
		"component", "builderType", "flakeRef", "flakeOutput", "flakeLockDigest",
		"toolchain", "platform", "network", "env", "patches", "command", "workdir",
		"sourceDest", "sourceCommit", "sourceTreeHash", "vendorSpecDigest",
		"sourceReadOnly", "writeDir", "outputType", "artifactPath", "version")); err != nil {
		return err
	}
	if n.ExecutionBinding != nil {
		if err := n.ExecutionBinding.ValidateFor(n.Requirements); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"build node %q has invalid execution binding", n.ID)
		}
	}
	if !canonicalNodeID.MatchString(n.Component) || n.ID != "build-"+n.Component {
		return e.New(e.CodeInvalidCatalog,
			"build node %q must be named build-<canonical component>", n.ID)
	}
	if len(n.Needs) != 1 {
		return e.New(e.CodeInvalidCatalog,
			"build node %q must depend on exactly one pinned fetch", n.ID)
	}
	if !exactOCIIdentity(n.Toolchain) {
		return e.New(e.CodeTagToolchain,
			"build node %q toolchain %q is not an exact OCI digest pin", n.ID, n.Toolchain)
	}
	arch, ok := platformArch(n.Platform)
	if !ok {
		return e.New(e.CodeInvalidCatalog,
			"build node %q platform %q is not linux/amd64 or linux/arm64", n.ID, n.Platform)
	}
	wantNetwork := NetworkNone
	if n.BuilderType == BuilderBuildKit {
		wantNetwork = NetworkDefault
	}
	if n.Network != wantNetwork {
		return e.New(e.CodeInvalidCatalog,
			"build node %q network mode %q differs from builder mode %q", n.ID, n.Network, wantNetwork)
	}
	if err := validateBuildEnv(n.ID, arch, n.Env); err != nil {
		return err
	}
	if err := validatePatches(n); err != nil {
		return err
	}
	if err := validateCommand(n.ID+" build", n.Command); err != nil {
		return err
	}
	if n.Workdir != "/src" {
		return e.New(e.CodeInvalidCatalog,
			"build node %q workdir %q is outside the fixed /src root", n.ID, n.Workdir)
	}
	if !n.SourceReadOnly || n.WriteDir == "" {
		return e.New(e.CodeEngineCapability,
			"build node %q requires sourceReadOnly=true and a private writeDir", n.ID)
	}
	if !safeRelativePath(n.SourceDest) || !safeRelativePath(n.WriteDir) || !safeRelativePath(n.ArtifactPath) {
		return e.New(e.CodeInvalidCatalog,
			"build node %q source/output paths must be bounded relative paths", n.ID)
	}
	if !strings.HasPrefix(n.ArtifactPath, n.WriteDir+"/") {
		return e.New(e.CodeInvalidCatalog,
			"build node %q artifactPath %q is outside private writeDir %q", n.ID, n.ArtifactPath, n.WriteDir)
	}
	if !exactGitSHA.MatchString(n.SourceCommit) || !exactGitSHA.MatchString(n.SourceTreeHash) ||
		!canonical.IsDigest(n.VendorSpecDigest) {
		return e.New(e.CodeUnpinnedSource,
			"build node %q does not carry exact source/tree/vendor pins", n.ID)
	}
	if !knownOutputTypes[n.OutputType] {
		return e.New(e.CodeInvalidCatalog,
			"build node %q has unknown outputType %q", n.ID, n.OutputType)
	}
	if !safeVersion(n.Version) {
		return e.New(e.CodeInvalidCatalog, "build node %q has no canonical component version", n.ID)
	}
	switch n.BuilderType {
	case BuilderBuildKit:
		if n.OutputType != "oci-image" || n.FlakeRef != "" || n.FlakeOutput != "" || n.FlakeLockDigest != "" {
			return e.New(e.CodeInvalidCatalog, "rootless BuildKit node %q requires an OCI export and build arguments", n.ID)
		}
		for _, variable := range n.Env {
			if variable.Name == "BUILDKITD_FLAGS" {
				return e.New(e.CodeInvalidCatalog, "rootless BuildKit node %q may not override fixed daemon isolation", n.ID)
			}
		}
	case BuilderOCIHermetic:
		if n.FlakeRef != "" || n.FlakeOutput != "" || n.FlakeLockDigest != "" {
			return e.New(e.CodeInvalidCatalog,
				"OCI build node %q must not carry Nix flake fields", n.ID)
		}
	case BuilderNix:
		if !safeReference(n.FlakeRef) || !flakeOutput.MatchString(n.FlakeOutput) ||
			!canonical.IsDigest(n.FlakeLockDigest) {
			return e.New(e.CodeUnpinnedSource,
				"Nix build node %q requires exact flake ref/output/closure pins", n.ID)
		}
	default:
		return e.New(e.CodeInvalidCatalog,
			"build node %q has unknown builderType %q", n.ID, n.BuilderType)
	}
	return nil
}

func validateVMImageNode(n Node) error {
	if err := rejectUnexpectedNodeFields(n, allowFields(
		"requirements", "executionBinding", "component", "builderType", "toolchain", "toolchainPlatform",
		"platform", "network", "patches", "command", "workdir", "sourceDest",
		"sourceCommit", "sourceTreeHash", "vendorSpecDigest", "disk", "packer",
		"materials", "outputType", "artifactPath", "version")); err != nil {
		return err
	}
	if n.ExecutionBinding != nil {
		if err := n.ExecutionBinding.ValidateFor(n.Requirements); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"VM build node %q has invalid execution binding", n.ID)
		}
	}
	if !canonicalNodeID.MatchString(n.Component) || n.ID != "build-"+n.Component {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q must be named build-<canonical component>", n.ID)
	}
	if len(n.Needs) == 0 {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q requires one pinned source dependency", n.ID)
	}
	if !exactOCIIdentity(n.Toolchain) {
		return e.New(e.CodeTagToolchain,
			"VM build node %q toolchain %q is not an exact OCI digest pin", n.ID, n.Toolchain)
	}
	if _, ok := platformArch(n.Platform); !ok {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q platform %q is not linux/amd64 or linux/arm64", n.ID, n.Platform)
	}
	if err := validateVMRequirements(n); err != nil {
		return err
	}
	hostPlatform := execution.HostPlatform(n.ToolchainPlatform)
	if !slices.Contains(n.Requirements.AllowedHostPlatforms, hostPlatform) {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q toolchain platform %q is not an allowed execution host", n.ID, n.ToolchainPlatform)
	}
	if n.ExecutionBinding != nil && n.ExecutionBinding.Class.HostPlatform != hostPlatform {
		return e.New(e.CodeEngineCapability,
			"VM build node %q toolchain platform %q differs from selected execution host %q",
			n.ID, n.ToolchainPlatform, n.ExecutionBinding.Class.HostPlatform)
	}
	if n.Network != NetworkNone {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q network mode %q differs from admitted mode %q", n.ID, n.Network, NetworkNone)
	}
	if err := validatePatches(n); err != nil {
		return err
	}
	if n.Workdir != "/src" || !safeRelativePath(n.SourceDest) || !safeRelativePath(n.ArtifactPath) {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q source and output paths are not bounded beneath their private roots", n.ID)
	}
	if !exactGitSHA.MatchString(n.SourceCommit) || !exactGitSHA.MatchString(n.SourceTreeHash) ||
		!canonical.IsDigest(n.VendorSpecDigest) {
		return e.New(e.CodeUnpinnedSource,
			"VM build node %q does not carry exact source/tree/vendor pins", n.ID)
	}
	if n.OutputType != "disk-image" || n.Disk == nil || n.Disk.Format != "qcow2" ||
		(n.Disk.Encryption != "none" && n.Disk.Encryption != "luks2") {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q requires a qcow2 disk-image with encryption none or luks2", n.ID)
	}
	if !safeVersion(n.Version) {
		return e.New(e.CodeInvalidCatalog, "VM build node %q has no canonical component version", n.ID)
	}
	commandProducer := len(n.Command) != 0
	packerProducer := n.Packer != nil
	if commandProducer == packerProducer {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q requires exactly one command or Packer producer", n.ID)
	}
	if commandProducer {
		if len(n.Materials) != 0 {
			return e.New(e.CodeEngineCapability,
				"VM command node %q has no typed material-manifest handoff", n.ID)
		}
		return validateCommand(n.ID+" VM build", n.Command)
	}
	if err := validateIRPackerConfig(n.ID, n.Packer); err != nil {
		return err
	}
	return validateMaterialBindings(n.ID, n.Materials)
}

func validateVMRequirements(n Node) error {
	requirements := n.Requirements
	if requirements == nil || requirements.Resources == nil ||
		requirements.Resources.CPUMillis == 0 || requirements.Resources.MemoryMiB == 0 ||
		requirements.Resources.EphemeralStorageMiB == 0 || len(requirements.AllowedHostPlatforms) == 0 ||
		len(requirements.Capabilities) != 1 ||
		requirements.Capabilities[0].Name != execution.CapabilityKVM || requirements.TimeoutSeconds == 0 {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q requires complete resources, host platforms, KVM policy, and timeout", n.ID)
	}
	if requirements.Capabilities[0].Policy == execution.CapabilityRequired && n.ExecutionBinding != nil &&
		string(n.ExecutionBinding.Class.HostPlatform) != n.Platform {
		return e.New(e.CodeEngineCapability,
			"VM build node %q requires KVM on guest-matching host platform %q", n.ID, n.Platform)
	}
	return nil
}

func validateIRPackerConfig(nodeID string, config *PackerConfig) error {
	if config == nil || !safetext.Valid(config.ProjectPath, 4096, false) ||
		!safeRelativePath(config.ProjectPath) || strings.ContainsAny(config.ProjectPath, "{}") {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q has an unsafe Packer project path", nodeID)
	}
	if !safetext.Valid(config.BuildName, 256, false) || strings.ContainsAny(config.BuildName, ",*?[]\\") {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q has an unsafe or inexact Packer build name", nodeID)
	}
	public := make([]packertransport.Variable, len(config.Variables))
	previous := ""
	for index, variable := range config.Variables {
		if index > 0 && variable.Name <= previous {
			return e.New(e.CodeInvalidCatalog,
				"VM build node %q Packer variables must be sorted by unique name", nodeID)
		}
		previous = variable.Name
		public[index] = packertransport.Variable{Name: variable.Name, Value: variable.Value}
	}
	if err := packertransport.ValidateVariables([]string{
		config.ArchitectureVariable,
		config.MaterialManifestVariable,
		config.OutputDirectoryVariable,
	}, public); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err,
			"VM build node %q has invalid Packer variable bindings", nodeID)
	}
	return nil
}

func validateMaterialBindings(nodeID string, bindings []MaterialBinding) error {
	components := make(map[string]string, len(bindings))
	producers := make(map[string]string, len(bindings))
	previous := ""
	for index, binding := range bindings {
		if _, err := material.StagingPath(binding.Binding); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err,
				"VM build node %q material binding %d", nodeID, index)
		}
		if index > 0 && binding.Binding <= previous {
			return e.New(e.CodeInvalidCatalog,
				"VM build node %q material bindings must be sorted and unique", nodeID)
		}
		previous = binding.Binding
		if !canonicalNodeID.MatchString(binding.Component) || !canonicalNodeID.MatchString(binding.Producer) {
			return e.New(e.CodeInvalidCatalog,
				"VM build node %q material %q has a non-canonical component or producer", nodeID, binding.Binding)
		}
		if prior, duplicate := components[binding.Component]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"VM build node %q bindings %q and %q select component %q",
				nodeID, prior, binding.Binding, binding.Component)
		}
		if prior, duplicate := producers[binding.Producer]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"VM build node %q bindings %q and %q select producer %q",
				nodeID, prior, binding.Binding, binding.Producer)
		}
		components[binding.Component] = binding.Binding
		producers[binding.Producer] = binding.Binding
	}
	return nil
}

func sourceDependency(node Node, byID map[string]Node) (Node, error) {
	var fetch Node
	for _, dependency := range node.Needs {
		candidate := byID[dependency]
		if candidate.Kind != KindFetch {
			continue
		}
		if fetch.ID != "" {
			return Node{}, e.New(e.CodeInvalidCatalog,
				"build node %q depends on more than one source fetch", node.ID)
		}
		fetch = candidate
	}
	if fetch.ID == "" {
		return Node{}, e.New(e.CodeInvalidCatalog,
			"build node %q has no pinned source fetch dependency", node.ID)
	}
	return fetch, nil
}

func validateVMMaterialDependencies(node Node, byID map[string]Node, fetchID string) error {
	expected := make(map[string]MaterialBinding, len(node.Materials))
	for _, binding := range node.Materials {
		producer, ok := byID[binding.Producer]
		if !ok || (producer.Kind != KindBuild && producer.Kind != KindPull) ||
			producer.Component != binding.Component {
			return e.New(e.CodeInvalidCatalog,
				"VM build node %q material %q does not bind producer %q for component %q",
				node.ID, binding.Binding, binding.Producer, binding.Component)
		}
		expected[binding.Producer] = binding
	}
	for _, dependency := range node.Needs {
		if dependency == fetchID {
			continue
		}
		if _, ok := expected[dependency]; !ok {
			return e.New(e.CodeInvalidCatalog,
				"VM build node %q has undeclared material producer dependency %q", node.ID, dependency)
		}
		delete(expected, dependency)
	}
	if len(expected) != 0 {
		return e.New(e.CodeInvalidCatalog,
			"VM build node %q omits one or more declared material producer dependencies", node.ID)
	}
	return nil
}

func validatePullNode(n Node) error {
	if err := rejectUnexpectedNodeFields(n, allowFields(
		"component", "artifact", "digest", "platform", "network", "env",
		"outputType", "artifactPath", "version")); err != nil {
		return err
	}
	if !canonicalNodeID.MatchString(n.Component) || n.ID != "pull-"+n.Component {
		return e.New(e.CodeInvalidCatalog,
			"pull node %q must be named pull-<canonical component>", n.ID)
	}
	if len(n.Needs) != 0 {
		return e.New(e.CodeInvalidCatalog, "pull node %q must not have dependencies", n.ID)
	}
	if !safeReference(n.Artifact) || !canonical.IsDigest(n.Digest) {
		return e.New(e.CodeUnpinnedSource,
			"pull node %q requires an artifact transport and exact content digest", n.ID)
	}
	if _, ok := platformArch(n.Platform); !ok {
		return e.New(e.CodeInvalidCatalog,
			"pull node %q platform %q is not linux/amd64 or linux/arm64", n.ID, n.Platform)
	}
	if n.Network != NetworkDefault {
		return e.New(e.CodeInvalidCatalog,
			"pull node %q network mode %q is invalid; acquisition requires %q", n.ID, n.Network, NetworkDefault)
	}
	if !safeVersion(n.Version) {
		return e.New(e.CodeInvalidCatalog, "pull node %q has no canonical component version", n.ID)
	}
	switch n.OutputType {
	case "oci-image":
		if n.ArtifactPath != "" || len(n.Env) != 0 {
			return e.New(e.CodeInvalidCatalog,
				"OCI pull node %q must not carry a file path or packaging environment", n.ID)
		}
	case "host-package":
		if !safeRelativePath(n.ArtifactPath) {
			return e.New(e.CodeInvalidCatalog,
				"pull node %q artifactPath %q is not bounded and relative", n.ID, n.ArtifactPath)
		}
		if err := validateSourceDateOnly(n.ID, n.Env); err != nil {
			return err
		}
	case "raw":
		if !safeRelativePath(n.ArtifactPath) || len(n.Env) != 0 {
			return e.New(e.CodeInvalidCatalog,
				"raw pull node %q requires a bounded artifactPath and no environment", n.ID)
		}
	default:
		return e.New(e.CodeInvalidCatalog,
			"pull node %q outputType %q is not valid for byte-preserving acquisition", n.ID, n.OutputType)
	}
	return nil
}

func validateVerifyNode(n Node) error {
	if err := rejectUnexpectedNodeFields(n, allowFields()); err != nil {
		return err
	}
	if n.ID != "verify" {
		return e.New(e.CodeInvalidCatalog,
			"terminal verify node must have canonical ID %q, got %q", "verify", n.ID)
	}
	return nil
}

func validateBuildEnv(nodeID, arch string, env []EnvVar) error {
	values, err := validateEnv(nodeID, env, allowedBuildEnv)
	if err != nil {
		return err
	}
	required := map[string]string{
		"TZ": "UTC", "LC_ALL": "C", "GOTOOLCHAIN": "local", "GOOS": "linux",
		"GOARCH": arch, "GOFLAGS": "-trimpath -buildvcs=false",
	}
	for name, want := range required {
		if values[name] != want {
			return e.New(e.CodeInvalidCatalog,
				"build node %q environment %s=%q, want %q", nodeID, name, values[name], want)
		}
	}
	if values["CGO_ENABLED"] != "0" && values["CGO_ENABLED"] != "1" {
		return e.New(e.CodeInvalidCatalog,
			"build node %q environment CGO_ENABLED must be 0 or 1", nodeID)
	}
	if !positiveEpoch(values["SOURCE_DATE_EPOCH"]) {
		return e.New(e.CodeUnpinnedSource,
			"build node %q requires a positive pinned SOURCE_DATE_EPOCH", nodeID)
	}
	if arch == "amd64" {
		if values["GOAMD64"] != "v1" || values["GOARM64"] != "" {
			return e.New(e.CodeInvalidCatalog,
				"build node %q requires GOAMD64=v1 and no GOARM64", nodeID)
		}
	} else if values["GOARM64"] != "v8.0" || values["GOAMD64"] != "" {
		return e.New(e.CodeInvalidCatalog,
			"build node %q requires GOARM64=v8.0 and no GOAMD64", nodeID)
	}
	if cc, ok := values["CC"]; ok && strings.TrimSpace(cc) == "" {
		return e.New(e.CodeInvalidCatalog, "build node %q carries an empty CC pin", nodeID)
	}
	return nil
}

func validateSourceDateOnly(nodeID string, env []EnvVar) error {
	values, err := validateEnv(nodeID, env, map[string]bool{"SOURCE_DATE_EPOCH": true})
	if err != nil {
		return err
	}
	if len(values) != 1 || !positiveEpoch(values["SOURCE_DATE_EPOCH"]) {
		return e.New(e.CodeUnpinnedSource,
			"pull node %q requires exactly one positive SOURCE_DATE_EPOCH pin", nodeID)
	}
	return nil
}

func validateEnv(nodeID string, env []EnvVar, allowed map[string]bool) (map[string]string, error) {
	values := make(map[string]string, len(env))
	previous := ""
	for i, variable := range env {
		if !allowed[variable.Name] {
			return nil, e.New(e.CodeInvalidCatalog,
				"node %q environment variable %q is not allow-listed", nodeID, variable.Name)
		}
		if i > 0 && variable.Name <= previous {
			return nil, e.New(e.CodeInvalidCatalog,
				"node %q environment must be strictly sorted by unique name", nodeID)
		}
		if strings.ContainsRune(variable.Value, '\x00') {
			return nil, e.New(e.CodeInvalidCatalog,
				"node %q environment variable %q contains NUL", nodeID, variable.Name)
		}
		previous = variable.Name
		values[variable.Name] = variable.Value
	}
	return values, nil
}

func validatePatches(n Node) error {
	seen := make(map[string]bool, len(n.Patches))
	for i, patch := range n.Patches {
		if !safeRelativePath(patch.Name) || seen[patch.Name] {
			return e.New(e.CodeInvalidCatalog,
				"build node %q patch[%d] name %q is unsafe or duplicated", n.ID, i, patch.Name)
		}
		seen[patch.Name] = true
		if !canonical.IsDigest(patch.SHA256) {
			return e.New(e.CodeUnpinnedSource,
				"build node %q patch %q has no exact digest pin", n.ID, patch.Name)
		}
		sum := sha256.Sum256([]byte(patch.Content))
		if "sha256:"+hex.EncodeToString(sum[:]) != patch.SHA256 {
			return e.New(e.CodeUnpinnedSource,
				"build node %q patch %q content does not match its digest", n.ID, patch.Name)
		}
	}
	return nil
}

func validateCommand(label string, command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return e.New(e.CodeInvalidCatalog, "%s command is empty", label)
	}
	for _, argument := range command {
		if strings.ContainsRune(argument, '\x00') {
			return e.New(e.CodeInvalidCatalog, "%s command contains NUL", label)
		}
	}
	return nil
}

func exactOCIIdentity(reference string) bool {
	if canonical.IsDigest(reference) {
		return true
	}
	prefix, digest, ok := strings.Cut(reference, "@")
	return ok && prefix != "" && !strings.Contains(prefix, "@") && safeReference(prefix) && canonical.IsDigest(digest)
}

func platformArch(platform string) (string, bool) {
	if err := target.ValidatePlatform(platform); err != nil {
		return "", false
	}
	return target.Arch(platform), true
}

func safeRelativePath(value string) bool {
	return value != "." && value == strings.TrimSpace(value) && fs.ValidPath(value) &&
		!strings.Contains(value, `\`)
}

func safeReference(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "\x00\r\n\t ")
}

func safeVersion(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "\x00\r\n\t ")
}

func positiveEpoch(value string) bool {
	epoch, err := strconv.ParseInt(value, 10, 64)
	return err == nil && epoch > 0 && strconv.FormatInt(epoch, 10) == value
}

func validateAcyclic(nodes []Node, byID map[string]Node) error {
	state := make(map[string]uint8, len(nodes))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return e.New(e.CodeInvalidCatalog, "IR graph contains a dependency cycle at node %q", id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, dependency := range byID[id].Needs {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, node := range nodes {
		if err := visit(node.ID); err != nil {
			return err
		}
	}
	return nil
}

func allowFields(fields ...string) map[string]bool {
	allowed := map[string]bool{"id": true, "kind": true, "needs": true}
	for _, field := range fields {
		allowed[field] = true
	}
	return allowed
}

// rejectUnexpectedNodeFields makes new Node fields fail closed by default. Adding a
// field to the IR therefore requires an explicit semantic decision for every kind,
// rather than silently extending an executable caller-composed document.
func rejectUnexpectedNodeFields(n Node, allowed map[string]bool) error {
	value, typ := reflect.ValueOf(n), reflect.TypeOf(n)
	for i := 0; i < value.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" || allowed[name] || value.Field(i).IsZero() {
			continue
		}
		return e.New(e.CodeInvalidCatalog,
			"%s node %q carries forbidden field %s", n.Kind, n.ID, name)
	}
	return nil
}

package ir

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

var (
	canonicalRecipeID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
	canonicalNodeID   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	canonicalToken    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._+-]*[a-z0-9])?$`)
	exactGitSHA       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	flakeOutput       = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._+-]*[A-Za-z0-9])?$`)
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
// recipeDigest merely because its reduced DigestView looks harmless.
func (g *Graph) Validate() error {
	if g == nil {
		return e.New(e.CodeInvalidCatalog, "IR graph is nil")
	}
	if g.SchemaVersion != SchemaVersion {
		return e.New(e.CodeUnsupported,
			"IR graph schemaVersion %q is unsupported; want %q", g.SchemaVersion, SchemaVersion)
	}
	if !canonicalRecipeID.MatchString(g.RecipeID) {
		return e.New(e.CodeInvalidCatalog,
			"IR graph recipeId %q is not a canonical nonempty identifier", g.RecipeID)
	}
	if err := validateResolvedCriteria(g.Criteria); err != nil {
		return err
	}
	if g.AICR != nil {
		if err := g.AICR.Validate(); err != nil {
			return err
		}
		if g.Criteria.Category != "accelerated-runtime" || g.Criteria.OS != g.AICR.OS {
			return e.New(e.CodeInvalidCatalog,
				"IR AICR activation does not match resolved category/os criteria")
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
			if node.Platform != "linux/"+g.Criteria.Arch {
				return e.New(e.CodeInvalidCatalog,
					"node %q platform %q does not match resolved architecture %q",
					node.ID, node.Platform, g.Criteria.Arch)
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
		fetch := byID[node.Needs[0]]
		if fetch.Kind != KindFetch {
			return e.New(e.CodeInvalidCatalog,
				"build node %q must depend on exactly one fetch node, got %q (%s)",
				node.ID, fetch.ID, fetch.Kind)
		}
		fetchConsumers[fetch.ID]++
		if node.SourceDest != fetch.Dest || node.SourceCommit != fetch.Commit ||
			node.SourceTreeHash != fetch.TreeHash || node.VendorSpecDigest != fetch.VendorSpecDigest {
			return e.New(e.CodeInvalidCatalog,
				"build node %q source pins do not exactly match dependency %q", node.ID, fetch.ID)
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
// adapter executes one node in isolation. Graph.Validate adds topology, criteria,
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

func validateResolvedCriteria(c criteria.Criteria) error {
	if err := c.ValidateActive(); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"category": c.Category, "role": c.Role, "k8sVersion": c.K8sVersion,
		"os": c.OS, "arch": c.Arch, "clusterType": c.ClusterType,
	} {
		if value == criteria.Wildcard || value != strings.TrimSpace(value) || strings.ContainsRune(value, '\x00') {
			return e.New(e.CodeInvalidCatalog,
				"resolved IR criteria %s %q is not concrete and canonical", name, value)
		}
	}
	if c.OS == "" || !canonicalToken.MatchString(c.OS) {
		return e.New(e.CodeInvalidCatalog, "resolved IR criteria os %q is not canonical", c.OS)
	}
	if !criteria.ValidArch(c.Arch) {
		return e.New(e.CodeInvalidCatalog, "resolved IR criteria arch %q is not concrete", c.Arch)
	}
	if c.K8sVersion != "" && !canonicalToken.MatchString(strings.TrimPrefix(c.K8sVersion, "v")) {
		return e.New(e.CodeInvalidCatalog,
			"resolved IR criteria k8sVersion %q is not canonical", c.K8sVersion)
	}
	return nil
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
	switch platform {
	case "linux/amd64":
		return "amd64", true
	case "linux/arm64":
		return "arm64", true
	default:
		return "", false
	}
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

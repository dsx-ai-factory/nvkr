package buildgraph

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
)

// Compile lowers a resolved output into the pinned build DAG. Components that
// share the same commit, tree, and vendor-content identity collapse into one fetch
// node; transport locators never affect this sharing key. Each buildable component
// gets a build node depending on it; a single terminal verify node depends on all
// artifact producers (NKD-FR-BLD-001, NKD-FR-BLD-006). Tier-4 exclusions are never
// emitted as build nodes (NKD-FR-RC-026).
func Compile(r *Resolved) (*ir.Graph, error) {
	return compile(r, nil)
}

// CompileForHostPlatforms lowers a resolved graph after the factory has selected
// one execution-host platform for every VM producer. The selected host decides
// which digest-pinned VM toolchain enters Build identity; the guest/output
// platform remains Graph.Platform and node.Platform.
func CompileForHostPlatforms(r *Resolved, vmHosts map[string]execution.HostPlatform) (*ir.Graph, error) {
	return compile(r, vmHosts)
}

func compile(r *Resolved, vmHosts map[string]execution.HostPlatform) (*ir.Graph, error) {
	if r == nil {
		return nil, e.New(e.CodeInvalidCatalog, "cannot compile a nil resolved output")
	}
	for _, component := range r.Components {
		if !component.FromSource && component.Prebuilt == nil && component.ExclusionReason != "" {
			continue
		}
		if component.OutputType == "disk-image" && component.BuilderType != catalog.BuilderVMImage {
			return nil, e.New(e.CodeEngineCapability,
				"component %q declares a disk-image producer other than vm-image, which is not implemented", component.Name)
		}
		if component.BuilderType == catalog.BuilderVMImage && component.Packer == nil && len(component.Materials) != 0 {
			return nil, e.New(e.CodeEngineCapability,
				"component %q uses a VM command producer without a typed material-manifest handoff", component.Name)
		}
	}
	if err := validateResolved(r); err != nil {
		return nil, err
	}
	vmComponents := make(map[string]struct{})
	for _, component := range r.Components {
		if component.BuilderType == catalog.BuilderVMImage {
			vmComponents[component.Name] = struct{}{}
		}
	}
	if len(vmHosts) != len(vmComponents) {
		return nil, e.New(e.CodeEngineCapability,
			"VM host selection has %d entries, want exactly %d selected VM components",
			len(vmHosts), len(vmComponents))
	}
	for name := range vmHosts {
		if _, ok := vmComponents[name]; !ok {
			return nil, e.New(e.CodeEngineCapability,
				"VM host selection names unknown or non-VM component %q", name)
		}
	}
	g := &ir.Graph{SchemaVersion: ir.SchemaVersion, Platform: r.Platform}
	fetchByKey := map[string]string{}
	var verifyNeeds []string // build + pull node IDs the terminal verify waits on
	components := make(map[string]catalog.NamedComponent, len(r.Components))
	for _, component := range r.Components {
		components[component.Name] = component
	}
	if err := rejectUnsupportedMaterialTransports(r.Components, components); err != nil {
		return nil, err
	}

	for _, nc := range r.Components {
		// Prebuilt (no-compile) lane: one pull node acquires a pinned, content-addressed
		// artifact — no fetch, no toolchain, no build. Its Digest is the reproducibility
		// pin (part of buildDigest); the runner verifies the acquired bytes against it.
		if nc.Prebuilt != nil {
			arch := target.Arch(r.Platform)
			ref := nc.Prebuilt.URL
			if nc.Prebuilt.OCIArchive != "" {
				ref = ir.OCIBlobScheme + nc.Prebuilt.OCIArchive
			} else if ref == "" {
				ref = strings.TrimPrefix(nc.Prebuilt.Image, "oci://")
			}
			pid := "pull-" + nc.Name
			verifyNeeds = append(verifyNeeds, pid)
			g.Nodes = append(g.Nodes, ir.Node{
				ID:           pid,
				Kind:         ir.KindPull,
				Component:    nc.Name,
				Artifact:     renderArch(ref, arch),
				Digest:       nc.Prebuilt.Digests[arch],
				Platform:     "linux/" + arch,
				Network:      ir.NetworkDefault,
				Env:          pullEnv(nc),
				OutputType:   nc.OutputType,
				ArtifactPath: prebuiltArtifactName(nc),
				Version:      catalog.NormalizeVersion(nc.Tag),
			})
			continue
		}
		if !nc.FromSource {
			continue
		}
		arch := target.Arch(r.Platform)
		// Select the same native image for vendoring and compilation. The fetch receipt
		// binds this exact image+command declaration to the resulting dependency closure.
		toolchainArch := arch
		toolchainPlatform := ""
		if nc.BuilderType == catalog.BuilderVMImage {
			host, ok := vmHosts[nc.Name]
			if !ok {
				return nil, e.New(e.CodeEngineCapability,
					"VM component %q has no selected execution-host platform", nc.Name)
			}
			if !slices.Contains(nc.BuildRequirements.AllowedHostPlatforms, host) {
				return nil, e.New(e.CodeEngineCapability,
					"VM component %q selected undeclared execution-host platform %q", nc.Name, host)
			}
			if vmRequiresKVM(nc.BuildRequirements) && string(host) != r.Platform {
				return nil, e.New(e.CodeEngineCapability,
					"VM component %q requires guest-matching KVM host platform %q", nc.Name, r.Platform)
			}
			toolchainArch = target.Arch(string(host))
			toolchainPlatform = string(host)
		}
		vendorImage := nc.ToolchainImage
		if nc.BuilderType == catalog.BuilderVMImage {
			vendorImage = nc.ToolchainImages[toolchainArch]
		} else if t, ok := nc.ToolchainImages[toolchainArch]; ok {
			vendorImage = t
		}
		if vendorImage == "" {
			return nil, e.New(e.CodeUnpinnedSource,
				"component %q has no toolchain pin for execution architecture %q", nc.Name, toolchainArch)
		}
		// Vendoring is part of the per-target source closure. Render the same
		// single architecture token admitted for build commands before deriving
		// either the vendor identity or executable IR, so the identity always
		// authenticates the exact argv the fetch node will execute.
		vendorCommand := renderArchAll(nc.VendorCommand, arch)
		vendorSpecDigest, err := ir.VendorSpecIdentity(vendorCommand, vendorImage)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "component %q vendor specification", nc.Name)
		}
		// A source pin OR vendor specification change gets a fresh checkout directory
		// and node ID. Two components may intentionally use the same repo+commit with
		// different vendoring commands/images; letting those closures collide would
		// mutate shared input state and make dependency selection order-dependent.
		sourceIdentity, err := canonical.Digest(canonical.KindSourceInput, struct {
			Commit           string `json:"commit"`
			TreeHash         string `json:"treeHash"`
			VendorSpecDigest string `json:"vendorSpecDigest"`
		}{Commit: nc.Commit, TreeHash: nc.TreeHash, VendorSpecDigest: vendorSpecDigest})
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "component %q source identity", nc.Name)
		}
		sourceSuffix := strings.TrimPrefix(sourceIdentity, "sha256:")
		if len(sourceSuffix) != 64 {
			return nil, e.New(e.CodeInternal, "component %q source identity is not a canonical sha256 digest", nc.Name)
		}
		// Retain the complete content address. Truncating it would reintroduce a
		// caller-composable checkout/receipt collision even though the sharing map
		// itself was keyed by the full digest.
		dest := "source-" + sourceSuffix
		key := sourceIdentity
		fid, ok := fetchByKey[key]
		if !ok {
			fid = "fetch-" + dest
			fetchByKey[key] = fid
			g.Nodes = append(g.Nodes, ir.Node{
				ID:               fid,
				Kind:             ir.KindFetch,
				Network:          ir.NetworkDefault,
				Repo:             nc.Repo,
				Commit:           nc.Commit,
				TreeHash:         nc.TreeHash,
				Dest:             dest,
				VendorCmd:        vendorCommand,
				VendorImage:      vendorImage,
				VendorSpecDigest: vendorSpecDigest,
			})
		}
		bid := "build-" + nc.Name
		verifyNeeds = append(verifyNeeds, bid)
		// Native per-arch toolchain (model B): the X-native toolchain image runs on an
		// X platform, so the build is native to arch (no cross-compilation). VM builds
		// bind the independently selected execution-host platform and its exact per-host
		// toolchain route into logical build identity.
		toolchain := vendorImage
		builderType := nc.BuilderType
		if builderType == "" {
			builderType = ir.BuilderOCIHermetic
		}
		var flakeRef, flakeOutput, flakeLock string
		buildNetwork := ir.NetworkNone
		if builderType == ir.BuilderBuildKit || nc.BuildNetwork == ir.NetworkDefault {
			buildNetwork = ir.NetworkDefault
		}
		if builderType == ir.BuilderNix && nc.Flake != nil {
			flakeRef, flakeOutput, flakeLock = nc.Flake.Ref, nc.Flake.Output, nc.Flake.LockDigest
			if nc.Flake.Image != "" {
				toolchain = nc.Flake.Image // the pinned nix container runs the build
			}
		}
		needs := []string{fid}
		materials := materialBindings(nc, components)
		for _, binding := range materials {
			needs = append(needs, binding.Producer)
		}
		env := buildEnv(nc, target.Arch(r.Platform))
		command := renderArchAll(nc.BuildCommand, arch)
		workdir, sourceReadOnly, writeDir := "/src", nc.WriteDir != "", nc.WriteDir
		var disk *ir.DiskSpec
		var packer *ir.PackerConfig
		if builderType == catalog.BuilderVMImage {
			env = nil
			sourceReadOnly, writeDir = false, ""
			disk = &ir.DiskSpec{Format: nc.Disk.Format, Encryption: nc.Disk.Encryption}
			packer = packerConfig(nc.Packer)
		}
		g.Nodes = append(g.Nodes, ir.Node{
			ID:                bid,
			Kind:              ir.KindBuild,
			Needs:             needs,
			Requirements:      execution.Clone(nc.BuildRequirements),
			Component:         nc.Name,
			BuilderType:       builderType,
			FlakeRef:          flakeRef,
			FlakeOutput:       flakeOutput,
			FlakeLockDigest:   flakeLock,
			Toolchain:         toolchain,
			ToolchainPlatform: toolchainPlatform,
			Platform:          "linux/" + arch,
			Network:           buildNetwork,
			Env:               env,
			Patches:           patchRefs(nc.Patches),
			Command:           command,
			Workdir:           workdir,
			SourceDest:        dest,
			SourceCommit:      nc.Commit,
			SourceTreeHash:    nc.TreeHash,
			VendorSpecDigest:  vendorSpecDigest,
			SourceReadOnly:    sourceReadOnly,
			WriteDir:          writeDir,
			Disk:              disk,
			Packer:            packer,
			Materials:         materials,
			OutputType:        nc.OutputType,
			ArtifactPath:      renderArch(nc.ArtifactPath, arch),
			Version:           catalog.NormalizeVersion(nc.Tag),
		})
	}

	sort.Strings(verifyNeeds)
	g.Nodes = append(g.Nodes, ir.Node{ID: "verify", Kind: ir.KindVerify, Needs: verifyNeeds})
	g.Normalize()
	if err := g.Validate(); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "compiled build graph is invalid")
	}
	return g, nil
}

// rejectUnsupportedMaterialTransports rejects producer transports that the material
// handoff cannot represent. A prebuilt OCI image is acquired as an OCI layout,
// while a material binding currently carries exactly one authenticated regular
// file. Source-built OCI components remain valid because their authored artifact
// path names the regular-file archive emitted by that producer.
func rejectUnsupportedMaterialTransports(
	consumers []catalog.NamedComponent,
	components map[string]catalog.NamedComponent,
) error {
	for _, consumer := range consumers {
		for _, binding := range materialBindings(consumer, components) {
			producer := components[binding.Component]
			if producer.Prebuilt != nil && producer.OutputType == "oci-image" {
				return e.New(e.CodeEngineCapability,
					"component %q material binding %q selects prebuilt OCI image %q, but acquired OCI layouts cannot be staged as one regular-file material",
					consumer.Name, binding.Binding, binding.Component)
			}
		}
	}
	return nil
}

func vmRequiresKVM(requirements *execution.Requirements) bool {
	if requirements == nil {
		return false
	}
	for _, capability := range requirements.Capabilities {
		if capability.Name == execution.CapabilityKVM && capability.Policy == execution.CapabilityRequired {
			return true
		}
	}
	return false
}

func materialBindings(component catalog.NamedComponent, components map[string]catalog.NamedComponent) []ir.MaterialBinding {
	if len(component.Materials) == 0 {
		return nil
	}
	bindings := make([]ir.MaterialBinding, 0, len(component.Materials))
	for binding, name := range component.Materials {
		bindings = append(bindings, ir.MaterialBinding{
			Binding: binding, Component: name, Producer: producerNodeID(name, components),
		})
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Binding < bindings[j].Binding })
	return bindings
}

func producerNodeID(name string, components map[string]catalog.NamedComponent) string {
	if components[name].Prebuilt != nil {
		return "pull-" + name
	}
	return "build-" + name
}

func packerConfig(config *catalog.PackerConfig) *ir.PackerConfig {
	if config == nil {
		return nil
	}
	variables := make([]ir.PackerVariable, 0, len(config.Variables))
	for name, value := range config.Variables {
		variables = append(variables, ir.PackerVariable{Name: name, Value: value})
	}
	sort.Slice(variables, func(i, j int) bool { return variables[i].Name < variables[j].Name })
	return &ir.PackerConfig{
		ProjectPath: config.ProjectPath, PackerVersion: config.PackerVersion, BuildName: config.BuildName,
		ArchitectureVariable:      config.ArchitectureVariable,
		AcceleratorVariable:       config.AcceleratorVariable,
		MaterialManifestVariable:  config.MaterialManifestVariable,
		MaterialManifest:          material.CloneProjection(config.MaterialManifest),
		OutputDirectoryVariable:   config.OutputDirectoryVariable,
		SSHPrivateKeyVariable:     config.SSHPrivateKeyVariable,
		SSHPublicKeyVariable:      config.SSHPublicKeyVariable,
		BuildLogDirectoryVariable: config.BuildLogDirectoryVariable,
		HTTPDirectoryVariable:     config.HTTPDirectoryVariable,
		Variables:                 variables,
	}
}

// pullEnv carries the deterministic packaging epoch for a prebuilt component that is
// packaged into a .deb (host-package): SOURCE_DATE_EPOCH from the version pin, so the
// assembled .deb is byte-stable. An acquired oci-image or a raw binary needs no epoch,
// so its pull node stays env-free (keeping buildDigest sensitive only to what affects
// the artifact).
func pullEnv(nc catalog.NamedComponent) []ir.EnvVar {
	if nc.OutputType != "host-package" {
		return nil
	}
	return []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: strconv.FormatInt(nc.SourceDate, 10)}}
}

// prebuiltArtifactName is the on-disk filename a downloaded binary is written to (the
// component name — the conventional binary name). An acquired oci-image has no local
// file, so it carries no artifact path.
func prebuiltArtifactName(nc catalog.NamedComponent) string {
	if nc.Prebuilt != nil && nc.Prebuilt.Image != "" {
		return ""
	}
	return nc.Name
}

// buildEnv assembles the pinned, allowlisted environment for a build node: the
// reproducibility base plus the component's deterministic version stamps
// (NKD-FR-BLD-012/023). The runc lane is the only CGO case (NKD-FR-BLD-025).
func buildEnv(nc catalog.NamedComponent, arch string) []ir.EnvVar {
	cgo := "0"
	if nc.CGO {
		cgo = "1"
	}
	// versionStamp (extra pinned env like KUBE_GIT_*/CC) goes on first; the pinned
	// reproducibility base is applied second so it is authoritative and cannot be
	// shadowed. The catalog loader also fails closed on a collision (defense in
	// depth), so this ordering only matters for non-reserved keys.
	m := map[string]string{}
	for k, v := range nc.VersionStamp {
		m[k] = v
	}
	for k, v := range map[string]string{
		"SOURCE_DATE_EPOCH": strconv.FormatInt(nc.SourceDate, 10),
		"TZ":                "UTC",
		"LC_ALL":            "C",
		"GOTOOLCHAIN":       "local",
		"GOOS":              "linux",
		"GOARCH":            arch,
		"CGO_ENABLED":       cgo,
		// The reproducibility flag discipline (NKD-FR-BLD-023, NKD-FR-REP-004,
		// NKD-NFR-005), pinned in the IR itself so it is part of buildDigest and
		// applied uniformly to every Go lane regardless of the toolchain image's own
		// ENV: -trimpath strips absolute build paths; -buildvcs=false suppresses VCS
		// stamping (revision/time/dirty), which would otherwise vary with the .git
		// state of the fetched checkout. GOFLAGS entries only apply to `go` commands
		// that accept them, so make- and gcc-based lanes ignore what they don't use.
		// The -ldflags="-buildid=" strip is carried per-command in the catalog for the
		// direct `go build` lanes (CLI -ldflags would otherwise override a GOFLAGS one).
		"GOFLAGS": "-trimpath -buildvcs=false",
	} {
		m[k] = v
	}
	// Per-arch microarchitecture baseline (deterministic; only the target arch's flag
	// is set): amd64 -> GOAMD64=v1, arm64 -> GOARM64=v8.0. Keeps the env minimal and
	// correct for each arch rather than carrying an amd64-only flag everywhere.
	switch arch {
	case "amd64":
		m["GOAMD64"] = "v1"
	case "arm64":
		m["GOARM64"] = "v8.0"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]ir.EnvVar, 0, len(keys))
	for _, k := range keys {
		env = append(env, ir.EnvVar{Name: k, Value: m[k]})
	}
	return env
}

// renderArch substitutes the {{.Arch}} token (the one per-arch literal a build
// command or artifact path needs under model B — e.g. kubernetes' KUBE_BUILD_PLATFORMS
// and its _output/.../linux/<arch>/ path) with the resolved target arch. It is a total
// literal substitution, not template evaluation: a string without the token is returned
// unchanged, and the catalog loader has already failed closed on any other {{...}} token,
// so a typo can never reach a build as a literal.
func renderArch(s, arch string) string {
	return strings.ReplaceAll(s, "{{.Arch}}", arch)
}

// renderArchAll applies renderArch to each element of a build command.
func renderArchAll(cmd []string, arch string) []string {
	if len(cmd) == 0 {
		return nil
	}
	out := make([]string, len(cmd))
	for i, s := range cmd {
		out[i] = renderArch(s, arch)
	}
	return out
}

// patchRefs converts a component's ordered patch series into IR patch refs,
// preserving order (patches apply in sequence, so order is part of the digest).
func patchRefs(ps []catalog.Patch) []ir.PatchRef {
	if len(ps) == 0 {
		return nil
	}
	out := make([]ir.PatchRef, len(ps))
	for i, p := range ps {
		out[i] = ir.PatchRef{Name: p.Name, SHA256: p.SHA256, Content: p.Content}
	}
	return out
}

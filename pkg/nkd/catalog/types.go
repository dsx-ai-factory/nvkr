// Package catalog models the declarative SourceBuildRegistry, its components, and the
// DataProvider seam through which all catalog reads flow (SRD §4.6/§4.7). The
// catalog is split like go.mod/go.sum: a hand-authored registry that declares
// sources + components, and a machine-managed lockfile that pins each source, so
// version bumps churn only the lock (ADR-22). Callers supply the catalog explicitly;
// maintained product inputs live with their owning Definition.
package catalog

import (
	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

// From-source builder types (ADR-10). Executable builders mirror ir.Builder*;
// schema-only builders fail at the build-graph compiler capability boundary.
const (
	BuilderOCIHermetic = "oci-hermetic"
	BuilderNix         = "nix"
	BuilderBuildKit    = "buildkit-rootless"
	BuilderVMImage     = "vm-image"
)

// DiskSpec describes the disk payload emitted by a component. It is required
// exactly when OutputType is disk-image.
type DiskSpec struct {
	Format     string `json:"format"`
	Encryption string `json:"encryption"`
}

// PackerConfig selects the direct-Packer producer for a vm-image component. The
// HCL project remains source-owned and is addressed relative to the pinned source
// root. The contract names the variables that future admitted lowering must use for
// architecture, an authenticated local-material manifest, and a private output
// directory. NKD does not synthesize or interpret HCL.
type PackerConfig struct {
	ProjectPath string `json:"projectPath"`
	// BuildName is the exact compiled build name future lowering passes through
	// Packer's -only option. Wildcards and build-name lists are not accepted.
	BuildName                string `json:"buildName"`
	ArchitectureVariable     string `json:"architectureVariable"`
	MaterialManifestVariable string `json:"materialManifestVariable"`
	OutputDirectoryVariable  string `json:"outputDirectoryVariable"`
	// Variables are public, literal string values passed to source-owned Packer
	// variables. They are build inputs, not a secret channel; future lowering must
	// expose them in the invocation record and let the pinned Packer executable
	// validate them against the source-owned HCL declarations.
	Variables map[string]string `json:"variables,omitempty"`
}

// Metadata is descriptive catalog identity; it does not affect resolution.
type Metadata struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// Source is a pinned upstream repository shared by one or more components. The
// registry declares it (repo, constraint, toolchain); the lockfile pins it. All
// components referencing a source bump together (NKD-FR-RC-022).
//
// The toolchain can be given two ways: an explicit digest (ToolchainImage, e.g. a
// locally-derived image with extra tools), or ToolchainBaseImage — an official
// image name (e.g. "golang") whose digest is resolved automatically from the
// source's .go-version at the pinned tag, so the toolchain tracks the Go version a
// bump requires without a hand edit (ADR-22 §2).
type Source struct {
	Kind       string      `json:"kind,omitempty"` // github-action for hosted workflow dependencies; empty for build sources
	Tool       *ToolSource `json:"tool,omitempty"`
	Repo       string      `json:"repo"`
	Constraint string      `json:"constraint,omitempty"` // semver range for `bump`, e.g. 1.32.x
	// TagPrefix is the module-scoped tag prefix a monorepo publishes its releases under
	// (e.g. "cli/" for github.com/NVIDIA/nke's cli/v0.1.0-rc.2). When set, `catalog pin`
	// resolves refs/tags/<prefix><tag>, `catalog bump` considers ONLY tags carrying the
	// prefix, and the version stamps expose the prefix-free version while Pin.Tag keeps
	// the full upstream tag (the only thing git can resolve). Empty means the repo tags
	// releases at its root, the single-module default.
	TagPrefix          string `json:"tagPrefix,omitempty"`
	ToolchainImage     string `json:"toolchainImage,omitempty"`     // explicit digest pin (arch-agnostic or single-arch)
	ToolchainBaseImage string `json:"toolchainBaseImage,omitempty"` // resolve <base>:<.go-version> digest on pin
	// ToolchainImagesByArch pins a toolchain per execution-host architecture. For
	// ordinary builders execution and target architecture are the same. A vm-image
	// builder may target a different guest architecture under its declared KVM/TCG
	// policy. When set, this map takes precedence over ToolchainImage for listed arches.
	ToolchainImagesByArch map[string]string `json:"toolchainImagesByArch,omitempty"`
	VendorCommand         []string          `json:"vendorCommand,omitempty"` // run in the fetch step (network) to vendor deps for a non-vendored repo
}

// Pin is the resolved, machine-managed pin for a source, written by `catalog pin`.
// GoVersion and ToolchainImage are populated when the source resolves its toolchain
// from .go-version.
type Pin struct {
	Tool            *ToolPin `json:"tool,omitempty"`
	Tag             string   `json:"tag"`
	Commit          string   `json:"commit"`
	TreeHash        string   `json:"treeHash"`
	SourceDateEpoch int64    `json:"sourceDateEpoch"`
	GoVersion       string   `json:"goVersion,omitempty"`
	ToolchainImage  string   `json:"toolchainImage,omitempty"`
}

// ArtifactPin is the machine-managed digest pin for a prebuilt component in the
// no-compile lane, written by `catalog pin-artifacts`: the content digest of the
// acquired artifact, per target arch. Tag records the version the digests correspond
// to, so a stale pin (digests from an older version) is visible in the lock diff. The
// registry declares the acquisition URL/image template; the lock pins WHAT it must
// hash to — the same sources+lock split the from-source lane uses (ADR-22 §1).
type ArtifactPin struct {
	Tag     string            `json:"tag"`
	Digests map[string]string `json:"digests"` // arch -> sha256:<64-hex>
}

// PinLock is the machine-generated lockfile: source name -> resolved pin, plus
// component name -> resolved artifact digests for the prebuilt lane.
type PinLock struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Pins       map[string]Pin         `json:"pins"`
	Artifacts  map[string]ArtifactPin `json:"artifacts,omitempty"`

	// revision is the exact-byte snapshot observed by LoadLock. It is deliberately
	// unexported: callers may edit semantic pins, but cannot forge the compare-and-swap
	// precondition used by WriteLock.
	revision lockRevision
}

// Patch is one ordered, content-addressed patch applied before build (NKD-FR-RC-022).
// Name is a provider-relative path. Content is populated by Load only after the
// provider bytes have matched SHA256; authored registries cannot provide it inline.
// Carrying the verified bytes in the resolved catalog makes the compiled IR a
// transport-complete execution document rather than a dangling filename reference.
type Patch struct {
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// FlakePin pins a nix builder's locked flake (ADR-10 §2): a flake reference, a fully
// pinned lock digest over all transitive inputs, an output attribute, and the pinned
// nix container image. All are build-semantic inputs (part of buildDigest); the
// lock closure is pre-realized in the fetch step so the build runs offline (§7).
type FlakePin struct {
	Ref        string `json:"ref"`             // flake reference, e.g. github:owner/repo?rev=<sha>
	LockDigest string `json:"lockDigest"`      // sha256 of the pinned flake.lock closure
	Output     string `json:"output"`          // output attribute, e.g. packages.x86_64-linux.default
	Image      string `json:"image,omitempty"` // pinned nix container image (@sha256:), else falls back to the toolchain
}

// Prebuilt declares a component acquired in the no-compile lane (NKD-FR-BLD-050):
// nkdctl downloads a pinned, content-addressed artifact instead of building it from
// source. Exactly one of URL (an https binary/tarball) or Image (an OCI image name,
// pulled by digest) is set. URL/Image are Go text/templates rendered from the
// version pin at load ({{.Tag}} {{.Version}} {{.Major}} {{.Minor}} {{.Commit}}); the
// {{.Arch}} token is resolved per target arch at compile, mirroring the from-source
// lane. Digests pins the expected sha256 per arch on the resolved shape. The authored
// registry must leave it empty: pins.lock.yaml's artifacts section is the single
// authoritative source, avoiding duplicate semantic entries. A component is prebuilt XOR
// from-source XOR an exclusion — declaring both fromSource and prebuilt fails closed.
type Prebuilt struct {
	URL     string            `json:"url,omitempty"`
	Image   string            `json:"image,omitempty"`
	Digests map[string]string `json:"digests,omitempty"` // arch -> sha256:<64-hex>
	// Evidence is the upstream's external supply-chain reference (ADR-13 §2). A prebuilt
	// component MUST declare it explicitly — either a verified reference (whose bytes
	// hash to the declared digest, else resolution fails closed) or an explicit
	// `none: true` — so acquired evidence is never silently absent. Distinct from a
	// Tier-4 exclusion (fromSource:false + reason).
	Evidence *ExternalEvidence `json:"evidence,omitempty"`
}

// ExternalEvidence references the upstream's external SBOM/provenance for a prebuilt
// (no-compile) artifact (ADR-13 §2/§9). Each Ref, when present, must carry a pinned
// Digest the referenced bytes must hash to. None=true is the honest "no upstream
// evidence exists" marker; it is mutually exclusive with any Ref.
type ExternalEvidence struct {
	None             bool   `json:"none,omitempty"`
	SBOMRef          string `json:"sbomRef,omitempty"`
	SBOMDigest       string `json:"sbomDigest,omitempty"`
	ProvenanceRef    string `json:"provenanceRef,omitempty"`
	ProvenanceDigest string `json:"provenanceDigest,omitempty"`
}

// Component is a fully-resolved SourceBuildRegistry entry: either a from-source
// buildable component (source + pin merged in) or an explicit Tier-4 exclusion
// (NKD-FR-RC-026). It is the concrete shape every downstream stage consumes; the
// loader produces it from the raw registry entry + its source + the lock.
type Component struct {
	// Platforms is the sorted, nonempty allowlist of supported output targets.
	Platforms  []string `json:"platforms"`
	FromSource bool     `json:"fromSource"`

	// BuilderType selects the from-source build substrate (ADR-10). It is a
	// build-semantic input and flows into buildDigest.
	BuilderType string `json:"builderType,omitempty"`
	// BuildRequirements declares the generic runner contract for this component's
	// build step. It is operational metadata, not builder-specific behavior or proof
	// that a selected runner satisfies the request.
	BuildRequirements *execution.Requirements `json:"buildRequirements,omitempty"`
	// Flake pins a nix builder's locked flake (set only when BuilderType==nix).
	Flake *FlakePin `json:"flake,omitempty"`

	// from-source pins (populated by the loader from the source + lock)
	Repo           string  `json:"repo,omitempty"`
	Tag            string  `json:"tag,omitempty"`
	Commit         string  `json:"commit,omitempty"`   // authoritative full SHA
	TreeHash       string  `json:"treeHash,omitempty"` // expected source tree hash
	Patches        []Patch `json:"patches,omitempty"`
	ToolchainImage string  `json:"toolchainImage,omitempty"` // must be @sha256: digest
	// ToolchainImages is the resolved per-execution-arch toolchain set. Ordinary
	// compilation selects the target arch. VM compilation requires and selects one
	// exact eligible host-platform entry independently from the guest/output arch.
	ToolchainImages map[string]string `json:"toolchainImages,omitempty"`
	CGO             bool              `json:"cgo,omitempty"`
	OutputType      string            `json:"outputType,omitempty"`
	Disk            *DiskSpec         `json:"disk,omitempty"`
	// Packer selects direct Packer execution for vm-image. When nil, the existing
	// BuildCommand route remains authoritative. The two forms are mutually exclusive.
	Packer       *PackerConfig `json:"packer,omitempty"`
	BuildCommand []string      `json:"buildCommand,omitempty"`
	// Materials binds source-owned input names to other component outputs in the
	// same output's selected producer set. Each component may be bound exactly once.
	// VM lowering turns these declarations into build-graph edges and stages
	// only the verified outputs named here.
	Materials map[string]string `json:"materials,omitempty"`
	// For direct Packer, ArtifactPath is relative to the private output directory.
	// Other builders retain their existing workspace-relative interpretation.
	ArtifactPath  string            `json:"artifactPath,omitempty"`
	WriteDir      string            `json:"writeDir,omitempty"`        // writable output subdir; enables parallel-safe isolation
	VendorCommand []string          `json:"vendorCommand,omitempty"`   // from the source: vendor step for non-vendored repos
	SourceDate    int64             `json:"sourceDateEpoch,omitempty"` // committer %ct of Commit
	VersionStamp  map[string]string `json:"versionStamp,omitempty"`    // rendered -X-style stamps

	// prebuilt (the no-compile lane): set when this component is acquired, not built.
	// The URL/Image templates are already version-rendered (only {{.Arch}} may remain);
	// Digests is copied from the lock-pinned per-arch artifact entry.
	Prebuilt *Prebuilt `json:"prebuilt,omitempty"`

	// exclusion (required when neither FromSource nor Prebuilt)
	ExclusionReason string `json:"exclusionReason,omitempty"`
}

// AICRProfile pins the exact upstream profile and generator inputs for an
// explicitly selected AICR output. It is independent of ordinary components.
type AICRProfile = aicr.Profile

// Catalog is the loaded, fully resolved registry and lock. Producer selection is
// explicit in each Definition output; the catalog contains no authored groups.
type Catalog struct {
	Actions      map[string]ActionPin
	Tools        map[string]ToolPin
	APIVersion   string
	Kind         string
	Metadata     Metadata
	Sources      map[string]Source
	Components   map[string]Component
	AICRProfiles map[string]aicr.Profile
}

// NamedComponent pairs a component with its registry key, for ordered iteration
// after resolution.
type NamedComponent struct {
	Name string
	Component
}

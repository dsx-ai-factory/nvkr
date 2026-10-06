// Package ir defines nkdctl's engine-neutral pipeline IR: the fully-pinned build
// DAG that is the single source of truth for emission, build, and verification
// (SRD §4.5, §5.1). The full IR may carry compiler-selected Environment metadata,
// but contains no runtime-observed host, clock, or random values. Build identity
// is derived from the deterministic content view defined in marshal.go.
package ir

import (
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/material"
)

// SchemaVersion identifies the IR shape; it participates in the digest so a schema
// change is a pin change.
const SchemaVersion = "nkd.dev/ir/v4"

// Node kinds.
const (
	KindFetch  = "fetch"  // acquire pinned source (network-permitted)
	KindBuild  = "build"  // hermetic from-source compile in a pinned toolchain container
	KindPull   = "pull"   // acquire a pinned prebuilt artifact by content digest (network-permitted; the no-compile lane)
	KindVerify = "verify" // unconditional terminal node: re-hash each artifact against its recorded digest
)

// Network modes for a node.
const (
	NetworkNone    = "none"    // hermetic build sandbox (NKD-FR-BLD-011)
	NetworkDefault = "default" // explicitly admitted acquisition or wrapped-build networking
)

// Builder types (ADR-10) refine the from-source lane. The builder type is a
// build-semantic input carried in the IR, so it is part of buildDigest: switching
// a component's builder is a NEW build identity, never a variant (ADR-10 §4).
const (
	BuilderOCIHermetic = "oci-hermetic"      // hermetic compile in a pinned toolchain container (the default)
	BuilderNix         = "nix"               // produced from a locked flake in a pinned nix container
	BuilderBuildKit    = "buildkit-rootless" // networked wrapped Dockerfile build; not qualified execution
	BuilderVMImage     = "vm-image"          // source-owned command or Packer project producing one disk image
)

// DiskSpec is the exact disk payload contract carried by a VM build node.
type DiskSpec struct {
	Format     string `json:"format"`
	Encryption string `json:"encryption"`
}

// PackerVariable is one public literal passed to a source-owned Packer project.
// Variables are sorted by Name and are ordinary logical build inputs, never a
// credential channel.
type PackerVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PackerConfig is the typed direct-Packer producer declaration. Runtime paths
// are deliberately absent; the admitted executor derives those from held source,
// material, toolchain, and workspace capabilities.
type PackerConfig struct {
	ProjectPath               string               `json:"projectPath"`
	PackerVersion             string               `json:"packerVersion"`
	BuildName                 string               `json:"buildName"`
	ArchitectureVariable      string               `json:"architectureVariable"`
	AcceleratorVariable       string               `json:"acceleratorVariable"`
	MaterialManifestVariable  string               `json:"materialManifestVariable"`
	MaterialManifest          *material.Projection `json:"materialManifest,omitempty"`
	OutputDirectoryVariable   string               `json:"outputDirectoryVariable"`
	SSHPrivateKeyVariable     string               `json:"sshPrivateKeyVariable,omitempty"`
	SSHPublicKeyVariable      string               `json:"sshPublicKeyVariable,omitempty"`
	BuildLogDirectoryVariable string               `json:"buildLogDirectoryVariable,omitempty"`
	HTTPDirectoryVariable     string               `json:"httpDirectoryVariable,omitempty"`
	AcquisitionArchive        string               `json:"acquisitionArchive,omitempty"`
	Variables                 []PackerVariable     `json:"variables,omitempty"`
}

// MaterialBinding ties one source-owned input name to one exact producer node.
// Producer is redundant with the graph edge by design: Graph.Validate checks
// both representations so a caller cannot relabel a dependency without changing
// the producer it actually waits for.
type MaterialBinding struct {
	Binding   string `json:"binding"`
	Component string `json:"component"`
	Producer  string `json:"producer"`
}

// Engine classes (ADR-07 §1) are the OBSERVED build substrate a component was
// actually built under — recorded in the detached build record and verified equal to
// the builder the producer declared. Because it is an observation, it never enters the
// build identity.
const (
	EngineOCIHermetic     = "oci-hermetic"
	EngineNix             = "nix"
	EnginePrebuiltAcquire = "prebuilt-acquire" // the no-compile lane
)

// EnvVar is one allowlisted, pinned environment variable (NKD-FR-BLD-012).
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PatchRef is one content-addressed patch applied to the source before build.
// Order is significant (patches apply in sequence), so it is never sorted; it is
// part of the digest so an altered patch set changes buildDigest (NKD-FR-RC-022).
type PatchRef struct {
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// ExecutableArchive binds the release transport separately from the selected
// executable's Node.Digest. Neither digest can substitute for the other.
type ExecutableArchive struct {
	Digest string `json:"digest"`
	Member string `json:"member"`
}

// Node is one vertex of the build DAG. Field presence depends on Kind; unused
// fields are omitted from serialization so the digest stays tight.
type Node struct {
	ImageParentDigest string   `json:"imageParentDigest,omitempty"`
	ID                string   `json:"id"`
	Kind              string   `json:"kind"`
	Needs             []string `json:"needs,omitempty"` // upstream node IDs (sorted)
	// Requirements is the deterministic runner request for this step. Build nodes are
	// the currently admitted kind; other kinds fail closed until their compiler
	// projection exists. It travels with executable bytes but is excluded from
	// buildDigest because it does not describe the content produced by the graph.
	Requirements *execution.Requirements `json:"requirements,omitempty"`
	// ExecutionBinding is the exact Environment execution class selected by the
	// factory compiler. It is executable Plan metadata and is excluded from Build
	// identity. An unbound graph remains valid before Environment compilation.
	ExecutionBinding *execution.Binding `json:"executionBinding,omitempty"`

	// fetch
	Repo        string   `json:"repo,omitempty"`
	Commit      string   `json:"commit,omitempty"`      // full 40-hex SHA (authoritative pin)
	TreeHash    string   `json:"treeHash,omitempty"`    // expected source tree hash
	Dest        string   `json:"dest,omitempty"`        // checkout dir, relative to the source root
	VendorCmd   []string `json:"vendorCmd,omitempty"`   // optional: vendor deps here (network) so the build stays hermetic
	VendorImage string   `json:"vendorImage,omitempty"` // toolchain image the vendor step runs in

	// pull (the prebuilt-acquire lane — NKD-FR-BLD-050): download a pinned artifact
	// instead of building it. The reproducibility pin moves from "same inputs rebuild
	// the same bytes" to "these exact content-addressed bytes": Digest is the expected
	// sha256 of the acquired artifact and is part of buildDigest (a repin is a build identity
	// change, exactly as a source-commit bump is). Fail-closed: acquisition verifies the
	// bytes against Digest and refuses a mismatch — no from-source guarantee is claimed.
	Artifact          string             `json:"artifact,omitempty"` // HTTPS file URL, OCI image name, or oci-blob://repository@digest; transport only
	Digest            string             `json:"digest,omitempty"`   // pinned expected content digest, sha256:<64-hex>
	ExecutableArchive *ExecutableArchive `json:"executableArchive,omitempty"`

	// build
	Component string `json:"component,omitempty"`
	// BuilderType selects the from-source build substrate (oci-hermetic | nix,
	// ADR-10). It is a build-semantic input, so it participates in buildDigest; the
	// runner dispatches on it and records the observed engineClass for verify.
	BuilderType string `json:"builderType,omitempty"`
	// FlakeRef / FlakeOutput / FlakeLockDigest pin a nix build's locked flake, output
	// attribute, and transitive-closure lock digest (ADR-10 §2); empty for an
	// oci-hermetic build. FlakeRef is transport (not in buildDigest); FlakeLockDigest
	// and FlakeOutput are the nix content identity. The closure is pre-realized in the
	// fetch step and the build runs offline against it.
	FlakeRef        string `json:"flakeRef,omitempty"`
	FlakeOutput     string `json:"flakeOutput,omitempty"`
	FlakeLockDigest string `json:"flakeLockDigest,omitempty"`
	Toolchain       string `json:"toolchain,omitempty"` // OCI image pinned by @sha256: digest
	// ToolchainPlatform is the identity-bearing host platform used to select a
	// VM toolchain. It is distinct from Platform, which is the guest/output.
	ToolchainPlatform string `json:"toolchainPlatform,omitempty"`
	// Platform is the target run platform (linux/<arch>). It forces the container to
	// run the toolchain image's NATIVE arch variant (of a multi-arch toolchain) — or,
	// on a foreign host, under emulation — so GOARCH compilation is native, never a
	// cross-compile (model B). Derived from the selected target platform, so it is part of the
	// per-arch buildDigest (NKD-NFR-093).
	Platform   string     `json:"platform,omitempty"`
	Network    string     `json:"network,omitempty"`
	Env        []EnvVar   `json:"env,omitempty"`     // sorted by Name
	Patches    []PatchRef `json:"patches,omitempty"` // ordered; applied before build (not sorted)
	Command    []string   `json:"command,omitempty"`
	Workdir    string     `json:"workdir,omitempty"`    // working dir inside the container
	SourceDest string     `json:"sourceDest,omitempty"` // source dir to mount (relative to source root)
	// SourceCommit/SourceTreeHash/VendorSpecDigest let an independently executed
	// build node authenticate the fetch receipt it consumes. They duplicate the
	// upstream fetch node's pins deliberately: exec-node is a self-contained module
	// boundary and must not trust whichever bytes happen to occupy SourceDest.
	SourceCommit     string `json:"sourceCommit,omitempty"`
	SourceTreeHash   string `json:"sourceTreeHash,omitempty"`
	VendorSpecDigest string `json:"vendorSpecDigest,omitempty"`
	// Isolation contract that makes independent build nodes safe to run in
	// parallel: the shared source is mounted read-only, and each build gets its
	// own fresh writable WriteDir (a subdir of Workdir, e.g. _output) so concurrent
	// builds never clobber each other's outputs (NKD-FR-BLD-011 hermeticity).
	SourceReadOnly bool   `json:"sourceReadOnly,omitempty"`
	WriteDir       string `json:"writeDir,omitempty"`
	// Disk and Packer are present only for a vm-image build. Command selects the
	// alternative exact-argv VM producer. Materials are sorted by Binding and each
	// entry is also represented by an exact producer dependency in Needs.
	Disk         *DiskSpec         `json:"disk,omitempty"`
	Packer       *PackerConfig     `json:"packer,omitempty"`
	Materials    []MaterialBinding `json:"materials,omitempty"`
	OutputType   string            `json:"outputType,omitempty"`   // host-package | oci-image | ...
	ArtifactPath string            `json:"artifactPath,omitempty"` // built artifact, relative to Workdir
	Version      string            `json:"version,omitempty"`      // component version (from the pin tag), for packaging
}

// Graph is the whole resolved pipeline IR.
type Graph struct {
	SchemaVersion string `json:"schemaVersion"`
	Platform      string `json:"platform"`
	Nodes         []Node `json:"nodes"` // sorted by ID
}

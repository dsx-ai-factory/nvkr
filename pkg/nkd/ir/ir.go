// Package ir defines nkdctl's engine-neutral pipeline IR: the fully-pinned build
// DAG that is the single source of truth for emission, build, and verification
// (SRD §4.5, §5.1). The IR contains no host/clock/random values, so its
// deterministic serialization yields a stable recipeDigest.
package ir

import (
	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

// SchemaVersion identifies the IR shape; it participates in the digest so a schema
// change is a pin change.
const SchemaVersion = "nkd.dev/ir/v1"

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
	NetworkDefault = "default" // permitted only for explicit fetch/vendoring steps
)

// Builder types (ADR-10) refine the from-source lane. The builder type is a
// build-semantic input carried in the IR, so it is part of recipeDigest: switching
// a component's builder is a NEW recipe, never a variant (ADR-10 §4).
const (
	BuilderOCIHermetic = "oci-hermetic" // hermetic compile in a pinned toolchain container (the default)
	BuilderNix         = "nix"          // produced from a locked flake in a pinned nix container
)

// Engine classes (ADR-07 §1) are the OBSERVED build substrate a component was
// actually built under — recorded in the detached build record and verified equal to
// the builder the recipe declared. Because it is an observation, it never enters the
// recipe identity.
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
// part of the digest so an altered patch set changes recipeDigest (NKD-FR-RC-022).
type PatchRef struct {
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// AICRActivation is the output-affecting accelerated-runtime selection bound into
// recipe identity. These are the exact inputs passed to `aicr recipe`, plus the
// digest of the canonical AICR recipe they must reproduce and the digest of the
// exact generator executable. Human profile identity and repository location are
// deliberately absent: they are labels/transport, not content identity.
type AICRActivation struct {
	Service         string `json:"service"`
	Accelerator     string `json:"accelerator"`
	OS              string `json:"os"`
	Intent          string `json:"intent"`
	Platform        string `json:"platform,omitempty"`
	RecipeDigest    string `json:"recipeDigest"`
	GeneratorDigest string `json:"generatorDigest"`
}

// Validate prevents a caller-composed IR from gaining a recipe identity while
// omitting either AICR authority pin or carrying unsupported generator inputs.
func (a AICRActivation) Validate() error {
	if !canonical.IsDigest(a.RecipeDigest) {
		return e.New(e.CodeUnpinnedSource,
			"AICR activation recipe digest %q is not a lowercase sha256:<64-hex> pin", a.RecipeDigest)
	}
	if !canonical.IsDigest(a.GeneratorDigest) {
		return e.New(e.CodeUnpinnedSource,
			"AICR activation generator digest %q is not a lowercase sha256:<64-hex> pin", a.GeneratorDigest)
	}
	if err := criteria.ValidateAICRValues(a.Service, a.Accelerator, a.OS, a.Intent, a.Platform); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "AICR activation")
	}
	return nil
}

// Node is one vertex of the build DAG. Field presence depends on Kind; unused
// fields are omitted from serialization so the digest stays tight.
type Node struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Needs []string `json:"needs,omitempty"` // upstream node IDs (sorted)

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
	// sha256 of the acquired artifact and is part of recipeDigest (a repin is a recipe
	// change, exactly as a source-commit bump is). Fail-closed: acquisition verifies the
	// bytes against Digest and refuses a mismatch — no from-source guarantee is claimed.
	Artifact string `json:"artifact,omitempty"` // https URL (binary) or OCI image name (pulled by @Digest); {{.Arch}} already resolved
	Digest   string `json:"digest,omitempty"`   // pinned expected content digest, sha256:<64-hex>

	// build
	Component string `json:"component,omitempty"`
	// BuilderType selects the from-source build substrate (oci-hermetic | nix,
	// ADR-10). It is a build-semantic input, so it participates in recipeDigest; the
	// runner dispatches on it and records the observed engineClass for verify.
	BuilderType string `json:"builderType,omitempty"`
	// FlakeRef / FlakeOutput / FlakeLockDigest pin a nix build's locked flake, output
	// attribute, and transitive-closure lock digest (ADR-10 §2); empty for an
	// oci-hermetic build. FlakeRef is transport (not in recipeDigest); FlakeLockDigest
	// and FlakeOutput are the nix content identity. The closure is pre-realized in the
	// fetch step and the build runs offline against it.
	FlakeRef        string `json:"flakeRef,omitempty"`
	FlakeOutput     string `json:"flakeOutput,omitempty"`
	FlakeLockDigest string `json:"flakeLockDigest,omitempty"`
	Toolchain       string `json:"toolchain,omitempty"` // OCI image pinned by @sha256: digest
	// Platform is the target run platform (linux/<arch>). It forces the container to
	// run the toolchain image's NATIVE arch variant (of a multi-arch toolchain) — or,
	// on a foreign host, under emulation — so GOARCH compilation is native, never a
	// cross-compile (model B). Derived from criteria.Arch, so it is part of the
	// per-arch recipeDigest (NKD-NFR-093).
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
	OutputType     string `json:"outputType,omitempty"`   // host-package | oci-image | ...
	ArtifactPath   string `json:"artifactPath,omitempty"` // built artifact, relative to Workdir
	Version        string `json:"version,omitempty"`      // component version (from the pin tag), for packaging
}

// Graph is the whole resolved pipeline IR.
type Graph struct {
	SchemaVersion string            `json:"schemaVersion"`
	RecipeID      string            `json:"recipeId"`
	Criteria      criteria.Criteria `json:"criteria"`
	// AICR is present only for an activated accelerated-runtime recipe. It remains in
	// LogicalGraph because changing any generator input or its pinned canonical output
	// changes what is rendered and must therefore change recipeDigest.
	AICR  *AICRActivation `json:"aicr,omitempty"`
	Nodes []Node          `json:"nodes"` // sorted by ID
}

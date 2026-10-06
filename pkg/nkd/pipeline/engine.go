// Package pipeline executes exact producer instructions and observes their results.
// Local.Run executes a build graph; ExecNode executes one referenced instruction.
// This package owns source snapshots, process lifetimes, and terminal artifact
// receipts. Whole-Run scheduling and hosted generation belong to factory.
package pipeline

import (
	"context"
	"io"
	"os"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Runner executes the IR in the operator's own environment. Context is carried in
// RunOptions; implementations MUST reject a nil RunOptions.Ctx at admission, before
// validation or I/O.
type Runner interface {
	Name() string
	Run(g *ir.Graph, buildDigest string, opts RunOptions) (*Report, error)
}

// RunOptions parameterize a local run.
type RunOptions struct {
	// Ctx carries cancellation into every subprocess (ADR-17 §2). Subprocesses get
	// bounded SIGTERM→SIGKILL reaping, and the cleanup registry makes a separately
	// bounded force-removal attempt for every possibly-live container. It is required
	// by the public Run, VerifyResults, and ExecNode entry points; nil fails admission.
	Ctx        context.Context
	reap       *reaper // cleanup registry (set by Run; force-removes started containers on cancel)
	SourceRoot string  // where fetch nodes check out sources (default .nkd/work/src)
	// SourceWorkspace grants descriptor-relative access for a source-only fetch.
	// HoldSourceWorkspace issues the handle; Close cancels and joins its fetches.
	// It confers no build authority and exposes no directory descriptor.
	// SourceRoot must be relative to this directory. Held Git execution is Linux-only.
	SourceWorkspace *SourceWorkspace
	// SourceGitEnvironment describes the factory-admitted source-only child
	// environment. It is used only with SourceWorkspace; nil selects the fixed
	// public-source baseline, never the ambient process environment.
	SourceGitEnvironment []string
	Only                 []string // if set, build only these component names
	Jobs                 int      // max concurrent local work slots across fetch/source/build nodes
	DryRun               bool     // print the docker invocations instead of executing
	// Launch selects ADR-18 §8 for a single exec-node: instead of running the
	// build DIRECTLY (assuming the engine placed the job inside the toolchain container),
	// the helper ORCHESTRATES the pinned toolchain container itself — the same code path as
	// Local.Run — so a generic CI runner needs only a container runtime plus this helper.
	Launch     bool
	KeepGoing  bool         // continue after a failed build node instead of failing closed
	Package    bool         // assemble each artifact into its outputType (oci-image/.deb) and record that digest
	PackageDir string       // where packaged artifacts are written (default .nkd/work/packages)
	Runtime    string       // container runtime binary (default "docker"; e.g. docker | podman)
	OCI        OCITransport // daemonless OCI registry/layout adapter (default: anonymous registry client)
	Stdout     io.Writer    // build log sink
	Stderr     io.Writer
	progress   *progressSink // concurrency-safe live lifecycle output; node logs remain buffered
	// sources is a run-owned single-flight cache for exact prepared source inputs.
	// It is intentionally private: authority and lifetime cannot cross a Run call.
	sources *preparedSourceCache
	// fetchDirectory is the current fetch owner's held checkout, not a caller path.
	fetchDirectory        *os.File
	fetchGitDirectory     *os.File
	fetchReceiptDirectory *os.File
	fetchWorkspace        *os.File
	// BuildMaterials is a process-local capability over the exact manifest and
	// producer bytes selected by the factory Plan. It is required for a build node
	// with material bindings and rejected by every other node.
	BuildMaterials *BuildMaterials
	// Qualification is required for a from-source run whose report may back a signed
	// validation vector or a Reproduced/ValidatedVariant claim. Nil permits an
	// unqualified from-source development run or a profile-free, terminally verified
	// prebuilt acquisition; the latter still yields authority and requires Ctx.
	Qualification *QualifiedExecution
	execution     *verifiedExecution
}

func requireRunContext(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a non-nil context", operation)
	}
	return nil
}

// ArtifactQualification is an explicit producer-side authority label. It is not
// itself trusted: terminal verification still rechecks the opaque execution receipt
// and artifact bytes. Its purpose is to ensure an exec-node result that could not
// observe its provider-selected execution never looks accidentally authoritative.
type ArtifactQualification string

const (
	ArtifactUnqualified            ArtifactQualification = "unqualified"
	ArtifactQualifiedExecution     ArtifactQualification = "qualified-execution"
	ArtifactProfileFreeAcquisition ArtifactQualification = "profile-free-acquisition"
)

// ArtifactResult records one built artifact and its content digest — the seed for
// the expected-digests manifest (SRD §6.3).
type ArtifactResult struct {
	Component     string                `json:"component"`
	OutputType    string                `json:"outputType"`
	Qualification ArtifactQualification `json:"qualification"`
	Toolchain     string                `json:"toolchain,omitempty"` // digest-pinned toolchain image that produced it (provenance)
	// InputDigest identifies the exact post-vendor, post-patch source snapshot that
	// entered this build. It is observed by the runner, not copied from a declaration.
	InputDigest string `json:"inputDigest,omitempty"`
	// MaterialManifestDigest identifies the exact canonical manifest staged for
	// this producer. It is present iff the producer consumed material bindings.
	MaterialManifestDigest string `json:"materialManifestDigest,omitempty"`
	// BuilderType is the DECLARED from-source substrate (from the IR node). EngineClass
	// is the OBSERVED substrate the runner actually used. Verify fails closed if they
	// diverge — a producer that declares nix but whose artifact came off a native lane is
	// a different build, not a sanctioned variant (ADR-07 §1).
	BuilderType string `json:"builderType,omitempty"`
	EngineClass string `json:"engineClass,omitempty"`
	// ProfileDigest and ExecutionBinding are present only when this producer ran
	// after a valid, exact-node stage-two receipt. Terminal CI verification rejects
	// from-source results that omit or change either binding.
	ProfileDigest    string `json:"profileDigest,omitempty"`
	ExecutionBinding string `json:"executionBinding,omitempty"`
	Path             string `json:"path"`
	SHA256           string `json:"sha256"`
	Size             int64  `json:"size"`
	Packaged         bool   `json:"packaged,omitempty"`
	// Ref is set for a pulled, content-addressed OCI image (the no-compile lane):
	// "name@sha256:<digest>". Such an image has no single local file, so verify
	// re-confirms it is present in the local store by this digest (offline) rather than
	// re-hashing a file. Empty for from-source and binary/.deb artifacts.
	Ref string `json:"ref,omitempty"`
	// TransportPath is a non-identity carrier used between isolated CI jobs (for
	// example a docker-save archive for a digest-pinned OCI image). Verification
	// imports it offline and then re-checks Ref by manifest digest.
	TransportPath string `json:"transportPath,omitempty"`
	// AbsPath is the host file whose sha256 equals SHA256 (the binary, the .deb, or
	// the OCI manifest blob), used by verify to re-check. Not serialized. Empty for a
	// pulled image (use Ref instead).
	AbsPath string `json:"-"`
}

// NodeResult records the outcome of one DAG node.
type NodeResult struct {
	ID       string        `json:"id"`
	Kind     string        `json:"kind"`
	Status   string        `json:"status"` // ok | failed | skipped | dry-run
	Duration time.Duration `json:"duration"`
	Detail   string        `json:"detail,omitempty"`
}

// Report is the result of a local run.
type Report struct {
	BuildDigest   string           `json:"buildDigest"`
	Nodes         []NodeResult     `json:"nodes"`
	Artifacts     []ArtifactResult `json:"artifacts"`
	qualification reportQualification
}

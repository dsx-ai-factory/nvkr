package catalog

import (
	"regexp"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

var (
	fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// A toolchain must be pinned by an immutable digest, never a tag. Two digest
	// forms are accepted: a registry repo digest (name@sha256:...) and a bare
	// local image ID (sha256:...), the latter being a content-addressed pin for a
	// locally-built toolchain used by the local engine. A tag (e.g. golang:1.23.3)
	// matches neither and fails closed (NKD-FR-RC-024, NKD-FR-BLD-010).
	digestPinned   = regexp.MustCompile(`(?:@|^)sha256:[0-9a-f]{64}$`)
	knownOutputSet = map[string]bool{
		"host-package": true, "oci-image": true, "disk-image": true, "raw": true, "tarball": true,
	}
	// prebuiltOutputSet constrains a prebuilt component's delivery form: URL bytes
	// become a host-package, raw file, or tarball; an Image is itself the oci-image.
	prebuiltOutputSet = map[string]bool{"host-package": true, "oci-image": true, "raw": true, "tarball": true}
	// reproEnvKeys are the reproducibility/hermeticity variables the compiler owns
	// and pins from target/component metadata. A catalog's versionStamp may add
	// extra pinned env (KUBE_GIT_*, CC), but must not resolve to a value that
	// overrides these — that would silently break hermeticity, so it fails closed.
	reproEnvKeys = map[string]bool{
		"SOURCE_DATE_EPOCH": true, "TZ": true, "LC_ALL": true, "GOTOOLCHAIN": true,
		"GOOS": true, "GOARCH": true, "GOAMD64": true, "GOARM64": true, "CGO_ENABLED": true, "GOFLAGS": true,
	}
)

const (
	catalogAPIVersion = "nkd.dev/catalog/v3"
	catalogKind       = "SourceBuildRegistry"
)

// registryDoc is the hand-authored registry as parsed from disk, before sources
// and lock pins are merged in.
type registryDoc struct {
	APIVersion string                  `json:"apiVersion"`
	Kind       string                  `json:"kind"`
	Metadata   Metadata                `json:"metadata"`
	Sources    map[string]Source       `json:"sources"`
	Components map[string]rawComponent `json:"components"`
}

// rawComponent is a component as authored: it references a source by name and
// carries version-stamp templates, not resolved pins.
type rawComponent struct {
	Helm            *HelmPackaging     `json:"helm,omitempty"`
	PinnedBy        string             `json:"pinnedBy,omitempty"`
	Platforms       []string           `json:"platforms"`
	FromSource      bool               `json:"fromSource"`
	Source          string             `json:"source,omitempty"`
	BuilderType     string             `json:"builderType,omitempty"`    // oci-hermetic (default) | nix | buildkit-rootless | vm-image (ADR-10)
	BuildNetwork    string             `json:"buildNetwork,omitempty"`   // none (default) | default; default is admitted separately by Environment
	ToolchainImage  string             `json:"toolchainImage,omitempty"` // component-specific exact builder image
	Flake           *FlakePin          `json:"flake,omitempty"`          // required iff builderType==nix
	CGO             bool               `json:"cgo,omitempty"`
	OutputType      string             `json:"outputType,omitempty"`
	Disk            *DiskSpec          `json:"disk,omitempty"`
	Packer          *PackerConfig      `json:"packer,omitempty"`
	BuildCommand    []string           `json:"buildCommand,omitempty"`
	Materials       map[string]string  `json:"materials,omitempty"`
	ArtifactPath    string             `json:"artifactPath,omitempty"`
	WriteDir        string             `json:"writeDir,omitempty"`
	VersionStamp    map[string]string  `json:"versionStamp,omitempty"` // Go text/template values
	Patches         []patchDeclaration `json:"patches,omitempty"`
	Prebuilt        *Prebuilt          `json:"prebuilt,omitempty"` // the no-compile lane
	ExclusionReason string             `json:"exclusionReason,omitempty"`

	BuildRequirements *execution.Requirements `json:"buildRequirements,omitempty"` // generic build-step runner request
}

// patchDeclaration is the authored patch shape. It intentionally has no content
// field: patch bytes must come through the catalog DataProvider and match the pin.
type patchDeclaration struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

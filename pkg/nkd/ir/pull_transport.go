package ir

import (
	"net/url"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// OCIBlobScheme distinguishes exact archive bytes from an OCI image closure.
const OCIBlobScheme = "oci-blob://"

// ParseOCIBlobArtifact admits a fully qualified, tag-free blob transport whose
// digest agrees with the separate logical content pin. It performs no I/O.
func ParseOCIBlobArtifact(artifact, digest string) (name.Digest, error) {
	if !strings.HasPrefix(artifact, OCIBlobScheme) || !canonical.IsDigest(digest) {
		return name.Digest{}, e.New(e.CodeUnpinnedSource, "OCI blob acquisition requires an explicit transport and exact sha256 pin")
	}
	reference := strings.TrimPrefix(artifact, OCIBlobScheme)
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil {
		return name.Digest{}, e.Wrap(e.CodeUnpinnedSource, err, "invalid OCI blob transport %q", artifact)
	}
	// NewDigest accepts tag@digest and expands implicit registries. Both are
	// deliberately excluded from this transport contract.
	if ref.Name() != reference || ref.DigestStr() != digest {
		return name.Digest{}, e.New(e.CodeUnpinnedSource, "OCI blob transport %q must be a canonical repository@%s without a tag", artifact, digest)
	}
	return ref, nil
}

// NativeImageReference projects the immutable delivered native subject. A bound
// parent stays in the full node and never becomes the delivered image digest.
// Legacy tag-only transports preserve their prior reference spelling.
func NativeImageReference(node Node) (string, error) {
	if node.OutputType != "oci-image" || !canonical.IsDigest(node.Digest) {
		return "", e.New(e.CodeUnverifiedRef, "native image reference requires an OCI image and exact child")
	}
	if err := validatePullArtifact(node); err != nil {
		return "", err
	}
	parsed, err := name.ParseReference(node.Artifact)
	if err != nil {
		return "", e.Wrap(e.CodeUnverifiedRef, err, "native image reference")
	}
	_, authored := parsed.(name.Digest)
	if node.ImageParentDigest != "" || authored {
		return parsed.Context().Digest(node.Digest).Name(), nil
	}
	return node.Artifact + "@" + node.Digest, nil
}

func validatePullArtifact(node Node) error {
	if strings.HasPrefix(node.Artifact, OCIBlobScheme) {
		if node.OutputType != "raw" {
			return e.New(e.CodeInvalidCatalog, "OCI blob pull %q requires raw byte-preserving output", node.ID)
		}
		_, err := ParseOCIBlobArtifact(node.Artifact, node.Digest)
		return err
	}
	if node.OutputType == "oci-image" {
		if strings.Contains(node.Artifact, "://") {
			return e.New(e.CodeInvalidCatalog, "OCI image pull %q requires an image name, not an archive or URL transport", node.ID)
		}
		parsed, err := name.ParseReference(node.Artifact)
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "OCI pull reference %q", node.ID)
		}
		if node.ImageParentDigest != "" && !canonical.IsDigest(node.ImageParentDigest) {
			return e.New(e.CodeUnverifiedRef, "OCI pull %q has invalid parent digest", node.ID)
		}
		if authored, ok := parsed.(name.Digest); ok {
			if !canonical.IsDigest(authored.DigestStr()) {
				return e.New(e.CodeUnverifiedRef, "OCI pull %q has invalid authored digest", node.ID)
			}
			want := node.ImageParentDigest
			if want == "" {
				want = node.Digest
			}
			if authored.DigestStr() != want {
				return e.New(e.CodeUnverifiedRef, "OCI pull %q changes its authored digest", node.ID)
			}
		}
		return nil
	}
	location, err := url.Parse(node.Artifact)
	if err != nil || (location.Scheme != "https" && location.Scheme != "http") || location.Hostname() == "" || location.User != nil || location.Fragment != "" {
		return e.New(e.CodeInvalidCatalog, "file pull %q requires an HTTP(S) URL or explicit pinned OCI blob transport", node.ID)
	}
	return nil
}

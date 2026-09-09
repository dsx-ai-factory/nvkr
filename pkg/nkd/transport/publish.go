package transport

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/release"
)

var canonicalArtifactType = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)

// Artifact is one local artifact proposed for publication. Digest is checked against
// release authorization and rehashed at the transport seam before any write.
type Artifact struct {
	Name      string
	Kind      string // "oci" | "package" | "object"
	Path      string
	Digest    string
	Referrers []Referrer
}

// Referrer is a digest-bound blob attached to an OCI artifact.
type Referrer struct {
	ArtifactType string
	Path         string
	// IdentityDigest is the NKD domain-separated semantic identity authorized by
	// release. Digest is the ordinary sha256 of the exact registry blob bytes.
	IdentityDigest string
	Digest         string
}

// Targets names the destination per artifact kind (ADR-20 §2).
type Targets struct {
	OCI     Target
	Package Target
	Object  Target
}

// ClearedArtifactSet is a private capability binding local publication metadata to
// the exact artifact digest set authorized by release.Authorize.
type ClearedArtifactSet struct {
	valid               bool
	authorizationDigest string
	artifacts           []Artifact
}

// BindClearedArtifactSet checks that the proposal contains every authorized artifact
// exactly once and no additional artifact. File bytes are deliberately rehashed later
// by PublishCleared to catch mutation after binding.
func BindClearedArtifactSet(authorization *release.Authorization, artifacts []Artifact) (*ClearedArtifactSet, error) {
	// Authorization intentionally refuses until the release publication commit
	// protocol exists; retaining this boundary prevents a diagnostic binder from
	// becoming production authority.
	//nolint:staticcheck
	if err := authorization.RequirePublicationCommit(); err != nil {
		return nil, err
	}
	return bindClearedArtifactSetDiagnostic(authorization, artifacts)
}

// bindClearedArtifactSetDiagnostic exercises the artifact-only transport
// mechanics without claiming that the release publication root/commit protocol is
// complete. Production callers must use BindClearedArtifactSet, which fails closed.
func bindClearedArtifactSetDiagnostic(authorization *release.Authorization, artifacts []Artifact) (*ClearedArtifactSet, error) {
	authorized, err := authorization.ArtifactDigests()
	if err != nil {
		return nil, err
	}
	if len(authorized) == 0 {
		return nil, e.New(e.CodeReleaseIncomplete, "release authorization contains no publishable artifacts")
	}
	authorizationDigest, err := authorization.Digest()
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, digest := range authorized {
		want[digest] = true
	}
	publication, err := authorization.PublicationManifest()
	if err != nil {
		return nil, err
	}
	requiredReferrers := make(map[string]map[string]release.PublicationReferrer)
	for _, required := range publication.Referrers {
		if requiredReferrers[required.SubjectDigest] == nil {
			requiredReferrers[required.SubjectDigest] = map[string]release.PublicationReferrer{}
		}
		requiredReferrers[required.SubjectDigest][required.ArtifactType] = required
	}
	seenDigest := map[string]bool{}
	seenName := map[string]bool{}
	bound := make([]Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Name) == "" || strings.TrimSpace(artifact.Name) != artifact.Name ||
			strings.TrimSpace(artifact.Path) == "" || strings.TrimSpace(artifact.Path) != artifact.Path {
			return nil, e.New(e.CodeInvalidCatalog, "publication artifact requires name and path")
		}
		if seenName[artifact.Name] {
			return nil, e.New(e.CodeInvalidCatalog, "publication repeats artifact name %q", artifact.Name)
		}
		seenName[artifact.Name] = true
		if !isDigest(artifact.Digest) || !want[artifact.Digest] {
			return nil, e.New(e.CodeUnverifiedRef, "artifact %q digest %q is not in release authorization", artifact.Name, artifact.Digest)
		}
		if seenDigest[artifact.Digest] {
			return nil, e.New(e.CodeInvalidCatalog, "publication repeats authorized artifact digest %s", artifact.Digest)
		}
		seenDigest[artifact.Digest] = true
		switch artifact.Kind {
		case "oci", "package", "object":
		default:
			return nil, e.New(e.CodeUnsupported, "unknown artifact kind %q for %q", artifact.Kind, artifact.Name)
		}
		copyArtifact := artifact
		copyArtifact.Referrers = append([]Referrer(nil), artifact.Referrers...)
		referrerDestinations := make(map[string]string, len(copyArtifact.Referrers))
		seenReferrers := make(map[string]bool, len(copyArtifact.Referrers))
		for _, referrer := range copyArtifact.Referrers {
			if !canonicalArtifactType.MatchString(referrer.ArtifactType) ||
				strings.TrimSpace(referrer.Path) == "" || strings.TrimSpace(referrer.Path) != referrer.Path ||
				!isDigest(referrer.IdentityDigest) || !isDigest(referrer.Digest) {
				return nil, e.New(e.CodeUnverifiedRef,
					"artifact %q has a referrer without type, path, semantic identity, and immutable blob digest", artifact.Name)
			}
			required, ok := requiredReferrers[artifact.Digest][referrer.ArtifactType]
			if !ok || required.IdentityDigest != referrer.IdentityDigest || required.BlobDigest != referrer.Digest {
				return nil, e.New(e.CodeGateUnmet,
					"artifact %q referrer %s/%s/%s is not in the release publication manifest",
					artifact.Name, referrer.ArtifactType, referrer.IdentityDigest, referrer.Digest)
			}
			if seenReferrers[referrer.ArtifactType] {
				return nil, e.New(e.CodeImmutableViolation,
					"artifact %q repeats required referrer type %q", artifact.Name, referrer.ArtifactType)
			}
			seenReferrers[referrer.ArtifactType] = true
			destination := safeName(referrer.ArtifactType)
			if prior, exists := referrerDestinations[destination]; exists {
				return nil, e.New(e.CodeImmutableViolation,
					"artifact %q referrer types %q and %q resolve to the same immutable destination %q",
					artifact.Name, prior, referrer.ArtifactType, destination)
			}
			referrerDestinations[destination] = referrer.ArtifactType
		}
		if len(seenReferrers) != len(requiredReferrers[artifact.Digest]) {
			missing := make([]string, 0)
			for artifactType := range requiredReferrers[artifact.Digest] {
				if !seenReferrers[artifactType] {
					missing = append(missing, artifactType)
				}
			}
			sort.Strings(missing)
			return nil, e.New(e.CodeReleaseIncomplete,
				"artifact %q omits release-required referrer type(s): %s",
				artifact.Name, strings.Join(missing, ", "))
		}
		sort.Slice(copyArtifact.Referrers, func(i, j int) bool {
			return copyArtifact.Referrers[i].ArtifactType < copyArtifact.Referrers[j].ArtifactType
		})
		bound = append(bound, copyArtifact)
	}
	if len(seenDigest) != len(want) {
		var missing []string
		for digest := range want {
			if !seenDigest[digest] {
				missing = append(missing, digest)
			}
		}
		sort.Strings(missing)
		return nil, e.New(e.CodeReleaseIncomplete, "publication omits cleared artifact digest(s): %s", strings.Join(missing, ", "))
	}
	sort.Slice(bound, func(i, j int) bool { return bound[i].Name < bound[j].Name })
	return &ClearedArtifactSet{valid: true, authorizationDigest: authorizationDigest, artifacts: bound}, nil
}

// PublishCleared snapshots and rehashes the complete set before any adapter is
// called, then publishes only those private snapshots. This closes the gap between
// clearance preflight and adapters reopening caller-controlled paths.
func PublishCleared(ctx context.Context, seams Seams, targets Targets, set *ClearedArtifactSet) error {
	if err := transportContextErr(ctx); err != nil {
		return err
	}
	if set == nil || !set.valid || set.authorizationDigest == "" {
		return e.New(e.CodeGateUnmet, "publishing requires a cleared artifact-set capability")
	}
	packageNames := map[string]bool{}
	for _, artifact := range set.artifacts {
		if err := requireTarget(artifact.Kind, targets); err != nil {
			return err
		}
		if artifact.Kind == "package" {
			base := filepath.Base(artifact.Path)
			destination := safeName(base)
			if packageNames[destination] {
				return e.New(e.CodeImmutableViolation, "multiple packages resolve to destination name %q", destination)
			}
			packageNames[destination] = true
		}
	}
	snapshots, cleanup, err := snapshotPublication(ctx, set.artifacts)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := transportContextErr(ctx); err != nil {
		return err
	}

	resolved := seams.Resolve()
	for _, artifact := range snapshots {
		if err := transportContextErr(ctx); err != nil {
			return err
		}
		switch artifact.Kind {
		case "oci":
			if _, err := resolved.OCI.Push(ctx, targets.OCI, artifact.Path, artifact.Digest); err != nil {
				return err
			}
			for _, referrer := range artifact.Referrers {
				if err := resolved.OCI.AttachReferrer(ctx, targets.OCI, artifact.Digest, referrer.ArtifactType, referrer.Path, referrer.Digest); err != nil {
					return err
				}
			}
		case "package":
			if err := resolved.Package.Publish(ctx, targets.Package, artifact.Path, artifact.Digest); err != nil {
				return err
			}
		case "object":
			file, err := os.Open(artifact.Path)
			if err != nil {
				return e.Wrap(e.CodeInternal, err, "opening object %s", artifact.Path)
			}
			err = resolved.Object.Put(ctx, targets.Object, artifact.Digest, artifact.Digest, file)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return e.Wrap(e.CodeInternal, closeErr, "closing object %s", artifact.Path)
			}
		}
	}
	return nil
}

func requireTarget(kind string, targets Targets) error {
	var target string
	switch kind {
	case "oci":
		target = targets.OCI.Ref
	case "package":
		target = targets.Package.Ref
	case "object":
		target = targets.Object.Ref
	}
	if strings.TrimSpace(target) == "" || strings.TrimSpace(target) != target {
		return e.New(e.CodeUsage, "publication target for %s artifact is required", kind)
	}
	return nil
}

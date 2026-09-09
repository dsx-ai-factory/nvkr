package delivery

import (
	"context"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/cluster"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/internal/heldfile"
	"github.com/nhensley/nkdctl/pkg/nkd/release"
	"github.com/nhensley/nkdctl/pkg/nkd/trust"
)

const (
	MediaClearanceDocument   = "application/vnd.nkd.clearance.v7+json"
	MediaPublicationManifest = "application/vnd.nkd.publication-manifest.v1+json"
	MediaReleaseSet          = "application/vnd.nkd.release-set.v2+json"
	MediaInventory           = "application/vnd.nkd.inventory.v2+json"
	MediaClusterDefinition   = "application/vnd.nkd.cluster-definition.v1+json"
	MediaCosignSignature     = "application/vnd.dev.cosign.simplesigning.v1+json"
	MediaPEMCertificate      = "application/x-pem-file"
	MediaSigstoreBundle      = "application/vnd.dev.sigstore.bundle.v0.3+json"
	MediaSigstoreTrustedRoot = "application/vnd.dev.sigstore.trustedroot+json"
)

// QualifiedPublicationRequest joins an already-authenticated clearance capability
// to its exact transport proof and the exhaustive authorized artifact/referrer set.
// Derived binary bundles are permitted only when every entry is itself a cleared
// artifact. No caller boolean or serialized root can construct this authority.
type QualifiedPublicationRequest struct {
	Authorization         *release.Authorization
	Clearance             trust.DocumentReceipt
	ReleaseSetPath        string
	InventoryPath         string
	ClusterDefinitionPath string
	SignaturePath         string
	CertificatePath       string
	BundlePath            string
	TrustedRootPath       string
	Objects               []ObjectInput
}

// QualifiedRoot is an opaque same-process capability proving that a qualified OCI
// root was constructed from one authenticated clearance and its exact proof bytes.
type QualifiedRoot struct {
	valid               bool
	result              Result
	authorizationDigest string
}

func (root *QualifiedRoot) Result() (Result, error) {
	if root == nil || !root.valid || !isDigest(root.authorizationDigest) {
		return Result{}, e.New(e.CodeGateUnmet, "qualified publication-root capability is invalid")
	}
	return root.result, nil
}

// BuildQualifiedPublicationRoot implements the non-circular commit protocol: the
// signed clearance is a child of, never a statement about, the OCI root index.
// Consumers pin the index digest, extract and authenticate that clearance, then
// recompute the same authorized closure.
func BuildQualifiedPublicationRoot(ctx context.Context, out string, request QualifiedPublicationRequest) (*QualifiedRoot, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if request.Authorization == nil || !request.Clearance.Authenticated() || request.Clearance.Role() != trust.RoleClearance {
		return nil, e.New(e.CodeGateUnmet, "qualified publication requires an authenticated release-clearance receipt")
	}
	rehydrated, err := release.VerifyAuthenticatedAuthorization(ctx, request.Clearance)
	if err != nil {
		return nil, err
	}
	wantAuthorization, err := request.Authorization.Digest()
	if err != nil {
		return nil, err
	}
	gotAuthorization, err := rehydrated.Digest()
	if err != nil || gotAuthorization != wantAuthorization {
		return nil, e.New(e.CodeGateUnmet, "clearance receipt does not rehydrate the supplied release authorization")
	}
	releaseDigest, err := request.Authorization.ReleaseSetDigest()
	if err != nil {
		return nil, err
	}
	inventoryDigest, err := request.Authorization.InventoryDigest()
	if err != nil {
		return nil, err
	}
	publicationIdentity, err := request.Authorization.PublicationIdentity()
	if err != nil {
		return nil, err
	}
	publicationManifest, err := request.Authorization.PublicationManifest()
	if err != nil {
		return nil, err
	}
	publicationManifestDigest, err := publicationManifest.Digest()
	if err != nil {
		return nil, err
	}
	publicationManifestRaw, err := canonical.Marshal(publicationManifest)
	if err != nil {
		return nil, err
	}

	releaseRaw, err := readHeldBytes(ctx, request.ReleaseSetPath, "ReleaseSet", 16<<20)
	if err != nil {
		return nil, err
	}
	var set release.ReleaseSet
	if err := canonical.UnmarshalExact(releaseRaw, &set); err != nil {
		return nil, err
	}
	if digest, err := set.Digest(); err != nil || digest != releaseDigest {
		return nil, e.New(e.CodeUnverifiedRef, "qualified publication ReleaseSet does not match authorization")
	}
	inventoryRaw, err := readHeldBytes(ctx, request.InventoryPath, "Inventory", 16<<20)
	if err != nil {
		return nil, err
	}
	var inventory release.Inventory
	if err := canonical.UnmarshalExact(inventoryRaw, &inventory); err != nil {
		return nil, err
	}
	if digest, err := inventory.Digest(); err != nil || digest != inventoryDigest || set.InventoryRef != inventoryDigest {
		return nil, e.New(e.CodeInventoryUnpinned, "qualified publication Inventory does not match authorization")
	}

	objects := append([]ObjectInput(nil), request.Objects...)
	if err := validateAuthorizedObjects(ctx, request.Authorization, publicationManifest, objects); err != nil {
		return nil, err
	}
	objects = append(objects,
		bytesObject("clearance", "clearance", MediaClearanceDocument, wantAuthorization, request.Clearance.SnapshotSHA256(), request.Clearance.ExactBytes()),
		bytesObject("publication-manifest", "publication-manifest", MediaPublicationManifest, publicationManifestDigest, "", publicationManifestRaw),
		bytesObject("release-set", "release-set", MediaReleaseSet, releaseDigest, digestBytes(releaseRaw), releaseRaw),
		bytesObject("inventory", "inventory", MediaInventory, inventoryDigest, digestBytes(inventoryRaw), inventoryRaw),
	)
	proofObjects, err := qualifiedProofObjects(ctx, request)
	if err != nil {
		return nil, err
	}
	objects = append(objects, proofObjects...)
	clusterDigest := ""
	if request.ClusterDefinitionPath != "" {
		clusterRaw, err := readHeldBytes(ctx, request.ClusterDefinitionPath, "ClusterDefinition", 16<<20)
		if err != nil {
			return nil, err
		}
		var definition cluster.ClusterDefinition
		if err := canonical.UnmarshalExact(clusterRaw, &definition); err != nil {
			return nil, err
		}
		clusterDigest, err = definition.Digest()
		if err != nil || definition.ReleaseSetRef != releaseDigest {
			return nil, e.New(e.CodeUnverifiedRef, "ClusterDefinition does not reference the authorized ReleaseSet")
		}
		objects = append(objects, bytesObject("cluster-definition", "cluster-definition", MediaClusterDefinition, clusterDigest, digestBytes(clusterRaw), clusterRaw))
	}
	rootModel := PublicationRoot{SchemaVersion: PublicationRootSchema, Qualification: QualificationQualified,
		PublicationIdentity: publicationIdentity, PublicationManifest: publicationManifestDigest,
		ReleaseSetDigest: releaseDigest, InventoryDigest: inventoryDigest,
		ClusterDefinitionDigest: clusterDigest, ClearanceDigest: wantAuthorization}
	result, err := buildPublicationRoot(ctx, out, rootModel, objects)
	if err != nil {
		return nil, err
	}
	return &QualifiedRoot{valid: true, result: result, authorizationDigest: wantAuthorization}, nil
}

func validateAuthorizedObjects(ctx context.Context, authorization *release.Authorization, manifest release.PublicationManifest, objects []ObjectInput) error {
	wantArtifacts, err := authorization.ArtifactDigests()
	if err != nil {
		return err
	}
	wantArtifact := make(map[string]bool, len(wantArtifacts))
	for _, digest := range wantArtifacts {
		wantArtifact[digest] = true
	}
	artifacts := map[string]bool{}
	referrers := map[string]bool{}
	for _, required := range manifest.Referrers {
		referrers[required.ArtifactType+"\x00"+required.IdentityDigest+"\x00"+required.BlobDigest] = true
	}
	for i := range objects {
		input := &objects[i]
		if qualifiedReservedName(input.Name) {
			return e.New(e.CodeGateUnmet,
				"qualified publication object name %q is reserved for authenticated root material", input.Name)
		}
		if input.Layout {
			_, raw, err := readIndex(ctx, input.Path)
			if err != nil {
				return err
			}
			rootDigest := digestBytes(raw)
			if input.BlobDigest == "" {
				input.BlobDigest = rootDigest
			}
			if input.BlobDigest != rootDigest {
				return e.New(e.CodeVerifyFailed, "qualified OCI object %q does not match its root digest", input.Name)
			}
		} else {
			got, err := digestHeldPath(ctx, input.Path, "publication object", maxDeliveryFileBytes)
			if err != nil {
				return err
			}
			if input.BlobDigest == "" {
				input.BlobDigest = got
			}
			if input.BlobDigest != got {
				return e.New(e.CodeVerifyFailed, "qualified object %q does not match its blob digest", input.Name)
			}
		}
		if input.Kind == "artifact" && input.SemanticDigest == input.BlobDigest {
			artifacts[input.SemanticDigest] = true
			continue
		}
		if referrers[input.ArtifactType+"\x00"+input.SemanticDigest+"\x00"+input.BlobDigest] {
			delete(referrers, input.ArtifactType+"\x00"+input.SemanticDigest+"\x00"+input.BlobDigest)
			continue
		}
		if input.ArtifactType == MediaBinaryBundle && input.Layout {
			bundle, result, err := InspectBinaryBundle(ctx, input.Path)
			if err != nil {
				return err
			}
			if input.SemanticDigest != result.SemanticDigest {
				return e.New(e.CodeUnverifiedRef,
					"derived binary bundle %q semantic digest does not match its exact config", input.Name)
			}
			for _, entry := range bundle.Entries {
				if !wantArtifact[entry.SHA256] {
					return e.New(e.CodeGateUnmet, "derived binary bundle %q contains uncleared artifact %s", input.Name, entry.SHA256)
				}
			}
			continue
		}
		return e.New(e.CodeGateUnmet, "qualified publication object %q is neither a cleared artifact, required referrer, nor exact derived bundle", input.Name)
	}
	for _, digest := range wantArtifacts {
		if !artifacts[digest] {
			return e.New(e.CodeReleaseIncomplete, "qualified publication omits cleared artifact %s", digest)
		}
	}
	if len(referrers) != 0 {
		return e.New(e.CodeReleaseIncomplete, "qualified publication omits %d required referrer(s)", len(referrers))
	}
	return nil
}

func qualifiedReservedName(name string) bool {
	switch name {
	case "clearance", "clearance-signature", "clearance-certificate", "clearance-bundle",
		"trusted-root", "publication-manifest", "release-set", "inventory", "cluster-definition":
		return true
	default:
		return false
	}
}

func qualifiedProofObjects(ctx context.Context, request QualifiedPublicationRequest) ([]ObjectInput, error) {
	type proof struct {
		name, kind, media, path, want string
		required                      bool
	}
	proofs := []proof{
		{"clearance-signature", "signature", MediaCosignSignature, request.SignaturePath, request.Clearance.SignatureSHA256(), request.Clearance.SignatureSHA256() != ""},
		{"clearance-certificate", "certificate", MediaPEMCertificate, request.CertificatePath, request.Clearance.CertificateBlobSHA256(), true},
		{"clearance-bundle", "sigstore-bundle", MediaSigstoreBundle, request.BundlePath, request.Clearance.BundleSHA256(), request.Clearance.BundleSHA256() != ""},
		{"trusted-root", "trusted-root", MediaSigstoreTrustedRoot, request.TrustedRootPath, request.Clearance.TrustedRootSHA256(), request.Clearance.TrustedRootSHA256() != ""},
	}
	objects := make([]ObjectInput, 0, len(proofs))
	for _, proof := range proofs {
		if proof.required && (proof.path == "" || !isDigest(proof.want)) {
			return nil, e.New(e.CodeGateUnmet, "qualified publication lacks exact %s proof material", proof.name)
		}
		if proof.path == "" {
			continue
		}
		raw, err := readHeldBytes(ctx, proof.path, proof.name, 32<<20)
		if err != nil {
			return nil, err
		}
		if digestBytes(raw) != proof.want {
			return nil, e.New(e.CodeSignatureInvalid, "%s bytes do not match authenticated clearance receipt", proof.name)
		}
		objects = append(objects, bytesObject(proof.name, proof.kind, proof.media, proof.want, proof.want, raw))
	}
	return objects, nil
}

func bytesObject(name, kind, media, semantic, blob string, raw []byte) ObjectInput {
	if blob == "" {
		blob = digestBytes(raw)
	}
	return ObjectInput{Name: name, Kind: kind, ArtifactType: media, LayerMediaType: media,
		SemanticDigest: semantic, BlobDigest: blob, Bytes: append([]byte(nil), raw...)}
}

func readHeldBytes(ctx context.Context, path, label string, max int64) ([]byte, error) {
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: label, MaxBytes: max})
	if err != nil {
		return nil, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding %s", label))
	}
	defer func() { _ = held.Close() }()
	return held.ReadAllContext(ctx, max)
}

func digestHeldPath(ctx context.Context, path, label string, max int64) (string, error) {
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: label, MaxBytes: max})
	if err != nil {
		return "", classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "holding %s", label))
	}
	defer func() { _ = held.Close() }()
	if err := held.AssertUnchangedContext(ctx); err != nil {
		return "", classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "rechecking %s", label))
	}
	return held.Digest(), nil
}

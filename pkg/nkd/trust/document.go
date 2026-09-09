package trust

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/internal/heldfile"
)

// MaxAuthenticatedDocumentBytes bounds the memory and private-disk footprint of a
// trust document. Validation vectors, profiles, status declarations, and
// conformance results are metadata, not artifact payloads.
const MaxAuthenticatedDocumentBytes int64 = 16 << 20

// MaxTrustedRootBytes bounds operator-selected Sigstore trust metadata before it
// can reach a verification adapter. A trusted root is policy metadata, never an
// artifact payload.
const MaxTrustedRootBytes int64 = 32 << 20

// SignerRole keeps independently governed signer authorities from being silently
// reused for a different trust object.
type SignerRole string

const (
	RoleValidationVector SignerRole = "validation-vector"
	RoleBuilderProfile   SignerRole = "builder-profile"
	RoleStatus           SignerRole = "status"
	RoleConformance      SignerRole = "conformance"
	RolePromotionPolicy  SignerRole = "promotion-policy"
	RoleReleaseSet       SignerRole = "release-set"
	RoleClearance        SignerRole = "release-clearance"
	// RoleReproduction and RoleCompatibility complete the release-gate signer vocabulary
	// so the reproduction/compatibility/conformance gates authenticate under ONE role
	// system (trust.SignerRole via DocumentAuthenticator) rather than a parallel one.
	// release.ReceiptRole.TrustRole() is the single bridge onto these; keeping the trust
	// roles here means a future wired ReceiptAuthenticator does not fork a second vocabulary.
	RoleReproduction  SignerRole = "release-reproduction"
	RoleCompatibility SignerRole = "release-compatibility"
)

func (r SignerRole) valid() bool {
	switch r {
	case RoleValidationVector, RoleBuilderProfile, RoleStatus, RoleConformance, RolePromotionPolicy,
		RoleReleaseSet, RoleClearance, RoleReproduction, RoleCompatibility:
		return true
	default:
		return false
	}
}

// RolePolicy is an immutable snapshot of the trust policy for exactly one signer
// role. Its fields are intentionally private: callers cannot retarget a validated
// policy by mutating a role or identity slice after construction.
type RolePolicy struct {
	role              SignerRole
	identities        []string
	issuer            string
	source            string
	trustedRootDigest string
}

// NewRolePolicy resolves Store precedence now and binds the resulting identities
// and expected OIDC issuer to one signer role.
func NewRolePolicy(role SignerRole, store Store, issuer string) (RolePolicy, error) {
	if !role.valid() {
		return RolePolicy{}, e.New(e.CodeTrustPolicyMissing, "unknown signer role %q", role)
	}
	identities, source := store.Trusted()
	if len(identities) == 0 {
		return RolePolicy{}, missingRolePolicyError(role,
			"no trusted identity is resolvable for %s signer policy (source=%s)", role, source)
	}
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return RolePolicy{}, missingRolePolicyError(role,
			"%s signer policy has no pinned OIDC issuer", role)
	}
	return RolePolicy{
		role: role, identities: append([]string(nil), identities...), issuer: issuer, source: source,
	}, nil
}

// Role reports the one signer role this immutable policy governs.
func (p RolePolicy) Role() SignerRole { return p.role }

// WithTrustedRootDigest returns a new immutable policy that authorizes exactly one
// operator-pinned Sigstore trusted-root byte identity. The digest originates in
// operator configuration, never in the document or detached signature material.
func (p RolePolicy) WithTrustedRootDigest(digest string) (RolePolicy, error) {
	if !p.role.valid() || len(p.identities) == 0 || p.issuer == "" {
		return RolePolicy{}, e.New(e.CodeTrustPolicyMissing,
			"cannot add a trusted-root pin to an incomplete signer policy")
	}
	digest = strings.TrimSpace(digest)
	if !isSHA256Digest(digest) {
		return RolePolicy{}, e.New(e.CodeUnpinnedInput,
			"offline signer policy requires an exact lowercase sha256 trusted-root digest")
	}
	p.identities = append([]string(nil), p.identities...)
	p.trustedRootDigest = digest
	return p, nil
}

func (p RolePolicy) constraints() (identities []string, issuer string) {
	return append([]string(nil), p.identities...), p.issuer
}

func (p RolePolicy) authorize(ev VerificationEvidence) (string, error) {
	if ev.OIDCIssuer == "" || ev.OIDCIssuer != p.issuer {
		return "", untrustedRoleError(p.role,
			"verified %s certificate issuer %q does not match pinned issuer %q",
			p.role, ev.OIDCIssuer, p.issuer)
	}
	have := make(map[string]bool, len(ev.CertificateIdentities))
	for _, id := range ev.CertificateIdentities {
		have[id] = true
	}
	for _, trusted := range p.identities {
		if have[trusted] {
			return trusted, nil
		}
	}
	return "", untrustedRoleError(p.role,
		"verified %s certificate identities %q are not in the trusted set (%s: %s)",
		p.role, strings.Join(ev.CertificateIdentities, ", "), p.source, strings.Join(p.identities, ", "))
}

func missingRolePolicyError(role SignerRole, format string, args ...any) error {
	if role == RoleBuilderProfile {
		return e.New(e.CodeUntrustedProfile, format, args...)
	}
	return e.New(e.CodeTrustPolicyMissing, format, args...)
}

func untrustedRoleError(role SignerRole, format string, args ...any) error {
	if role == RoleBuilderProfile {
		return e.New(e.CodeUntrustedProfile, format, args...)
	}
	return e.New(e.CodeUntrustedSigner, format, args...)
}

// SignatureMaterial names detached signature evidence. These are observed transport
// paths, never trust policy: the expected trusted-root digest lives only in the
// immutable RolePolicy. The qualified adapter turns each path into a held capability.
// CertificatePath is required so actual verified claims come from its exact bytes.
type SignatureMaterial struct {
	SignaturePath   string
	CertificatePath string
	BundlePath      string
	TrustedRootPath string
}

// DocumentVerificationRequest is constructed only by DocumentAuthenticator after
// it has created a private byte-for-byte snapshot of the source document.
type DocumentVerificationRequest struct {
	SnapshotPath string
	// ExactBytes is the defensive copy captured by DocumentAuthenticator. A
	// production verifier must consume these bytes directly rather than reopening
	// SnapshotPath; the latter remains only as a diagnostic seam for injected test
	// verifiers that inspect the authenticator's private snapshot.
	ExactBytes        []byte
	Signature         SignatureMaterial
	Offline           bool
	Role              SignerRole
	AllowedIdentities []string
	OIDCIssuer        string
	TrustedRootDigest string
}

// VerificationEvidence contains claims captured from the certificate that the
// adapter actually verified. There is deliberately no caller-settable "Verified"
// boolean: success is represented only by a nil adapter error, followed by policy
// authorization inside this package.
type VerificationEvidence struct {
	CertificateIdentities []string
	OIDCIssuer            string
	CertificateSHA256     string
	// The blob digests bind the exact detached transport material consumed by
	// the verifier. They are not signer policy; qualified publication uses them
	// to carry the same proof bytes into an OCI bootstrap root.
	SignatureSHA256       string
	CertificateBlobSHA256 string
	BundleSHA256          string
	TrustedRootBlobSHA256 string
}

// DocumentVerifier is the real verification seam. Production injects the qualified
// cosign adapter; tests inject an adversarial adapter. Neither can bypass the
// canonical/schema/policy checks performed after this method returns.
type DocumentVerifier interface {
	VerifyDocument(context.Context, DocumentVerificationRequest) (VerificationEvidence, error)
}

// DocumentSchema is a closed decoder entry keyed by schemaVersion. Use JSONSchema
// so decoding always rejects fields unknown to the selected Go schema.
type DocumentSchema struct {
	version string
	decode  func([]byte) (any, error)
}

// JSONSchema creates a strict JSON schema-dispatch entry for T.
func JSONSchema[T any](version string) DocumentSchema {
	return DocumentSchema{
		version: strings.TrimSpace(version),
		decode: func(raw []byte) (any, error) {
			var value T
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&value); err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "strictly decoding authenticated document")
			}
			if err := requireJSONEOF(dec); err != nil {
				return nil, err
			}
			return value, nil
		},
	}
}

// DocumentAuthenticator is a deep module: one call snapshots, verifies, checks
// snapshot immutability, canonical-validates, dispatches by schemaVersion, strictly
// decodes, authorizes the certificate identity, and returns an opaque receipt.
type DocumentAuthenticator struct {
	verifier DocumentVerifier
	schemas  map[string]DocumentSchema
}

// NewDocumentAuthenticator constructs the authenticated-document module. Duplicate
// or empty schema registrations fail at construction rather than at verification.
func NewDocumentAuthenticator(verifier DocumentVerifier, schemas ...DocumentSchema) (*DocumentAuthenticator, error) {
	if verifier == nil {
		return nil, e.New(e.CodeInternal, "authenticated-document verifier is nil")
	}
	registered := make(map[string]DocumentSchema, len(schemas))
	for _, schema := range schemas {
		if schema.version == "" || schema.decode == nil {
			return nil, e.New(e.CodeInternal, "authenticated-document schema registration is incomplete")
		}
		if _, exists := registered[schema.version]; exists {
			return nil, e.New(e.CodeInternal, "authenticated-document schema %q is registered more than once", schema.version)
		}
		registered[schema.version] = schema
	}
	return &DocumentAuthenticator{verifier: verifier, schemas: registered}, nil
}

// AuthenticateRequest binds a source document, detached evidence, signer role, and
// the immutable policy for that same role.
type AuthenticateRequest struct {
	SourcePath string
	Signature  SignatureMaterial
	Offline    bool
	Role       SignerRole
	Policy     RolePolicy
}

// DocumentReceipt can only be produced as authenticated by Authenticate. Critical
// fields are private; its zero value is explicitly unauthenticated.
type DocumentReceipt struct {
	authenticated         bool
	exactBytes            []byte
	decode                func([]byte) (any, error)
	schemaVersion         string
	role                  SignerRole
	signer                string
	issuer                string
	certificateSHA256     string
	signatureSHA256       string
	certificateBlobSHA256 string
	bundleSHA256          string
	trustedRootSHA256     string
	snapshotSHA256        string
}

func (r DocumentReceipt) Authenticated() bool           { return r.authenticated }
func (r DocumentReceipt) SchemaVersion() string         { return r.schemaVersion }
func (r DocumentReceipt) Role() SignerRole              { return r.role }
func (r DocumentReceipt) SignerIdentity() string        { return r.signer }
func (r DocumentReceipt) OIDCIssuer() string            { return r.issuer }
func (r DocumentReceipt) CertificateSHA256() string     { return r.certificateSHA256 }
func (r DocumentReceipt) SignatureSHA256() string       { return r.signatureSHA256 }
func (r DocumentReceipt) CertificateBlobSHA256() string { return r.certificateBlobSHA256 }
func (r DocumentReceipt) BundleSHA256() string          { return r.bundleSHA256 }
func (r DocumentReceipt) TrustedRootSHA256() string     { return r.trustedRootSHA256 }
func (r DocumentReceipt) SnapshotSHA256() string        { return r.snapshotSHA256 }

// Decoded returns a fresh strict decode of the authenticated exact bytes. Callers
// cannot mutate slice, map, or pointer state retained by the capability.
func (r DocumentReceipt) Decoded() any {
	if !r.authenticated || r.decode == nil {
		return nil
	}
	decoded, err := r.decode(r.exactBytes)
	if err != nil {
		// Authenticate already ran this exact decoder over these immutable bytes.
		// A later failure is fail-closed and indicates a broken schema decoder.
		return nil
	}
	return decoded
}

// ExactBytes returns a defensive copy of the one snapshot that passed every gate.
func (r DocumentReceipt) ExactBytes() []byte { return append([]byte(nil), r.exactBytes...) }

// Authenticate is the only qualified path from a mutable source pathname to a
// decoded trust document. Signature verification deliberately precedes parsing.
func (a *DocumentAuthenticator) Authenticate(ctx context.Context, req AuthenticateRequest) (DocumentReceipt, error) {
	if err := documentContextError(ctx, "authenticating trust document"); err != nil {
		return DocumentReceipt{}, err
	}
	if a == nil || a.verifier == nil {
		return DocumentReceipt{}, e.New(e.CodeInternal,
			"authenticated-document verifier is unavailable")
	}
	if !req.Role.valid() {
		return DocumentReceipt{}, e.New(e.CodeTrustPolicyMissing, "unknown signer role %q", req.Role)
	}
	if req.Policy.role != req.Role {
		return DocumentReceipt{}, e.New(e.CodeTrustPolicyMissing,
			"%s document cannot use %s signer policy", req.Role, req.Policy.role)
	}
	if req.Offline {
		if strings.TrimSpace(req.Signature.BundlePath) == "" {
			return DocumentReceipt{}, e.New(e.CodeSignatureInvalid,
				"qualified offline verification requires a Sigstore bundle with an inclusion proof")
		}
	}
	if err := verifyTrustedRootPin(ctx, req.Signature, req.Policy.trustedRootDigest, req.Offline); err != nil {
		return DocumentReceipt{}, err
	}

	raw, snapshotPath, cleanup, err := privateDocumentSnapshot(ctx, req.SourcePath)
	if err != nil {
		return DocumentReceipt{}, err
	}
	defer cleanup()

	identities, issuer := req.Policy.constraints()
	ev, err := a.verifier.VerifyDocument(ctx, DocumentVerificationRequest{
		SnapshotPath:      snapshotPath,
		ExactBytes:        append([]byte(nil), raw...),
		Signature:         req.Signature,
		Offline:           req.Offline,
		Role:              req.Role,
		AllowedIdentities: identities,
		OIDCIssuer:        issuer,
		TrustedRootDigest: req.Policy.trustedRootDigest,
	})
	if err != nil {
		return DocumentReceipt{}, err
	}
	if err := documentContextError(ctx, "authenticating trust document"); err != nil {
		return DocumentReceipt{}, err
	}
	if err := assertSnapshotUnchanged(ctx, snapshotPath, raw); err != nil {
		return DocumentReceipt{}, err
	}
	if !isSHA256Digest(ev.CertificateSHA256) {
		return DocumentReceipt{}, e.New(e.CodeSignatureInvalid,
			"verification adapter returned no valid certificate digest")
	}
	signer, err := req.Policy.authorize(ev)
	if err != nil {
		return DocumentReceipt{}, err
	}

	schemaVersion, err := canonicalSchemaVersion(raw)
	if err != nil {
		return DocumentReceipt{}, err
	}
	schema, ok := a.schemas[schemaVersion]
	if !ok {
		return DocumentReceipt{}, e.New(e.CodeVerifyFailed,
			"authenticated document uses unknown schemaVersion %q", schemaVersion)
	}
	if _, err := schema.decode(raw); err != nil {
		return DocumentReceipt{}, err
	}
	sum := sha256.Sum256(raw)
	return DocumentReceipt{
		authenticated:         true,
		exactBytes:            append([]byte(nil), raw...),
		decode:                schema.decode,
		schemaVersion:         schemaVersion,
		role:                  req.Role,
		signer:                signer,
		issuer:                ev.OIDCIssuer,
		certificateSHA256:     ev.CertificateSHA256,
		signatureSHA256:       ev.SignatureSHA256,
		certificateBlobSHA256: ev.CertificateBlobSHA256,
		bundleSHA256:          ev.BundleSHA256,
		trustedRootSHA256:     req.Policy.trustedRootDigest,
		snapshotSHA256:        "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

func isSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	raw := strings.TrimPrefix(value, "sha256:")
	if strings.ToLower(raw) != raw {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

// verifyTrustedRootPin ensures that an operator-selected offline root is authority
// by exact bytes, not by a mutable pathname. The production adapter repeats this
// check over the exact held bytes it gives cosign; doing it here also makes every
// DocumentVerifier implementation fail before verification when the policy pin is
// absent or mismatched.
func verifyTrustedRootPin(ctx context.Context, material SignatureMaterial, expectedDigest string, offline bool) error {
	if err := documentContextError(ctx, "verifying trusted-root pin"); err != nil {
		return err
	}
	path := strings.TrimSpace(material.TrustedRootPath)
	digest := strings.TrimSpace(expectedDigest)
	if path == "" {
		if offline {
			return e.New(e.CodeUnpinnedInput,
				"qualified offline verification requires an exact trusted-root path and sha256 digest")
		}
		if digest != "" {
			return e.New(e.CodeUnpinnedInput,
				"trusted-root digest was configured without a trusted-root path")
		}
		return nil
	}
	if !isSHA256Digest(digest) {
		return e.New(e.CodeUnpinnedInput,
			"operator-selected trusted root requires an exact lowercase sha256 digest")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{
		Label: "operator-trusted-root", MaxBytes: MaxTrustedRootBytes,
	})
	if err != nil {
		if contextErr := documentContextError(ctx, "opening pinned trusted root"); contextErr != nil {
			return contextErr
		}
		return e.Wrap(e.CodeUnpinnedInput, err, "opening pinned trusted root %s", path)
	}
	defer func() { _ = held.Close() }()
	got := held.Digest()
	if got != digest {
		return e.New(e.CodeUnpinnedInput,
			"trusted-root digest %s does not match pinned %s", got, digest)
	}
	return nil
}

func privateDocumentSnapshot(ctx context.Context, sourcePath string) ([]byte, string, func(), error) {
	if err := documentContextError(ctx, "snapshotting trust document"); err != nil {
		return nil, "", func() {}, err
	}
	held, err := heldfile.OpenContext(ctx, sourcePath, heldfile.Options{
		Label: "authenticated-trust-document", MaxBytes: MaxAuthenticatedDocumentBytes,
	})
	if err != nil {
		if contextErr := documentContextError(ctx, "snapshotting trust document"); contextErr != nil {
			return nil, "", func() {}, contextErr
		}
		return nil, "", func() {}, e.Wrap(e.CodeVerifyFailed, err, "opening trust document %s", sourcePath)
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, MaxAuthenticatedDocumentBytes)
	if err != nil {
		if contextErr := documentContextError(ctx, "snapshotting trust document"); contextErr != nil {
			return nil, "", func() {}, contextErr
		}
		return nil, "", func() {}, e.Wrap(e.CodeVerifyFailed, err, "snapshotting trust document %s", sourcePath)
	}
	if err := documentContextError(ctx, "creating private trust-document snapshot"); err != nil {
		return nil, "", func() {}, err
	}
	dir, err := os.MkdirTemp("", "nkd-auth-document-*")
	if err != nil {
		return nil, "", func() {}, e.Wrap(e.CodeInternal, err, "creating private authenticated-document directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "document.snapshot")
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		cleanup()
		return nil, "", func() {}, e.Wrap(e.CodeInternal, err, "creating private authenticated-document snapshot")
	}
	if _, err = copyDocumentContext(ctx, out, bytes.NewReader(raw)); err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(path, 0o400)
	}
	if err != nil {
		cleanup()
		return nil, "", func() {}, e.Wrap(e.CodeInternal, err, "sealing private authenticated-document snapshot")
	}
	return raw, path, cleanup, nil
}

func assertSnapshotUnchanged(ctx context.Context, path string, want []byte) error {
	if err := documentContextError(ctx, "rechecking private trust-document snapshot"); err != nil {
		return err
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{
		Label: "verified-private-document", MaxBytes: MaxAuthenticatedDocumentBytes,
	})
	if err != nil {
		if contextErr := documentContextError(ctx, "rechecking private trust-document snapshot"); contextErr != nil {
			return contextErr
		}
		return e.Wrap(e.CodeSignatureInvalid, err, "re-reading verified private document snapshot")
	}
	defer func() { _ = held.Close() }()
	got, err := held.ReadAllContext(ctx, MaxAuthenticatedDocumentBytes)
	if err != nil {
		if contextErr := documentContextError(ctx, "rechecking private trust-document snapshot"); contextErr != nil {
			return contextErr
		}
		return e.Wrap(e.CodeSignatureInvalid, err, "re-reading verified private document snapshot")
	}
	if !bytes.Equal(got, want) {
		return e.New(e.CodeSignatureInvalid, "private document snapshot changed during signature verification")
	}
	return nil
}

func documentContextError(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a non-nil context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return nil
}

func copyDocumentContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if err := documentContextError(ctx, "copying authenticated document bytes"); err != nil {
			return total, err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != read {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func canonicalSchemaVersion(raw []byte) (string, error) {
	if err := rejectDuplicateJSONNames(raw); err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "decoding authenticated JSON")
	}
	if err := requireJSONEOF(dec); err != nil {
		return "", err
	}
	obj, ok := tree.(map[string]any)
	if !ok {
		return "", e.New(e.CodeVerifyFailed, "authenticated document must be a JSON object")
	}
	canonicalBytes, err := canonical.Marshal(tree)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(raw, canonicalBytes) {
		return "", e.New(e.CodeVerifyFailed,
			"authenticated document bytes are not the canonical JSON representation")
	}
	version, ok := obj["schemaVersion"].(string)
	if !ok || strings.TrimSpace(version) == "" {
		return "", e.New(e.CodeVerifyFailed,
			"authenticated document has no non-empty string schemaVersion")
	}
	return version, nil
}

func rejectDuplicateJSONNames(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walkJSONValue(dec); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "validating authenticated JSON member names")
	}
	return requireJSONEOF(dec)
}

func walkJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			nameToken, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return e.New(e.CodeVerifyFailed, "JSON object member name is not a string")
			}
			if seen[name] {
				return e.New(e.CodeVerifyFailed, "duplicate JSON object member %q", name)
			}
			seen[name] = true
			if err := walkJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return e.New(e.CodeVerifyFailed, "malformed JSON object")
		}
	case '[':
		for dec.More() {
			if err := walkJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return e.New(e.CodeVerifyFailed, "malformed JSON array")
		}
	default:
		return e.New(e.CodeVerifyFailed, "unexpected JSON delimiter %q", delim)
	}
	return nil
}

func requireJSONEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return e.New(e.CodeVerifyFailed, "authenticated document contains more than one JSON value")
	}
	return e.Wrap(e.CodeVerifyFailed, err, "checking authenticated JSON boundary")
}

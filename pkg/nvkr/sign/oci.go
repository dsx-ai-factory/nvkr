// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

// oci.go is the OCI signing/attestation adapter family: it binds a signature and
// an SLSA provenance attestation to an exact published artifact digest, and it
// records what was (or would be) executed.
//
// Two adapters implement OCISigner:
//
//   - DryRunSigner never touches a registry and never executes anything. It
//     records the exact argv that a real signer would run, plus the identity,
//     issuer and predicate digest that argv would bind. Its record is explicit
//     that nothing was signed, so it can never be mistaken for signed evidence.
//   - CosignOCISigner shells cosign through the package's CommandRunner seam.
//     Its stock path (ExecRunner) executes an exactly pinned cosign capability
//     through Linux anonymous-descriptor execution and fails closed elsewhere,
//     exactly like the qualified sign-blob/verify-blob adapters in this package.
//
// Registry credentials NEVER appear in argv or in a record. The bundle reader
// uses explicitly scoped in-memory credentials; subprocesses receive a private
// 0600 config.json in a 0700 DOCKER_CONFIG directory owned by the operation.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// Record schema versions. Records are wire objects consumed by later gates, so
// their schema version is part of the contract.
const (
	SigningRecordSchemaVersion      = "nvkr.dev/oci-signing-record/v1"
	VerificationRecordSchemaVersion = "nvkr.dev/oci-verification-record/v1"
)

const (
	// CosignAttestationType is cosign's `--type` token for SLSA provenance v1.
	CosignAttestationType = "slsaprovenance1"
	// ProvenancePredicateType is the in-toto predicateType that token selects.
	ProvenancePredicateType = "https://slsa.dev/provenance/v1"

	// PredicatePathPlaceholder stands in for the private predicate pathname in a
	// dry-run record: no such file is ever created, and a random temporary path
	// would make the record non-deterministic.
	PredicatePathPlaceholder = "${NVKR_PREDICATE_FILE}"

	maxOCIPredicateBytes int64 = 4 << 20
	maxCosignOutputBytes int64 = 8 << 20
	dockerConfigDirName        = "docker"
	dockerConfigFileName       = "config.json"
)

// Subject is the exact published OCI artifact a signature or attestation binds
// to. Ref is the digest-pinned reference and Digest is the same digest stated
// independently, so a caller that resolved the two from different sources (a
// publication ref file and a recomputed layout digest) cannot silently sign the
// wrong bytes.
type Subject struct {
	Ref    string // repository@sha256:<hex>
	Digest string // sha256:<hex>
}

// Repository returns the repository part of the subject reference.
func (s Subject) Repository() string {
	if at := strings.Index(s.Ref, "@"); at > 0 {
		return s.Ref[:at]
	}
	return ""
}

// Validate refuses any subject that is not exactly digest-pinned, and any
// subject whose reference digest disagrees with its stated digest.
func (s Subject) Validate() error {
	if strings.TrimSpace(s.Ref) != s.Ref || s.Ref == "" || strings.ContainsAny(s.Ref, " \t\r\n") {
		return e.New(e.CodeUsage, "OCI signing subject reference is empty or non-canonical")
	}
	if !canonical.IsDigest(s.Digest) {
		return e.New(e.CodeUnverifiedRef,
			"OCI signing subject digest %q is not an exact lowercase sha256 identity", s.Digest)
	}
	if strings.Count(s.Ref, "@") != 1 {
		return e.New(e.CodeUnpinnedSource,
			"OCI signing subject reference %q must be repository@sha256:<hex>", s.Ref)
	}
	at := strings.Index(s.Ref, "@")
	if at == 0 || strings.Contains(s.Ref[:at], "@") {
		return e.New(e.CodeUnpinnedSource,
			"OCI signing subject reference %q has no repository", s.Ref)
	}
	if s.Ref[at+1:] != s.Digest {
		return e.New(e.CodeVerifyFailed,
			"OCI signing subject reference digest %q does not equal the subject digest %q",
			s.Ref[at+1:], s.Digest)
	}
	return nil
}

// SigningRecord is the durable record of one sign+attest operation. In dry-run
// mode WouldRun is populated and Ran is empty; in cosign mode the opposite. A
// record is never evidence that a signature verifies — only a Verify result
// against a real registry is.
type SigningRecord struct {
	SchemaVersion   string     `json:"schemaVersion"`
	DryRun          bool       `json:"dryRun"`
	Ref             string     `json:"ref"`
	Digest          string     `json:"digest"`
	PredicateSHA256 string     `json:"predicateSha256"`
	PredicateType   string     `json:"predicateType"`
	Identity        string     `json:"identity"`
	Issuer          string     `json:"issuer"`
	WouldRun        [][]string `json:"wouldRun,omitempty"`
	Ran             [][]string `json:"ran,omitempty"`
	CosignVersion   string     `json:"cosignVersion,omitempty"`
}

// VerificationRecord is the durable record of one verification attempt. In
// dry-run mode SignatureVerified and AttestationVerified are always false: no
// registry was contacted and nothing was checked.
type VerificationRecord struct {
	SchemaVersion       string     `json:"schemaVersion"`
	DryRun              bool       `json:"dryRun"`
	Ref                 string     `json:"ref"`
	Digest              string     `json:"digest"`
	Identity            string     `json:"identity"`
	Issuer              string     `json:"issuer"`
	PredicateType       string     `json:"predicateType"`
	SignatureVerified   bool       `json:"signatureVerified"`
	AttestationVerified bool       `json:"attestationVerified"`
	ObservedIdentity    string     `json:"observedIdentity,omitempty"`
	ObservedIssuer      string     `json:"observedIssuer,omitempty"`
	WouldRun            [][]string `json:"wouldRun,omitempty"`
	Ran                 [][]string `json:"ran,omitempty"`
	CosignVersion       string     `json:"cosignVersion,omitempty"`
}

// VerifyPolicy is the exact identity policy a verification must enforce. Both
// fields are mandatory: an unpinned identity or issuer would accept any Fulcio
// certificate and is therefore not a trust decision at all.
type VerifyPolicy struct {
	// ExpectedPredicate optionally binds the exact production predicate, including Plan and invocation.
	ExpectedPredicate  []byte
	IdentityRegexp     string
	Issuer             string
	RequireAttestation bool
}

// OCISigner signs and attests an exact published OCI artifact, and verifies one.
type OCISigner interface {
	SignAndAttest(ctx context.Context, subject Subject, predicate []byte) (SigningRecord, error)
	Verify(ctx context.Context, subject Subject, policy VerifyPolicy) (VerificationRecord, error)
}

// Validate enforces the signing-record contract before it is published.
func (r SigningRecord) Validate() error {
	if r.SchemaVersion != SigningRecordSchemaVersion {
		return e.New(e.CodeInvalidCatalog,
			"signing record schemaVersion %q is unsupported (want %q)", r.SchemaVersion, SigningRecordSchemaVersion)
	}
	if err := (Subject{Ref: r.Ref, Digest: r.Digest}).Validate(); err != nil {
		return err
	}
	if !canonical.IsDigest(r.PredicateSHA256) {
		return e.New(e.CodeUnverifiedRef,
			"signing record predicateSha256 %q is not an exact lowercase sha256 identity", r.PredicateSHA256)
	}
	if r.PredicateType != ProvenancePredicateType {
		return e.New(e.CodeInvalidCatalog,
			"signing record predicateType %q is unsupported (want %q)", r.PredicateType, ProvenancePredicateType)
	}
	if strings.TrimSpace(r.Identity) == "" || strings.TrimSpace(r.Issuer) == "" {
		return e.New(e.CodeTrustPolicyMissing,
			"signing record requires both a certificate identity and an OIDC issuer")
	}
	if r.DryRun != (len(r.Ran) == 0) || r.DryRun != (len(r.WouldRun) > 0) {
		return e.New(e.CodeInvalidCatalog,
			"signing record must record wouldRun when dryRun and ran otherwise")
	}
	return nil
}

// Canonical renders the exact canonical JSON bytes of a validated record.
func (r SigningRecord) Canonical() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(r)
}

// Validate enforces the verification-record contract before it is published.
func (r VerificationRecord) Validate() error {
	if r.SchemaVersion != VerificationRecordSchemaVersion {
		return e.New(e.CodeInvalidCatalog,
			"verification record schemaVersion %q is unsupported (want %q)",
			r.SchemaVersion, VerificationRecordSchemaVersion)
	}
	if err := (Subject{Ref: r.Ref, Digest: r.Digest}).Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Identity) == "" || strings.TrimSpace(r.Issuer) == "" {
		return e.New(e.CodeTrustPolicyMissing,
			"verification record requires both a certificate identity and an OIDC issuer")
	}
	if r.DryRun && (r.SignatureVerified || r.AttestationVerified) {
		return e.New(e.CodeInvalidCatalog,
			"a dry-run verification record cannot claim a verified signature or attestation")
	}
	if r.DryRun != (len(r.Ran) == 0) || r.DryRun != (len(r.WouldRun) > 0) {
		return e.New(e.CodeInvalidCatalog,
			"verification record must record wouldRun when dryRun and ran otherwise")
	}
	return nil
}

// Canonical renders the exact canonical JSON bytes of a validated record.
func (r VerificationRecord) Canonical() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(r)
}

// ociSignArgs is shared by every adapter so a dry run describes the truth.
func ociSignArgs(ref string) []string {
	return []string{"sign", "--yes", "--new-bundle-format", ref}
}

func ociAttestArgs(ref, predicatePath string) []string {
	return []string{"attest", "--yes", "--new-bundle-format", "--type", CosignAttestationType, "--predicate", predicatePath, ref}
}

// argv renders a complete recorded command line: the executable followed by its
// arguments, so the record is a faithful description of one invocation.
func argv(binary string, args []string) []string {
	out := []string{binary}
	return append(out, args...)
}

func requireOCIPredicate(predicate []byte) error {
	return requireOCIPredicateWithLimit(predicate, maxOCIPredicateBytes, e.CodeProvenanceMissing, "provenance predicate")
}

// Provenance and CycloneDX have separate bounded document contracts. Share JSON
// admission without imposing the provenance limit on a complete inventory.
func requireOCIPredicateWithLimit(predicate []byte, limit int64, code e.Code, label string) error {
	if len(predicate) == 0 {
		return e.New(code, "refusing to attest an empty %s", label)
	}
	if int64(len(predicate)) > limit {
		return e.New(code, "%s exceeds the attestation size limit", label)
	}
	if err := canonical.ValidateJSONDocument(predicate); err != nil {
		return e.Wrap(code, err, "%s is not a single well-formed JSON document", label)
	}
	return nil
}

// resolvePolicy fills a policy from the adapter's configured identity/issuer and
// then requires both. An unpinned identity can never authorize a publication.
func resolvePolicy(policy VerifyPolicy, identity, issuer string) (VerifyPolicy, error) {
	if len(policy.ExpectedPredicate) > 0 {
		if !policy.RequireAttestation || int64(len(policy.ExpectedPredicate)) > maxOCIPredicateBytes {
			return VerifyPolicy{}, e.New(e.CodeTrustPolicyMissing, "exact predicate requires bounded attestation verification")
		}
		if err := canonical.ValidateJSONDocument(policy.ExpectedPredicate); err != nil {
			return VerifyPolicy{}, e.Wrap(e.CodeTrustPolicyMissing, err, "invalid expected predicate")
		}
		policy.ExpectedPredicate = append([]byte(nil), policy.ExpectedPredicate...)
	}
	if strings.TrimSpace(policy.IdentityRegexp) == "" {
		policy.IdentityRegexp = identity
	}
	if strings.TrimSpace(policy.Issuer) == "" {
		policy.Issuer = issuer
	}
	policy.IdentityRegexp = strings.TrimSpace(policy.IdentityRegexp)
	policy.Issuer = strings.TrimSpace(policy.Issuer)
	if policy.IdentityRegexp == "" || policy.Issuer == "" {
		return VerifyPolicy{}, e.New(e.CodeTrustPolicyMissing,
			"OCI verification requires both a certificate identity regexp and an OIDC issuer")
	}
	if _, err := regexp.Compile(policy.IdentityRegexp); err != nil {
		return VerifyPolicy{}, e.Wrap(e.CodeTrustPolicyMissing, err, "invalid certificate identity policy")
	}
	return policy, nil
}

// DryRunSigner records what a real signer would run and executes nothing. It is
// available on every platform because it has no execution path at all.
type DryRunSigner struct {
	identity string
	issuer   string
}

var _ OCISigner = (*DryRunSigner)(nil)

// NewDryRunSigner requires the identity and issuer the real signing would bind,
// so a dry-run record names the exact trust policy it stands in for.
func NewDryRunSigner(identity, issuer string) (*DryRunSigner, error) {
	identity = strings.TrimSpace(identity)
	issuer = strings.TrimSpace(issuer)
	if identity == "" || issuer == "" {
		return nil, e.New(e.CodeTrustPolicyMissing,
			"dry-run OCI signing requires the certificate identity and OIDC issuer it stands in for")
	}
	return &DryRunSigner{identity: identity, issuer: issuer}, nil
}

// SignAndAttest records the exact sign and attest invocations without running
// them and without contacting any registry.
func (s *DryRunSigner) SignAndAttest(ctx context.Context, subject Subject, predicate []byte) (SigningRecord, error) {
	if s == nil {
		return SigningRecord{}, e.New(e.CodeInternal, "dry-run OCI signer is not initialized")
	}
	if err := contextError(ctx); err != nil {
		return SigningRecord{}, err
	}
	if err := subject.Validate(); err != nil {
		return SigningRecord{}, err
	}
	if err := requireOCIPredicate(predicate); err != nil {
		return SigningRecord{}, err
	}
	record := SigningRecord{
		SchemaVersion:   SigningRecordSchemaVersion,
		DryRun:          true,
		Ref:             subject.Ref,
		Digest:          subject.Digest,
		PredicateSHA256: digestBytes(predicate),
		PredicateType:   ProvenancePredicateType,
		Identity:        s.identity,
		Issuer:          s.issuer,
		WouldRun: [][]string{
			argv(DefaultBinary, ociSignArgs(subject.Ref)),
			argv(DefaultBinary, ociAttestArgs(subject.Ref, PredicatePathPlaceholder)),
		},
	}
	if err := record.Validate(); err != nil {
		return SigningRecord{}, err
	}
	return record, nil
}

// Verify records the exact verification invocations without running them.
// Registry discovery uses HTTP and has no subprocess argv. The returned record
// never claims a verified signature or attestation.
func (s *DryRunSigner) Verify(ctx context.Context, subject Subject, policy VerifyPolicy) (VerificationRecord, error) {
	if s == nil {
		return VerificationRecord{}, e.New(e.CodeInternal, "dry-run OCI signer is not initialized")
	}
	if err := contextError(ctx); err != nil {
		return VerificationRecord{}, err
	}
	if err := subject.Validate(); err != nil {
		return VerificationRecord{}, err
	}
	resolved, err := resolvePolicy(policy, s.identity, s.issuer)
	if err != nil {
		return VerificationRecord{}, err
	}
	record := VerificationRecord{
		SchemaVersion: VerificationRecordSchemaVersion,
		DryRun:        true,
		Ref:           subject.Ref,
		Digest:        subject.Digest,
		Identity:      resolved.IdentityRegexp,
		Issuer:        resolved.Issuer,
		PredicateType: ProvenancePredicateType,
		WouldRun: [][]string{
			argv(DefaultBinary, ociVerifyBundleArgs(SignatureBundlePlaceholder, subject.Digest, CosignSignaturePredicateType, resolved)),
		},
	}
	if resolved.RequireAttestation {
		record.WouldRun = append(record.WouldRun,
			argv(DefaultBinary, ociVerifyBundleArgs(ProvenanceBundlePlaceholder, subject.Digest, ProvenancePredicateType, resolved)))
	}
	if err := record.Validate(); err != nil {
		return VerificationRecord{}, err
	}
	return record, nil
}

// Credential is one registry credential. Direct reads scope it to the subject
// registry; subprocesses receive it through a private Docker config file. It
// never reaches argv, a record, or the child's inherited ambient environment.
type Credential struct {
	Registry string
	Username string
	Password string
}

// Validate admits the existing complete-or-empty registry credential shape.
// It performs no I/O and grants no registry host or signing authority.
func (c Credential) Validate() error { return c.validate() }

func (c Credential) empty() bool {
	return c.Registry == "" && c.Username == "" && c.Password == ""
}

func (c Credential) validate() error {
	if c.empty() {
		return nil
	}
	if strings.TrimSpace(c.Registry) == "" || strings.TrimSpace(c.Username) == "" || c.Password == "" {
		return e.New(e.CodeUsage,
			"a registry credential requires a registry, a username, and a password")
	}
	return nil
}

// EnvCommandRunner is the optional environment-aware extension of CommandRunner.
// The OCI adapter must control DOCKER_CONFIG and the ambient cosign controls, so
// an injected runner that implements this interface observes the exact
// environment the stock path would use. Runners are part of their caller's
// trusted computing base.
type EnvCommandRunner interface {
	RunEnv(ctx context.Context, binary string, args, env []string, stdout, stderr io.Writer) error
}

// EnvRunnerFunc adapts a function to EnvCommandRunner.
type EnvRunnerFunc func(ctx context.Context, binary string, args, env []string, stdout, stderr io.Writer) error

// RunEnv implements EnvCommandRunner.
func (f EnvRunnerFunc) RunEnv(ctx context.Context, binary string, args, env []string, stdout, stderr io.Writer) error {
	return f(ctx, binary, args, env, stdout, stderr)
}

// Run implements CommandRunner by discarding the environment; the OCI adapter
// always prefers RunEnv.
func (f EnvRunnerFunc) Run(ctx context.Context, binary string, args []string, stdout, stderr io.Writer) error {
	return f(ctx, binary, args, nil, stdout, stderr)
}

// CosignOCIOptions configures the cosign OCI adapter.
type CosignOCIOptions struct {
	Binary string
	// BinaryDigest pins the exact cosign executable. It is mandatory for stock
	// execution and unused by an injected runner.
	BinaryDigest string
	Identity     string
	Issuer       string
	Credential   Credential
	// Runner selects the subprocess seam. nil selects the stock pinned adapter.
	Runner CommandRunner
}

// CosignOCISigner signs and attests published OCI artifacts with cosign.
type CosignOCISigner struct {
	binary       string
	binaryDigest string
	runner       CommandRunner
	identity     string
	issuer       string
	credential   Credential
	// Tests may replace descriptive transport independently of crypto. Production
	// always constructs one private, lazy source for the complete Verify call.
	bundleSource func(context.Context, Subject, Credential) (*ociBundleSource, error)

	versionOnce  sync.Mutex
	version      string
	versionKnown bool
}

var _ OCISigner = (*CosignOCISigner)(nil)

// NewCosignOCISigner constructs the adapter. Construction deliberately succeeds
// on every platform so argv and record contracts stay testable everywhere; the
// stock execution path itself fails closed with CodeEngineCapability off Linux.
func NewCosignOCISigner(options CosignOCIOptions) (*CosignOCISigner, error) {
	binary := strings.TrimSpace(options.Binary)
	if binary == "" {
		binary = DefaultBinary
	}
	identity := strings.TrimSpace(options.Identity)
	issuer := strings.TrimSpace(options.Issuer)
	if identity == "" || issuer == "" {
		return nil, e.New(e.CodeTrustPolicyMissing,
			"cosign OCI signing requires an expected certificate identity and OIDC issuer")
	}
	if err := options.Credential.validate(); err != nil {
		return nil, err
	}
	runner := options.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	if options.BinaryDigest != "" && !canonical.IsDigest(options.BinaryDigest) {
		return nil, e.New(e.CodeUnpinnedInput,
			"cosign OCI signing binary digest must be an exact lowercase sha256 identity")
	}
	return &CosignOCISigner{
		binary:       binary,
		binaryDigest: options.BinaryDigest,
		runner:       runner,
		identity:     identity,
		issuer:       issuer,
		credential:   options.Credential,
	}, nil
}

// SignAndAttest runs `cosign sign` and `cosign attest` against the exact subject
// digest, passing the predicate through a private 0600 file whose bytes are
// re-checked after cosign exits.
func (s *CosignOCISigner) SignAndAttest(ctx context.Context, subject Subject, predicate []byte) (SigningRecord, error) {
	if s == nil || s.runner == nil {
		return SigningRecord{}, e.New(e.CodeInternal, "cosign OCI signer is not initialized")
	}
	if err := contextError(ctx); err != nil {
		return SigningRecord{}, err
	}
	if err := s.admit(); err != nil {
		return SigningRecord{}, err
	}
	if err := subject.Validate(); err != nil {
		return SigningRecord{}, err
	}
	if err := requireOCIPredicate(predicate); err != nil {
		return SigningRecord{}, err
	}
	exactPredicate := append([]byte(nil), predicate...)

	workspace, cleanup, err := newPrivateWorkspace("nvkr-cosign-oci-sign-*")
	if err != nil {
		return SigningRecord{}, err
	}
	defer cleanup()
	predicatePath := filepath.Join(workspace, "predicate.json")
	if err := writePrivateFile(ctx, predicatePath, exactPredicate); err != nil {
		return SigningRecord{}, err
	}
	dockerConfig, err := s.writeDockerConfig(ctx, workspace)
	if err != nil {
		return SigningRecord{}, err
	}
	environment := ociCosignEnvironment(os.Environ(), dockerConfig)

	signArgs := ociSignArgs(subject.Ref)
	attestArgs := ociAttestArgs(subject.Ref, predicatePath)
	for _, args := range [][]string{signArgs, attestArgs} {
		var stdout, stderr bytes.Buffer
		if err := s.run(ctx, args, environment, &stdout, &stderr); err != nil {
			if ctxErr := contextError(ctx); ctxErr != nil {
				return SigningRecord{}, ctxErr
			}
			return SigningRecord{}, e.Wrap(e.CodeSignatureInvalid, err,
				"cosign %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
		}
	}
	if err := assertPrivateFileUnchanged(predicatePath, exactPredicate); err != nil {
		return SigningRecord{}, err
	}

	record := SigningRecord{
		SchemaVersion:   SigningRecordSchemaVersion,
		Ref:             subject.Ref,
		Digest:          subject.Digest,
		PredicateSHA256: digestBytes(exactPredicate),
		PredicateType:   ProvenancePredicateType,
		Identity:        s.identity,
		Issuer:          s.issuer,
		Ran: [][]string{
			argv(s.binary, signArgs),
			argv(s.binary, attestArgs),
		},
		CosignVersion: s.detectVersion(ctx, environment),
	}
	if err := record.Validate(); err != nil {
		return SigningRecord{}, err
	}
	return record, nil
}

// Verify downloads only standard Sigstore bundles and authenticates their exact
// bytes with cosign. Certificate claims come from the verified bundle, never
// from presentation output or caller assertions. No artifact copy or legacy
// attachment fallback is used.
func (s *CosignOCISigner) Verify(ctx context.Context, subject Subject, policy VerifyPolicy) (VerificationRecord, error) {
	if s == nil || s.runner == nil {
		return VerificationRecord{}, e.New(e.CodeInternal, "cosign OCI signer is not initialized")
	}
	if err := contextError(ctx); err != nil {
		return VerificationRecord{}, err
	}
	if err := s.admit(); err != nil {
		return VerificationRecord{}, err
	}
	if err := subject.Validate(); err != nil {
		return VerificationRecord{}, err
	}
	resolved, err := resolvePolicy(policy, s.identity, s.issuer)
	if err != nil {
		return VerificationRecord{}, err
	}
	if _, err := admitOCIRegistry(subject, s.credential); err != nil {
		return VerificationRecord{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	workspace, cleanup, err := newPrivateWorkspace("nvkr-cosign-oci-verify-*")
	if err != nil {
		return VerificationRecord{}, err
	}
	defer cleanup()
	dockerConfig, err := s.writeDockerConfig(ctx, workspace)
	if err != nil {
		return VerificationRecord{}, err
	}
	environment := ociCosignEnvironment(os.Environ(), dockerConfig)
	source, err := s.newOCIBundleSource(ctx, subject)
	if err != nil {
		return VerificationRecord{}, err
	}
	defer source.close()

	record := VerificationRecord{
		SchemaVersion: VerificationRecordSchemaVersion,
		Ref:           subject.Ref,
		Digest:        subject.Digest,
		Identity:      resolved.IdentityRegexp,
		Issuer:        resolved.Issuer,
		PredicateType: ProvenancePredicateType,
	}

	// The expected production predicate belongs to the provenance attestation;
	// the artifact signature has its own distinct predicate and evidence type.
	signaturePolicy := resolved
	signaturePolicy.ExpectedPredicate = nil
	identity, issuer, ran, err := s.verifyOCIType(ctx, source, subject, CosignSignaturePredicateType, signaturePolicy, workspace, environment)
	if err != nil {
		return VerificationRecord{}, err
	}
	record.Ran = ran
	record.SignatureVerified = true
	record.ObservedIdentity = identity
	record.ObservedIssuer = issuer
	if resolved.RequireAttestation {
		_, _, ran, err := s.verifyOCIType(ctx, source, subject, ProvenancePredicateType, resolved, workspace, environment)
		if err != nil {
			return VerificationRecord{}, err
		}
		record.Ran = append(record.Ran, ran...)
		record.AttestationVerified = true
	}
	record.CosignVersion = s.detectVersion(ctx, environment)
	if err := contextError(ctx); err != nil {
		return VerificationRecord{}, err
	}
	if err := record.Validate(); err != nil {
		return VerificationRecord{}, err
	}
	return record, nil
}

// capture runs one cosign subcommand and returns its stdout, mapping a non-zero
// exit to the crypto-gate error code.
func (s *CosignOCISigner) capture(ctx context.Context, args, environment []string) ([]byte, error) {
	var stdout, stderr cosignOutputBuffer
	if err := s.run(ctx, args, environment, &stdout, &stderr); err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, e.Wrap(e.CodeSignatureInvalid, err,
			"cosign %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded || stderr.exceeded {
		return nil, e.New(e.CodeSignatureInvalid, "cosign %s produced an oversized report", args[0])
	}
	return stdout.Bytes(), nil
}

// Bound memory while the subprocess is writing, including a registry-controlled
// stream of bundles. A misbehaving injected runner cannot suppress the limit.
type cosignOutputBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *cosignOutputBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *cosignOutputBuffer) String() string { return b.buffer.String() }

func (b *cosignOutputBuffer) Write(p []byte) (int, error) {
	remaining := int(maxCosignOutputBytes) - b.buffer.Len()
	if len(p) > remaining {
		b.exceeded = true
		n, _ := b.buffer.Write(p[:remaining])
		return n, io.ErrShortWrite
	}
	return b.buffer.Write(p)
}

// detectVersion is a best-effort introspection of the cosign build actually in
// use. It is never authority, so a failure leaves the field empty instead of
// failing the operation, and it is not recorded as a signing invocation.
func (s *CosignOCISigner) detectVersion(ctx context.Context, environment []string) string {
	s.versionOnce.Lock()
	defer s.versionOnce.Unlock()
	if s.versionKnown {
		return s.version
	}
	s.versionKnown = true
	var stdout, stderr bytes.Buffer
	if err := s.run(ctx, []string{"version", "--json"}, environment, &stdout, &stderr); err != nil {
		return ""
	}
	var report struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &report); err != nil {
		return ""
	}
	s.version = strings.TrimSpace(report.GitVersion)
	return s.version
}

// admit proves the configured execution lane can actually run before any
// predicate, credential, registry, or output work happens. Unsupported lanes
// return a coded capability error and produce no partial output.
func (s *CosignOCISigner) admit() error {
	if _, ok := s.runner.(EnvCommandRunner); ok {
		return nil
	}
	if !isExecRunner(s.runner) {
		return nil
	}
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return e.Wrap(e.CodeEngineCapability, err,
			"qualified OCI cosign execution requires Linux anonymous descriptor execution")
	}
	if !canonical.IsDigest(s.binaryDigest) {
		return e.New(e.CodeUnpinnedInput,
			"qualified OCI cosign execution refuses an ambient unpinned executable; configure its exact sha256 digest")
	}
	return nil
}

// run dispatches one invocation. An injected environment-aware runner sees the
// exact computed environment; a plain injected runner is diagnostic and does
// not; the stock ExecRunner selects the pinned, Linux-only execution path.
func (s *CosignOCISigner) run(ctx context.Context, args, environment []string, stdout, stderr io.Writer) error {
	if runner, ok := s.runner.(EnvCommandRunner); ok {
		return runner.RunEnv(ctx, s.binary, args, environment, stdout, stderr)
	}
	if !isExecRunner(s.runner) {
		return s.runner.Run(ctx, s.binary, args, stdout, stderr)
	}
	return s.runStock(ctx, args, environment, stdout, stderr)
}

// runStock executes the exactly pinned cosign capability. Off Linux there is no
// anonymous-descriptor execution primitive, so it fails closed before any
// registry, credential, or predicate work is observable.
func (s *CosignOCISigner) runStock(ctx context.Context, args, environment []string, stdout, stderr io.Writer) error {
	if err := heldfile.RequireExecutableSupport(); err != nil {
		return e.Wrap(e.CodeEngineCapability, err,
			"qualified OCI cosign execution requires Linux anonymous descriptor execution")
	}
	if !canonical.IsDigest(s.binaryDigest) {
		return e.New(e.CodeUnpinnedInput,
			"qualified OCI cosign execution refuses an ambient unpinned executable; configure its exact sha256 digest")
	}
	executable, cleanup, err := snapshotPinnedCosignExecutableHeldContext(ctx, s.binary, s.binaryDigest)
	if err != nil {
		return err
	}
	defer cleanup()
	return heldfile.RunContext(ctx, executable, args, heldfile.RunOptions{
		Stdout: stdout, Stderr: stderr, Env: environment,
	})
}

// writeDockerConfig materializes the registry credential as a private Docker
// config directory and returns its path. Empty credentials produce an explicit
// empty auth map, preventing ambient Docker or Podman credential discovery.
func (s *CosignOCISigner) writeDockerConfig(ctx context.Context, workspace string) (string, error) {
	dir := filepath.Join(workspace, dockerConfigDirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", e.Wrap(e.CodeInternal, err, "creating private registry credential directory")
	}
	auths := map[string]any{}
	if !s.credential.empty() {
		auth := base64.StdEncoding.EncodeToString([]byte(s.credential.Username + ":" + s.credential.Password))
		auths[s.credential.Registry] = map[string]string{"auth": auth}
	}
	document, err := json.Marshal(map[string]any{"auths": auths})
	if err != nil {
		return "", e.Wrap(e.CodeInternal, err, "encoding private registry credential")
	}
	if err := writePrivateFile(ctx, filepath.Join(dir, dockerConfigFileName), document); err != nil {
		return "", err
	}
	return dir, nil
}

// ociCosignEnvironment is the qualified cosign environment plus exclusive
// control of Docker registry configuration. When a private workspace is supplied,
// HOME is replaced because the upstream Docker keychain checks HOME before
// DOCKER_CONFIG. Blob adapters without a registry workspace keep their ambient
// home and cache locations. It keeps the ambient scrub of the
// sign-blob/verify-blob adapters (loader injection, COSIGN_*, SIGSTORE_*, TUF_*,
// SOURCE_DATE_EPOCH) and additionally removes ambient Docker/Podman registry
// configuration controls.
// ACTIONS_ID_TOKEN_REQUEST_URL/TOKEN are deliberately preserved: they are how
// keyless signing obtains its workload identity in GitHub Actions.
func ociCosignEnvironment(base []string, dockerConfigDir string) []string {
	scrubbed := qualifiedCosignEnvironment(base)
	out := make([]string, 0, len(scrubbed))
	for _, entry := range scrubbed {
		name := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			name = entry[:i]
		}
		switch strings.ToUpper(name) {
		case "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR":
			if dockerConfigDir != "" {
				continue
			}
		case "DOCKER_CONFIG", "DOCKER_AUTH_CONFIG", "REGISTRY_AUTH_FILE", "REGISTRY_AUTH_TOKEN":
			continue
		}
		out = append(out, entry)
	}
	if dockerConfigDir != "" {
		out = append(out, "HOME="+filepath.Dir(dockerConfigDir), "DOCKER_CONFIG="+dockerConfigDir)
	}
	return out
}

type inTotoStatement struct {
	Predicate     json.RawMessage `json:"predicate"`
	Type          string          `json:"_type"`
	PredicateType string          `json:"predicateType"`
	Subject       []struct {
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
}

func newPrivateWorkspace(pattern string) (string, func(), error) {
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", func() {}, e.Wrap(e.CodeInternal, err, "creating private OCI signing directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return "", func() {}, e.Wrap(e.CodeInternal, err, "sealing private OCI signing directory")
	}
	return dir, cleanup, nil
}

// writePrivateFile writes owner-only bytes that a child process reads by path.
// The file is created exclusively so it can never adopt a pre-existing inode.
func writePrivateFile(ctx context.Context, path string, data []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating private OCI signing material")
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return e.Wrap(e.CodeInternal, writeErr, "sealing private OCI signing material")
	}
	return nil
}

// assertPrivateFileUnchanged proves the child consumed the exact bytes NVKR
// wrote: a predicate that changed mid-run would attest something else.
func assertPrivateFileUnchanged(path string, want []byte) error {
	got, err := os.ReadFile(path) //nolint:gosec // owner-only path this package created
	if err != nil {
		return e.Wrap(e.CodeSignatureInvalid, err, "rechecking the private attestation predicate")
	}
	if !bytes.Equal(got, want) {
		return e.New(e.CodeSignatureInvalid,
			"the private attestation predicate changed while cosign was running")
	}
	return nil
}

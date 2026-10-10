// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"debug/elf"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

const (
	maxVerificationMaterialBytes int64 = 32 << 20
	maxCosignExecutableBytes     int64 = 256 << 20
)

// CommandRunner is the subprocess seam beneath CosignVerifier. ExecRunner is the
// production adapter; RunnerFunc is the test adapter. Identity is never returned by
// this seam: CosignVerifier derives it from the exact protocol-selected certificate
// (the v0.3 bundle leaf or matching held legacy sidecar).
type CommandRunner interface {
	Run(context.Context, string, []string, io.Writer, io.Writer) error
}

// RunnerFunc adapts a function to CommandRunner for tests and controlled embedding.
type RunnerFunc func(context.Context, string, []string, io.Writer, io.Writer) error

func (f RunnerFunc) Run(ctx context.Context, binary string, args []string, stdout, stderr io.Writer) error {
	return f(ctx, binary, args, stdout, stderr)
}

// ExecRunner is the production subprocess adapter.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, binary string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// runHeld is the only stock qualified-verification subprocess path. ExtraFiles
// become child descriptors 3..N. Linux executes the sealed static ELF capability
// and opens verification inputs through its inherited descriptor namespace, so no
// mutable pathname is reopened. Darwin deliberately fails CodeEngineCapability:
// its /dev/fd mount is noexec and it has no fexecve/execveat equivalent.
func (ExecRunner) runHeld(ctx context.Context, executable *heldfile.File, args []string, stdin io.Reader,
	inputs []*heldfile.File, stdout, stderr io.Writer,
) error {
	descriptors := make([]heldfile.Descriptor, len(inputs))
	for i := range inputs {
		descriptors[i] = inputs[i]
	}
	return heldfile.RunContext(ctx, executable, args, heldfile.RunOptions{
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
		Env: qualifiedCosignEnvironment(os.Environ()),
	}, descriptors...)
}

// qualifiedCosignEnvironment removes ambient controls that cosign/viper would
// otherwise reinterpret as signing, verification, or trust-anchor options. Stock
// authority is configured only by explicit validated arguments. SOURCE_DATE_EPOCH
// is also excluded because cosign uses it as its notion of now.
func qualifiedCosignEnvironment(base []string) []string {
	base = heldfile.SanitizedEnvironment(base)
	out := make([]string, 0, len(base))
	for _, entry := range base {
		name := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			name = entry[:i]
		}
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "COSIGN_") || strings.HasPrefix(upper, "SIGSTORE_") ||
			strings.HasPrefix(upper, "TUF_") || upper == "SOURCE_DATE_EPOCH" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// CosignVerifier is the qualified keyless DocumentVerifier adapter. The stock
// adapter sends the authenticator's exact document bytes on stdin and exposes
// signature material plus the pinned cosign executable only as inherited,
// unlinked, read-only regular-file descriptors. It then captures signer claims
// from the exact certificate cosign used: the bundle-embedded leaf for Sigstore
// v0.3, or the held detached certificate for the legacy protocol.
type CosignVerifier struct {
	binary               string
	runner               CommandRunner
	expectedBinaryDigest string
	requiresBinaryPin    bool
}

// NewPinnedCosignVerifier constructs the production adapter. It rejects platforms
// without an exact descriptor-exec primitive immediately, before any
// authentication request can be read. On Linux each verification resolves the
// configured binary, snapshots and checks its exact static-ELF bytes, then
// executes only the sealed inherited descriptor.
func NewPinnedCosignVerifier(binary, expectedDigest string) (*CosignVerifier, error) {
	return newPinnedCosignVerifier(binary, expectedDigest, ExecRunner{})
}

func newPinnedCosignVerifier(binary, expectedDigest string, runner CommandRunner) (*CosignVerifier, error) {
	if strings.TrimSpace(binary) == "" {
		binary = DefaultBinary
	}
	if runner == nil {
		return nil, e.New(e.CodeInternal, "qualified cosign verifier runner is nil")
	}
	if !canonical.IsDigest(expectedDigest) {
		return nil, e.New(e.CodeUnpinnedInput,
			"qualified cosign verifier requires an exact lowercase sha256 binary digest")
	}
	if isExecRunner(runner) {
		if err := heldfile.RequireExecutableSupport(); err != nil {
			return nil, e.Wrap(e.CodeEngineCapability, err,
				"qualified cosign verifier requires Linux anonymous descriptor execution")
		}
	}
	return &CosignVerifier{binary: binary, runner: runner,
		expectedBinaryDigest: expectedDigest, requiresBinaryPin: true}, nil
}

func isExecRunner(runner CommandRunner) bool {
	switch runner.(type) {
	case ExecRunner, *ExecRunner:
		return true
	default:
		return false
	}
}

var _ trust.DocumentVerifier = (*CosignVerifier)(nil)

// VerifyDocument implements trust.DocumentVerifier. A nil error means cosign
// verified the exact held inputs and the returned claims were parsed from the
// same exact certificate selected by that protocol; it does not by itself
// authorize the signer role.
func (v *CosignVerifier) VerifyDocument(ctx context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	if v == nil || v.runner == nil {
		return trust.VerificationEvidence{}, e.New(e.CodeInternal, "qualified cosign verifier is not initialized")
	}
	if ctx == nil {
		return trust.VerificationEvidence{}, e.New(e.CodeUsage,
			"qualified cosign verification requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return trust.VerificationEvidence{}, e.Wrap(e.CodeTimeout, err,
			"qualified cosign verification canceled")
	}
	if err := validateTrustedRootPinConfiguration(req.Signature, req.TrustedRootDigest, req.Offline); err != nil {
		return trust.VerificationEvidence{}, err
	}
	if isExecRunner(v.runner) {
		return v.verifyDocumentHeld(ctx, req)
	}
	return v.verifyDocumentInjected(ctx, req)
}

// verifyDocumentHeld is the production authority path. It never asks the child to
// reopen an NVKR-created pathname: the subject travels on stdin and every other
// input, including the executable, travels through an inherited descriptor.
func (v *CosignVerifier) verifyDocumentHeld(ctx context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	if !v.requiresBinaryPin || v.expectedBinaryDigest == "" {
		return trust.VerificationEvidence{}, e.New(e.CodeUnpinnedInput,
			"qualified cosign verification refuses an ambient unpinned executable; configure its exact sha256 digest")
	}
	if req.ExactBytes == nil {
		return trust.VerificationEvidence{}, e.New(e.CodeSignatureInvalid,
			"stock qualified verification requires authenticator-supplied exact document bytes")
	}
	executable, executableCleanup, err := snapshotPinnedCosignExecutableHeldContext(ctx, v.binary, v.expectedBinaryDigest)
	if err != nil {
		return trust.VerificationEvidence{}, err
	}
	defer executableCleanup()

	material, cleanup, err := snapshotVerificationMaterialHeldContext(ctx, req.Signature, req.TrustedRootDigest)
	if err != nil {
		return trust.VerificationEvidence{}, err
	}
	defer cleanup()
	req.SnapshotPath = "-"
	req.Signature = material.paths
	args, err := qualifiedVerifyBlobArgs(req, material.newBundle)
	if err != nil {
		return trust.VerificationEvidence{}, err
	}
	var stdout, stderr bytes.Buffer
	// Reaching this branch proves v.runner has the stock ExecRunner concrete
	// identity. Dispatch through a fresh stateless value so a wrapper cannot
	// accidentally become production authority through interface substitution.
	runner := ExecRunner{}
	if err := runner.runHeld(ctx, executable, args, bytes.NewReader(req.ExactBytes), material.files, &stdout, &stderr); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return trust.VerificationEvidence{}, e.Wrap(e.CodeTimeout, ctxErr,
				"qualified cosign verification canceled")
		}
		return trust.VerificationEvidence{}, e.Wrap(e.CodeSignatureInvalid, err,
			"qualified cosign verify-blob failed: %s", strings.TrimSpace(stderr.String()))
	}
	if err := material.assertUnchangedContext(ctx); err != nil {
		return trust.VerificationEvidence{}, err
	}
	return verificationEvidenceFromCertificate(req, material.certificate, material.signature,
		material.certificateBlob, material.bundle, material.trustedRoot)
}

// verifyDocumentInjected preserves the narrow custom-runner seam for tests and
// controlled embedding. The injected runner is explicitly part of that caller's
// TCB and is never selected by a stock command.
func (v *CosignVerifier) verifyDocumentInjected(ctx context.Context, req trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	binary := v.binary
	if v.requiresBinaryPin {
		if v.expectedBinaryDigest == "" {
			return trust.VerificationEvidence{}, e.New(e.CodeUnpinnedInput,
				"qualified cosign verification refuses an ambient unpinned executable; configure its exact sha256 digest")
		}
		pinnedBinary, binaryCleanup, err := snapshotPinnedCosignExecutableNamedDiagnostic(ctx, v.binary, v.expectedBinaryDigest)
		if err != nil {
			return trust.VerificationEvidence{}, err
		}
		defer binaryCleanup()
		binary = pinnedBinary
	}
	documentBytes, err := readPrivateSnapshotContext(ctx, req.SnapshotPath)
	if err != nil {
		return trust.VerificationEvidence{}, err
	}
	if req.ExactBytes != nil && !bytes.Equal(documentBytes, req.ExactBytes) {
		return trust.VerificationEvidence{}, e.New(e.CodeSignatureInvalid,
			"private document snapshot does not match authenticator-supplied exact bytes")
	}
	material, cleanup, err := snapshotVerificationMaterialContext(ctx, req.Signature, req.TrustedRootDigest)
	if err != nil {
		return trust.VerificationEvidence{}, err
	}
	defer cleanup()
	req.Signature = material.paths

	args, err := qualifiedVerifyBlobArgs(req, material.newBundle)
	if err != nil {
		return trust.VerificationEvidence{}, err
	}
	var stdout, stderr bytes.Buffer
	if err := v.runner.Run(ctx, binary, args, &stdout, &stderr); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return trust.VerificationEvidence{}, e.Wrap(e.CodeTimeout, ctxErr,
				"qualified cosign verification canceled")
		}
		return trust.VerificationEvidence{}, e.Wrap(e.CodeSignatureInvalid, err,
			"qualified cosign verify-blob failed: %s", strings.TrimSpace(stderr.String()))
	}
	if err := assertPrivateBytesContext(ctx, req.SnapshotPath, documentBytes, "document snapshot"); err != nil {
		return trust.VerificationEvidence{}, err
	}
	if err := material.assertUnchangedContext(ctx); err != nil {
		return trust.VerificationEvidence{}, err
	}

	return verificationEvidenceFromCertificate(req, material.certificate, material.signature,
		material.certificateBlob, material.bundle, material.trustedRoot)
}

func verificationEvidenceFromCertificate(req trust.DocumentVerificationRequest, certificate, signature,
	certificateBlob, bundle, trustedRoot []byte,
) (trust.VerificationEvidence, error) {
	cert, err := parseCertificate(certificate)
	if err != nil {
		return trust.VerificationEvidence{}, err
	}
	identities := certificateIdentities(cert)
	issuer, err := certificateOIDCIssuer(cert)
	if err != nil {
		return trust.VerificationEvidence{}, err
	}
	if !containsAny(identities, req.AllowedIdentities) {
		return trust.VerificationEvidence{}, untrustedCertificateError(req.Role,
			"certificate cosign verified has identities %q, none allowed for %s",
			strings.Join(identities, ", "), req.Role)
	}
	if issuer != req.OIDCIssuer {
		return trust.VerificationEvidence{}, untrustedCertificateError(req.Role,
			"certificate cosign verified has OIDC issuer %q, expected %q for %s",
			issuer, req.OIDCIssuer, req.Role)
	}
	certSum := sha256.Sum256(cert.Raw)
	return trust.VerificationEvidence{
		CertificateIdentities: identities,
		OIDCIssuer:            issuer,
		CertificateSHA256:     "sha256:" + hex.EncodeToString(certSum[:]),
		SignatureSHA256:       digestOptionalMaterial(signature),
		CertificateBlobSHA256: digestOptionalMaterial(certificateBlob),
		BundleSHA256:          digestOptionalMaterial(bundle),
		TrustedRootBlobSHA256: digestOptionalMaterial(trustedRoot),
	}, nil
}

func digestOptionalMaterial(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateTrustedRootPinConfiguration(material trust.SignatureMaterial, expectedDigest string, offline bool) error {
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
	if !canonical.IsDigest(digest) {
		return e.New(e.CodeUnpinnedInput,
			"operator-selected trusted root requires an exact lowercase sha256 digest")
	}
	return nil
}

func pinnedBytes(label string, raw []byte, want string) error {
	sum := sha256.Sum256(raw)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if got != want {
		return e.New(e.CodeUnpinnedInput,
			"%s digest %s does not match pinned %s", label, got, want)
	}
	return nil
}

// snapshotPinnedCosignExecutableHeld captures the configured cosign bytes once,
// checks their external pin, and returns only an unlinked read-only file
// capability. The caller never executes the configured or a temporary pathname.
func snapshotPinnedCosignExecutableHeldContext(ctx context.Context, binary, want string) (*heldfile.File, func(), error) {
	if err := contextError(ctx); err != nil {
		return nil, func() {}, err
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, func() {}, e.Wrap(e.CodeEngineCapability, err,
			"resolving pinned cosign executable %q", binary)
	}
	target, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return nil, func() {}, e.Wrap(e.CodeEngineCapability, err,
			"resolving pinned cosign executable target %q", resolved)
	}
	held, err := heldfile.OpenContext(ctx, target, heldfile.Options{
		Label: "cosign-executable", Executable: true, MaxBytes: maxCosignExecutableBytes,
	})
	if err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return nil, func() {}, ctxErr
		}
		return nil, func() {}, e.Wrap(e.CodeEngineCapability, err,
			"holding pinned cosign executable %q", target)
	}
	if held.Digest() != want {
		_ = held.Close()
		return nil, func() {}, e.New(e.CodeUnpinnedInput,
			"cosign executable digest %s does not match pinned %s", held.Digest(), want)
	}
	if err := requireStaticELFCosign(held); err != nil {
		_ = held.Close()
		return nil, func() {}, err
	}
	cleanup := func() { _ = held.Close() }
	return held, cleanup, nil
}

// requireStaticELFCosign closes the remaining loader substitution boundary on
// Linux: exact executable bytes are a stable tool identity only when no
// filesystem-selected ELF interpreter or DT_NEEDED library participates.
func requireStaticELFCosign(held *heldfile.File) error {
	parsed, err := elf.NewFile(held)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err,
			"qualified cosign executable is not a supported static ELF binary")
	}
	defer func() { _ = parsed.Close() }()
	for _, program := range parsed.Progs {
		if program.Type == elf.PT_INTERP {
			return e.New(e.CodeEngineCapability,
				"qualified cosign executable has a filesystem-selected dynamic loader; use a static cosign binary")
		}
	}
	libraries, err := parsed.ImportedLibraries()
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err,
			"inspecting qualified cosign runtime closure")
	}
	if len(libraries) != 0 {
		return e.New(e.CodeEngineCapability,
			"qualified cosign executable has dynamic library dependencies %q; use a static cosign binary",
			strings.Join(libraries, ", "))
	}
	return nil
}

// snapshotPinnedCosignExecutableNamedDiagnostic retains the historical named-file
// protocol only for explicitly injected runners. Such a runner and this mutable
// same-UID boundary are part of the embedding caller's diagnostic TCB; stock
// authority dispatch never reaches this helper.

func snapshotPinnedCosignExecutableNamedDiagnostic(ctx context.Context, binary, want string) (string, func(), error) {
	if err := contextError(ctx); err != nil {
		return "", func() {}, err
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return "", nil, e.Wrap(e.CodeEngineCapability, err,
			"resolving diagnostic pinned cosign executable %q", binary)
	}
	source, err := heldfile.OpenContext(ctx, resolved, heldfile.Options{
		Label: "diagnostic-cosign-executable", MaxBytes: maxCosignExecutableBytes,
	})
	if err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return "", func() {}, ctxErr
		}
		return "", nil, e.Wrap(e.CodeEngineCapability, err,
			"snapshotting diagnostic pinned cosign executable %q", resolved)
	}
	defer func() { _ = source.Close() }()

	dir, err := os.MkdirTemp("", "nvkr-cosign-diagnostic-*")
	if err != nil {
		return "", nil, e.Wrap(e.CodeInternal, err,
			"creating private diagnostic cosign directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	if err := os.Chmod(dir, 0o700); err != nil {
		cleanup()
		return "", nil, e.Wrap(e.CodeInternal, err,
			"sealing private diagnostic cosign directory")
	}
	destination := filepath.Join(dir, "cosign")
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		cleanup()
		return "", nil, e.Wrap(e.CodeInternal, err,
			"creating private diagnostic cosign snapshot")
	}
	_, copyErr := source.WriteToContext(ctx, out)
	syncErr := out.Sync()
	closeErr := out.Close()
	unchangedErr := source.AssertUnchangedContext(ctx)
	if copyErr != nil || syncErr != nil || closeErr != nil || unchangedErr != nil {
		cleanup()
		if ctxErr := contextError(ctx); ctxErr != nil {
			return "", nil, ctxErr
		}
		return "", nil, e.New(e.CodeEngineCapability,
			"diagnostic cosign executable %q changed while being snapshotted", resolved)
	}
	got := source.Digest()
	if got != want {
		cleanup()
		return "", nil, e.New(e.CodeUnpinnedInput,
			"diagnostic cosign executable digest %s does not match pinned %s", got, want)
	}
	return destination, cleanup, nil
}

func untrustedCertificateError(role trust.SignerRole, format string, args ...any) error {
	if role == trust.RoleBuilderProfile {
		return e.New(e.CodeUntrustedProfile, format, args...)
	}
	return e.New(e.CodeUntrustedSigner, format, args...)
}

// qualifiedVerifyBlobArgs receives the protocol detected from exact held bundle
// bytes. It never offers an insecure-ignore-tlog or unsupported offline escape.
func qualifiedVerifyBlobArgs(req trust.DocumentVerificationRequest, newBundle bool) ([]string, error) {
	if strings.TrimSpace(req.SnapshotPath) == "" {
		return nil, e.New(e.CodeSignatureInvalid, "qualified verification has no private document snapshot")
	}
	if strings.TrimSpace(req.Signature.CertificatePath) == "" {
		return nil, e.New(e.CodeSignatureInvalid,
			"qualified keyless verification requires the signing certificate")
	}
	if strings.TrimSpace(req.Signature.SignaturePath) == "" && strings.TrimSpace(req.Signature.BundlePath) == "" {
		return nil, e.New(e.CodeSignatureInvalid,
			"qualified verification requires a detached signature or Sigstore bundle")
	}
	identities := dedupeSorted(req.AllowedIdentities)
	if len(identities) == 0 || strings.TrimSpace(req.OIDCIssuer) == "" {
		return nil, e.New(e.CodeTrustPolicyMissing,
			"qualified keyless verification requires pinned certificate identity and OIDC issuer constraints")
	}
	if err := validateTrustedRootPinConfiguration(req.Signature, req.TrustedRootDigest, req.Offline); err != nil {
		return nil, err
	}
	if req.Offline {
		if strings.TrimSpace(req.Signature.BundlePath) == "" {
			return nil, e.New(e.CodeSignatureInvalid,
				"qualified offline verification requires a Sigstore bundle")
		}
	}
	if strings.TrimSpace(req.Signature.TrustedRootPath) != "" && !newBundle {
		return nil, e.New(e.CodeSignatureInvalid,
			"qualified trusted-root verification requires a Sigstore v0.3 bundle")
	}

	args := []string{"verify-blob"}
	if !newBundle {
		if req.Signature.SignaturePath != "" {
			args = append(args, "--signature", req.Signature.SignaturePath)
		}
		args = append(args, "--certificate", req.Signature.CertificatePath)
	}
	if req.Signature.BundlePath != "" {
		args = append(args, "--bundle", req.Signature.BundlePath)
	}
	if newBundle {
		// Cosign's v0.3 protocol carries its signature and leaf certificate in
		// the bundle and rejects --signature/--certificate as incompatible.
		args = append(args, "--new-bundle-format=true")
	}
	if req.Signature.TrustedRootPath != "" {
		args = append(args, "--trusted-root", req.Signature.TrustedRootPath)
	}
	if len(identities) == 1 {
		args = append(args, "--certificate-identity", identities[0])
	} else {
		quoted := make([]string, 0, len(identities))
		for _, id := range identities {
			quoted = append(quoted, regexp.QuoteMeta(id))
		}
		args = append(args, "--certificate-identity-regexp", "^(?:"+strings.Join(quoted, "|")+")$")
	}
	args = append(args, "--certificate-oidc-issuer", req.OIDCIssuer)
	return append(args, req.SnapshotPath), nil
}

type privateMaterial struct {
	paths           trust.SignatureMaterial
	files           map[string][]byte
	signature       []byte
	certificate     []byte
	certificateBlob []byte
	bundle          []byte
	trustedRoot     []byte
	newBundle       bool
}

type heldVerificationMaterial struct {
	paths           trust.SignatureMaterial
	files           []*heldfile.File
	signature       []byte
	certificate     []byte
	certificateBlob []byte
	bundle          []byte
	trustedRoot     []byte
	newBundle       bool
}

// snapshotVerificationMaterialHeld captures every caller pathname through one
// opened descriptor, validates the root pin over those captured bytes, and then
// turns each capture into an unlinked read-only descriptor. Descriptor numbering
// starts at four because child descriptor three is the held executable.
func snapshotVerificationMaterialHeldContext(ctx context.Context, source trust.SignatureMaterial, trustedRootDigest string) (heldVerificationMaterial, func(), error) {
	if err := contextError(ctx); err != nil {
		return heldVerificationMaterial{}, func() {}, err
	}
	out := heldVerificationMaterial{}
	cleanup := func() {
		for i := len(out.files) - 1; i >= 0; i-- {
			_ = out.files[i].Close()
		}
	}
	copyOne := func(label, sourcePath string) (string, []byte, error) {
		if strings.TrimSpace(sourcePath) == "" {
			return "", nil, nil
		}
		file, err := heldfile.OpenContext(ctx, sourcePath, heldfile.Options{
			Label: label, MaxBytes: maxVerificationMaterialBytes,
		})
		if err != nil {
			if ctxErr := contextError(ctx); ctxErr != nil {
				return "", nil, ctxErr
			}
			code := e.CodeSignatureInvalid
			if label == "trusted-root.json" {
				code = e.CodeUnpinnedInput
			}
			return "", nil, e.Wrap(code, err, "holding %s", label)
		}
		if label == "trusted-root.json" {
			if file.Digest() != trustedRootDigest {
				_ = file.Close()
				return "", nil, e.New(e.CodeUnpinnedInput,
					"trusted root digest %s does not match pinned %s",
					file.Digest(), trustedRootDigest)
			}
		}
		path, err := heldfile.ChildPath(1 + len(out.files))
		if err != nil {
			_ = file.Close()
			return "", nil, err
		}
		raw, err := file.ReadAllContext(ctx, maxVerificationMaterialBytes)
		if err != nil {
			_ = file.Close()
			return "", nil, err
		}
		out.files = append(out.files, file)
		return path, raw, nil
	}
	var err error
	if out.paths.SignaturePath, out.signature, err = copyOne("signature", source.SignaturePath); err != nil {
		cleanup()
		return heldVerificationMaterial{}, func() {}, err
	}
	if out.paths.CertificatePath, out.certificateBlob, err = copyOne("certificate.pem", source.CertificatePath); err != nil {
		cleanup()
		return heldVerificationMaterial{}, func() {}, err
	}
	out.certificate = append([]byte(nil), out.certificateBlob...)
	if out.paths.BundlePath, out.bundle, err = copyOne("bundle.sigstore", source.BundlePath); err != nil {
		cleanup()
		return heldVerificationMaterial{}, func() {}, err
	}
	if out.certificate, out.newBundle, err = bindBundleCertificate(out.bundle, out.certificate); err != nil {
		cleanup()
		return heldVerificationMaterial{}, func() {}, err
	}
	if out.paths.TrustedRootPath, out.trustedRoot, err = copyOne("trusted-root.json", source.TrustedRootPath); err != nil {
		cleanup()
		return heldVerificationMaterial{}, func() {}, err
	}
	return out, cleanup, nil
}

func (m heldVerificationMaterial) assertUnchangedContext(ctx context.Context) error {
	for _, file := range m.files {
		if err := file.AssertUnchangedContext(ctx); err != nil {
			if ctxErr := contextError(ctx); ctxErr != nil {
				return ctxErr
			}
			return e.New(e.CodeSignatureInvalid,
				"held verification material changed while cosign was verifying it")
		}
	}
	return nil
}

func snapshotVerificationMaterialContext(ctx context.Context, source trust.SignatureMaterial, trustedRootDigest string) (privateMaterial, func(), error) {
	if err := contextError(ctx); err != nil {
		return privateMaterial{}, func() {}, err
	}
	dir, err := os.MkdirTemp("", "nvkr-cosign-material-*")
	if err != nil {
		return privateMaterial{}, func() {}, e.Wrap(e.CodeInternal, err, "creating private cosign-material directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	out := privateMaterial{files: map[string][]byte{}}
	copyOne := func(label, sourcePath string) (string, []byte, error) {
		if strings.TrimSpace(sourcePath) == "" {
			return "", nil, nil
		}
		in, err := heldfile.OpenContext(ctx, sourcePath, heldfile.Options{
			Label: label, MaxBytes: maxVerificationMaterialBytes,
		})
		if err != nil {
			if ctxErr := contextError(ctx); ctxErr != nil {
				return "", nil, ctxErr
			}
			return "", nil, e.Wrap(e.CodeSignatureInvalid, err, "opening %s", label)
		}
		defer func() { _ = in.Close() }()
		data, err := in.ReadAllContext(ctx, maxVerificationMaterialBytes)
		if err != nil {
			if ctxErr := contextError(ctx); ctxErr != nil {
				return "", nil, ctxErr
			}
			return "", nil, e.Wrap(e.CodeSignatureInvalid, err, "snapshotting %s", label)
		}
		if err := in.AssertUnchangedContext(ctx); err != nil {
			if ctxErr := contextError(ctx); ctxErr != nil {
				return "", nil, ctxErr
			}
			return "", nil, e.Wrap(e.CodeSignatureInvalid, err, "rechecking %s", label)
		}
		dest := filepath.Join(dir, label)
		if err := writeSealedContext(ctx, dest, data); err != nil {
			return "", nil, err
		}
		out.files[dest] = append([]byte(nil), data...)
		return dest, data, nil
	}
	if out.paths.SignaturePath, out.signature, err = copyOne("signature", source.SignaturePath); err != nil {
		cleanup()
		return privateMaterial{}, func() {}, err
	}
	if out.paths.CertificatePath, out.certificateBlob, err = copyOne("certificate.pem", source.CertificatePath); err != nil {
		cleanup()
		return privateMaterial{}, func() {}, err
	}
	out.certificate = append([]byte(nil), out.certificateBlob...)
	if out.paths.BundlePath, out.bundle, err = copyOne("bundle.sigstore", source.BundlePath); err != nil {
		cleanup()
		return privateMaterial{}, func() {}, err
	}
	if out.certificate, out.newBundle, err = bindBundleCertificate(out.bundle, out.certificate); err != nil {
		cleanup()
		return privateMaterial{}, func() {}, err
	}
	if out.paths.TrustedRootPath, out.trustedRoot, err = copyOne("trusted-root.json", source.TrustedRootPath); err != nil {
		cleanup()
		return privateMaterial{}, func() {}, err
	}
	if out.trustedRoot != nil {
		if err := pinnedBytes("trusted root", out.trustedRoot, trustedRootDigest); err != nil {
			cleanup()
			return privateMaterial{}, func() {}, err
		}
	}
	return out, cleanup, nil
}

func writeSealedContext(ctx context.Context, path string, data []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating private verification material")
	}
	if _, err = io.CopyBuffer(f, signContextReader{ctx: ctx, source: bytes.NewReader(data)}, make([]byte, 128<<10)); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = ctx.Err()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(path, 0o400)
	}
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "sealing private verification material")
	}
	return nil
}

func (m privateMaterial) assertUnchangedContext(ctx context.Context) error {
	for path, want := range m.files {
		if err := assertPrivateBytesContext(ctx, path, want, "signature material"); err != nil {
			return err
		}
	}
	return nil
}

func readPrivateSnapshotContext(ctx context.Context, path string) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "stating private document snapshot")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, e.New(e.CodeSignatureInvalid,
			"qualified verifier requires a regular private document snapshot (no group/other permissions)")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{
		Label: "private-document-snapshot", MaxBytes: maxSignedDocumentBytes,
	})
	if err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "holding private document snapshot")
	}
	defer func() { _ = held.Close() }()
	b, err := held.ReadAllContext(ctx, maxSignedDocumentBytes)
	if err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "reading private document snapshot")
	}
	return b, nil
}

func assertPrivateBytesContext(ctx context.Context, path string, want []byte, label string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{
		Label: label, MaxBytes: maxVerificationMaterialBytes,
	})
	if err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return ctxErr
		}
		return e.Wrap(e.CodeSignatureInvalid, err, "re-reading private %s", label)
	}
	defer func() { _ = held.Close() }()
	if held.Size() != int64(len(want)) || held.Digest() != digestBytes(want) {
		return e.New(e.CodeSignatureInvalid, "private %s changed while cosign was verifying it", label)
	}
	if err := held.AssertUnchangedContext(ctx); err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return ctxErr
		}
		return e.Wrap(e.CodeSignatureInvalid, err, "rechecking private %s", label)
	}
	return nil
}

type signContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader signContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}

// bindBundleCertificate detects Sigstore's self-contained v0.3 protocol from the
// exact captured bundle bytes. Cosign rejects detached certificate/signature flags
// for this protocol, so certificate evidence must come from the embedded leaf that
// cosign actually verifies. The still-required detached certificate is compared as
// a transport consistency assertion and can never substitute for the bundle leaf.
func bindBundleCertificate(bundle, detachedCertificate []byte) ([]byte, bool, error) {
	if len(bundle) == 0 {
		return detachedCertificate, false, nil
	}
	var envelope struct {
		MediaType            string `json:"mediaType"`
		VerificationMaterial struct {
			Certificate *struct {
				RawBytes string `json:"rawBytes"`
			} `json:"certificate"`
		} `json:"verificationMaterial"`
	}
	if err := json.Unmarshal(bundle, &envelope); err != nil {
		return detachedCertificate, false, nil // legacy bundle; cosign owns its schema validation
	}
	if envelope.MediaType != "application/vnd.dev.sigstore.bundle.v0.3+json" &&
		envelope.MediaType != "application/vnd.dev.sigstore.bundle+json;version=0.3" {
		return detachedCertificate, false, nil
	}
	if err := canonical.ValidateJSONDocument(bundle); err != nil {
		return nil, false, e.Wrap(e.CodeSignatureInvalid, err,
			"validating Sigstore v0.3 bundle representation")
	}
	if envelope.VerificationMaterial.Certificate == nil ||
		strings.TrimSpace(envelope.VerificationMaterial.Certificate.RawBytes) == "" {
		return nil, false, e.New(e.CodeSignatureInvalid,
			"Sigstore v0.3 keyless bundle has no embedded signing certificate")
	}
	der, err := base64.StdEncoding.DecodeString(envelope.VerificationMaterial.Certificate.RawBytes)
	if err != nil {
		return nil, false, e.Wrap(e.CodeSignatureInvalid, err,
			"decoding Sigstore v0.3 bundle certificate")
	}
	bundleCertificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, false, e.Wrap(e.CodeSignatureInvalid, err,
			"parsing Sigstore v0.3 bundle certificate")
	}
	detached, err := parseCertificate(detachedCertificate)
	if err != nil {
		return nil, false, err
	}
	if !bundleCertificate.Equal(detached) {
		return nil, false, e.New(e.CodeSignatureInvalid,
			"detached certificate does not match the certificate embedded in the Sigstore v0.3 bundle")
	}
	return append([]byte(nil), bundleCertificate.Raw...), true, nil
}

func parseCertificate(raw []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(raw)
	der := raw
	if block != nil {
		if block.Type != "CERTIFICATE" {
			return nil, e.New(e.CodeSignatureInvalid,
				"verified certificate material has PEM type %q, want CERTIFICATE", block.Type)
		}
		der = block.Bytes
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "parsing verified signing certificate")
	}
	return cert, nil
}

func certificateIdentities(cert *x509.Certificate) []string {
	var ids []string
	ids = append(ids, cert.EmailAddresses...)
	ids = append(ids, cert.DNSNames...)
	for _, uri := range cert.URIs {
		ids = append(ids, uri.String())
	}
	for _, ip := range cert.IPAddresses {
		ids = append(ids, ip.String())
	}
	return dedupeSorted(ids)
}

// Fulcio encodes the OIDC issuer in this certificate extension.
var fulcioOIDCIssuer = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}

func certificateOIDCIssuer(cert *x509.Certificate) (string, error) {
	for _, extension := range cert.Extensions {
		if !extension.Id.Equal(fulcioOIDCIssuer) {
			continue
		}
		var issuer string
		rest, err := asn1.Unmarshal(extension.Value, &issuer)
		if err == nil && len(rest) == 0 && issuer != "" {
			return issuer, nil
		}
		if raw := strings.TrimSpace(string(extension.Value)); raw != "" {
			return raw, nil
		}
	}
	return "", e.New(e.CodeSignatureInvalid,
		"verified signing certificate has no Fulcio OIDC issuer extension")
}

func containsAny(actual, allowed []string) bool {
	want := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		want[id] = true
	}
	for _, id := range actual {
		if want[id] {
			return true
		}
	}
	return false
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

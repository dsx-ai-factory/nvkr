// Package errors defines nkdctl's coded, structured errors. Every failure that a
// user can hit carries a stable machine-readable Code that maps deterministically
// to a process exit code, so the CLI and CI adapters can fail closed and name the
// offending input (SRD §7.2, NKD-FR-INT cross-cutting contracts).
//
// The exit-code contract is governed by ADR-03 (Coded-error & exit-code contract):
// it is a PUBLIC, FROZEN, BANDED contract. A downstream gate (a CI job, or a
// consumer such as NKE) branches on the integer, so codes never change meaning and
// are laid out with reserved gaps so new codes slot in without renumbering. The
// POSIX/signal ranges (126–165) are avoided; 1 is reserved for the semantic
// "internal" status only.
//
//	Band   Purpose                                     Integers
//	0–3    Semantic (non-error)                        success 0, internal 1, usage 2, diff 3
//	10–19  Resolve / input / preflight / crypto        10–19
//	20–29  Build / verify / trust-policy / disk        20–29
//	30–31  Retryable / transient (32–39 reserved)      30–31
//	40–54  Release / lifecycle                         40–54
package errors

import (
	"errors"
	"fmt"
)

// Code is a stable, documented error identifier. Codes never change meaning so
// downstream automation (and the verify node) can branch on them.
type Code string

const (
	// Band 0–3 — semantic (non-error) statuses. Success (exit 0) has no Code.
	CodeInternal Code = "NKD-E-INTERNAL" // an unexpected internal failure (e.g. serialization of derived data)
	CodeUsage    Code = "NKD-E-USAGE"    // invalid CLI usage / argument parse failure
	CodeDiff     Code = "NKD-E-DIFF"     // differences present (a non-error diff/verify-compare outcome)

	// Band 10–19 — resolve / input / preflight / crypto. The signature/trust gate
	// (signature-invalid) runs BEFORE the digest comparison (ADR-04).
	CodeNoMatch            Code = "NKD-E-NO-MATCH"             // reserved historical selector code: no match
	CodeAmbiguous          Code = "NKD-E-AMBIGUOUS"            // reserved historical selector code: ambiguous match
	CodeReservedAxis       Code = "NKD-E-RESERVED-AXIS"        // a reserved (v1-inactive) axis carried a value
	CodeUnsupported        Code = "NKD-E-UNSUPPORTED"          // active axis value outside its v1 domain
	CodeUnpinnedSource     Code = "NKD-E-UNPINNED-SOURCE"      // component lacks a full commit SHA / tree hash
	CodeTagToolchain       Code = "NKD-E-TOOLCHAIN-TAG"        // toolchain image referenced by tag, not digest
	CodeInvalidCatalog     Code = "NKD-E-INVALID-CATALOG"      // catalog failed schema/consistency validation
	CodePreflightFailed    Code = "NKD-E-PREFLIGHT-FAILED"     // host diverges from the selected builder profile on a fail-closed field
	CodeBuilderNotProvided Code = "NKD-E-BUILDER-NOT-PROVIDED" // builder profile does not provide a builder the selected build requires
	CodeSignatureInvalid   Code = "NKD-E-SIGNATURE-INVALID"    // a signature failed to verify (crypto gate, before digest compare)

	// Band 20–29 — build / verify / trust-policy / disk.
	CodeFetchFailed        Code = "NKD-E-FETCH-FAILED"         // source fetch failed or pin verification failed
	CodeBuildFailed        Code = "NKD-E-BUILD-FAILED"         // a build node exited non-zero
	CodeVerifyFailed       Code = "NKD-E-VERIFY-FAILED"        // an expected artifact was missing or its digest diverged
	CodeUntrustedSigner    Code = "NKD-E-UNTRUSTED-SIGNER"     // a verified signer matches none of the trusted set
	CodeTrustPolicyMissing Code = "NKD-E-TRUST-POLICY-MISSING" // signature required but no trust anchor resolvable
	CodeUntrustedProfile   Code = "NKD-E-UNTRUSTED-PROFILE"    // the builder profile is unsigned/untrusted/mismatched
	CodeEncryptedArtifact  Code = "NKD-E-ENCRYPTED-ARTIFACT"   // asked to assert byte-identity on encrypted (post-FDE) bytes
	CodeEnvelopeDecode     Code = "NKD-E-ENVELOPE-DECODE"      // a disk envelope could not be decoded to its logical stream
	CodeEngineCapability   Code = "NKD-E-ENGINE-CAPABILITY"    // target engine cannot enforce a required isolation/transport capability

	// Band 30–31 — retryable / transient (32–39 reserved).
	CodeUnavailable Code = "NKD-E-UNAVAILABLE" // a transient availability failure (network 5xx / dial / EOF)
	CodeTimeout     Code = "NKD-E-TIMEOUT"     // a deadline or cancellation elapsed

	// Band 40–54 — release / lifecycle.
	CodeVectorUnpinned     Code = "NKD-E-VECTOR-UNPINNED"     // a validation vector / expected-digests ref is not pinned
	CodeReleaseIncomplete  Code = "NKD-E-RELEASE-INCOMPLETE"  // a required member/artifact/evidence is missing (not promotable)
	CodeGateUnmet          Code = "NKD-E-GATE-UNMET"          // an independent gate (e.g. conformance) has no matching passing result
	CodeUnverifiedRef      Code = "NKD-E-UNVERIFIED-REF"      // a resolved digest/signature did not verify
	CodeCompatMismatch     Code = "NKD-E-COMPAT-MISMATCH"     // a compatibility constraint (kubernetes/nke/platform) is unmet
	CodeProvenanceMissing  Code = "NKD-E-PROVENANCE-MISSING"  // required provenance/evidence is absent
	CodeInventoryUnpinned  Code = "NKD-E-INVENTORY-UNPINNED"  // the inventory ref is not pinned by digest
	CodeImmutableViolation Code = "NKD-E-IMMUTABLE-VIOLATION" // an attempt to mutate an immutable publication
	CodeWithdrawn          Code = "NKD-E-WITHDRAWN"           // a signed withdrawn status declaration applies (fail closed)
	CodeUnpinnedInput      Code = "NKD-E-UNPINNED-INPUT"      // a non-content-addressable input under air-gap/mirror
	CodeMirrorMismatch     Code = "NKD-E-MIRROR-MISMATCH"     // a mirrored input did not hash to its declared identity
	CodePipelineDrift      Code = "NKD-E-PIPELINE-DRIFT"      // a committed pipeline diverges from its IR (ADR-21)
	CodeStatusCheckpoint   Code = "NKD-E-STATUS-CHECKPOINT"   // a signed lifecycle checkpoint is missing, stale, or rolled back
	CodePinSetMismatch     Code = "NKD-E-PIN-SET-MISMATCH"    // derived member disagrees with its pinned umbrella
	CodeWriteConflict      Code = "NKD-E-WRITE-CONFLICT"      // optimistic compare-and-swap lost to a concurrent writer
)

// exitCodes maps each Code to a distinct small process exit status. The allocation
// is banded (ADR-03) and frozen. Anything not found here (including nil) resolves
// via ExitCode's fallbacks. Every declared Code above MUST appear here, and every
// integer MUST be distinct — both are enforced by TestExitCodeContract.
var exitCodes = map[Code]int{
	// Band 0–3 — semantic.
	CodeInternal: 1,
	CodeUsage:    2,
	CodeDiff:     3,

	// Band 10–19 — resolve / input / preflight / crypto.
	CodeNoMatch:            10,
	CodeAmbiguous:          11,
	CodeReservedAxis:       12,
	CodeUnsupported:        13,
	CodeUnpinnedSource:     14,
	CodeTagToolchain:       15,
	CodeInvalidCatalog:     16,
	CodePreflightFailed:    17,
	CodeBuilderNotProvided: 18,
	CodeSignatureInvalid:   19,

	// Band 20–29 — build / verify / trust-policy / disk.
	CodeFetchFailed:        20,
	CodeBuildFailed:        21,
	CodeVerifyFailed:       22,
	CodeUntrustedSigner:    24,
	CodeTrustPolicyMissing: 25,
	CodeUntrustedProfile:   26,
	CodeEncryptedArtifact:  27,
	CodeEnvelopeDecode:     28,
	CodeEngineCapability:   29,

	// Band 30–31 — retryable / transient.
	CodeUnavailable: 30,
	CodeTimeout:     31,

	// Band 40–54 — release / lifecycle.
	CodeVectorUnpinned:     40,
	CodeReleaseIncomplete:  41,
	CodeGateUnmet:          42,
	CodeUnverifiedRef:      43,
	CodeCompatMismatch:     44,
	CodeProvenanceMissing:  45,
	CodeInventoryUnpinned:  46,
	CodeImmutableViolation: 47,
	CodeWithdrawn:          48,
	CodeUnpinnedInput:      49,
	CodeMirrorMismatch:     50,
	CodePipelineDrift:      51,
	CodeStatusCheckpoint:   52,
	CodeWriteConflict:      53,
	CodePinSetMismatch:     54,
}

// AllCodes is the canonical list of every declared Code, kept in exit-code order.
// It is the enumeration the contract test walks (Go has no reflection over the
// package's const block), so a new Code MUST be added here as well as to exitCodes.
var AllCodes = []Code{
	CodeInternal, CodeUsage, CodeDiff,
	CodeNoMatch, CodeAmbiguous, CodeReservedAxis, CodeUnsupported, CodeUnpinnedSource,
	CodeTagToolchain, CodeInvalidCatalog, CodePreflightFailed, CodeBuilderNotProvided, CodeSignatureInvalid,
	CodeFetchFailed, CodeBuildFailed, CodeVerifyFailed, CodeUntrustedSigner,
	CodeTrustPolicyMissing, CodeUntrustedProfile, CodeEncryptedArtifact, CodeEnvelopeDecode, CodeEngineCapability,
	CodeUnavailable, CodeTimeout,
	CodeVectorUnpinned, CodeReleaseIncomplete, CodeGateUnmet, CodeUnverifiedRef, CodeCompatMismatch,
	CodeProvenanceMissing, CodeInventoryUnpinned, CodeImmutableViolation, CodeWithdrawn, CodeUnpinnedInput,
	CodeMirrorMismatch, CodePipelineDrift, CodeStatusCheckpoint, CodeWriteConflict, CodePinSetMismatch,
}

// Error is a coded, wrappable error.
type Error struct {
	Code Code
	Msg  string
	Err  error // optional wrapped cause
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Msg, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Msg)
}

func (e *Error) Unwrap() error { return e.Err }

// New builds a coded error with a formatted message.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Wrap builds a coded error carrying an underlying cause.
func Wrap(code Code, cause error, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...), Err: cause}
}

// ExitCode returns the process exit status for err: the mapped status for a coded
// *Error, 1 for any other non-nil error, and 0 for nil.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *Error
	if errors.As(err, &e) {
		if code, ok := exitCodes[e.Code]; ok {
			return code
		}
	}
	return 1
}

// retryable is the set of Codes a downstream gate may retry rather than fail
// closed on: a transient availability failure or an elapsed deadline (ADR-03 §2).
var retryable = map[Code]bool{
	CodeUnavailable: true,
	CodeTimeout:     true,
}

// Retryable reports whether err is a transient failure a gate should retry
// (network 5xx / dial / EOF → unavailable; deadline / cancellation → timeout).
// Every other coded failure — and every non-coded error — is non-retryable, so a
// gate fails closed on it.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		return retryable[e.Code]
	}
	return false
}

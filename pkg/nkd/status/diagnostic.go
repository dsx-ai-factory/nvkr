package status

import (
	"context"
	"errors"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
)

// DiagnosticCheckpointRequest supplies document locations and explicit policy.
// The protected high-water file must already contain an operator-provisioned
// minimum. Locations and caller-selected epochs confer no release authority.
type DiagnosticCheckpointRequest struct {
	HighWaterPath string
	Policy        CheckpointPolicy
	AsOf          AsOf
	Checkpoint    trust.AuthenticateRequest
	Declarations  []trust.AuthenticateRequest
}

// DiagnosticCheckpointResult describes a decision after its checkpoint has been
// durably accepted. It is not a trusted-time or release-authorization receipt.
type DiagnosticCheckpointResult struct {
	CheckpointDigest string
	Sequence         uint64
	Decision         Decision
}

// AcceptDiagnosticCheckpoint owns authentication and durable diagnostic acceptance.
// It reads the protected minimum, authenticates the exact complete checkpoint,
// and commits receipt-derived state with byte-CAS before interpreting withdrawal.
// A withdrawn result therefore returns CodeWithdrawn after advancing the minimum.
func AcceptDiagnosticCheckpoint(ctx context.Context, request DiagnosticCheckpointRequest, verifier trust.DocumentVerifier) (DiagnosticCheckpointResult, error) {
	if err := statusContextError(ctx, "accepting diagnostic status checkpoint"); err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	if err := admitStatusState(); err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	if err := request.Policy.validate(); err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	if !request.AsOf.set || request.AsOf.epoch <= 0 {
		return DiagnosticCheckpointResult{}, e.New(e.CodeStatusCheckpoint, "diagnostic status acceptance requires an explicit positive as-of epoch")
	}
	if request.HighWaterPath == "" {
		return DiagnosticCheckpointResult{}, e.New(e.CodeUsage, "diagnostic status acceptance requires protected high-water storage")
	}
	for _, document := range append([]trust.AuthenticateRequest{request.Checkpoint}, request.Declarations...) {
		if document.Role != trust.RoleStatus || document.Policy.Role() != trust.RoleStatus {
			return DiagnosticCheckpointResult{}, e.New(e.CodeTrustPolicyMissing, "diagnostic checkpoint documents require status signer policy")
		}
	}
	authenticator, err := trust.NewDocumentAuthenticator(verifier,
		trust.JSONSchema[StatusCheckpoint](CheckpointSchemaVersion),
		trust.JSONSchema[StatusDeclaration](SchemaVersion))
	if err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	var state highWaterFile
	observed, err := readProtectedHighWaterContext(ctx, request.HighWaterPath, &state)
	if err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	if state.SchemaVersion != highWaterSchemaVersion {
		return DiagnosticCheckpointResult{}, e.New(e.CodeInvalidCatalog, "status high-water schemaVersion %q is unsupported (want %q)", state.SchemaVersion, highWaterSchemaVersion)
	}
	if state.Target != request.Policy.target || state.Authority != request.Policy.authority {
		return DiagnosticCheckpointResult{}, e.New(e.CodeStatusCheckpoint, "status high-water target/authority does not match checkpoint policy")
	}
	minimum, err := NewHighWaterMark(state.Target, state.Authority, state.Sequence, state.CheckpointDigest)
	if err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	checkpoint, err := authenticator.Authenticate(ctx, request.Checkpoint)
	if err != nil {
		return DiagnosticCheckpointResult{}, diagnosticAuthenticationError(ctx, err)
	}
	declarations := make([]trust.DocumentReceipt, 0, len(request.Declarations))
	for _, location := range request.Declarations {
		receipt, err := authenticator.Authenticate(ctx, location)
		if err != nil {
			return DiagnosticCheckpointResult{}, diagnosticAuthenticationError(ctx, err)
		}
		declarations = append(declarations, receipt)
	}
	verified, err := VerifyCheckpoint(ctx, CheckpointRequest{
		Policy: request.Policy, HighWater: minimum, AsOf: request.AsOf,
		Checkpoint: checkpoint, Declarations: declarations,
	})
	if err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	next, err := highWaterFromReceipt(verified)
	if err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	encoded, err := canonical.Marshal(next)
	if err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	if err := persistStatusHighWaterContext(ctx, request.HighWaterPath, observed, encoded); err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	// Interpreting withdrawal before this commit would permit a later invocation
	// to replay the old minimum after a correct fail-closed withdrawal decision.
	decision, err := verified.Decision()
	if err != nil {
		return DiagnosticCheckpointResult{}, err
	}
	return DiagnosticCheckpointResult{CheckpointDigest: next.CheckpointDigest, Sequence: next.Sequence, Decision: *decision}, nil
}

func diagnosticAuthenticationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "authenticating diagnostic status documents")
	}
	var coded *e.Error
	if errors.As(err, &coded) {
		return err
	}
	return e.Wrap(e.CodeSignatureInvalid, err, "authenticating diagnostic status document")
}

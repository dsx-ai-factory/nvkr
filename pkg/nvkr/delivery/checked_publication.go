// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
)

// CheckedPublication owns complete publication integrity checks and retained
// control, evidence and executable bytes. Other payloads are verified and released
// before issuance, so this owner cannot export a full layout or a disk payload.
// It grants no signature, execution or release authority. Shallow copies share
// revocation and Close joins active streams before releasing retained bytes.
type CheckedPublication struct {
	state *publicationState
}

func (publication *CheckedPublication) retainedState() *publicationState {
	if publication == nil {
		return nil
	}
	return publication.state
}

// Close releases the retained controls, evidence and executable layers.
func (publication *CheckedPublication) Close() error {
	return publication.retainedState().close()
}

// Description projects every independently checked output and its evidence.
func (publication *CheckedPublication) Description(reference string) (PublicationDescription, error) {
	return publication.retainedState().description(reference)
}

// Artifacts returns defensive complete output descriptions and content identity.
func (publication *CheckedPublication) Artifacts(reference string) ([]Output, string, error) {
	return publication.retainedState().artifacts(reference)
}

// RequireAttestedEvidence rejects raw or incomplete evidence without verifying signatures.
func (publication *CheckedPublication) RequireAttestedEvidence() error {
	return publication.retainedState().requireAttestedEvidence()
}

// VisitEvidence lends exact retained bundles under the shared lifetime lock.
// The callback must be bounded and must not reenter or close this owner.
func (publication *CheckedPublication) VisitEvidence(ctx context.Context, visit func(context.Context, EvidenceBinding, []byte) error) error {
	return publication.retainedState().visitEvidence(ctx, visit)
}

// ExecutableBundle returns defensive metadata for an independently checked bundle.
func (publication *CheckedPublication) ExecutableBundle(outputName string) (BinaryBundle, error) {
	return publication.retainedState().executableBundle(outputName)
}

// WriteExecutableMember checks the complete retained archive while streaming one
// member. The caller must bound destination writes and discard partial output on error.
func (publication *CheckedPublication) WriteExecutableMember(ctx context.Context, outputName, memberName string, destination io.Writer) (int64, error) {
	return publication.retainedState().writeExecutableMember(ctx, outputName, memberName, destination)
}

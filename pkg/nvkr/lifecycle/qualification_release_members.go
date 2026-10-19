// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

// Source lanes record how an output's bytes were obtained. They are read only
// from the output's cryptographically verified production provenance.
const (
	SourceLaneHermeticBuild = "hermetic-build"
	SourceLaneWrappedBuild  = "wrapped-build"
	SourceLaneAcquired      = "acquired"
)

var sourceLanes = map[string]string{
	provenance.BuildTypeHermeticBuild: SourceLaneHermeticBuild,
	provenance.BuildTypeWrappedBuild:  SourceLaneWrappedBuild,
	provenance.BuildTypeAcquired:      SourceLaneAcquired,
}

// QualificationReleaseMember is a defensive projection of one authenticated
// output's production provenance. ProvenanceDigest is the SHA-256 of the
// verified predicate's canonical JSON, without the optional transport newline
// the signature verifier also ignores. Decoding it restores no receipt.
type QualificationReleaseMember struct {
	Output           string `json:"output"`
	SourceLane       string `json:"sourceLane"`
	ProvenanceDigest string `json:"provenanceDigest"`
}

// ReleaseMembers returns the source lane and verified provenance identity of
// every output, in artifact-check order, while the receipt is live. It grants
// no qualification signing or release authority.
func (receipt *ArtifactCheckReceipt) ReleaseMembers() ([]QualificationReleaseMember, error) {
	description, err := receipt.Description()
	if err != nil {
		return nil, err
	}
	receipt.lifetime.mu.RLock()
	defer receipt.lifetime.mu.RUnlock()
	if receipt.lifetime.closed {
		return nil, e.New(e.CodeGateUnmet, "artifact check receipt is closed")
	}
	members := receipt.lifetime.members
	if len(members) != len(description.Outputs) {
		return nil, e.New(e.CodeReleaseIncomplete, "artifact check receipt lacks provenance for the complete output set")
	}
	for i, output := range description.Outputs {
		if members[i].Output != output.Name {
			return nil, e.New(e.CodeReleaseIncomplete, "artifact check receipt provenance differs from its output set")
		}
	}
	return append([]QualificationReleaseMember(nil), members...), nil
}

// qualificationReleaseMembers projects already verified predicates. Callers
// must have authenticated every predicate against its exact subject.
func qualificationReleaseMembers(outputs []ArtifactCheckOutput, predicates map[string][]byte) ([]QualificationReleaseMember, error) {
	members := make([]QualificationReleaseMember, 0, len(outputs))
	for _, output := range outputs {
		raw, found := predicates[output.Reference]
		if !found {
			return nil, e.New(e.CodeProvenanceMissing, "qualification omits production provenance for output %q", output.Name)
		}
		// UnmarshalExact proves these bytes are the canonical representation.
		exact := bytes.TrimSuffix(raw, []byte{'\n'})
		var predicate provenance.Predicate
		if err := canonical.UnmarshalExact(exact, &predicate); err != nil {
			return nil, e.Wrap(e.CodeProvenanceMissing, err, "decoding production provenance for output %q", output.Name)
		}
		lane, found := sourceLanes[predicate.BuildDefinition.BuildType]
		if !found {
			return nil, e.New(e.CodeProvenanceMissing, "output %q has an unsupported production build type", output.Name)
		}
		sum := sha256.Sum256(exact)
		members = append(members, QualificationReleaseMember{Output: output.Name, SourceLane: lane, ProvenanceDigest: "sha256:" + hex.EncodeToString(sum[:])})
	}
	return members, nil
}

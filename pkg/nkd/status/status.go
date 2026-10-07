// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package status authenticates signed status declarations and complete checkpoints.
// AcceptDiagnosticCheckpoint owns protected high-water storage, signature and
// complete-set verification, and commit-before-decision ordering. Diagnostic
// evaluation uses an explicit epoch and pinned signer policy; registry discovery
// and release authorization are not implemented.
package status

import (
	"context"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

const SchemaVersion = "nkd.dev/status-declaration/v1"

// Lifecycle actions (ADR-06 §2).
const (
	ActionSuperseded = "superseded"
	ActionDeprecated = "deprecated"
	ActionWithdrawn  = "withdrawn"
)

// StatusDeclaration references its target ONLY by immutable identity and never opens
// or rewrites the target (ADR-06 §2). Its own identity is its content digest.
type StatusDeclaration struct {
	SchemaVersion  string `json:"schemaVersion"`
	Target         string `json:"target"`               // publicationIdentity (a non-digest target fails closed)
	Action         string `json:"action"`               // superseded | deprecated | withdrawn
	ReplacedBy     string `json:"replacedBy,omitempty"` // required iff superseded — an immutable identity
	Reason         string `json:"reason,omitempty"`
	EffectiveEpoch int64  `json:"effectiveEpoch,omitempty"` // operator-supplied input, never a wall-clock read
}

// Digest is the declaration's own content identity.
func (d *StatusDeclaration) Digest() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindStatus, d)
}

// Marshal returns the exact canonical JSON bytes used for detached signing and
// authenticated-document verification.
func (d *StatusDeclaration) Marshal() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(d)
}

// Validate fails closed on a malformed declaration: a non-digest target, an unknown
// action, or a superseded action without an immutable replacedBy.
func (d *StatusDeclaration) Validate() error {
	if d == nil {
		return e.New(e.CodeInvalidCatalog, "status declaration is required")
	}
	if d.SchemaVersion != SchemaVersion {
		return e.New(e.CodeInvalidCatalog, "status schemaVersion %q is unsupported (want %q)", d.SchemaVersion, SchemaVersion)
	}
	if !isDigest(d.Target) {
		return e.New(e.CodeUnverifiedRef,
			"status declaration target %q is not an immutable identity (want sha256:<64-hex>)", d.Target)
	}
	switch d.Action {
	case ActionSuperseded:
		if !isDigest(d.ReplacedBy) {
			return e.New(e.CodeInvalidCatalog, "superseded declaration must name an immutable replacedBy identity")
		}
	case ActionDeprecated, ActionWithdrawn:
		if d.ReplacedBy != "" {
			return e.New(e.CodeInvalidCatalog, "%s declaration cannot carry replacedBy", d.Action)
		}
	default:
		return e.New(e.CodeInvalidCatalog, "unknown status action %q", d.Action)
	}
	if d.EffectiveEpoch < 0 {
		return e.New(e.CodeInvalidCatalog, "status effectiveEpoch cannot be negative")
	}
	return nil
}

// VerifiedDeclaration is a capability created only after schema validation and
// signature verification. Its critical state is intentionally private.
type VerifiedDeclaration struct {
	valid                 bool
	documentAuthenticated bool
	declaration           StatusDeclaration
	signer                string
	issuer                string
	certificateDigest     string
	canonicalPayload      string
	digest                string
}

// AsOf is an explicit evaluation time. The zero value means no as-of was supplied;
// future-dated declarations are then not enforced.
type AsOf struct {
	set   bool
	epoch int64
}

// At constructs an explicit as-of time.
func At(epoch int64) (AsOf, error) {
	if epoch <= 0 {
		return AsOf{}, e.New(e.CodeInvalidCatalog, "as-of epoch must be greater than zero")
	}
	return AsOf{set: true, epoch: epoch}, nil
}

// Decision is the enforcement outcome of consuming the status declarations attached
// to a release.
type Decision struct {
	Effective  bool   // is any honored declaration in effect?
	Action     string // the honored action (empty if none)
	ReplacedBy string // for superseded
	Warn       string // human warning for deprecated/superseded
}

func decisionFromAccepted(target string, accepted []VerifiedDeclaration, asOf AsOf) (*Decision, error) {
	ordered := append([]VerifiedDeclaration(nil), accepted...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].declaration.EffectiveEpoch != ordered[j].declaration.EffectiveEpoch {
			return ordered[i].declaration.EffectiveEpoch < ordered[j].declaration.EffectiveEpoch
		}
		return ordered[i].digest < ordered[j].digest
	})
	dec := &Decision{}
	for _, verified := range ordered {
		declaration := verified.declaration
		if declaration.EffectiveEpoch > 0 && (!asOf.set || declaration.EffectiveEpoch > asOf.epoch) {
			continue
		}
		switch declaration.Action {
		case ActionWithdrawn:
			return nil, e.New(e.CodeWithdrawn,
				"release %s has a verified withdrawn declaration from the pinned status signer%s", target, reason(declaration.Reason))
		case ActionSuperseded:
			dec.Effective = true
			dec.Action = ActionSuperseded
			dec.ReplacedBy = declaration.ReplacedBy
			dec.Warn = "release superseded; replaced by " + declaration.ReplacedBy
		case ActionDeprecated:
			if dec.Action == "" { // don't downgrade a superseded warning
				dec.Effective = true
				dec.Action = ActionDeprecated
				dec.Warn = "release deprecated" + reason(declaration.Reason)
			}
		}
	}
	return dec, nil
}

func isDigest(s string) bool {
	if !strings.HasPrefix(s, "sha256:") || len(s) != len("sha256:")+64 {
		return false
	}
	raw := strings.TrimPrefix(s, "sha256:")
	if strings.ToLower(raw) != raw {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func reason(r string) string {
	if r == "" {
		return ""
	}
	return " (" + r + ")"
}

func statusContextErr(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "status verification requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "status verification canceled")
	}
	return nil
}

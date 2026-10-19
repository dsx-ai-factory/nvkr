// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

const PromotionPolicySchemaVersion = "nvkr.dev/promotion-policy/v1"

// ArtifactKind is the closed set of artifact output classes understood by the
// current promotion-policy schema. Adding a class requires a schema migration so
// an older authorizer cannot silently apply the wrong evidence rule.
type ArtifactKind uint8

const (
	ArtifactHostPackage ArtifactKind = iota + 1
	ArtifactOCIImage
	ArtifactDiskImage
	ArtifactRaw
	ArtifactTarball
)

func (kind ArtifactKind) String() string {
	switch kind {
	case ArtifactHostPackage:
		return "host-package"
	case ArtifactOCIImage:
		return "oci-image"
	case ArtifactDiskImage:
		return "disk-image"
	case ArtifactRaw:
		return "raw"
	case ArtifactTarball:
		return "tarball"
	default:
		return ""
	}
}

func (kind ArtifactKind) valid() bool { return kind.String() != "" }

func (kind ArtifactKind) MarshalJSON() ([]byte, error) {
	if !kind.valid() {
		return nil, e.New(e.CodeInvalidCatalog, "unknown artifact kind %d", kind)
	}
	return json.Marshal(kind.String())
}

func (kind *ArtifactKind) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "decoding artifact kind")
	}
	switch value {
	case "host-package":
		*kind = ArtifactHostPackage
	case "oci-image":
		*kind = ArtifactOCIImage
	case "disk-image":
		*kind = ArtifactDiskImage
	case "raw":
		*kind = ArtifactRaw
	case "tarball":
		*kind = ArtifactTarball
	default:
		return e.New(e.CodeInvalidCatalog, "unknown artifact kind %q", value)
	}
	return nil
}

// ArtifactKindBinding classifies one exact required artifact. Inventory.Validate
// enforces a bijection with RequiredArtifacts so policy lookup cannot fall back to
// a caller guess or an unclassified release output.
type ArtifactKindBinding struct {
	ArtifactDigest string       `json:"artifactDigest"`
	Kind           ArtifactKind `json:"kind"`
}

// EvidenceRequirement is the promotion rule for one evidence class. Absence from
// a rule means that class is not required. WaivableReasons is a closed allow-list;
// an empty list means the required class cannot be waived.
type EvidenceRequirement struct {
	Type            EvidenceType        `json:"type"`
	WaivableReasons []EvidenceGapReason `json:"waivableReasons,omitempty"`
}

// PromotionRule maps one exact artifact kind to its evidence requirements.
type PromotionRule struct {
	ArtifactKind ArtifactKind          `json:"artifactKind"`
	Evidence     []EvidenceRequirement `json:"evidence"`
}

// PromotionPolicy is immutable governance input. It selects the exact
// compatibility and conformance policies and, per artifact kind, the evidence and
// waiver rules reserved for a future promotion implementation.
type PromotionPolicy struct {
	SchemaVersion             string          `json:"schemaVersion"`
	CompatibilityPolicyDigest string          `json:"compatibilityPolicyDigest"`
	ConformancePolicyDigest   string          `json:"conformancePolicyDigest"`
	Rules                     []PromotionRule `json:"rules"`
	EffectiveEpoch            int64           `json:"effectiveEpoch"`
	ExpiresEpoch              int64           `json:"expiresEpoch"`
}

func (policy PromotionPolicy) normalized() PromotionPolicy {
	policy.Rules = append([]PromotionRule(nil), policy.Rules...)
	for index := range policy.Rules {
		policy.Rules[index].Evidence = append([]EvidenceRequirement(nil), policy.Rules[index].Evidence...)
		for evidenceIndex := range policy.Rules[index].Evidence {
			policy.Rules[index].Evidence[evidenceIndex].WaivableReasons = append([]EvidenceGapReason(nil),
				policy.Rules[index].Evidence[evidenceIndex].WaivableReasons...)
			sort.Slice(policy.Rules[index].Evidence[evidenceIndex].WaivableReasons, func(i, j int) bool {
				return policy.Rules[index].Evidence[evidenceIndex].WaivableReasons[i] <
					policy.Rules[index].Evidence[evidenceIndex].WaivableReasons[j]
			})
		}
		sort.Slice(policy.Rules[index].Evidence, func(i, j int) bool {
			return policy.Rules[index].Evidence[i].Type < policy.Rules[index].Evidence[j].Type
		})
	}
	sort.Slice(policy.Rules, func(i, j int) bool {
		return policy.Rules[i].ArtifactKind < policy.Rules[j].ArtifactKind
	})
	return policy
}

func (policy PromotionPolicy) Validate() error {
	if policy.SchemaVersion != PromotionPolicySchemaVersion {
		return e.New(e.CodeInvalidCatalog, "promotion policy schemaVersion %q is unsupported (want %q)",
			policy.SchemaVersion, PromotionPolicySchemaVersion)
	}
	if !isDigest(policy.CompatibilityPolicyDigest) || !isDigest(policy.ConformancePolicyDigest) {
		return e.New(e.CodeUnverifiedRef,
			"promotion policy must bind exact compatibility and conformance policy digests")
	}
	if policy.EffectiveEpoch <= 0 || policy.ExpiresEpoch <= policy.EffectiveEpoch {
		return e.New(e.CodeGateUnmet,
			"promotion policy requires a positive effective epoch and a later expiry epoch")
	}
	if len(policy.Rules) == 0 {
		return e.New(e.CodeInvalidCatalog, "promotion policy declares no artifact-kind rules")
	}
	seenKinds := map[ArtifactKind]bool{}
	for _, rule := range policy.Rules {
		if !rule.ArtifactKind.valid() {
			return e.New(e.CodeInvalidCatalog, "promotion policy contains an unknown artifact kind")
		}
		if seenKinds[rule.ArtifactKind] {
			return e.New(e.CodeInvalidCatalog, "promotion policy repeats artifact kind %s", rule.ArtifactKind.String())
		}
		seenKinds[rule.ArtifactKind] = true
		seenEvidence := map[EvidenceType]bool{}
		for _, requirement := range rule.Evidence {
			if !requirement.Type.valid() {
				return e.New(e.CodeInvalidCatalog,
					"promotion policy rule %s contains an unknown evidence type", rule.ArtifactKind.String())
			}
			if seenEvidence[requirement.Type] {
				return e.New(e.CodeInvalidCatalog,
					"promotion policy rule %s repeats evidence type %s",
					rule.ArtifactKind.String(), requirement.Type.String())
			}
			seenEvidence[requirement.Type] = true
			seenReasons := map[EvidenceGapReason]bool{}
			for _, reason := range requirement.WaivableReasons {
				if !reason.valid() {
					return e.New(e.CodeInvalidCatalog,
						"promotion policy rule %s/%s contains an unknown waiver reason",
						rule.ArtifactKind.String(), requirement.Type.String())
				}
				if seenReasons[reason] {
					return e.New(e.CodeInvalidCatalog,
						"promotion policy rule %s/%s repeats waiver reason %s",
						rule.ArtifactKind.String(), requirement.Type.String(), reason.String())
				}
				seenReasons[reason] = true
			}
		}
	}
	return nil
}

// Marshal returns the normalized canonical bytes that must be signed.
func (policy PromotionPolicy) Marshal() ([]byte, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(policy.normalized())
}

// Digest returns the content identity of the normalized policy document. A digest
// alone is not authorization.
func (policy PromotionPolicy) Digest() (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindPromotionPolicy, policy.normalized())
}

// VerifyPromotionPolicy validates the exact authenticated document and returns
// its canonical identity. Authentication grants no release clearance.
func VerifyPromotionPolicy(ctx context.Context, document trust.DocumentReceipt) (string, error) {
	if err := contextErr(ctx); err != nil {
		return "", err
	}
	if !document.Authenticated() || document.Role() != trust.RolePromotionPolicy {
		return "", e.New(e.CodeSignatureInvalid,
			"promotion policy requires an authenticated %s document", trust.RolePromotionPolicy)
	}
	if document.SchemaVersion() != PromotionPolicySchemaVersion {
		return "", e.New(e.CodeInvalidCatalog,
			"authenticated promotion policy schemaVersion %q is unsupported (want %q)",
			document.SchemaVersion(), PromotionPolicySchemaVersion)
	}
	var policy PromotionPolicy
	switch decoded := document.Decoded().(type) {
	case PromotionPolicy:
		policy = decoded
	case *PromotionPolicy:
		if decoded == nil {
			return "", e.New(e.CodeSignatureInvalid,
				"authenticated promotion policy decoded to nil")
		}
		policy = *decoded
	default:
		return "", e.New(e.CodeSignatureInvalid,
			"authenticated promotion policy has unexpected decoded type")
	}
	if err := policy.Validate(); err != nil {
		return "", err
	}
	normalized := policy.normalized()
	payload, err := canonical.Marshal(normalized)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(document.ExactBytes(), payload) {
		return "", e.New(e.CodeInvalidCatalog,
			"authenticated promotion policy does not use the canonical rule ordering")
	}
	signer := strings.TrimSpace(document.SignerIdentity())
	if signer == "" {
		return "", e.New(e.CodeSignatureInvalid,
			"authenticated promotion policy has no signer identity")
	}
	digest, err := canonical.Digest(canonical.KindPromotionPolicy, normalized)
	if err != nil {
		return "", err
	}
	return digest, nil
}

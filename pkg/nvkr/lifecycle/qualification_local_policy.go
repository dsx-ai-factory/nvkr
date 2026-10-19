// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const LocalQualificationPolicySchema = "nvkr.dev/local-qualification-policy/v1"

// LoadLocalQualificationPolicy reads one explicit bounded JSON policy. Unknown
// fields, duplicate fields and trailing documents fail; no candidate metadata
// or ambient configuration supplies defaults or signer keys.
func LoadLocalQualificationPolicy(ctx context.Context, path string) (LocalQualificationPolicy, error) {
	if ctx == nil || path == "" {
		return LocalQualificationPolicy{}, e.New(e.CodeUsage, "local qualification requires a context and explicit policy path")
	}
	if err := ctx.Err(); err != nil {
		return LocalQualificationPolicy{}, e.Wrap(e.CodeTimeout, err, "local policy load canceled")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "local qualification policy", MaxBytes: maxQualificationPolicyBytes})
	if err != nil {
		return LocalQualificationPolicy{}, qualificationPolicyReadError(ctx, err)
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxQualificationPolicyBytes)
	if err != nil {
		return LocalQualificationPolicy{}, qualificationPolicyReadError(ctx, err)
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return LocalQualificationPolicy{}, e.Wrap(e.CodeTrustPolicyMissing, err, "invalid local qualification policy JSON")
	}
	var policy LocalQualificationPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return LocalQualificationPolicy{}, e.Wrap(e.CodeTrustPolicyMissing, err, "unsupported local qualification policy fields")
	}
	return policy.freeze()
}

// LocalQualificationPolicy is independently selected local trust, never a
// provider identity or an artifact-supplied trust store. It contains public keys
// only. Filesystem locators and private keys belong to the executing role.
type LocalQualificationPolicy struct {
	SchemaVersion     string                   `json:"schemaVersion"`
	Lane              string                   `json:"lane"`
	DefinitionDigest  string                   `json:"definitionDigest"`
	EnvironmentDigest string                   `json:"environmentDigest"`
	PlanDigest        string                   `json:"planDigest"`
	Platform          string                   `json:"platform"`
	CosignDigest      string                   `json:"cosignDigest"`
	BuilderKey        string                   `json:"builderKey"`
	SmokeKey          string                   `json:"smokeKey"`
	QualifierKey      string                   `json:"qualifierKey"`
	Smoke             QualificationSmokePolicy `json:"smoke"`
}

// freeze returns a deep, normalized description. It issues no authority and
// performs no I/O. Exact Plan and payload checks remain separate requirements.
func (policy LocalQualificationPolicy) freeze() (LocalQualificationPolicy, error) {
	if policy.SchemaVersion != LocalQualificationPolicySchema || policy.Lane != factory.TierLocalExecution.String() || !qualificationNativePlatform(policy.Platform) || policy.Smoke.Workflow != "" {
		return LocalQualificationPolicy{}, e.New(e.CodeTrustPolicyMissing, "local qualification requires its explicit unqualified schema, native platform and no hosted workflow")
	}
	for _, digest := range []string{policy.DefinitionDigest, policy.EnvironmentDigest, policy.PlanDigest, policy.CosignDigest} {
		if !canonical.IsDigest(digest) {
			return LocalQualificationPolicy{}, e.New(e.CodeTrustPolicyMissing, "local qualification requires exact input and cosign identities")
		}
	}
	if err := policy.Smoke.validateExecution(); err != nil {
		return LocalQualificationPolicy{}, err
	}
	if len(policy.Smoke.Images) != 1 || policy.Smoke.Images[policy.Platform] == "" {
		return LocalQualificationPolicy{}, e.New(e.CodeTrustPolicyMissing, "local rehearsal requires exactly its selected native execution image")
	}
	seen := map[string]bool{}
	for _, field := range []*string{&policy.BuilderKey, &policy.SmokeKey, &policy.QualifierKey} {
		key, err := sign.NormalizeLocalPublicKey([]byte(*field))
		if err != nil {
			return LocalQualificationPolicy{}, err
		}
		if seen[string(key)] {
			return LocalQualificationPolicy{}, e.New(e.CodeTrustPolicyMissing, "local builder, smoke and qualifier require distinct public keys")
		}
		*field = string(key)
		seen[string(key)] = true
	}
	raw, err := canonical.Marshal(policy)
	if err != nil {
		return LocalQualificationPolicy{}, err
	}
	var frozen LocalQualificationPolicy
	if err := canonical.UnmarshalExact(raw, &frozen); err != nil {
		return LocalQualificationPolicy{}, e.Wrap(e.CodeTrustPolicyMissing, err, "freezing local qualification policy")
	}
	sort.Slice(frozen.Smoke.NonToolOutputs, func(i, j int) bool {
		return frozen.Smoke.NonToolOutputs[i].Output < frozen.Smoke.NonToolOutputs[j].Output
	})
	for i := range frozen.Smoke.NonToolOutputs {
		sort.Strings(frozen.Smoke.NonToolOutputs[i].Components)
	}
	sort.Slice(frozen.Smoke.NonToolMembers, func(i, j int) bool {
		return frozen.Smoke.NonToolMembers[i].key() < frozen.Smoke.NonToolMembers[j].key()
	})
	return frozen, nil
}

func (policy LocalQualificationPolicy) Validate() error {
	_, err := policy.freeze()
	return err
}

// Digest uses the existing qualification policy family and a distinct schema.
// Role assignment is part of identity; PEM line wrapping and set order are not.
func (policy LocalQualificationPolicy) Digest() (string, error) {
	frozen, err := policy.freeze()
	if err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindQualificationPolicy, frozen)
}

// RequirePlan admits the complete graph and smoke scope before execution. It
// does not authenticate a completed Run or give permission to sign evidence.
func (policy LocalQualificationPolicy) RequirePlan(plan *factory.Plan) error {
	frozen, err := policy.freeze()
	if err != nil {
		return err
	}
	if plan == nil {
		return e.New(e.CodeUsage, "local qualification requires an explicit Plan")
	}
	digest, err := plan.Digest()
	if err != nil {
		return err
	}
	if digest != frozen.PlanDigest || plan.DefinitionDigest != frozen.DefinitionDigest || plan.EnvironmentDigest != frozen.EnvironmentDigest {
		return e.New(e.CodeTrustPolicyMissing, "local Plan differs from independently selected input identities")
	}
	_, err = toolSmokePlanScope(plan, frozen.Smoke)
	return err
}

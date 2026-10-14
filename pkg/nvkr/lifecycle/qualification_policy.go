// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	yamlv2 "go.yaml.in/yaml/v2"
	"sigs.k8s.io/yaml"
)

const QualificationPolicySchema = "nvkr.dev/qualification-policy/v1"
const maxQualificationPolicyBytes = 1 << 20

// QualificationPolicy is independently selected trust and runtime configuration.
// Loading it authenticates no Run, artifact, smoke result or signing invocation.
// Source input documents and generated candidate records cannot supply it.
type QualificationPolicy struct {
	SchemaVersion         string                           `json:"schemaVersion"`
	Source                qualificationorigin.SourcePolicy `json:"source"`
	QualificationWorkflow string                           `json:"qualificationWorkflow,omitempty"`
	Cosign                QualificationCosignPolicy        `json:"cosign"`
	OutsideMVPSuites      []string                         `json:"outsideMVPSuites,omitempty"`
	RequiredSuites        []string                         `json:"requiredSuites,omitempty"`
	Smoke                 *QualificationSmokePolicy        `json:"smoke,omitempty"`
}

// QualificationCosignPolicy pins the verifier and qualifier executable separately
// from the build's tool selection. An omitted Path selects cosign from PATH;
// execution must still authenticate its exact bytes against Digest.
type QualificationCosignPolicy struct {
	Path            string            `json:"path,omitempty"`
	Version         string            `json:"version,omitempty"`
	Digest          string            `json:"digest,omitempty"`
	PlatformDigests map[string]string `json:"platformDigests,omitempty"`
}

func (policy QualificationPolicy) Validate() error {
	if policy.SchemaVersion != QualificationPolicySchema {
		return e.New(e.CodeTrustPolicyMissing, "qualification policy has an unsupported schema")
	}
	if err := policy.Source.Validate(); err != nil {
		return err
	}
	if policy.QualificationWorkflow != "" {
		qualifier := policy.Source
		qualifier.Workflow = policy.QualificationWorkflow
		if err := qualifier.Validate(); err != nil {
			return err
		}
		if qualifier.Workflow == policy.Source.Workflow {
			return e.New(e.CodeTrustPolicyMissing, "qualification policy requires a workflow distinct from the builder")
		}
	}
	if err := policy.Cosign.validatePins(); err != nil {
		return err
	}
	if strings.TrimSpace(policy.Cosign.Path) != policy.Cosign.Path || strings.ContainsAny(policy.Cosign.Path, "\x00\r\n") {
		return e.New(e.CodeTrustPolicyMissing, "qualification cosign path contains invalid whitespace or control characters")
	}
	if policy.Smoke != nil {
		if err := policy.Smoke.validate(policy); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(policy.OutsideMVPSuites)+len(policy.RequiredSuites))
	for _, names := range [][]string{policy.OutsideMVPSuites, policy.RequiredSuites} {
		for _, name := range names {
			if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\x00\r\n") || seen[name] {
				return e.New(e.CodeTrustPolicyMissing, "qualification suite names must be nonempty, unique and consistently required or outside MVP scope")
			}
			seen[name] = true
		}
	}
	return nil
}

// LoadQualificationPolicy reads one bounded, strict authored YAML or JSON
// document. Explicit relative executable paths resolve from the policy directory;
// a bare command remains a PATH lookup for the pinned execution owner.
func LoadQualificationPolicy(ctx context.Context, path string) (QualificationPolicy, error) {
	if ctx == nil {
		return QualificationPolicy{}, e.New(e.CodeUsage, "qualification policy load requires a context")
	}
	if err := ctx.Err(); err != nil {
		return QualificationPolicy{}, e.Wrap(e.CodeTimeout, err, "qualification policy load canceled")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "qualification policy", MaxBytes: maxQualificationPolicyBytes})
	if err != nil {
		return QualificationPolicy{}, qualificationPolicyReadError(ctx, err)
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxQualificationPolicyBytes)
	if err != nil {
		return QualificationPolicy{}, qualificationPolicyReadError(ctx, err)
	}
	// The streaming decoder rejects a second document; YAML-to-JSON conversion
	// preserves scalar types so a number or boolean cannot become a suite name.
	decoder := yamlv2.NewDecoder(bytes.NewReader(raw))
	decoder.SetStrict(true)
	var document any
	if err := decoder.Decode(&document); err != nil {
		return QualificationPolicy{}, e.Wrap(e.CodeTrustPolicyMissing, err, "decoding qualification policy")
	}
	if err := decoder.Decode(&document); err != io.EOF {
		return QualificationPolicy{}, e.New(e.CodeTrustPolicyMissing, "qualification policy requires exactly one document")
	}
	encoded, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return QualificationPolicy{}, e.Wrap(e.CodeTrustPolicyMissing, err, "decoding qualification policy")
	}
	var policy QualificationPolicy
	jsonDecoder := json.NewDecoder(bytes.NewReader(encoded))
	jsonDecoder.DisallowUnknownFields()
	if err := jsonDecoder.Decode(&policy); err != nil {
		return QualificationPolicy{}, e.Wrap(e.CodeTrustPolicyMissing, err, "unsupported qualification policy fields or types")
	}
	if err := policy.Validate(); err != nil {
		return QualificationPolicy{}, err
	}
	if policy.Cosign.Path == "" {
		policy.Cosign.Path = "cosign"
	} else if !filepath.IsAbs(policy.Cosign.Path) && strings.ContainsRune(policy.Cosign.Path, '/') {
		base, err := filepath.Abs(filepath.Dir(path))
		if err != nil {
			return QualificationPolicy{}, e.Wrap(e.CodeTrustPolicyMissing, err, "resolving qualification policy directory")
		}
		policy.Cosign.Path = filepath.Join(base, policy.Cosign.Path)
	}
	return policy, nil
}

func qualificationPolicyReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "qualification policy read canceled")
	}
	return e.Wrap(e.CodeTrustPolicyMissing, err, "reading qualification policy")
}

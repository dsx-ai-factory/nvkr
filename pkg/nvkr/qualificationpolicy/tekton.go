// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationpolicy

import (
	"net/url"
	"regexp"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/signkey"
)

// Tekton selects the exact compiled execution and independent cluster signer
// roles. It describes trust only; successful provider execution and complete
// published-byte verification remain mandatory before qualification issuance.
type Tekton struct {
	APIServer    string `json:"apiServer"`
	Namespace    string `json:"namespace"`
	Platform     string `json:"platform"`
	PlanDigest   string `json:"planDigest"`
	PublisherKey string `json:"publisherKey"`
	SmokeKey     string `json:"smokeKey"`
	QualifierKey string `json:"qualifierKey"`
}

var clusterNamespace = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func (policy Tekton) Validate() error {
	u, err := url.Parse(policy.APIServer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.String() != policy.APIServer || !clusterNamespace.MatchString(policy.Namespace) || !canonical.IsDigest(policy.PlanDigest) || (policy.Platform != "linux/amd64" && policy.Platform != "linux/arm64") {
		return e.New(e.CodeTrustPolicyMissing, "Tekton qualification requires exact cluster, native platform and Plan trust")
	}
	seen := map[string]bool{}
	for _, raw := range []string{policy.PublisherKey, policy.SmokeKey, policy.QualifierKey} {
		key, err := signkey.NormalizePublicKey([]byte(raw))
		if err != nil {
			return err
		}
		if seen[string(key)] {
			return e.New(e.CodeTrustPolicyMissing, "Tekton publisher, smoke and qualifier keys must be distinct")
		}
		seen[string(key)] = true
	}
	return nil
}

// Identity maps one fixed role, never an artifact-supplied arbitrary account.
func (policy Tekton) Identity(role string) (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	if role != "publisher" && role != "smoke" && role != "qualifier" && role != "finalizer" {
		return "", e.New(e.CodeTrustPolicyMissing, "unknown Tekton qualification role")
	}
	return policy.APIServer + "/api/v1/namespaces/" + policy.Namespace + "/serviceaccounts/nvkr-" + role, nil
}

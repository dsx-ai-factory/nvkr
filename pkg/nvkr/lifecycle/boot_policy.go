// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

func admitSelectedBootPolicy(policy VerificationPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.Lane != LanePipeline || policy.Scope != ScopeSelectedBootArtifacts || policy.PublicKey != "" || policy.KeyIdentity != "" {
		return e.New(e.CodeEngineCapability, "selective boot verification requires keyless pipeline-attested selected-boot-artifacts scope")
	}
	return nil
}

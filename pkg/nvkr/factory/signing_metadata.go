// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// admitSigningMetadata binds a publisher's unsigned control-document handoff to
// this exact output route and Run. Cryptographic SBOM admission follows before
// signing; this check never claims to have observed the payload bytes.
func admitSigningMetadata(plan *Plan, node Node, subject sign.Subject, inspection delivery.Inspection) error {
	target, err := runtimeTarget(plan, node.Target)
	if err != nil {
		return err
	}
	if err := admitDevelopmentSubject(plan, node.Target, subject); err != nil {
		return err
	}
	if (!plan.Environment.GitHubDevelopment && subject.Ref != target.Ref+"@"+subject.Digest) || inspection.RootDigest != subject.Digest {
		return e.New(e.CodeVerifyFailed, "signing metadata differs from the planned publication")
	}
	for _, route := range plan.Routes {
		if route.Name != "release" && node.ID == NodeSign+"/"+route.Name && node.Target == route.Target {
			return admitEvidenceMetadata(plan, route, inspection)
		}
	}
	return e.New(e.CodePipelineDrift, "signing metadata requires one exact named output route")
}

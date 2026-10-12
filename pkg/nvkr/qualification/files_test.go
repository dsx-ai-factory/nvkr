// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"runtime"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

func fileOptionsFixture() FilesOptions {
	digest := "sha256:" + strings.Repeat("a", 64)
	return FilesOptions{ArtifactFiles: lifecycle.QualificationFilesOptions{
		DefinitionPath: "missing-definition", EnvironmentPath: "missing-environment", PlanPath: "missing-plan", CandidatePath: "missing-candidate",
		Source:   qualificationorigin.SourceRequest{Workflow: ".github/workflows/build.yml"},
		Consumer: lifecycle.ConsumerOptions{CosignBinary: "missing-cosign", CosignDigest: digest},
	}, QualificationWorkflow: ".github/workflows/qualify.yml", Signer: Options{CosignBinary: "missing-cosign", CosignDigest: digest}}
}

func TestQualificationFileAdmissionBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*FilesOptions)
		code e.Code
	}{
		{"same workflow", func(o *FilesOptions) { o.QualificationWorkflow = o.ArtifactFiles.Source.Workflow }, e.CodeTrustPolicyMissing},
		{"no workflow", func(o *FilesOptions) { o.QualificationWorkflow = "" }, e.CodeTrustPolicyMissing},
		{"required suite", func(o *FilesOptions) { o.Policy.RequiredSuites = []string{"nvke"} }, e.CodeEngineCapability},
		{"repeated suite", func(o *FilesOptions) { o.Policy.OutsideMVPSuites = []string{"nvke", "nvke"} }, e.CodeTrustPolicyMissing},
		{"missing candidate", func(o *FilesOptions) { o.ArtifactFiles.CandidatePath = "" }, e.CodeUsage},
		{"different verifier pin", func(o *FilesOptions) { o.ArtifactFiles.Consumer.CosignDigest = "sha256:" + strings.Repeat("b", 64) }, e.CodeUnpinnedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := fileOptionsFixture()
			tc.edit(&options)
			requireCode(t, admitFiles(options), tc.code)
			_, err := PublishFiles(t.Context(), options)
			code := tc.code
			if runtime.GOOS != "linux" {
				code = e.CodeEngineCapability
			}
			requireCode(t, err, code)
		})
	}
	options := fileOptionsFixture()
	options.Signer.CosignDigest = ""
	_, err := PublishFiles(t.Context(), options)
	code := e.CodeUnpinnedInput
	if runtime.GOOS != "linux" {
		code = e.CodeEngineCapability
	}
	requireCode(t, err, code)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = PublishFiles(ctx, fileOptionsFixture())
	requireCode(t, err, e.CodeTimeout)
}

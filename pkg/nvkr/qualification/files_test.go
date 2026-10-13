// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

func TestQualificationFileAdmissionBeforeIO(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		options FilesOptions
		code    e.Code
	}{
		{"nil context", nil, FilesOptions{}, e.CodeUsage},
		{"canceled", canceled, FilesOptions{}, e.CodeTimeout},
		{"empty options", t.Context(), FilesOptions{}, e.CodeEngineCapability},
		{"retired valid shape", t.Context(), FilesOptions{ArtifactFiles: lifecycle.QualificationFilesOptions{
			DefinitionPath: "/missing/definition", EnvironmentPath: "/missing/environment", PlanPath: "/missing/plan", CandidatePath: "/missing/candidate",
			Source:   qualificationorigin.SourceRequest{Repository: "example/nvkr", RunID: "42", Attempt: "1", Commit: strings.Repeat("a", 40), Workflow: ".github/workflows/build.yml", DefaultBranch: "main"},
			Consumer: lifecycle.ConsumerOptions{CosignBinary: "/missing/cosign", CosignDigest: "sha256:" + strings.Repeat("b", 64)},
		}, QualificationWorkflow: ".github/workflows/qualify.yml", Signer: Options{CosignBinary: "/missing/cosign", CosignDigest: "sha256:" + strings.Repeat("b", 64)}}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := PublishFiles(tc.ctx, tc.options)
			requireCode(t, err, tc.code)
			if result != nil {
				t.Fatal("retired adapter returned partial publication")
			}
			if tc.ctx == canceled && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause lost")
			}
		})
	}
}

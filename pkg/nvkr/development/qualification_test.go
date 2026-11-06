// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package development

import (
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestQualificationNamespaces(t *testing.T) {
	for _, tc := range []struct {
		workflow, suffix string
		valid            bool
	}{
		{ProducerWorkflow, "", true}, {".github/workflows/nvkr-dev-site.yml", "-site", true},
		{".github/workflows/nvkr-dev-qualify-site.yml", "", false},
		{".github/workflows/nvkr-dev-smoke-site.yml", "", false},
		{".github/workflows/nvkr-dev-qualification-site.yml", "", false},
		{".github/workflows/nvkr-run-site.yml", "", false},
		{".github/workflows/nvkr-dev-../site.yml", "", false},
	} {
		t.Run(tc.workflow, func(t *testing.T) {
			qualifier, smoke, err := QualificationWorkflows(tc.workflow)
			if !tc.valid {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
					t.Fatalf("expected capability refusal: %v", err)
				}
				return
			}
			if err != nil || qualifier != ".github/workflows/nvkr-dev-qualify"+tc.suffix+".yml" || smoke != ".github/workflows/nvkr-dev-smoke"+tc.suffix+".yml" {
				t.Fatalf("wrong workflows: %s %s %v", qualifier, smoke, err)
			}
			namespace, err := NamespaceForProducer("example/nvkr", tc.workflow)
			if err != nil || namespace.Prefix() != "ghcr.io/example/nvkr/development"+tc.suffix {
				t.Fatalf("wrong namespace: %v", err)
			}
		})
	}
}

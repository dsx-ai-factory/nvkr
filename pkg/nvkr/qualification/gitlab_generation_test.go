// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitLabQualificationGenerationRefusesBeforeInputsOrOutput(t *testing.T) {
	for _, engine := range []string{"gitlab", " GitLab "} {
		for _, check := range []bool{false, true} {
			t.Run(engine+"/check="+strconv.FormatBool(check), func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "must-not-exist")
				digest, err := GenerateFiles(t.Context(), "missing-definition.yaml", "missing-environment.yaml", "missing-policy.yaml", "test", engine, root, check)
				requireCode(t, err, e.CodeEngineCapability)
				if digest != "" || !strings.Contains(err.Error(), "GitLab qualification generation is unavailable") {
					t.Fatalf("unexpected refusal: digest %q error %v", digest, err)
				}
				if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
					t.Fatalf("unavailable qualification created output: %v", statErr)
				}
			})
		}
	}
}

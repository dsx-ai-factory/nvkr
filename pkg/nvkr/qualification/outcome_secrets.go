// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
)

// The generated production qualify step binds these to secrets or the job token;
// the runner gives every step of an id-token: write job the OIDC request token.
// TestQualificationGenerationOutcomeSecretsCoverStep keeps the list complete.
var outcomeSecretEnv = []string{"ACTIONS_ID_TOKEN_REQUEST_TOKEN", "NVKR_CI_STORAGE_TOKEN", "NVKR_GITHUB_TOKEN", "NVKR_REGISTRY_PASSWORD"}

const outcomeSourceTokenPrefix = "NVKR_PIN_SOURCE_TOKEN_"

// OutcomeSecrets returns the generated qualify step's secret values found in
// environ (os.Environ form), which qualificationoutcome redacts from the record:
// the job log masks them, but an uploaded artifact does not. A value, or a line
// of a multi-line value, too short to redact without erasing ordinary text is
// refused, so callers admit it before any work.
func OutcomeSecrets(environ []string) ([]string, error) {
	var values []string
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		if (!slices.Contains(outcomeSecretEnv, name) && !strings.HasPrefix(name, outcomeSourceTokenPrefix)) || strings.TrimSpace(value) == "" {
			continue
		}
		for _, line := range strings.Split(value, "\n") {
			if line = strings.TrimSpace(line); line != "" && len(line) < qualificationoutcome.MinSecretBytes {
				return nil, e.New(e.CodeUsage, "%s holds a value or line shorter than %d bytes, so the qualification outcome cannot redact it", name, qualificationoutcome.MinSecretBytes)
			}
		}
		values = append(values, value)
	}
	return values, nil
}

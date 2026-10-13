// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

type qualificationSourceAuth struct {
	environment     map[string]string
	secrets, tokens []string
}

var qualificationSecretExpression = regexp.MustCompile(`^\$\{\{ secrets\.([A-Za-z_][A-Za-z0-9_]*) \}\}$`)

func qualificationSourceAuthentication(plan *factory.Plan) (qualificationSourceAuth, error) {
	env, err := factory.GitHubCatalogSourceEnvironment(plan)
	if err != nil {
		return qualificationSourceAuth{}, err
	}
	result := qualificationSourceAuth{environment: env}
	secrets := map[string]bool{}
	for key, value := range env {
		if !strings.HasPrefix(key, "NVKR_PIN_SOURCE_TOKEN_") {
			continue
		}
		match := qualificationSecretExpression.FindStringSubmatch(value)
		if len(match) != 2 {
			return qualificationSourceAuth{}, e.New(e.CodeInvalidCatalog, "source credential does not have an exact GitHub secret reference")
		}
		result.tokens = append(result.tokens, key)
		if strings.EqualFold(match[1], "GITHUB_TOKEN") {
			env[key] = "${{ github.token }}"
		} else {
			if strings.HasPrefix(strings.ToUpper(match[1]), "GITHUB_") {
				return qualificationSourceAuth{}, e.New(e.CodeEngineCapability, "source secret uses a GitHub-reserved name")
			}
			secrets[match[1]] = true
		}
	}
	for key := range secrets {
		result.secrets = append(result.secrets, key)
	}
	sort.Strings(result.secrets)
	sort.Strings(result.tokens)
	return result, nil
}
func (auth qualificationSourceAuth) writeCallerSecrets(out *bytes.Buffer) {
	if len(auth.secrets) == 0 {
		return
	}
	fmt.Fprintln(out, "    secrets:")
	for _, name := range auth.secrets {
		fmt.Fprintf(out, "      %s: ${{ secrets.%s }}\n", name, name)
	}
}
func (auth qualificationSourceAuth) writeCallContract(out *bytes.Buffer) {
	if len(auth.secrets) == 0 {
		fmt.Fprintln(out, "    inputs: {}")
		return
	}
	fmt.Fprintln(out, "    secrets:")
	for _, name := range auth.secrets {
		fmt.Fprintf(out, "      %s:\n        required: true\n", name)
	}
}
func (auth qualificationSourceAuth) writeEnvironment(out *bytes.Buffer) {
	keys := make([]string, 0, len(auth.environment))
	for key := range auth.environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(out, "          %s: %s\n", key, qualificationYAML(auth.environment[key]))
	}
}
func (auth qualificationSourceAuth) requireTokens() string {
	var out strings.Builder
	for _, key := range auth.tokens {
		fmt.Fprintf(&out, "          [ -n \"${%s:-}\" ] || { printf '%%s\\n' 'nvkr: [NVKR-E-FETCH-FAILED] source credential is empty' >&2; exit 20; }\n", key)
	}
	return out.String()
}

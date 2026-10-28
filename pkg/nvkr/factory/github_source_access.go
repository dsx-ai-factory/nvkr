// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const sourceTokenEnv = "NVKR_SOURCE_TOKEN"

// Git invokes this helper through a pipe. The token remains in the invoking step's
// environment, never in an argument, remote URL, persisted config or carrier.
const sourceCredentialHelper = sourcegit.CredentialHelper

const requireSourceToken = `[ -n "${NVKR_SOURCE_TOKEN:-}" ] || { printf '%s\n' 'nvkr: [NVKR-E-FETCH-FAILED] source credential is empty' >&2; exit 20; }`

func githubSourceEnvironment(plan *Plan, node Node) map[string]string {
	if node.Kind != NodeFetch || !irProjectedNode(node) {
		return nil
	}
	for _, build := range plan.Builds {
		if build.Name != node.BuildTarget {
			continue
		}
		for _, source := range build.Graph.Nodes {
			if source.ID != node.IRNode || source.Kind != ir.KindFetch {
				continue
			}
			access, ok := plan.Environment.SourceAccess[source.Repo]
			if !ok {
				return nil
			}
			return map[string]string{
				sourceTokenEnv:        "${{ secrets." + strings.TrimPrefix(access.CredentialRef, "github:") + " }}",
				"GIT_TERMINAL_PROMPT": "0",
				"GIT_CONFIG_NOSYSTEM": "1",
				"GIT_CONFIG_GLOBAL":   "/dev/null",
				"GIT_CONFIG_COUNT":    "4",
				"GIT_CONFIG_KEY_0":    "url." + access.Mirror + ".insteadOf",
				"GIT_CONFIG_VALUE_0":  source.Repo,
				"GIT_CONFIG_KEY_1":    "credential.helper",
				"GIT_CONFIG_VALUE_1":  "",
				"GIT_CONFIG_KEY_2":    "credential." + access.Mirror + ".helper",
				"GIT_CONFIG_VALUE_2":  sourceCredentialHelper,
				"GIT_CONFIG_KEY_3":    "credential.useHttpPath",
				"GIT_CONFIG_VALUE_3":  "true",
			}
		}
	}
	return nil
}

// Catalog revalidation reads pinned Git trees again. Scope these credentials to
// the two steps that recompile authored inputs, including overlay catalogs.
func githubCatalogSourceEnvironment(plan *Plan) map[string]string {
	repos := map[string]bool{}
	for repository := range plan.catalogPinSetRepositories() {
		if _, ok := plan.Environment.SourceAccess[repository]; ok {
			repos[repository] = true
		}
	}
	if len(repos) == 0 {
		return nil
	}
	env := map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null"}
	index := 0
	add := func(key, value string) {
		suffix := strconv.Itoa(index)
		env["GIT_CONFIG_KEY_"+suffix] = key
		env["GIT_CONFIG_VALUE_"+suffix] = value
		index++
	}
	add("credential.helper", "")
	add("credential.useHttpPath", "true")
	for i, repo := range sortedKeys(repos) {
		access := plan.Environment.SourceAccess[repo]
		token := "NVKR_PIN_SOURCE_TOKEN_" + strconv.Itoa(i)
		env[token] = "${{ secrets." + strings.TrimPrefix(access.CredentialRef, "github:") + " }}"
		add("url."+access.Mirror+".insteadOf", repo)
		add("credential."+access.Mirror+".helper", strings.ReplaceAll(sourceCredentialHelper, sourceTokenEnv, token))
	}
	env["GIT_CONFIG_COUNT"] = strconv.Itoa(index)
	return env
}

func writeCatalogValidationStep(out *bytes.Buffer, plan *Plan, name, id, command string) {
	fmt.Fprintf(out, "      - name: %s\n        id: %s\n", name, id)
	env := githubCatalogSourceEnvironment(plan)
	if len(env) == 0 {
		fmt.Fprintf(out, "        run: %s\n", command)
		return
	}
	fmt.Fprintln(out, "        env:")
	for _, key := range sortedKeys(env) {
		fmt.Fprintf(out, "          %s: %s\n", key, yamlSingle(env[key]))
	}
	fmt.Fprintln(out, "        run: |")
	fmt.Fprintln(out, "          set -eu")
	for _, key := range sortedKeys(env) {
		if strings.HasPrefix(key, "NVKR_PIN_SOURCE_TOKEN_") {
			fmt.Fprintln(out, "          "+strings.ReplaceAll(requireSourceToken, sourceTokenEnv, key))
		}
	}
	fmt.Fprintln(out, "          "+command)
}

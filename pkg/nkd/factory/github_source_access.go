package factory

import (
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

const sourceTokenEnv = "NKD_SOURCE_TOKEN"

// Git invokes this helper through a pipe. The token remains in the fetch step's
// environment, never in an argument, remote URL, persisted config or carrier.
const sourceCredentialHelper = `!f() { if [ "$1" = get ]; then printf '%s\n' 'username=x-access-token' "password=$NKD_SOURCE_TOKEN"; fi; }; f`

const requireSourceToken = `[ -n "${NKD_SOURCE_TOKEN:-}" ] || { printf '%s\n' 'nkdctl: [NKD-E-FETCH-FAILED] source credential is empty' >&2; exit 20; }`

func githubSourceEnvironment(plan *Plan, node Node) map[string]string {
	if node.Kind != NodeFetch || !irProjectedNode(node) {
		return nil
	}
	for _, recipe := range plan.Recipes {
		if recipe.Name != node.RecipeTarget {
			continue
		}
		for _, source := range recipe.Graph.Nodes {
			if source.ID != node.IRNode || source.Kind != ir.KindFetch {
				continue
			}
			access, ok := plan.ExecutionSettings.SourceAccess[source.Repo]
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

package factory

import (
	"fmt"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

// Labels describe work; stable step IDs and exact commands retain graph binding.
// Names come from admitted plan fields, never credentials or arbitrary commands.
func githubNodeLabel(plan *Plan, node Node) string {
	name := strings.TrimPrefix(node.ID, node.Kind+"/")
	switch node.Kind {
	case NodeFetch:
		return "Fetch pinned source for " + githubSourceConsumers(plan, node)
	case NodeBuild:
		for _, recipe := range plan.Recipes {
			if recipe.Name == node.RecipeTarget && recipe.Graph != nil {
				for _, build := range recipe.Graph.Nodes {
					if build.ID == node.IRNode {
						return "Build " + build.Component + " (" + node.RecipeTarget + ")"
					}
				}
			}
		}
		return "Build " + name
	case NodeAcquire:
		return "Acquire pinned " + strings.TrimPrefix(name, "tool/")
	case NodeVerify:
		return "Verify built artifacts: " + node.RecipeTarget
	case NodeSBOM:
		return "Generate SBOMs: " + node.RecipeTarget
	case NodePackage:
		switch node.PackageKind {
		case PackageImage:
			return "Package container image: " + name
		case PackageHelm:
			if node.HelmImage != nil {
				return "Bind image digest and package Helm: " + name
			}
			return "Package Helm bundle: " + name
		case PackageBinaryBundle:
			return "Package binaries: " + name
		case PackageAPT:
			return "Package APT repository: " + name
		case PackageOCI:
			return "Package publication bundle: " + name
		}
		return "Package " + name
	case NodePublish:
		return "Publish " + name
	case NodeSign:
		return "Sign and attest " + name
	case NodeVerifyPublication:
		return "Verify published signatures: " + name
	case NodeComposition:
		return "Assemble cluster: " + name
	case NodeAICR:
		return "Generate deployment bundle: " + node.RecipeTarget
	default:
		return node.Kind + ": " + name
	}
}

func githubSourceConsumers(plan *Plan, node Node) string {
	var components []string
	for _, recipe := range plan.Recipes {
		if recipe.Name != node.RecipeTarget || recipe.Graph == nil {
			continue
		}
		for _, candidate := range recipe.Graph.Nodes {
			if candidate.Kind != ir.KindBuild {
				continue
			}
			for _, need := range candidate.Needs {
				if need == node.IRNode {
					components = append(components, candidate.Component)
				}
			}
		}
	}
	if len(components) == 0 {
		return node.RecipeTarget
	}
	return strings.Join(uniqueSorted(components), ", ") + " (" + node.RecipeTarget + ")"
}

func githubStageLabel(plan *Plan, nodes []Node) string {
	if len(nodes) == 1 {
		return githubNodeLabel(plan, nodes[0])
	}
	var packages, recipes []string
	bindsImage := false
	for _, node := range nodes {
		if node.Kind == NodePackage {
			packages = append(packages, strings.TrimPrefix(node.ID, "package/"))
		} else if node.RecipeTarget != "" {
			recipes = append(recipes, node.RecipeTarget)
		}
		bindsImage = bindsImage || node.HelmImage != nil
	}
	if len(packages) > 0 {
		label := "Verify and package outputs"
		if bindsImage {
			label += " with Helm image binding"
		}
		return label
	}
	return "Verify artifacts and generate SBOMs: " + strings.Join(uniqueSorted(recipes), ", ")
}

func githubArtifactLabel(plan *Plan, artifact Artifact) string {
	switch artifact.Kind {
	case "source-closure", "source-receipt":
		for _, node := range plan.Graph.Nodes {
			if node.ID == artifact.Producer {
				label := "source for "
				if artifact.Kind == "source-receipt" {
					label = "source verification for "
				}
				return label + githubSourceConsumers(plan, node)
			}
		}
	case string(TierTerminalObservation):
		return "artifact checks for " + artifact.RecipeTarget
	case "producer-result":
		return "build result for " + artifact.Component
	case "sbom-set":
		return "SBOMs for " + artifact.RecipeTarget
	}
	kind := map[string]string{
		"host-package": "binary", "oci-image": "image export", "tarball": "source archive",
		PackageImage: "container image", PackageHelm: "Helm bundle", PackageOCI: "publication bundle",
		PackageBinaryBundle: "binary bundle", PackageAPT: "APT repository",
		"provenance-predicate": "provenance", "published-reference": "published reference",
		"signing-record": "signature record", "verification-record": "signature checks", "pipeline-tool": "tool",
	}[artifact.Kind]
	if kind == "" {
		kind = strings.ReplaceAll(artifact.Kind, "-", " ")
	}
	return artifact.Name + " (" + kind + ")"
}

// The EXIT trap reports failure without swallowing the command's status. No env
// dump, command expansion, credentials or destination URLs enter these messages.
func githubNodeCommands(node Node, plan *Plan, graphDigests map[string]string) []string {
	commands := []string{
		"set -euo pipefail",
		"nkd_started=$SECONDS",
		"nkd_step=" + shellQuote(githubNodeLabel(plan, node)),
		`nkd_finish() { nkd_status=$?; if [ "$nkd_status" -eq 0 ]; then printf "Completed: %s (%ss)\n" "$nkd_step" "$((SECONDS - nkd_started))"; else printf "Failed: %s (exit %s, %ss)\n" "$nkd_step" "$nkd_status" "$((SECONDS - nkd_started))" >&2; fi; exit "$nkd_status"; }`,
		"trap nkd_finish EXIT",
		`printf 'Starting: %s\n' "$nkd_step"`,
		fmt.Sprintf("printf '%%s\\n' %s", shellQuote("Node: "+node.ID)),
	}
	if len(githubSourceEnvironment(plan, node)) > 0 {
		commands = append(commands, requireSourceToken)
	}
	commands = append(commands, generatedNodeCommands(node, plan, graphDigests, FactoryPlanPath, FactoryGraphDir, "$GITHUB_WORKSPACE")...)
	for _, output := range node.Outputs {
		commands = append(commands, "printf '%s\\n' "+shellQuote("Produced: "+output))
	}
	return commands
}

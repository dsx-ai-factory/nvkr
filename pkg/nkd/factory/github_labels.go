package factory

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Labels describe work; stable step IDs and exact commands retain graph binding.
// Names come from admitted plan fields, never credentials or arbitrary commands.
func githubNodeLabel(plan *Plan, node Node) string {
	name := strings.TrimPrefix(node.ID, node.Kind+"/")
	switch node.Kind {
	case NodeFetch:
		return "Fetch pinned source for " + githubSourceConsumers(plan, node)
	case NodeBuild:
		for _, build := range plan.Builds {
			if build.Name == node.BuildTarget && build.Graph != nil {
				for _, build := range build.Graph.Nodes {
					if build.ID == node.IRNode {
						return "Build " + build.Component + " (" + node.BuildTarget + ")"
					}
				}
			}
		}
		return "Build " + name
	case NodeAcquire:
		return "Acquire pinned " + strings.TrimPrefix(name, "tool/")
	case NodeVerify:
		return "Verify built artifacts: " + node.BuildTarget
	case NodeSBOM:
		return "Generate SBOMs: " + node.BuildTarget
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
	case NodeAttestEvidence:
		return "Create and verify SBOM attestations: " + name
	case NodeSign:
		return "Sign and attest release and deliverables: " + name
	case NodeVerifyPublication:
		return "Verify release and deliverable signatures: " + name
	default:
		return node.Kind + ": " + name
	}
}

func githubSourceConsumers(plan *Plan, node Node) string {
	var components []string
	for _, build := range plan.Builds {
		if build.Name != node.BuildTarget || build.Graph == nil {
			continue
		}
		for _, candidate := range build.Graph.Nodes {
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
		return node.BuildTarget
	}
	return strings.Join(uniqueSorted(components), ", ") + " (" + node.BuildTarget + ")"
}

func githubStageLabel(plan *Plan, nodes []Node) string {
	if len(nodes) == 1 {
		return githubNodeLabel(plan, nodes[0])
	}
	var packages, builds []string
	bindsImage := false
	for _, node := range nodes {
		if node.Kind == NodePackage {
			packages = append(packages, strings.TrimPrefix(node.ID, "package/"))
		} else if node.BuildTarget != "" {
			builds = append(builds, node.BuildTarget)
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
	return "Verify artifacts and generate SBOMs: " + strings.Join(uniqueSorted(builds), ", ")
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
		return "artifact checks for " + artifact.BuildTarget
	case "producer-result":
		return "build result for " + artifact.Component
	case "sbom-set":
		return "SBOMs for " + artifact.BuildTarget
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
func githubNodeCommands(node Node, plan *Plan) []string {
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
	commands = append(commands, generatedNodeCommands(node, plan, FactoryPlanPath, "$GITHUB_WORKSPACE")...)
	for _, output := range node.Outputs {
		commands = append(commands, "printf '%s\\n' "+shellQuote("Produced: "+output))
	}
	return commands
}

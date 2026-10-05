package factory

import (
	"bytes"
	"fmt"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Batch only the exact provider names of required carriers. The provider's batch
// action can succeed on partial/no matches; every subsequent exact unpack remains
// mandatory and fails closed before executing a consumer. No wildcard selects
// unrelated artifacts or merges their untrusted payloads directly into a workspace.
func writeGitHubStageInputSteps(out *bytes.Buffer, plan *Plan, stage githubStage, artifacts map[string]Artifact, nodeJobs map[string]string, actions githubActionRefs) error {
	inputs := stage.inputs(artifacts, nodeJobs)
	if len(inputs) == 0 {
		return nil
	}
	selected := make([]Artifact, len(inputs))
	var details []string
	var work []githubProgressCommand
	for i, input := range inputs {
		selected[i] = artifacts[input]
		details = append(details, githubArtifactDetail(plan, "Input", input))
		work = append(work, githubProgressCommand{phase: "restore " + input,
			run: fmt.Sprintf("printf '%%s\\n' %s || :\n./nkd-exec transport-unpack --archive %s --root . --path %s",
				githubShellText("Restore: "+input), shellQuote(carrierPath(input)), shellQuote(artifacts[input].Path))})
	}
	pattern, err := githubCarrierPattern(selected)
	if err != nil {
		return err
	}
	var restored bytes.Buffer
	if err := writeGitHubRestoreBatches(&restored, details, work); err != nil {
		return err
	}
	fmt.Fprintf(out, "      - name: Download %d required input carriers\n        uses: %s\n        with:\n          pattern: %s\n          path: .nkd/carriers\n          merge-multiple: 'true'\n", len(inputs), actions.Download, yamlSingle(pattern))
	out.Write(restored.Bytes())
	return nil
}

// Keep each exact carrier restore and its failure phase together. GitHub skips
// subsequent ordinary steps after a failure, so splitting a batch cannot allow
// the consumer to run with missing inputs. Admit every script before any output.
func writeGitHubRestoreBatches(out *bytes.Buffer, details []string, work []githubProgressCommand) error {
	if len(details) != len(work) || len(work) == 0 {
		return e.New(e.CodeInvalidCatalog, "GitHub restore batches require exact input descriptions")
	}
	type restoreBatch struct {
		commands []string
		count    int
	}
	var batches []restoreBatch
	start := 0
	for end := 1; end <= len(work); end++ {
		commands := githubProgressCommands("Restore required inputs", details[start:end], work[start:end])
		if githubScriptBytes(commands) > githubMaximumRunBytes {
			if end-start == 1 {
				return e.New(e.CodeEngineCapability, "one GitHub input restore exceeds the provider script limit")
			}
			batches = append(batches, restoreBatch{githubProgressCommands("Restore required inputs", details[start:end-1], work[start:end-1]), end - 1 - start})
			start = end - 1
			commands = githubProgressCommands("Restore required inputs", details[start:end], work[start:end])
			if githubScriptBytes(commands) > githubMaximumRunBytes {
				return e.New(e.CodeEngineCapability, "one GitHub input restore exceeds the provider script limit")
			}
		}
		if end == len(work) {
			batches = append(batches, restoreBatch{commands, end - start})
		}
	}
	for i, batch := range batches {
		label := fmt.Sprintf("Restore %d required inputs", batch.count)
		if len(batches) > 1 {
			label += fmt.Sprintf(" (%d/%d)", i+1, len(batches))
		}
		writeGitHubShellStep(out, label, batch.commands)
	}
	return nil
}

func writeGitHubOutputSteps(out *bytes.Buffer, plan *Plan, node Node, artifacts map[string]Artifact, actions githubActionRefs) {
	if len(node.Outputs) == 0 {
		return
	}
	var details []string
	var work []githubProgressCommand
	for _, output := range node.Outputs {
		details = append(details, githubArtifactDetail(plan, "Output", output))
		work = append(work, githubProgressCommand{phase: "archive " + output,
			run: fmt.Sprintf("printf '%%s\\n' %s || :\n./nkd-exec transport-pack --archive %s --root . --path %s",
				githubShellText("Archive: "+output), shellQuote(carrierPath(output)), shellQuote(artifacts[output].Path))})
	}
	writeGitHubShellStep(out, "Prepare output carriers: "+githubNodeLabel(plan, node),
		githubProgressCommands("Prepare output carriers", details, work))
	for _, output := range node.Outputs {
		actions.writeUploadStep(out, githubArtifactLabel(plan, artifacts[output]), githubArtifactName(output), carrierPath(output))
	}
}

func writeGitHubShellStep(out *bytes.Buffer, label string, commands []string) {
	fmt.Fprintf(out, "      - name: %s\n        run: |\n", yamlSingle(label))
	for _, command := range commands {
		for _, line := range strings.Split(command, "\n") {
			fmt.Fprintf(out, "          %s\n", line)
		}
	}
}

// Carrier names are derived from admitted IDs, so this exact brace set has no
// minimatch metacharacters. Fail before returning bytes on lossy name collisions.
func githubCarrierPattern(artifacts []Artifact) (string, error) {
	if len(artifacts) == 0 {
		return "", e.New(e.CodeInvalidCatalog, "GitHub carrier selection requires at least one artifact")
	}
	seenNames, seenPaths := map[string]string{}, map[string]string{}
	var names []string
	for _, artifact := range artifacts {
		name, path := githubArtifactName(artifact.ID), carrierPath(artifact.ID)
		if previous := seenNames[name]; previous != "" {
			return "", e.New(e.CodeInvalidCatalog, "GitHub artifacts %q and %q collide at carrier name %q", previous, artifact.ID, name)
		}
		if previous := seenPaths[path]; previous != "" {
			return "", e.New(e.CodeInvalidCatalog, "GitHub artifacts %q and %q collide at carrier path %q", previous, artifact.ID, path)
		}
		seenNames[name], seenPaths[path] = artifact.ID, artifact.ID
		names = append(names, name)
	}
	names = uniqueSorted(names)
	if len(names) == 1 {
		return names[0], nil
	}
	return "{" + strings.Join(names, ",") + "}", nil
}

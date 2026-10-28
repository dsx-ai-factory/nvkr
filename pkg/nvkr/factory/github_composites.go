// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

const githubActionsDirectory = ".github/nvkr-run/actions/"

func githubActionsDirectoryFor(plan *Plan) string {
	return strings.TrimSuffix(githubLayoutFor(plan).planPath, "plan.json") + "actions/"
}

// lowerGitHubActions changes step presentation only. Jobs, provider outcome IDs,
// dependencies and authority stay in the entry workflow. Provider controls stay
// on the caller; single-step transport actions can carry their implementation.
// Referenced outputs remain inline to preserve the caller's step namespace.
func lowerGitHubActions(plan *Plan, raw []byte, actions githubActionRefs) ([]byte, map[string][]byte, error) {
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		return nil, nil, e.Wrap(e.CodePipelineDrift, err, "parse GitHub workflow for action lowering")
	}
	if len(document.Content) != 1 {
		return nil, nil, e.New(e.CodePipelineDrift, "GitHub action lowering requires a workflow document")
	}
	// Extracted actions are separate YAML documents. Materialize existing aliases
	// before splitting so credentials and controls cannot retain external anchors.
	resolved, err := githubResolveAliases(&document, map[*yamlv3.Node]bool{})
	if err != nil {
		return nil, nil, err
	}
	document = *resolved
	files := map[string][]byte{}
	jobs := githubYAMLField(document.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yamlv3.MappingNode {
		return nil, nil, e.New(e.CodePipelineDrift, "GitHub action lowering requires jobs")
	}
	for index := 0; index < len(jobs.Content); index += 2 {
		id, job := jobs.Content[index].Value, jobs.Content[index+1]
		steps := githubYAMLField(job, "steps")
		if steps == nil || steps.Kind != yamlv3.SequenceNode {
			return nil, nil, e.New(e.CodePipelineDrift, "GitHub job %q requires steps", id)
		}
		// Preserve the original Linux default shell, including the job-container
		// sh default. Explicit `bash` would silently add pipefail to authored setup.
		shell := "bash -e {0}"
		if githubYAMLField(job, "container") != nil {
			shell = "sh -e {0}"
		}
		var lowered, pending []*yamlv3.Node
		checkedOut, segment, transfer := false, 0, 0
		flush := func() error {
			if len(pending) == 0 {
				return nil
			}
			segment++
			name := githubYAMLValue(job, "name")
			if segment > 1 {
				name = fmt.Sprintf("%s (part %d)", name, segment)
			}
			path := fmt.Sprintf("%sjobs/%s/part-%d", githubActionsDirectoryFor(plan), id, segment)
			call, err := githubComposite(files, path, name, pending, shell)
			if err != nil {
				return err
			}
			lowered = append(lowered, call)
			pending = nil
			return nil
		}
		for position := 0; position < len(steps.Content); position++ {
			step := steps.Content[position]
			keep := !checkedOut || !githubCompositeSafeStep(step)
			referenced := false
			// A step's outputs may be consumed by a later inline diagnostic.
			// Keep its ID in the job scope whenever any step refers to that ID.
			if stepID := githubYAMLValue(step, "id"); stepID != "" && githubYAMLContains(steps, "steps."+stepID+".") {
				keep = true
				referenced = true
			}
			if keep {
				if err := flush(); err != nil {
					return nil, nil, err
				}
				// A one-step transport wrapper leaves provider controls on the
				// caller while moving changing carrier names and action pins out
				// of the workflow. It cannot hide a producer or output-producing
				// shell behind a different step outcome.
				uses := githubYAMLValue(step, "uses")
				if checkedOut && !referenced && (uses == actions.Upload || uses == actions.Download) {
					transfer++
					path := fmt.Sprintf("%sjobs/%s/transfer-%d", githubActionsDirectoryFor(plan), id, transfer)
					call, err := githubControlledTransfer(files, path, step, shell)
					if err != nil {
						return nil, nil, err
					}
					lowered = append(lowered, call)
					continue
				}
				lowered = append(lowered, step)
				if githubYAMLValue(step, "uses") == actions.Checkout {
					checkedOut = true
				}
				continue
			}
			// Consolidate the repeated, ordered helper delivery in one action.
			if githubYAMLValue(step, "uses") == actions.Download && githubYAMLValue(step, "name") == "Download NVKR runner" && position+1 < len(steps.Content) &&
				githubYAMLValue(steps.Content[position+1], "name") == prepareHelperStepName && strings.TrimSpace(githubYAMLValue(steps.Content[position+1], "run")) == prepareHelperRunFor(plan) {
				artifact := githubYAMLField(githubYAMLField(step, "with"), "name")
				value := *artifact
				artifact.Value = "${{ inputs.runner-artifact }}"
				artifact.Style = 0
				path := githubActionsDirectoryFor(plan) + "prepare-runner"
				if shell == "sh -e {0}" {
					path += "-container"
				}
				call, err := githubComposite(files, path, "Prepare NVKR runner", steps.Content[position:position+2], shell)
				if err != nil {
					return nil, nil, err
				}
				// This single fixed input is data for download-artifact, never shell.
				var action yamlv3.Node
				if err := yamlv3.Unmarshal(files[path+"/action.yml"], &action); err != nil {
					return nil, nil, e.Wrap(e.CodeInternal, err, "decode shared helper action")
				}
				githubYAMLSet(action.Content[0], "inputs", githubYAMLMap("runner-artifact", githubActionInput("Same-Run helper artifact name")))
				files[path+"/action.yml"], err = encodeGitHubYAML(&action)
				if err != nil {
					return nil, nil, err
				}
				githubYAMLSet(call, "with", githubYAMLMap("runner-artifact", &value))
				pending = append(pending, call)
				position++
				continue
			}
			pending = append(pending, step)
		}
		if err := flush(); err != nil {
			return nil, nil, err
		}
		steps.Content = lowered
	}
	if err := anchorGitHubPreflight(plan, jobs, actions); err != nil {
		return nil, nil, err
	}
	workflow, err := encodeGitHubYAML(&document)
	return workflow, files, err
}

func githubResolveAliases(node *yamlv3.Node, active map[*yamlv3.Node]bool) (*yamlv3.Node, error) {
	if node == nil || active[node] {
		return nil, e.New(e.CodePipelineDrift, "generated workflow contains an invalid or recursive YAML alias")
	}
	active[node] = true
	defer delete(active, node)
	if node.Kind == yamlv3.AliasNode {
		return githubResolveAliases(node.Alias, active)
	}
	copyOfNode := *node
	copyOfNode.Anchor = ""
	copyOfNode.Content = make([]*yamlv3.Node, len(node.Content))
	for index, child := range node.Content {
		resolved, err := githubResolveAliases(child, active)
		if err != nil {
			return nil, err
		}
		copyOfNode.Content[index] = resolved
	}
	return &copyOfNode, nil
}

// Fixed preflight, runner preparation and complete semantic calls get readable
// aliases only after structural equality. Job policy and dependencies stay inline.
func anchorGitHubPreflight(plan *Plan, jobs *yamlv3.Node, actions githubActionRefs) error {
	// Rebuild presentation aliases from independent nodes. Whole-sequence sharing
	// must not remove an earlier definition that a later step alias still needs.
	resolved, err := githubResolveAliases(jobs, map[*yamlv3.Node]bool{})
	if err != nil {
		return err
	}
	*jobs = *resolved
	if err := anchorGitHubStepSequences(plan, jobs, actions); err != nil {
		return err
	}
	first := map[string]*yamlv3.Node{}
	for index := 1; index < len(jobs.Content); index += 2 {
		steps := githubYAMLField(jobs.Content[index], "steps")
		if steps == nil {
			continue // Reusable-workflow callers have no local steps.
		}
		for position, step := range steps.Content {
			if step.Kind == yamlv3.AliasNode {
				continue
			}
			name := ""
			switch {
			case strings.HasPrefix(githubYAMLValue(step, "uses"), "./"+githubActionsDirectoryFor(plan)+"shared/prepare-stage"):
				name = "prepare-stage-" + shortStableName(githubYAMLValue(githubYAMLField(step, "with"), "runner-artifact"))
				if strings.HasSuffix(githubYAMLValue(step, "uses"), "-container") {
					name += "-container"
				}
			case githubYAMLValue(step, "uses") == actions.Checkout:
				name = "checkout-workflow-source"
			case githubYAMLValue(step, "name") == githubPlatformCheckName:
				switch {
				case strings.Contains(githubYAMLValue(step, "run"), "Linux/x86_64"):
					name = "check-native-linux-amd64"
				case strings.Contains(githubYAMLValue(step, "run"), "Linux/aarch64"):
					name = "check-native-linux-arm64"
				}
			case strings.HasPrefix(githubYAMLValue(step, "uses"), "./"+githubActionsDirectoryFor(plan)+"shared/"):
				family := strings.TrimPrefix(githubYAMLValue(step, "uses"), "./"+githubActionsDirectoryFor(plan)+"shared/")
				binding := githubYAMLValue(githubYAMLField(step, "with"), "parameters")
				prefix := "${{ fromJSON(steps." + githubContextStepID + ".outputs.data)['"
				part := strings.TrimSuffix(strings.TrimPrefix(binding, prefix), "'] }}")
				controlled := false
				for _, field := range githubTransferControlFields {
					controlled = controlled || githubYAMLField(step, field) != nil
				}
				if !controlled && binding == githubParametersExpression(part) && githubActionPart(part) {
					name = "operation-" + family + "-" + part
				}
			}
			if name == "" {
				continue
			}
			if prior := first[name]; prior != nil {
				// The selected surfaces are generated constants. Still require
				// complete structural equality before sharing them.
				copyOfPrior := *prior
				copyOfPrior.Anchor = ""
				previous, err := encodeGitHubYAML(&copyOfPrior)
				if err != nil {
					return err
				}
				current, err := encodeGitHubYAML(step)
				if err != nil {
					return err
				}
				if bytes.Equal(previous, current) {
					prior.Anchor = name
					steps.Content[position] = &yamlv3.Node{Kind: yamlv3.AliasNode, Value: name, Alias: prior}
				}
			} else {
				first[name] = step
			}
		}
	}
	return nil
}

func githubCompositeSafeStep(step *yamlv3.Node) bool {
	// Authored setup can use any workflow expression or action-dependent
	// environment variable. Its caller scope is part of the existing contract.
	if githubYAMLValue(step, "name") == "Configure build environment" {
		return false
	}
	for _, key := range []string{"if", "timeout-minutes", "continue-on-error"} {
		if githubYAMLField(step, key) != nil {
			return false
		}
	}
	return !githubYAMLContains(step, "steps.")
}

var githubTransferControlFields = []string{"id", "if", "timeout-minutes", "continue-on-error"}

const githubAlwaysCondition = "${{ always() }}"

func githubControlledTransfer(files map[string][]byte, path string, step *yamlv3.Node, shell string) (*yamlv3.Node, error) {
	inner := *step
	inner.Content = append([]*yamlv3.Node(nil), step.Content...)
	for _, key := range githubTransferControlFields {
		githubYAMLRemove(&inner, key)
	}
	// GitHub rechecks embedded conditions on cancellation. An always-run
	// transport must retain that behavior inside its wrapper; ordinary and
	// failure-only transfers must remain cancellable through default success().
	if githubYAMLValue(step, "if") == githubAlwaysCondition {
		githubYAMLSet(&inner, "if", githubYAMLString(githubAlwaysCondition))
	}
	call, err := githubComposite(files, path, githubYAMLValue(step, "name"), []*yamlv3.Node{&inner}, shell)
	if err != nil {
		return nil, err
	}
	for _, key := range githubTransferControlFields {
		if value := githubYAMLField(step, key); value != nil {
			githubYAMLSet(call, key, value)
		}
	}
	return call, nil
}

func githubComposite(files map[string][]byte, path, name string, steps []*yamlv3.Node, shell string) (*yamlv3.Node, error) {
	inputs, arguments := githubYAMLMap(), githubYAMLMap()
	for _, step := range steps {
		if githubYAMLField(step, "run") != nil {
			githubYAMLSet(step, "shell", githubYAMLString(shell))
		}
		// Only these environment values require caller-only contexts. Retain
		// their exact original scope; do not put tokens on the action's env.
		if env := githubYAMLField(step, "env"); env != nil {
			for index := 0; index < len(env.Content); index += 2 {
				key, value := env.Content[index].Value, env.Content[index+1]
				if !strings.Contains(value.Value, "secrets.") && !strings.Contains(value.Value, "vars.") && value.Value != "${{ toJSON(needs) }}" {
					continue
				}
				input := strings.ToLower(strings.ReplaceAll(key, "_", "-"))
				original := *value
				if previous := githubYAMLField(arguments, input); previous != nil && previous.Value != value.Value {
					return nil, e.New(e.CodeEngineCapability, "generated action %q has conflicting step input %q", path, input)
				}
				githubYAMLSet(arguments, input, &original)
				githubYAMLSet(inputs, input, githubActionInput("Caller value for step environment "+key))
				value.Value, value.Style = "${{ inputs."+input+" }}", 0
			}
		}
		if with := githubYAMLField(step, "with"); with != nil {
			for index := 0; index < len(with.Content); index += 2 {
				key, value := with.Content[index].Value, with.Content[index+1]
				if !strings.Contains(value.Value, "steps.") {
					continue
				}
				input := "with-" + key
				original := *value
				githubYAMLSet(arguments, input, &original)
				githubYAMLSet(inputs, input, githubActionInput("Caller step output for action input "+key))
				value.Value, value.Style = "${{ inputs."+input+" }}", 0
			}
		}
	}
	action := githubYAMLMap("name", githubYAMLString(name), "description", githubYAMLString("Generated NVKR steps; regenerate with the complete Run bundle."))
	action.HeadComment = "SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.\nSPDX-License-Identifier: Apache-2.0\n\nGENERATED BY nvkr run generate — DO NOT EDIT"
	if len(inputs.Content) > 0 {
		githubYAMLSet(action, "inputs", inputs)
	}
	githubYAMLSet(action, "runs", githubYAMLMap("using", githubYAMLString("composite"), "steps", &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq", Content: steps}))
	raw, err := encodeGitHubYAML(action)
	if err != nil {
		return nil, err
	}
	files[path+"/action.yml"] = raw
	call := githubYAMLMap("name", githubYAMLString(name), "uses", githubYAMLString("./"+path))
	if len(arguments.Content) > 0 {
		githubYAMLSet(call, "with", arguments)
	}
	return call, nil
}

func githubActionInput(description string) *yamlv3.Node {
	return githubYAMLMap("description", githubYAMLString(description), "required", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "true"})
}

// expandGitHubActions restores the concrete step graph for semantic admission.
// This is a deliberately closed macro format, not a general Actions interpreter.
// Exact regeneration additionally authenticates every action and its call sites.
func expandGitHubActions(plan *Plan, raw []byte, files map[string][]byte) ([]byte, error) {
	var err error
	raw, files, err = materializeGitHubPrivateTransfers(plan, raw, files)
	if err != nil {
		return nil, err
	}
	raw, files, err = materializeGitHubSharedActions(plan, raw, files)
	if err != nil {
		return nil, err
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "parse generated workflow")
	}
	if len(document.Content) != 1 {
		return nil, e.New(e.CodeInvalidCatalog, "generated workflow requires a document")
	}
	jobs := githubYAMLField(document.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yamlv3.MappingNode {
		return nil, e.New(e.CodeInvalidCatalog, "generated workflow requires jobs")
	}
	for index := 1; index < len(jobs.Content); index += 2 {
		job := jobs.Content[index]
		steps := githubYAMLField(job, "steps")
		if steps == nil || steps.Kind != yamlv3.SequenceNode {
			return nil, e.New(e.CodeInvalidCatalog, "generated job requires steps")
		}
		shell := "bash -e {0}"
		if githubYAMLField(job, "container") != nil {
			shell = "sh -e {0}"
		}
		expanded, err := expandGitHubSteps(plan, steps.Content, files, shell, 0)
		if err != nil {
			return nil, err
		}
		steps.Content = expanded
	}
	return encodeGitHubYAML(&document)
}

func expandGitHubSteps(plan *Plan, steps []*yamlv3.Node, files map[string][]byte, shell string, depth int) ([]*yamlv3.Node, error) {
	if depth > 2 {
		return nil, e.New(e.CodeEngineCapability, "generated actions exceed the two-level nesting contract")
	}
	var result []*yamlv3.Node
	for _, step := range steps {
		if step.Kind == yamlv3.AliasNode {
			step = step.Alias
		}
		// Lowering only aliases complete inline preflight steps. Remove their
		// presentation anchors in the expanded graph used by semantic checks.
		copyOfStep := *step
		copyOfStep.Anchor = ""
		step = &copyOfStep
		uses := githubYAMLValue(step, "uses")
		if !strings.HasPrefix(uses, "./") {
			if depth > 0 && githubYAMLField(step, "run") != nil {
				if githubYAMLValue(step, "shell") != shell {
					return nil, e.New(e.CodeEngineCapability, "generated action changes the job's shell")
				}
				githubYAMLRemove(step, "shell")
			}
			result = append(result, step)
			continue
		}
		path := strings.TrimPrefix(uses, "./") + "/action.yml"
		if !strings.HasPrefix(path, githubActionsDirectoryFor(plan)) || !safeGeneratedPath(path) || len(files[path]) == 0 {
			return nil, e.New(e.CodePipelineDrift, "generated action %q is missing or outside the owned bundle", uses)
		}
		for index := 0; index < len(step.Content); index += 2 {
			switch step.Content[index].Value {
			case "name", "uses", "with", "id", "if", "timeout-minutes", "continue-on-error":
			default:
				return nil, e.New(e.CodeEngineCapability, "generated action call has unsupported field %q", step.Content[index].Value)
			}
		}
		var action yamlv3.Node
		if err := yamlv3.Unmarshal(files[path], &action); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "decode generated action %s", path)
		}
		if len(action.Content) != 1 {
			return nil, e.New(e.CodePipelineDrift, "generated action %q requires a document", path)
		}
		runs := githubYAMLField(action.Content[0], "runs")
		inner := githubYAMLField(runs, "steps")
		if githubYAMLValue(runs, "using") != "composite" || inner == nil || inner.Kind != yamlv3.SequenceNode {
			return nil, e.New(e.CodeEngineCapability, "generated action %q must contain composite steps", path)
		}
		inputs, arguments := githubYAMLField(action.Content[0], "inputs"), githubYAMLField(step, "with")
		if githubYAMLLength(inputs) != githubYAMLLength(arguments) {
			return nil, e.New(e.CodeEngineCapability, "generated action %q input set differs", path)
		}
		if inputs != nil {
			for index := 0; index < len(inputs.Content); index += 2 {
				key := inputs.Content[index].Value
				value := githubYAMLField(arguments, key)
				if value == nil || value.Kind != yamlv3.ScalarNode {
					return nil, e.New(e.CodeEngineCapability, "generated action %q omits input %q", path, key)
				}
				githubYAMLSubstitute(inner, "${{ inputs."+key+" }}", value)
			}
		}
		if githubYAMLContains(inner, "${{ inputs.") {
			return nil, e.New(e.CodeEngineCapability, "generated action %q has unresolved inputs", path)
		}
		expanded, err := expandGitHubSteps(plan, inner.Content, files, shell, depth+1)
		if err != nil {
			return nil, err
		}
		if githubYAMLValue(step, "if") == githubAlwaysCondition {
			if len(expanded) != 1 || githubYAMLValue(expanded[0], "uses") == "" || githubYAMLValue(expanded[0], "if") != githubAlwaysCondition {
				return nil, e.New(e.CodeEngineCapability, "always-run generated action %q must preserve its transport cancellation condition", path)
			}
			githubYAMLRemove(expanded[0], "if")
		}
		for _, key := range githubTransferControlFields {
			if value := githubYAMLField(step, key); value != nil {
				if len(expanded) != 1 || githubYAMLValue(expanded[0], "uses") == "" || githubYAMLField(expanded[0], key) != nil {
					return nil, e.New(e.CodeEngineCapability, "controlled generated action %q requires exactly one transport step", path)
				}
				githubYAMLSet(expanded[0], key, value)
			}
		}
		result = append(result, expanded...)
	}
	return result, nil
}

func githubYAMLField(node *yamlv3.Node, name string) *yamlv3.Node {
	if node == nil || node.Kind != yamlv3.MappingNode {
		return nil
	}
	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == name {
			return node.Content[index+1]
		}
	}
	return nil
}

func githubYAMLOnlyFields(node *yamlv3.Node, allowed ...string) bool {
	if node == nil || node.Kind != yamlv3.MappingNode || len(node.Content)%2 != 0 {
		return false
	}
	seen := map[string]bool{}
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yamlv3.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
			return false
		}
		seen[key.Value] = true
		found := false
		for _, name := range allowed {
			found = found || key.Value == name
		}
		if !found {
			return false
		}
	}
	return true
}

func githubYAMLValue(node *yamlv3.Node, name string) string {
	if value := githubYAMLField(node, name); value != nil {
		return value.Value
	}
	return ""
}

func githubYAMLString(value string) *yamlv3.Node {
	return &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: value}
}

func githubYAMLMap(values ...any) *yamlv3.Node {
	node := &yamlv3.Node{Kind: yamlv3.MappingNode, Tag: "!!map"}
	for index := 0; index < len(values); index += 2 {
		githubYAMLSet(node, values[index].(string), values[index+1].(*yamlv3.Node))
	}
	return node
}

func githubYAMLSet(node *yamlv3.Node, name string, value *yamlv3.Node) {
	if existing := githubYAMLField(node, name); existing != nil {
		*existing = *value
		return
	}
	node.Content = append(node.Content, githubYAMLString(name), value)
}

func githubYAMLRemove(node *yamlv3.Node, name string) {
	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == name {
			node.Content = append(node.Content[:index], node.Content[index+2:]...)
			return
		}
	}
}

func githubYAMLLength(node *yamlv3.Node) int {
	if node == nil {
		return 0
	}
	return len(node.Content)
}

func githubYAMLContains(node *yamlv3.Node, value string) bool {
	if strings.Contains(node.Value, value) {
		return true
	}
	for _, child := range node.Content {
		if githubYAMLContains(child, value) {
			return true
		}
	}
	return false
}

func githubYAMLSubstitute(node *yamlv3.Node, expression string, value *yamlv3.Node) {
	if node.Kind == yamlv3.ScalarNode && node.Value == expression {
		*node = *value
		return
	}
	for _, child := range node.Content {
		githubYAMLSubstitute(child, expression, value)
	}
}

func encodeGitHubYAML(node *yamlv3.Node) ([]byte, error) {
	// yaml.v3's Node encoder resolves plain scalars using YAML 1.2. Retain
	// string semantics for the boolean words also recognized by YAML 1.1
	// consumers without changing authored quotes or multiline script styles.
	var quoteBooleanWords func(*yamlv3.Node)
	quoteBooleanWords = func(current *yamlv3.Node) {
		if current.Kind == yamlv3.ScalarNode && current.Tag == "!!str" &&
			current.Style&(yamlv3.SingleQuotedStyle|yamlv3.DoubleQuotedStyle|yamlv3.LiteralStyle|yamlv3.FoldedStyle) == 0 {
			switch strings.ToLower(current.Value) {
			case "y", "yes", "n", "no", "on", "off":
				current.Style |= yamlv3.SingleQuotedStyle
			}
		}
		for _, child := range current.Content {
			quoteBooleanWords(child)
		}
	}
	quoteBooleanWords(node)
	var output bytes.Buffer
	encoder := yamlv3.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "encode generated GitHub YAML")
	}
	if err := encoder.Close(); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "close generated GitHub YAML")
	}
	return output.Bytes(), nil
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strconv"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

type githubActionInvocation struct {
	Family    string
	Name      string
	Body      *yamlv3.Node
	Values    map[string]string
	Arguments *yamlv3.Node
}

type githubInterfaceRole struct {
	name     string
	step     *yamlv3.Node
	optional bool
	kind     string
}

const githubAttemptUploadNameSuffix = "-${{ github.run_id }}-${{ github.run_attempt }}"

const githubStandaloneUploadNameExpression = "${{ fromJSON(inputs.parameters).upload_name_attempt == 'true' && format('{0}-{1}-{2}', fromJSON(inputs.parameters).upload_with_name, github.run_id, github.run_attempt) || fromJSON(inputs.parameters).upload_with_name }}"

// projectGitHubActionInterfaces recognizes the generator's closed operations.
// Families are deliberate interfaces, independent of YAML shape, source pins or
// output cardinality. Extra uploads stay ordered before the next node executes.
func projectGitHubActionInterfaces(plan *Plan, body, call *yamlv3.Node, actions githubActionRefs) ([]githubActionInvocation, error) {
	steps := githubYAMLField(githubYAMLField(body, "runs"), "steps")
	if steps == nil || steps.Kind != yamlv3.SequenceNode {
		return nil, e.New(e.CodeEngineCapability, "semantic GitHub action requires composite steps")
	}
	var invocations []githubActionInvocation
	appendInterface := func(family, name string, roles ...githubInterfaceRole) error {
		invocation, err := githubBuildActionInterface(plan, body, call, actions, family, name, roles)
		if err == nil {
			invocations = append(invocations, invocation)
		}
		return err
	}
	for index := 0; index < len(steps.Content); {
		step := steps.Content[index]
		var download, restore, images *yamlv3.Node
		if githubYAMLValue(step, "uses") == actions.Download && index+2 < len(steps.Content) && githubInterfaceOperation(plan, steps.Content[index+1]) == "restore-inputs" {
			next := index + 2
			if githubInterfaceOperation(plan, steps.Content[next]) == "prepare-images" {
				next++
			}
			if next < len(steps.Content) && githubInterfaceOperation(plan, steps.Content[next]) == "run-node" {
				download, restore = step, steps.Content[index+1]
				index += 2
				step = steps.Content[index]
			}
		}
		if githubInterfaceOperation(plan, step) == "prepare-images" {
			images = step
			index++
			if index == len(steps.Content) || githubInterfaceOperation(plan, steps.Content[index]) != "run-node" {
				if err := appendInterface("prepare-images", githubYAMLValue(images, "name"), githubInterfaceRole{"images", images, false, "prepare-images"}); err != nil {
					return nil, err
				}
				continue
			}
			step = steps.Content[index]
		}
		switch operation := githubInterfaceOperation(plan, step); {
		case operation == "run-node" || operation == "pack-outputs":
			family, name := "execute-node", githubYAMLValue(step, "name")
			var execute, pack *yamlv3.Node
			if operation == "run-node" {
				execute = step
				if githubYAMLField(githubYAMLField(step, "env"), sourceTokenEnv) != nil {
					family = "execute-source"
				}
				index++
				if index < len(steps.Content) && githubInterfaceOperation(plan, steps.Content[index]) == "pack-outputs" {
					pack = steps.Content[index]
					_, producer, _, err := githubInterfaceCommand(plan, execute)
					_, collector, _, packErr := githubInterfaceCommand(plan, pack)
					if err != nil || packErr != nil || producer != collector {
						return nil, e.New(e.CodeEngineCapability, "semantic GitHub execution must collect its own node")
					}
					index++
				}
			} else {
				family, pack = "collect-node-outputs", step
				index++
			}
			var uploads []*yamlv3.Node
			for index < len(steps.Content) && githubYAMLValue(steps.Content[index], "uses") == actions.Upload && githubYAMLField(steps.Content[index], "if") == nil {
				uploads = append(uploads, steps.Content[index])
				index++
			}
			if (pack == nil) != (len(uploads) == 0) {
				return nil, e.New(e.CodeEngineCapability, "semantic GitHub output collection requires packing and uploads together")
			}
			var roles []githubInterfaceRole
			if execute != nil {
				roles = append(roles, githubInterfaceRole{"download", download, true, "download"}, githubInterfaceRole{"restore", restore, true, "restore-inputs"},
					githubInterfaceRole{"images", images, true, "prepare-images"}, githubInterfaceRole{"execute", execute, false, family})
			}
			roles = append(roles, githubInterfaceRole{"pack", pack, execute != nil, "pack-outputs"})
			for slot, role := range []string{"upload", "extra_upload"} {
				var upload *yamlv3.Node
				if slot < len(uploads) {
					upload = uploads[slot]
				}
				roles = append(roles, githubInterfaceRole{role, upload, true, "upload"})
			}
			if err := appendInterface(family, name, roles...); err != nil {
				return nil, err
			}
			for _, upload := range uploads[min(2, len(uploads)):] {
				if err := appendInterface("upload-carrier", githubYAMLValue(upload, "name"), githubInterfaceRole{"upload", upload, false, "upload-standalone"}); err != nil {
					return nil, err
				}
			}
		case githubYAMLValue(step, "uses") == actions.Download:
			index++
			roles := []githubInterfaceRole{{"download", step, false, "download"}}
			family := "download-carriers"
			if index < len(steps.Content) && githubInterfaceOperation(plan, steps.Content[index]) == "restore-inputs" {
				family = "prepare-inputs"
				roles = append(roles, githubInterfaceRole{"restore", steps.Content[index], false, "restore-inputs"})
				index++
			}
			if err := appendInterface(family, githubYAMLValue(step, "name"), roles...); err != nil {
				return nil, err
			}
		case githubYAMLValue(step, "uses") == actions.Upload:
			family := "upload-carrier"
			if githubYAMLValue(step, "if") == githubAlwaysCondition {
				family += "-always"
			}
			if err := appendInterface(family, githubYAMLValue(step, "name"), githubInterfaceRole{"upload", step, false, "upload-standalone"}); err != nil {
				return nil, err
			}
			index++
		case operation == "recover":
			if index+1 >= len(steps.Content) || githubYAMLValue(steps.Content[index+1], "name") != "Finalize Run and check completeness" ||
				!strings.Contains(githubYAMLValue(steps.Content[index+1], "run"), "./nvkr-exec run finalize --definition ") {
				// The Run summary observes finalization in the caller's step
				// namespace, leaving recovery as its own semantic operation.
				if err := appendInterface("recover-inputs", githubYAMLValue(step, "name"), githubInterfaceRole{"recover", step, false, "recover"}); err != nil {
					return nil, err
				}
				index++
				continue
			}
			if err := appendInterface("finalize-run", "Finalize Run and check completeness", githubInterfaceRole{"recover", step, false, "recover"},
				githubInterfaceRole{"finalize", steps.Content[index+1], false, "finalize"}); err != nil {
				return nil, err
			}
			index += 2
		default:
			return nil, e.New(e.CodeEngineCapability, "unsupported semantic GitHub step %q", githubYAMLValue(step, "name"))
		}
	}
	if len(invocations) != 1 {
		for _, key := range githubTransferControlFields {
			if githubYAMLField(call, key) != nil {
				return nil, e.New(e.CodeEngineCapability, "controlled GitHub action cannot expand into multiple semantic calls")
			}
		}
	}
	return invocations, nil
}

func githubInterfaceOperation(plan *Plan, step *yamlv3.Node) string {
	operation, _, _, err := githubInterfaceCommand(plan, step)
	if err != nil {
		return ""
	}
	return operation
}

func githubInterfaceCommand(plan *Plan, step *yamlv3.Node) (string, string, bool, error) {
	run := githubYAMLValue(step, "run")
	newline := strings.HasSuffix(run, "\n")
	run = strings.TrimSuffix(run, "\n")
	for _, operation := range []struct{ name, flag string }{{"run-node", "node"}, {"pack-outputs", "node"}, {"restore-inputs", "stage"}, {"prepare-images", "stage"}} {
		prefix := githubRunnerCommand(plan, operation.name, "", "") + " --" + operation.flag + " "
		if !strings.HasPrefix(run, prefix) {
			continue
		}
		quoted := strings.TrimPrefix(run, prefix)
		selector := quoted
		if len(selector) >= 2 && strings.HasPrefix(selector, "'") && strings.HasSuffix(selector, "'") {
			selector = strings.ReplaceAll(selector[1:len(selector)-1], `'"'"'`, "'")
		}
		if selector == "" || shellQuote(selector) != quoted {
			return "", "", false, e.New(e.CodeEngineCapability, "semantic GitHub operation requires one literal selector")
		}
		return operation.name, selector, newline, nil
	}
	if run == githubRunnerCommand(plan, "recover", "", "") {
		return "recover", "", newline, nil
	}
	return "", "", false, e.New(e.CodeEngineCapability, "semantic GitHub action contains an unknown command")
}

func githubBuildActionInterface(plan *Plan, original, call *yamlv3.Node, actions githubActionRefs, family, name string, roles []githubInterfaceRole) (githubActionInvocation, error) {
	invocation := githubActionInvocation{Family: family, Name: name, Values: map[string]string{}, Arguments: githubYAMLMap()}
	inputs := githubYAMLMap("parameters", githubActionInput("Plan-derived parameters for this operation; never shell source"))
	shell := ""
	for _, role := range roles {
		if role.step == nil || githubYAMLField(role.step, "run") == nil {
			continue
		}
		current := githubYAMLValue(role.step, "shell")
		if (current != "bash -e {0}" && current != "sh -e {0}") || (shell != "" && shell != current) {
			return invocation, e.New(e.CodeEngineCapability, "semantic GitHub action changes its shell")
		}
		shell = current
	}
	if shell == "sh -e {0}" {
		invocation.Family += "-container"
	}
	var projected []*yamlv3.Node
	for _, role := range roles {
		step, err := githubProjectInterfaceRole(plan, original, call, actions, &invocation, inputs, role, shell)
		if err != nil {
			return invocation, err
		}
		projected = append(projected, step)
	}
	invocation.Body = githubYAMLMap("name", githubYAMLString(strings.ReplaceAll(invocation.Family, "-", " ")),
		"description", githubYAMLString("Generated NVKR semantic operation; regenerate with the complete Run bundle."),
		"inputs", inputs, "runs", githubYAMLMap("using", githubYAMLString("composite"), "steps", &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq", Content: projected}))
	invocation.Body.HeadComment = "SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.\nSPDX-License-Identifier: Apache-2.0\n\nGENERATED BY nvkr run generate — DO NOT EDIT"
	return invocation, nil
}

func githubProjectInterfaceRole(plan *Plan, original, call *yamlv3.Node, actions githubActionRefs, invocation *githubActionInvocation,
	inputs *yamlv3.Node, role githubInterfaceRole, shell string,
) (*yamlv3.Node, error) {
	values, prefix := invocation.Values, role.name+"_"
	present := role.step != nil
	values[prefix+"present"] = strconv.FormatBool(present)
	values[prefix+"name"], values[prefix+"id"] = "", ""
	values[prefix+"run_newline"] = "false"
	step := githubYAMLMap("name", githubYAMLString(githubParameterExpression(prefix+"name")), "id", githubYAMLString("operation_"+role.name))
	if role.optional {
		githubYAMLSet(step, "if", githubYAMLString("${{ fromJSON(inputs.parameters)."+prefix+"present == 'true' }}"))
	}
	if present {
		if err := githubInterfaceFields(role.step, "name", "id", "run", "shell", "env", "uses", "with", "if"); err != nil {
			return nil, err
		}
		values[prefix+"name"], values[prefix+"id"] = githubYAMLValue(role.step, "name"), githubYAMLValue(role.step, "id")
		if condition := githubYAMLField(role.step, "if"); condition != nil {
			if invocation.Family != "upload-carrier-always" || condition.Value != githubAlwaysCondition || role.optional {
				return nil, e.New(e.CodeEngineCapability, "semantic GitHub role changes its controlled condition")
			}
			githubYAMLSet(step, "if", githubYAMLString(githubAlwaysCondition))
		}
	}
	if role.kind == "upload" || role.kind == "upload-standalone" || role.kind == "download" {
		return githubProjectInterfaceTransfer(call, actions, invocation, inputs, role, step)
	}
	operation := role.kind
	if operation == "execute-node" || operation == "execute-source" {
		operation = "run-node"
	}
	if operation == "run-node" || operation == "pack-outputs" || operation == "restore-inputs" || operation == "prepare-images" {
		values[prefix+"selector"] = ""
	}
	if present {
		if githubYAMLField(role.step, "uses") != nil || githubYAMLField(role.step, "with") != nil {
			return nil, e.New(e.CodeEngineCapability, "semantic GitHub command cannot include an action transport")
		}
		if role.kind != "finalize" {
			actual, selector, newline, err := githubInterfaceCommand(plan, role.step)
			if err != nil || actual != operation {
				return nil, e.New(e.CodeEngineCapability, "semantic GitHub role %q has an incompatible command", role.name)
			}
			if selector != "" {
				values[prefix+"selector"] = selector
			}
			values[prefix+"run_newline"] = strconv.FormatBool(newline)
		} else {
			values[prefix+"run_newline"] = strconv.FormatBool(strings.HasSuffix(githubYAMLValue(role.step, "run"), "\n"))
		}
	}
	env := githubYAMLMap()
	var originalEnv *yamlv3.Node
	if present {
		originalEnv = githubYAMLField(role.step, "env")
	}
	if role.kind == "recover" && originalEnv == nil {
		return nil, e.New(e.CodeEngineCapability, "semantic GitHub recovery omits its provider-result input")
	}
	if role.kind == "prepare-images" {
		expected := githubImageEnvironment()
		for key := range expected {
			input := strings.ToLower(strings.ReplaceAll(key, "_", "-"))
			expected[key] = "${{ inputs." + input + " }}"
		}
		if present && !githubInterfaceEnvironmentEqual(originalEnv, expected) {
			return nil, e.New(e.CodeEngineCapability, "semantic image preparation changes its read credential")
		}
		for _, key := range sortedKeys(expected) {
			input := strings.ToLower(strings.ReplaceAll(key, "_", "-"))
			githubYAMLSet(inputs, input, githubYAMLMap("description", githubYAMLString("Independent private toolchain reader when image preparation is present"), "required", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "false"}, "default", githubYAMLString("")))
			if present {
				value := githubYAMLValue(githubYAMLField(call, "with"), input)
				if value != githubImageEnvironment()[key] {
					return nil, e.New(e.CodeEngineCapability, "semantic image preparation changes its caller credential")
				}
				githubYAMLSet(invocation.Arguments, input, githubYAMLString(value))
			}
			githubYAMLSet(env, key, githubYAMLString(expected[key]))
		}
	} else if originalEnv != nil {
		if originalEnv.Kind != yamlv3.MappingNode || (role.kind != "execute-source" && role.kind != "recover" && role.kind != "finalize") {
			return nil, e.New(e.CodeEngineCapability, "semantic GitHub role has an unsupported credential environment")
		}
		if role.kind == "execute-source" {
			if err := githubInterfaceSourceEnvironment(originalEnv); err != nil {
				return nil, err
			}
		}
		if role.kind == "recover" && !githubInterfaceEnvironmentEqual(originalEnv, map[string]string{"NVKR_JOB_RESULTS": "${{ inputs.nvkr-job-results }}"}) {
			return nil, e.New(e.CodeEngineCapability, "semantic GitHub recovery changes its provider-result input")
		}
		for _, key := range githubInterfaceKeys(originalEnv) {
			value := githubYAMLField(originalEnv, key)
			if value.Kind != yamlv3.ScalarNode {
				return nil, e.New(e.CodeEngineCapability, "semantic GitHub environment requires scalar values")
			}
			if strings.Contains(value.Value, "${{") || strings.HasPrefix(value.Value, "!") {
				if err := githubInterfaceBindInput(original, call, invocation.Arguments, inputs, value.Value); err != nil {
					return nil, err
				}
				githubYAMLSet(env, key, githubYAMLString(value.Value))
			} else {
				values[prefix+"env_"+key] = value.Value
				githubYAMLSet(env, key, githubYAMLString(githubParameterExpression(prefix+"env_"+key)))
			}
		}
	}
	run := ""
	switch operation {
	case "run-node", "pack-outputs", "restore-inputs", "prepare-images":
		flag := "node"
		if operation == "restore-inputs" || operation == "prepare-images" {
			flag = "stage"
		}
		run = githubRunnerCommand(plan, operation, "", "") + " --" + flag + ` "$` + githubSelectorEnvironment + `"`
		githubYAMLSet(env, githubSelectorEnvironment, githubYAMLString(githubParameterExpression(prefix+"selector")))
	case "recover":
		run = githubRunnerCommand(plan, "recover", "", "")
	case "finalize":
		run = strings.TrimSuffix(githubYAMLValue(role.step, "run"), "\n")
	}
	githubYAMLSet(step, "run", githubYAMLString(run))
	githubYAMLSet(step, "shell", githubYAMLString(shell))
	if len(env.Content) > 0 {
		githubYAMLSet(step, "env", env)
	}
	return step, nil
}

func githubProjectInterfaceTransfer(call *yamlv3.Node, actions githubActionRefs, invocation *githubActionInvocation,
	inputs *yamlv3.Node, role githubInterfaceRole, step *yamlv3.Node,
) (*yamlv3.Node, error) {
	prefix, values := role.name+"_", invocation.Values
	defaults := map[string]string{"name": "artifact", "path": "", "if-no-files-found": "warn", "include-hidden-files": "false", "retention-days": "0"}
	uses := actions.Upload
	if role.kind == "download" {
		uses = actions.Download
		defaults = map[string]string{"pattern": "", "path": "", "merge-multiple": "false"}
	}
	var originalWith *yamlv3.Node
	if role.step != nil {
		if githubYAMLValue(role.step, "uses") != uses || githubYAMLField(role.step, "run") != nil || githubYAMLField(role.step, "env") != nil {
			return nil, e.New(e.CodeEngineCapability, "semantic GitHub transport changes its pinned action or environment")
		}
		originalWith = githubYAMLField(role.step, "with")
		if originalWith == nil || originalWith.Kind != yamlv3.MappingNode {
			return nil, e.New(e.CodeEngineCapability, "semantic GitHub transport requires action inputs")
		}
		for _, key := range githubInterfaceKeys(originalWith) {
			if _, ok := defaults[key]; !ok {
				return nil, e.New(e.CodeEngineCapability, "semantic GitHub transport has unsupported input %q", key)
			}
		}
	}
	with := githubYAMLMap()
	attemptName := false
	for _, key := range sortedKeys(defaults) {
		field := prefix + "with_" + strings.ReplaceAll(key, "-", "_")
		value := githubYAMLField(originalWith, key)
		values[field+"_present"] = strconv.FormatBool(value != nil)
		values[field] = defaults[key]
		if value != nil {
			if value.Kind != yamlv3.ScalarNode {
				return nil, e.New(e.CodeEngineCapability, "semantic GitHub transport requires scalar inputs")
			}
			values[field] = value.Value
		}
		if strings.Contains(values[field], "${{") {
			base, attempt := githubAttemptUploadName(values[field], githubYAMLValue(originalWith, "path"))
			switch {
			case role.kind == "upload-standalone" && key == "name" && attempt:
				values[field], attemptName = base, true
			case role.kind == "upload-standalone" && key == "path" && values[field] == "${{ inputs.with-path }}":
			default:
				return nil, e.New(e.CodeEngineCapability, "semantic GitHub transport has an unsupported provider expression")
			}
		}
		githubYAMLSet(with, key, githubYAMLString(githubParameterExpression(field)))
	}
	if role.kind == "upload-standalone" {
		// Provider expressions remain literal action code. The descriptor holds
		// only the admitted name prefix and a flag; returned JSON is never eval.
		values[prefix+"name_attempt"] = strconv.FormatBool(attemptName)
		githubYAMLSet(with, "name", githubYAMLString(githubStandaloneUploadNameExpression))
		input := githubYAMLMap("description", githubYAMLString("Caller diagnostic path, or empty for the Plan-derived path"), "required", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "false"}, "default", githubYAMLString(""))
		githubYAMLSet(inputs, "with-path", input)
		argument := githubYAMLString("")
		fromCaller := values[prefix+"with_path"] == "${{ inputs.with-path }}"
		if fromCaller {
			argument = githubYAMLField(githubYAMLField(call, "with"), "with-path")
			if argument == nil || argument.Kind != yamlv3.ScalarNode {
				return nil, e.New(e.CodeEngineCapability, "semantic upload omits its caller diagnostic path")
			}
			values[prefix+"with_path"] = ""
		}
		values[prefix+"path_input"] = strconv.FormatBool(fromCaller)
		githubYAMLSet(invocation.Arguments, "with-path", githubInterfaceClone(argument))
		githubYAMLSet(with, "path", githubYAMLString("${{ inputs.with-path || fromJSON(inputs.parameters)."+prefix+"with_path }}"))
	}
	githubYAMLSet(step, "uses", githubYAMLString(uses))
	githubYAMLSet(step, "with", with)
	return step, nil
}

// Only these owned diagnostic/handoff routes use provider attempt names.
// All other expressions remain outside the descriptive parameter interface.
func githubAttemptUploadName(name, path string) (string, bool) {
	base, ok := strings.CutSuffix(name, githubAttemptUploadNameSuffix)
	if !ok {
		return "", false
	}
	if base == "nvkr-qualification" && path == githubQualificationHandoffPath {
		return base, true
	}
	output, ok := strings.CutPrefix(base, "nvkr-inspection-")
	if !ok || output == "" || path != ".nvkr/diagnostics/inspection/"+output+".jsonl" {
		return "", false
	}
	for _, character := range output {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-') {
			return "", false
		}
	}
	return base, true
}

func githubInterfaceBindInput(original, call, arguments, inputs *yamlv3.Node, expression string) error {
	if !strings.Contains(expression, "${{ inputs.") {
		return nil
	}
	name := strings.TrimSuffix(strings.TrimPrefix(expression, "${{ inputs."), " }}")
	if expression != "${{ inputs."+name+" }}" || name == "parameters" {
		return e.New(e.CodeEngineCapability, "semantic GitHub environment has an unsupported input binding")
	}
	declaration := githubYAMLField(githubYAMLField(original, "inputs"), name)
	value := githubYAMLField(githubYAMLField(call, "with"), name)
	if declaration == nil || value == nil || value.Kind != yamlv3.ScalarNode {
		return e.New(e.CodeEngineCapability, "semantic GitHub action omits explicit input %q", name)
	}
	githubYAMLSet(inputs, name, githubInterfaceClone(declaration))
	githubYAMLSet(arguments, name, githubInterfaceClone(value))
	return nil
}

func githubInterfaceFields(node *yamlv3.Node, allowed ...string) error {
	if node == nil || node.Kind != yamlv3.MappingNode {
		return e.New(e.CodeEngineCapability, "semantic GitHub step requires a mapping")
	}
	for _, key := range githubInterfaceKeys(node) {
		found := false
		for _, candidate := range allowed {
			found = found || key == candidate
		}
		if !found {
			return e.New(e.CodeEngineCapability, "semantic GitHub step has unsupported field %q", key)
		}
	}
	return nil
}

func githubInterfaceKeys(node *yamlv3.Node) []string {
	keys := map[string]bool{}
	for index := 0; node != nil && index < len(node.Content); index += 2 {
		keys[node.Content[index].Value] = true
	}
	return sortedKeys(keys)
}

func githubInterfaceEnvironmentEqual(node *yamlv3.Node, expected map[string]string) bool {
	if node == nil || node.Kind != yamlv3.MappingNode || len(node.Content) != 2*len(expected) {
		return false
	}
	for key, value := range expected {
		if githubYAMLValue(node, key) != value {
			return false
		}
	}
	return true
}

func githubInterfaceSourceEnvironment(env *yamlv3.Node) error {
	fixed := map[string]string{
		sourceTokenEnv: "${{ inputs.nvkr-source-token }}", "GIT_TERMINAL_PROMPT": "0", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_COUNT": "4", "GIT_CONFIG_KEY_1": "credential.helper",
		"GIT_CONFIG_VALUE_1": "", "GIT_CONFIG_VALUE_2": sourceCredentialHelper,
		"GIT_CONFIG_KEY_3": "credential.useHttpPath", "GIT_CONFIG_VALUE_3": "true",
	}
	if len(env.Content) != 2*(len(fixed)+3) {
		return e.New(e.CodeEngineCapability, "semantic source execution changes its credential interface")
	}
	for key, expected := range fixed {
		value := githubYAMLField(env, key)
		if value == nil || value.Kind != yamlv3.ScalarNode || value.Value != expected {
			return e.New(e.CodeEngineCapability, "semantic source execution changes fixed Git setting %q", key)
		}
	}
	for _, key := range []string{"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_2"} {
		value := githubYAMLField(env, key)
		if value == nil || value.Kind != yamlv3.ScalarNode || value.Value == "" || strings.Contains(value.Value, "${{") || strings.HasPrefix(value.Value, "!") {
			return e.New(e.CodeEngineCapability, "semantic source execution requires a literal Git route")
		}
	}
	return nil
}

func githubInterfaceClone(node *yamlv3.Node) *yamlv3.Node {
	copy := *node
	copy.Content = make([]*yamlv3.Node, len(node.Content))
	for index, child := range node.Content {
		copy.Content[index] = githubInterfaceClone(child)
	}
	return &copy
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	_ "embed"
	"maps"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

// The transport executes before the compiled helper is available. It contains
// no third-party runtime dependencies and is authenticated as a bundle member.
// It conveys bytes, never artifact or release authority.
//
//go:embed github_transfer_action.js
var githubTransferScript []byte

const (
	githubTransferIndexID   = "nvkr-transfer-index"
	githubTransferContextID = "nvkr-transfer-context"
	githubTransferResultsID = "nvkr-transfer-results"
)

// The provider serializes the needs object as JSON before inserting it here.
// JSON escapes newlines inside every string, so a bare delimiter line cannot
// occur in the serialized object. The quoted heredoc disables shell expansion;
// no output value, branch name or other caller data becomes shell source. A file
// avoids Linux's 128 KiB per-environment-string limit on large finalizer joins.
const githubTransferContextScript = `set -eu
umask 077
nvkr_needs_file="$(mktemp "${RUNNER_TEMP:?}/nvkr-needs.XXXXXXXX")"
cat >"$nvkr_needs_file" <<'NVKR_PRIVATE_NEEDS_JSON_END'
${{ toJSON(needs) }}
NVKR_PRIVATE_NEEDS_JSON_END
printf 'path=%s\n' "$nvkr_needs_file" >>"${GITHUB_OUTPUT:?}"
`

const githubTransferContextPath = "${{ steps." + githubTransferContextID + ".outputs.path }}"
const githubTransferResults = "${{ steps." + githubTransferResultsID + ".outputs.results }}"

var githubTransferBindings = map[string]string{
	"transfer-registry": "${{ vars.NVKR_TRANSFER_PACKAGE }}",
	"transfer-user":     "${{ vars.NVKR_TRANSFER_USER }}",
	"transfer-read":     "${{ secrets.NVKR_TRANSFER_READ }}",
	"transfer-write":    "${{ secrets.NVKR_TRANSFER_WRITE }}",
	"transfer-needs":    githubTransferContextPath,
}

func githubTransferDirectory(plan *Plan) string {
	return githubActionsDirectoryFor(plan) + "private-transfer"
}

// Lower the existing exact carrier interface only after semantic action sharing.
// Credentials flow through explicit composite inputs, never job environments.
func lowerGitHubPrivateTransfers(plan *Plan, raw []byte, files map[string][]byte, refs githubActionRefs) ([]byte, error) {
	path := githubTransferDirectory(plan)
	documents := map[string]*yamlv3.Node{}
	for name, data := range files {
		if !strings.HasSuffix(name, "/action.yml") {
			continue
		}
		var document yamlv3.Node
		if err := yamlv3.Unmarshal(data, &document); err != nil || len(document.Content) != 1 {
			return nil, e.New(e.CodePipelineDrift, "invalid action while lowering private transport")
		}
		documents[name] = &document
	}
	required := map[string]map[string]bool{}
	visiting := map[string]bool{}
	var rewriteAction func(string) (map[string]bool, error)
	rewriteSteps := func(steps *yamlv3.Node, top bool) (map[string]bool, error) {
		needed := map[string]bool{}
		if steps == nil {
			return needed, nil
		}
		if steps.Kind == yamlv3.AliasNode {
			steps = steps.Alias
		}
		for _, step := range steps.Content {
			if step.Kind == yamlv3.AliasNode {
				step = step.Alias
			}
			uses := githubYAMLValue(step, "uses")
			if top {
				if with := githubYAMLField(step, "with"); githubYAMLValue(with, "nvkr-job-results") == "${{ toJSON(needs) }}" {
					githubYAMLSet(with, "nvkr-job-results", githubYAMLString(githubTransferResults))
				}
			}
			if top && githubYAMLValue(step, "name") == "Upload complete qualification handoff" {
				handoff := githubQualificationHandoffStep(refs)
				*step = *githubYAMLMap("name", githubYAMLString(handoff.Name), "uses", githubYAMLString(handoff.Uses),
					"if", githubYAMLString(handoff.If), "timeout-minutes", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!int", Value: "5"}, "with", githubYAMLMap())
				for _, key := range sortedKeys(handoff.With) {
					githubYAMLSet(githubYAMLField(step, "with"), key, githubYAMLString(handoff.With[key]))
				}
				continue
			}
			if top && githubYAMLValue(step, "id") == githubRunUploadID {
				continue
			}
			bindings := map[string]string{}
			if uses == refs.Upload || uses == refs.Download {
				mode, credential := "upload", "transfer-write"
				if uses == refs.Download {
					mode, credential = "download", "transfer-read"
				}
				githubYAMLSet(step, "uses", githubYAMLString("./"+path))
				with := githubYAMLField(step, "with")
				if with == nil {
					with = githubYAMLMap()
					githubYAMLSet(step, "with", with)
				}
				githubYAMLSet(with, "mode", githubYAMLString(mode))
				bindings = map[string]string{"registry": "transfer-registry", "username": "transfer-user", "token": credential}
				if mode == "download" {
					bindings["expected-index-file"] = "transfer-needs"
					githubYAMLSet(with, "recovery", githubYAMLString("${{ github.job == 'nvkr-lifecycle-run' }}"))
				}
			} else if strings.HasPrefix(uses, "./"+githubActionsDirectoryFor(plan)) {
				child, err := rewriteAction(strings.TrimPrefix(uses, "./") + "/action.yml")
				if err != nil {
					return nil, err
				}
				for key := range child {
					bindings[key] = key
				}
			}
			for _, key := range sortedKeys(bindings) {
				input := bindings[key]
				value := "${{ inputs." + input + " }}"
				if top {
					value = githubTransferBindings[input]
				} else {
					needed[input] = true
				}
				with := githubYAMLField(step, "with")
				if with == nil {
					with = githubYAMLMap()
					githubYAMLSet(step, "with", with)
				}
				githubYAMLSet(with, key, githubYAMLString(value))
			}
		}
		return needed, nil
	}
	rewriteAction = func(name string) (map[string]bool, error) {
		if value, ok := required[name]; ok {
			return value, nil
		}
		if visiting[name] || documents[name] == nil {
			return nil, e.New(e.CodePipelineDrift, "private transport requires an acyclic owned action graph")
		}
		visiting[name] = true
		body := documents[name].Content[0]
		needed, err := rewriteSteps(githubYAMLField(githubYAMLField(body, "runs"), "steps"), false)
		if err != nil {
			return nil, err
		}
		if len(needed) > 0 {
			inputs := githubYAMLField(body, "inputs")
			if inputs == nil {
				inputs = githubYAMLMap()
				githubYAMLSet(body, "inputs", inputs)
			}
			for _, key := range sortedKeys(needed) {
				githubYAMLSet(inputs, key, githubActionInput("Private GHCR transport binding"))
			}
		}
		required[name] = needed
		delete(visiting, name)
		return needed, nil
	}
	for _, name := range sortedKeys(documents) {
		if _, err := rewriteAction(name); err != nil {
			return nil, err
		}
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil || len(document.Content) != 1 {
		return nil, e.New(e.CodePipelineDrift, "invalid private transport workflow")
	}
	jobs := githubYAMLField(document.Content[0], "jobs")
	seen := map[*yamlv3.Node]bool{}
	var contextStep, indexStep, outputs *yamlv3.Node
	for i := 1; i < len(jobs.Content); i += 2 {
		job := jobs.Content[i]
		steps := githubYAMLField(job, "steps")
		if steps.Kind == yamlv3.AliasNode {
			steps = steps.Alias
		}
		if !seen[steps] {
			if _, err := rewriteSteps(steps, true); err != nil {
				return nil, err
			}
			for position, step := range steps.Content {
				if step.Kind == yamlv3.AliasNode {
					step = step.Alias
				}
				with := githubYAMLField(step, "with")
				if githubYAMLValue(with, "transfer-needs") != githubTransferContextPath && githubYAMLValue(with, "expected-index-file") != githubTransferContextPath {
					continue
				}
				var capture *yamlv3.Node
				if contextStep == nil {
					contextStep = githubYAMLMap("name", githubYAMLString("Stage private transfer dependency context"),
						"id", githubYAMLString(githubTransferContextID), "if", githubYAMLString("${{ always() }}"),
						"shell", githubYAMLString("bash"), "timeout-minutes", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!int", Value: "1"},
						"run", githubYAMLString(githubTransferContextScript))
					contextStep.Anchor = "private-transfer-context"
					capture = contextStep
				} else {
					capture = &yamlv3.Node{Kind: yamlv3.AliasNode, Value: contextStep.Anchor, Alias: contextStep}
				}
				prefix := append([]*yamlv3.Node(nil), steps.Content[:position]...)
				prefix = append(prefix, capture)
				if jobs.Content[i-1].Value == "nvkr-lifecycle-run" {
					prefix = append(prefix, githubYAMLMap("name", githubYAMLString("Project producer outcomes for recovery"),
						"id", githubYAMLString(githubTransferResultsID), "if", githubYAMLString("${{ always() }}"),
						"uses", githubYAMLString("./"+path), "with", githubYAMLMap("mode", githubYAMLString("results"),
							"expected-index-file", githubYAMLString(githubTransferContextPath))))
				}
				steps.Content = append(prefix, steps.Content[position:]...)
				break
			}
			if indexStep == nil {
				indexStep = githubYAMLMap("name", githubYAMLString("Record private transfer digests"), "id", githubYAMLString(githubTransferIndexID), "if", githubYAMLString("${{ always() }}"), "uses", githubYAMLString("./"+path), "with", githubYAMLMap("mode", githubYAMLString("index")))
				indexStep.Anchor = "private-transfer-index"
				steps.Content = append(steps.Content, indexStep)
			} else {
				steps.Content = append(steps.Content, &yamlv3.Node{Kind: yamlv3.AliasNode, Value: indexStep.Anchor, Alias: indexStep})
			}
			seen[steps] = true
		}
		if outputs == nil {
			outputs = githubYAMLMap(githubTransferIndexID, githubYAMLString("${{ steps."+githubTransferIndexID+".outputs.index }}"))
			outputs.Anchor = "private-transfer-outputs"
			githubYAMLSet(job, "outputs", outputs)
		} else {
			githubYAMLSet(job, "outputs", &yamlv3.Node{Kind: yamlv3.AliasNode, Value: outputs.Anchor, Alias: outputs})
		}
	}
	for name, document := range documents {
		encoded, err := encodeGitHubYAML(document)
		if err != nil {
			return nil, err
		}
		files[name] = encoded
	}
	inputs := githubYAMLMap()
	for _, name := range []string{"mode", "registry", "username", "token", "name", "pattern", "path", "expected-index", "expected-index-file", "recovery", "merge-multiple", "if-no-files-found", "include-hidden-files", "retention-days"} {
		description := "Private transport " + name
		if name == "retention-days" {
			description = "Legacy graph parameter; GHCR retention is operator-managed and this value does not expire data."
		}
		githubYAMLSet(inputs, name, githubYAMLMap("description", githubYAMLString(description), "required", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "false"}, "default", githubYAMLString("")))
	}
	action := githubYAMLMap("name", githubYAMLString("Private GHCR transfer"), "description", githubYAMLString("Transfer scoped bytes through a private, unlinked GHCR package using exact OCI digests."), "inputs", inputs, "outputs", githubYAMLMap("index", githubYAMLMap("description", githubYAMLString("Exact uploaded manifest digests")), "results", githubYAMLMap("description", githubYAMLString("Bounded producer outcomes without transfer indices"))), "runs", githubYAMLMap("using", githubYAMLString("node24"), "main", githubYAMLString("index.js")))
	action.HeadComment = "SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.\nSPDX-License-Identifier: Apache-2.0\n\nGENERATED BY nvkr run generate — DO NOT EDIT"
	encoded, err := encodeGitHubYAML(action)
	if err != nil {
		return nil, err
	}
	files[path+"/action.yml"] = encoded
	files[path+"/index.js"] = append([]byte(nil), githubTransferScript...)
	return encodeGitHubYAML(&document)
}

// Recover the abstract transfer operations for graph validation. Exact bundle
// regeneration separately compares the concrete transport, credentials, outputs
// and runtime bytes, so this projection grants no alternate executable form.
func projectGitHubPrivateTransfers(plan *Plan, raw []byte, files map[string][]byte) ([]byte, map[string][]byte, error) {
	path := githubTransferDirectory(plan)
	if len(files[path+"/action.yml"]) == 0 {
		return raw, files, nil
	}
	refs, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		return nil, nil, err
	}
	result := maps.Clone(files)
	var visit func(*yamlv3.Node)
	visit = func(node *yamlv3.Node) {
		if node.Kind == yamlv3.MappingNode {
			if githubYAMLValue(node, "uses") == "./"+path {
				with := githubYAMLField(node, "with")
				mode := githubYAMLValue(with, "mode")
				if mode == "upload" {
					githubYAMLSet(node, "uses", githubYAMLString(refs.Upload))
				}
				if mode == "download" {
					githubYAMLSet(node, "uses", githubYAMLString(refs.Download))
				}
				for _, key := range []string{"mode", "registry", "username", "token", "expected-index", "expected-index-file", "recovery"} {
					githubYAMLRemove(with, key)
				}
			}
			for _, field := range []string{"with", "inputs"} {
				if values := githubYAMLField(node, field); values != nil {
					if field == "with" && githubYAMLValue(values, "nvkr-job-results") == githubTransferResults {
						githubYAMLSet(values, "nvkr-job-results", githubYAMLString("${{ toJSON(needs) }}"))
					}
					for key := range githubTransferBindings {
						githubYAMLRemove(values, key)
					}
					if len(values.Content) == 0 {
						githubYAMLRemove(node, field)
					}
				}
			}
			if outputs := githubYAMLField(node, "outputs"); outputs != nil {
				githubYAMLRemove(outputs, githubTransferIndexID)
				if len(outputs.Content) == 0 {
					githubYAMLRemove(node, "outputs")
				}
			}
		}
		if node.Kind == yamlv3.SequenceNode {
			var kept []*yamlv3.Node
			for _, child := range node.Content {
				id := githubYAMLValue(child, "id")
				if id != githubTransferIndexID && id != githubTransferContextID && id != githubTransferResultsID {
					kept = append(kept, child)
				}
			}
			node.Content = kept
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	project := func(data []byte, workflow bool) ([]byte, error) {
		var doc yamlv3.Node
		if err := yamlv3.Unmarshal(data, &doc); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "decode private transport projection")
		}
		if len(doc.Content) != 1 {
			code := e.CodePipelineDrift
			if workflow {
				code = e.CodeInvalidCatalog
			}
			return nil, e.New(code, "private transport projection requires a document")
		}
		resolved, err := githubResolveAliases(&doc, map[*yamlv3.Node]bool{})
		if err != nil {
			return nil, err
		}
		visit(resolved)
		return encodeGitHubYAML(resolved)
	}
	for name, data := range result {
		if strings.HasSuffix(name, "/action.yml") && name != path+"/action.yml" {
			result[name], err = project(data, false)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	delete(result, path+"/action.yml")
	delete(result, path+"/index.js")
	raw, err = project(raw, true)
	return raw, result, err
}

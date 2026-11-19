// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Admit ownership from the opaque composition's original members, before any
// catalog load. A flattened Definition or caller-authored attribution is not
// evidence that a consumer and its runtime were declared by the same member.
func admitCompositeRuntimeMembers(composition authored.Composition, environment authored.Environment, record *CompositionRecord) error {
	if err := environment.Validate(); err != nil {
		return err
	}
	members, err := compositeRuntimeMembers(composition.Effective(), environment, record)
	if err != nil {
		return err
	}
	for _, member := range composition.Members() {
		for _, tool := range member.Definition.InternalTools {
			if members[internalToolSelection(tool).Name] != member.Name {
				return e.New(e.CodeEngineCapability, "composite runtime %q must serve a disk output of its declaring member %q", tool.Name, member.Name)
			}
		}
	}
	return nil
}

// Derive the lookup used by the cross-member producer comparison from the
// Definition, exact consumer selection and output attribution already bound by
// the Plan. This description does not independently authenticate member files;
// compile and captured-input replay use admitCompositeRuntimeMembers above.
func compositeRuntimeMembers(input authored.Definition, environment authored.Environment, record *CompositionRecord) (map[string]string, error) {
	tools := make(map[string]authored.InternalTool, len(input.InternalTools))
	for _, tool := range input.InternalTools {
		if tool.Format != authored.OutputOCI {
			return nil, e.New(e.CodeEngineCapability, "composite internal tool %q requires an OCI runtime consumer", tool.Name)
		}
		tools[tool.Name] = tool
	}
	outputs := make(map[string]authored.DefinitionOutput, len(input.Outputs))
	for _, output := range input.Outputs {
		outputs[output.Name] = output
	}
	members := make(map[string]string, len(tools))
	for _, name := range sortedKeys(environment.Runtime.ProducedRuntimes) {
		toolName := environment.Runtime.ProducedRuntimes[name]
		tool, found := tools[toolName]
		output := outputs[name]
		if !found || output.Format != authored.OutputDiskImage || output.Platform != tool.Platform {
			return nil, e.New(e.CodeEngineCapability, "composite runtime %q requires its exact native disk consumer %q", toolName, name)
		}
		build := internalToolSelection(tool).Name
		if _, used := members[build]; used {
			return nil, e.New(e.CodeEngineCapability, "composite runtime %q requires one distinct disk consumer", toolName)
		}
		member, found := record.MemberOf(name)
		if !found {
			return nil, e.New(e.CodeUnverifiedRef, "composite runtime consumer %q has no attributed member", name)
		}
		members[build] = member
	}
	for _, tool := range input.InternalTools {
		if _, found := members[internalToolSelection(tool).Name]; !found {
			return nil, e.New(e.CodeEngineCapability, "composite runtime %q has no selected disk consumer", tool.Name)
		}
	}
	return members, nil
}

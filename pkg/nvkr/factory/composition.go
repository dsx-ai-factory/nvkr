// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// CompositePlanSchemaVersion is the Plan major that carries a composition
// record. Plain-Definition Plans keep PlanSchemaVersion and their bytes.
const CompositePlanSchemaVersion = "nvkr.dev/run-plan/v6"

// CompositionRecord is the definition-owned record carried by composite Plans.
type CompositionRecord = authored.CompositionRecord

func newCompositionRecord(composition authored.Composition) (*CompositionRecord, error) {
	return authored.NewCompositionRecord(composition)
}

// compileComposition compiles an admitted composite's effective Definition
// through the ordinary compiler, then binds the composition record. Each
// distinct catalog stack loads once per compile.
func compileComposition(ctx context.Context, composition authored.Composition, environmentPath string, environment authored.Environment,
	generatorVersion string, loadStack func(context.Context, authored.Catalog) (*catalog.Stack, error),
) (*Plan, error) {
	if err := admitCompositeEnvironment(environment); err != nil {
		return nil, err
	}
	record, err := newCompositionRecord(composition)
	if err != nil {
		return nil, err
	}
	stacks := map[string]*catalog.Stack{}
	loadOnce := func(ctx context.Context, spec authored.Catalog) (*catalog.Stack, error) {
		key := spec.Base + "\x00" + strings.Join(spec.Overlays, "\x00")
		if stack, ok := stacks[key]; ok {
			return stack, nil
		}
		stack, err := loadStack(ctx, spec)
		if err != nil {
			return nil, err
		}
		stacks[key] = stack
		return stack, nil
	}
	plan, err := compileDocumentsFor(ctx, composition.Path(), composition.Effective(), environmentPath, environment, generatorVersion, loadOnce, record)
	if err != nil {
		return nil, err
	}
	if plan.DefinitionDigest != composition.Digest() {
		return nil, e.New(e.CodeUnverifiedRef, "composite Plan does not bind the admitted composite digest")
	}
	if err := checkCrossMemberProducers(*plan, record); err != nil {
		return nil, err
	}
	return plan, nil
}

// admitDefinitionForm keeps named development bundles composite-only: the
// single fixed development layout belongs to plain Definitions (ADR-26 §9).
func admitDefinitionForm(environment authored.Environment, record *CompositionRecord) error {
	if record == nil && environment.GitHubDevelopment && environment.GitHubBundle != "" {
		return e.New(e.CodeEngineCapability, "githubDevelopment does not support a named githubBundle for a plain Definition")
	}
	return nil
}

// admitCompositeEnvironment refuses execution modes ADR-26 does not admit for
// composites before any catalog work. Runtime Plans recompile from a plain
// Definition file at dispatch and do not carry a composition record.
func admitCompositeEnvironment(environment authored.Environment) error {
	if environment.GitHubRuntimePlan {
		return e.New(e.CodeEngineCapability, "githubRuntimePlan does not support composite Definitions")
	}
	return nil
}

// checkCrossMemberProducers enforces ADR-26 §3: outputs of different members
// that select one (component, platform) must resolve the identical effective
// producer. Variants inside one member keep ADR-11 semantics.
func checkCrossMemberProducers(plan Plan, record *CompositionRecord) error {
	type producer struct {
		member string
		bytes  []byte
	}
	seen := map[string][]producer{}
	for _, build := range plan.Builds {
		output, _, _ := strings.Cut(build.Name, "/")
		member, ok := record.MemberOf(output)
		if !ok {
			return e.New(e.CodeUnverifiedRef, "native build %q has no attributed output", build.Name)
		}
		for _, component := range build.ResolvedComponents {
			resolved, err := canonical.Marshal(component)
			if err != nil {
				return e.Wrap(e.CodeInternal, err, "encoding producer %q", component.Name)
			}
			key := component.Name + "\x00" + build.Platform
			for _, previous := range seen[key] {
				if previous.member != member && !bytes.Equal(previous.bytes, resolved) {
					return e.New(e.CodeInvalidCatalog, "members %q and %q select divergent producers for component %q on %s",
						previous.member, member, component.Name, build.Platform)
				}
			}
			seen[key] = append(seen[key], producer{member: member, bytes: resolved})
		}
	}
	return nil
}

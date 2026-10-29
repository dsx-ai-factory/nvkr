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

// CompositionRecord binds a composite's identity, its members and every
// output's declaring member into a Plan (ADR-26 §4). Attribution is
// descriptive membership inside the authenticated record; it grants nothing.
type CompositionRecord struct {
	Kind                      string                       `json:"kind"`
	Metadata                  authored.Metadata            `json:"metadata"`
	Members                   []authored.MemberIdentity    `json:"members"`
	EffectiveDefinitionDigest string                       `json:"effectiveDefinitionDigest"`
	Outputs                   []authored.OutputAttribution `json:"outputs"`
}

// Validate requires a complete record consistent with the effective
// Definition and the definitionDigest that names the composite.
func (record CompositionRecord) Validate(definition authored.Definition, definitionDigest string) error {
	if record.Kind != authored.CompositeKind {
		return e.New(e.CodeUnverifiedRef, "composition record has unsupported definition kind %q", record.Kind)
	}
	compositeDigest, err := authored.CompositeDigest(record.Metadata, record.Members)
	if err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "composition record members")
	}
	if compositeDigest != definitionDigest {
		return e.New(e.CodeUnverifiedRef, "composition record does not recompute definitionDigest")
	}
	effectiveDigest, err := definition.Digest()
	if err != nil {
		return err
	}
	if record.EffectiveDefinitionDigest != effectiveDigest || definition.Metadata != record.Metadata {
		return e.New(e.CodeUnverifiedRef, "composition record does not bind the effective Definition")
	}
	if len(record.Outputs) != len(definition.Outputs) {
		return e.New(e.CodeUnverifiedRef, "composition record must attribute every output exactly once")
	}
	listed := make(map[string]int, len(record.Members))
	for _, member := range record.Members {
		listed[member.Name] = 0
	}
	for i, output := range definition.Outputs {
		attribution := record.Outputs[i]
		if attribution.Output != output.Name {
			return e.New(e.CodeUnverifiedRef, "composition record attribution for output %q is missing or out of order", output.Name)
		}
		count, ok := listed[attribution.Member]
		if !ok {
			return e.New(e.CodeUnverifiedRef, "output %q is attributed to unlisted member %q", output.Name, attribution.Member)
		}
		listed[attribution.Member] = count + 1
	}
	for name, count := range listed {
		if count == 0 {
			return e.New(e.CodeUnverifiedRef, "composition record lists member %q without outputs", name)
		}
	}
	return nil
}

// MemberOf returns the member that declared output, if any.
func (record CompositionRecord) MemberOf(output string) (string, bool) {
	for _, attribution := range record.Outputs {
		if attribution.Output == output {
			return attribution.Member, true
		}
	}
	return "", false
}

// Clone returns an independent record.
func (record *CompositionRecord) Clone() *CompositionRecord {
	if record == nil {
		return nil
	}
	value := *record
	value.Members = append([]authored.MemberIdentity(nil), record.Members...)
	value.Outputs = append([]authored.OutputAttribution(nil), record.Outputs...)
	return &value
}

func newCompositionRecord(composition authored.Composition) (*CompositionRecord, error) {
	effective := composition.Effective()
	digest, err := effective.Digest()
	if err != nil {
		return nil, err
	}
	composite := composition.Composite()
	return &CompositionRecord{Kind: authored.CompositeKind, Metadata: composite.Metadata, Members: composite.MemberIdentities(),
		EffectiveDefinitionDigest: digest, Outputs: composition.Attribution()}, nil
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
	plan, err := compileDocumentsWithLoader(ctx, composition.Path(), composition.Effective(), environmentPath, environment, generatorVersion, loadOnce)
	if err != nil {
		return nil, err
	}
	if err := checkCrossMemberProducers(*plan, record); err != nil {
		return nil, err
	}
	plan.SchemaVersion, plan.DefinitionDigest, plan.Composition = CompositePlanSchemaVersion, composition.Digest(), record
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
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

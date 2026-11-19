// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// CompositionRecord binds a composite's identity, its members, its declared
// variants, derived declarations and every output's owner into a Plan, Run record or
// publication root (ADR-26 §4). Attribution is descriptive membership inside
// the authenticated record; it grants no member-scoped authority.
type CompositionRecord struct {
	Kind                      string                   `json:"kind"`
	Metadata                  Metadata                 `json:"metadata"`
	Members                   []MemberIdentity         `json:"members"`
	Variants                  []DeclaredVariant        `json:"variants,omitempty"`
	DerivedOutputs            []CompositeDerivedOutput `json:"derivedOutputs,omitempty"`
	EffectiveDefinitionDigest string                   `json:"effectiveDefinitionDigest"`
	Outputs                   []OutputAttribution      `json:"outputs"`
}

// NewCompositionRecord records an admitted composition.
func NewCompositionRecord(composition Composition) (*CompositionRecord, error) {
	digest, err := composition.effective.Digest()
	if err != nil {
		return nil, err
	}
	return &CompositionRecord{Kind: CompositeKind, Metadata: composition.composite.Metadata, Members: composition.composite.MemberIdentities(),
		Variants: cloneVariants(composition.composite.Variants), DerivedOutputs: cloneCompositeDerived(composition.composite.DerivedOutputs), EffectiveDefinitionDigest: digest, Outputs: composition.Attribution()}, nil
}

// ValidateMembership requires a complete record whose members recompute
// definitionDigest and whose attribution names exactly outputs, in order, each
// to a listed member or a declared composite-derived output, with no listed
// member left without an output. Without the
// effective Definition it cannot check that declared variant members select
// their components; Validate adds that check when the Definition is available.
func (record CompositionRecord) ValidateMembership(definitionDigest string, outputs []string) error {
	if err := record.validate(definitionDigest); err != nil {
		return err
	}
	if len(record.Outputs) != len(outputs) {
		return e.New(e.CodeUnverifiedRef, "composition record must attribute every output exactly once")
	}
	for i, output := range outputs {
		if record.Outputs[i].Output != output {
			return e.New(e.CodeUnverifiedRef, "composition record attribution for output %q is missing or out of order", output)
		}
	}
	return nil
}

// ValidateOutputLocal requires the same complete record but lets outputs be the
// non-empty subset one output-local publication carries. Final Release issuance
// still requires ValidateMembership over the complete output set. Like
// ValidateMembership, it does not check variant selection; Validate does.
func (record CompositionRecord) ValidateOutputLocal(definitionDigest string, outputs []string) error {
	if err := record.validate(definitionDigest); err != nil {
		return err
	}
	if len(outputs) == 0 {
		return e.New(e.CodeUnverifiedRef, "composition record publication has no outputs")
	}
	for _, output := range outputs {
		if !slices.ContainsFunc(record.Outputs, func(attribution OutputAttribution) bool { return attribution.Output == output }) {
			return e.New(e.CodeUnverifiedRef, "composition record does not attribute output %q", output)
		}
	}
	return nil
}

func (record CompositionRecord) validate(definitionDigest string) error {
	if record.Kind != CompositeKind {
		return e.New(e.CodeUnverifiedRef, "composition record has unsupported definition kind %q", record.Kind)
	}
	compositeDigest, err := record.DefinitionDigest()
	if err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "composition record members")
	}
	if compositeDigest != definitionDigest {
		return e.New(e.CodeUnverifiedRef, "composition record does not recompute definitionDigest")
	}
	if !canonical.IsDigest(record.EffectiveDefinitionDigest) {
		return e.New(e.CodeUnverifiedRef, "composition record has no exact effective Definition digest")
	}
	listed := make(map[string]int, len(record.Members))
	for _, member := range record.Members {
		listed[member.Name] = 0
	}
	derived := make(map[string]CompositeDerivedOutput, len(record.DerivedOutputs))
	for _, output := range record.DerivedOutputs {
		derived[output.Name] = output
	}
	seen := make(map[string]bool, len(record.Outputs))
	for _, attribution := range record.Outputs {
		if seen[attribution.Output] {
			return e.New(e.CodeUnverifiedRef, "composition record must attribute every output exactly once")
		}
		seen[attribution.Output] = true
		if _, compositeOwned := derived[attribution.Output]; compositeOwned {
			if attribution.Member != "" {
				return e.New(e.CodeUnverifiedRef, "composite-derived output %q cannot belong to a member", attribution.Output)
			}
			continue
		}
		count, ok := listed[attribution.Member]
		if !ok {
			return e.New(e.CodeUnverifiedRef, "output %q is attributed to unlisted member %q", attribution.Output, attribution.Member)
		}
		listed[attribution.Member] = count + 1
	}
	for name, output := range derived {
		if !seen[name] {
			return e.New(e.CodeUnverifiedRef, "composite-derived output %q has no attribution", name)
		}
		for _, material := range output.Derived.Outputs {
			_, derives := derived[material]
			if !seen[material] || derives {
				return e.New(e.CodeUnverifiedRef, "composite-derived output %q has missing or derived material %q", name, material)
			}
		}
	}
	for name, count := range listed {
		if count == 0 {
			return e.New(e.CodeUnverifiedRef, "composition record lists member %q without outputs", name)
		}
	}
	return nil
}

// Validate additionally binds the record to the effective Definition and
// requires every declared variant member to select its component there.
func (record CompositionRecord) Validate(effective Definition, definitionDigest string) error {
	names := make([]string, 0, len(effective.Outputs))
	for _, output := range effective.Outputs {
		names = append(names, output.Name)
	}
	if err := record.ValidateMembership(definitionDigest, names); err != nil {
		return err
	}
	digest, err := effective.Digest()
	if err != nil {
		return err
	}
	if record.EffectiveDefinitionDigest != digest || effective.Metadata != record.Metadata {
		return e.New(e.CodeUnverifiedRef, "composition record does not bind the effective Definition")
	}
	if err := record.validateDerivedEffective(effective); err != nil {
		return err
	}
	return checkVariantSelection(effective, record.Outputs, record.Variants)
}

// MemberOf returns the member that declared output, if any.
func (record CompositionRecord) MemberOf(output string) (string, bool) {
	for _, attribution := range record.Outputs {
		if attribution.Output == output {
			return attribution.Member, attribution.Member != ""
		}
	}
	return "", false
}

// DeclaresVariant reports whether the composite lets members left and right
// resolve different producers for component. It expects a record that Validate
// or ValidateMembership has admitted, which refuses repeated components; it
// validates nothing itself and grants no authority.
func (record CompositionRecord) DeclaresVariant(component, left, right string) bool {
	for _, variant := range record.Variants {
		if variant.Component == component {
			return slices.Contains(variant.Members, left) && slices.Contains(variant.Members, right)
		}
	}
	return false
}

// Clone returns an independent record.
func (record *CompositionRecord) Clone() *CompositionRecord {
	if record == nil {
		return nil
	}
	value := *record
	value.Members = append([]MemberIdentity(nil), record.Members...)
	value.Variants = cloneVariants(record.Variants)
	value.DerivedOutputs = cloneCompositeDerived(record.DerivedOutputs)
	value.Outputs = append([]OutputAttribution(nil), record.Outputs...)
	return &value
}

// Equal reports whether two optional records are identical.
func (record *CompositionRecord) Equal(other *CompositionRecord) bool {
	if record == nil || other == nil {
		return record == other
	}
	left, leftErr := canonical.Marshal(record)
	right, rightErr := canonical.Marshal(other)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

// DefinitionDigest recomputes the composite identity, including its derived declarations.
func (record CompositionRecord) DefinitionDigest() (string, error) {
	return compositeDigest(record.Metadata, record.Members, record.Variants, record.DerivedOutputs)
}

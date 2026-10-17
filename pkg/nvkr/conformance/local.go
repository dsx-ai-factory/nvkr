// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
	"github.com/google/go-containerregistry/pkg/name"
)

// LocalImageBinding names a local deployment alias or a pinned tool-image
// location. It supplies no artifact integrity or publication authority: the
// lifecycle owner supplies exact subjects, and the API observations must match.
type LocalImageBinding struct {
	Output    string `json:"output"`
	Reference string `json:"reference"`
}

type LocalBindings struct {
	ProductImages []LocalImageBinding `json:"productImages"`
	ToolImages    []LocalImageBinding `json:"toolImages"`
}

type localSources struct {
	product, tools                       *lifecycle.LocalDiagnosticArtifacts
	productDescription, toolsDescription lifecycle.LocalDiagnosticDescription
	productBindings, toolBindings        []LocalImageBinding
}

// NewLocal borrows lifecycle-owned complete local Runs for one disposable local
// conformance execution. It reuses the upstream runner and original-report
// capture, but never manufactures published-source or qualification receipts.
// Keep both owners open through Produce and its joined cleanup.
func NewLocal(product, tools *lifecycle.LocalDiagnosticArtifacts, options Options, bindings LocalBindings) (*Producer, error) {
	if options.Artifacts != nil || options.ToolArtifacts != nil {
		return nil, e.New(e.CodeUsage, "local conformance cannot mix published and local artifact owners")
	}
	validated, err := validateOptions(options)
	if err != nil {
		return nil, err
	}
	local := &localSources{product: product, tools: tools,
		productBindings: append([]LocalImageBinding(nil), bindings.ProductImages...),
		toolBindings:    append([]LocalImageBinding(nil), bindings.ToolImages...)}
	local.productDescription, err = product.Description()
	if err != nil {
		return nil, err
	}
	local.toolsDescription, err = tools.Description()
	if err != nil {
		return nil, err
	}
	productView, aliases, err := localView(local.productDescription, local.productBindings, true)
	if err != nil {
		return nil, err
	}
	toolsView, _, err := localView(local.toolsDescription, local.toolBindings, false)
	if err != nil {
		return nil, err
	}
	if _, err := selectOutputs(productView, validated.Targets); err != nil {
		return nil, err
	}
	if err := validateToolSelections(toolsView, validated); err != nil {
		return nil, err
	}
	if _, _, err := resolveImages(toolsView, validated); err != nil {
		return nil, err
	}
	validated.localImageAliases = aliases
	return &Producer{options: validated, local: local}, nil
}

// localView is a detached data projection for the existing private selection
// helpers. It is never an ArtifactCheckReceipt: only the concrete local owners
// can admit Produce. No hosted origin, verification policy or release is added.
func localView(description lifecycle.LocalDiagnosticDescription, bindings []LocalImageBinding, allowAlias bool) (lifecycle.ArtifactCheckDescription, map[string]string, error) {
	view := lifecycle.ArtifactCheckDescription{}
	aliases := map[string]string{}
	if description.Lane != "unqualified-local-execution" {
		return view, nil, e.New(e.CodeEngineCapability, "local conformance requires complete local diagnostic Runs")
	}
	if len(bindings) == 0 || len(bindings) > 64 {
		return view, nil, e.New(e.CodeUsage, "local conformance requires bounded explicit image bindings")
	}
	for _, output := range description.Outputs {
		view.Outputs = append(view.Outputs, lifecycle.ArtifactCheckOutput{
			Name: output.Name, Kind: output.Kind, Subject: output.Digest,
			BuildDigest: output.BuildDigest, Platform: output.OS + "/" + output.Arch,
			ImageIndex: output.ImageIndex, BuildSubjects: output.BuildSubjects,
		})
	}
	for _, binding := range bindings {
		if !safetext.Valid(binding.Output, 4096, false) || !safetext.Valid(binding.Reference, 1024, false) {
			return view, nil, e.New(e.CodeUsage, "local image binding requires explicit bounded names")
		}
		if _, exists := aliases[binding.Output]; exists {
			return view, nil, e.New(e.CodeUsage, "local image binding repeats an output")
		}
		ref, err := name.ParseReference(binding.Reference, name.StrictValidation)
		if err != nil || !canonicalLocalReference(ref, binding.Reference, allowAlias) {
			return view, nil, e.New(e.CodeUsage, "local image location requires a canonical OCI reference")
		}
		matches := 0
		for i := range view.Outputs {
			output := &view.Outputs[i]
			if output.Name != binding.Output {
				continue
			}
			matches++
			if output.Kind != lifecycle.OutputOCI {
				return view, nil, e.New(e.CodeUsage, "local image binding must select an OCI output")
			}
			if digest, ok := ref.(name.Digest); ok {
				if !localImageDigestMatches(*output, digest.DigestStr()) {
					return view, nil, failure("local image binding differs from held output bytes")
				}
			} else if !allowAlias {
				return view, nil, e.New(e.CodeUnpinnedInput, "local conformance tool images require digest references")
			}
			output.Reference = ref.Context().Name() + "@" + output.Subject
		}
		if matches != 1 {
			return view, nil, failure("local image binding must select one exact held output")
		}
		aliases[binding.Output] = binding.Reference
	}
	return view, aliases, nil
}

// The upstream reference parser owns syntax. It intentionally removes an OCI
// tag when normalizing a tag@digest reference; local PodSpecs may retain that
// exact tag. Require the upstream tag parser to confirm its explicit canonical
// repository while keeping the digest check against held bytes unchanged.
func canonicalLocalReference(ref name.Reference, value string, allowAlias bool) bool {
	if ref.Name() == value {
		return true
	}
	if !allowAlias {
		return false
	}
	if _, ok := ref.(name.Digest); !ok {
		return false
	}
	prefix, _, _ := strings.Cut(value, "@")
	tag, err := name.NewTag(prefix, name.StrictValidation)
	return err == nil && tag.Name() == prefix && tag.Repository.Name() == ref.Context().Name()
}

func localImageDigestMatches(output lifecycle.ArtifactCheckOutput, digest string) bool {
	if digest == output.Subject {
		return true
	}
	if output.ImageIndex != nil {
		for _, child := range output.ImageIndex.Children {
			if child.ManifestDigest == digest {
				return true
			}
		}
	}
	return false
}

func (p *Producer) produceLocal(ctx context.Context) (output testresults.Output, resultErr error) {
	err := p.local.tools.WithDiagnosticOperation(ctx, func(toolsContext context.Context, tools lifecycle.LocalDiagnosticDescription) error {
		return p.local.product.WithDiagnosticOperation(toolsContext, func(operation context.Context, product lifecycle.LocalDiagnosticDescription) error {
			productView, _, err := localView(product, p.local.productBindings, true)
			if err != nil {
				return err
			}
			toolsView, _, err := localView(tools, p.local.toolBindings, false)
			if err != nil {
				return err
			}
			output, err = p.produce(operation, productView, toolsView)
			return err
		})
	})
	if err != nil && output.SchemaVersion != "" {
		output.CaptureStatus = testresults.CapturePartial
	}
	return output, err
}

func localSourceFacts(scope string, description lifecycle.LocalDiagnosticDescription) []testresults.EnvironmentFact {
	facts := []testresults.EnvironmentFact{}
	for _, fact := range []struct{ name, value string }{
		{"lane", description.Lane}, {"attempt", description.Attempt},
		{"planDigest", description.PlanDigest}, {"definitionDigest", description.DefinitionDigest},
		{"environmentDigest", description.EnvironmentDigest}, {"catalogDigest", description.CatalogDigest}, {"contentSubject", description.ContentSubject},
		{"ordinaryRoot", description.OrdinaryRoot},
	} {
		facts = append(facts, testresults.EnvironmentFact{Scope: scope, Name: fact.name, Phase: "before", Metadata: testresults.Metadata{Status: "observed", Value: fact.value}})
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].Name < facts[j].Name })
	return facts
}

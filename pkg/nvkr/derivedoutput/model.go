// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package derivedoutput owns publication-bound output declarations and their
// logical identity. These descriptions grant no authority over package bytes.
package derivedoutput

import (
	"regexp"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

const InventoryV1 = "aicr-inventory-v1"

var outputName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

// Selection is authored alongside an output. Its named siblings must all be
// ordinary chart or image outputs; another derived output cannot be described.
type Selection struct {
	Encoding string   `json:"encoding"`
	Outputs  []string `json:"outputs"`
}

func (s *Selection) Clone() *Selection {
	if s == nil {
		return nil
	}
	return &Selection{Encoding: s.Encoding, Outputs: slices.Clone(s.Outputs)}
}

func (s Selection) Validate() error {
	if s.Encoding != InventoryV1 || len(s.Outputs) == 0 || len(s.Outputs) > 10000 {
		return e.New(e.CodeInvalidCatalog, "derived output requires a supported encoding and explicit sibling outputs")
	}
	previous := ""
	for _, output := range s.Outputs {
		if len(output) > 128 || !outputName.MatchString(output) || output <= previous {
			return e.New(e.CodeInvalidCatalog, "derived output selections must be canonical, sorted and unique")
		}
		previous = output
	}
	return nil
}

// Input binds a named package requirement, never an observed payload digest.
type Input struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	BuildDigest string `json:"buildDigest"`
}

// Material records an observed sibling package subject, separately from the
// frozen logical binding. It is descriptive until the owning boundary checks it.
type Material struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

func (b Binding) ValidateMaterials(materials []Material) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if len(materials) != len(b.Inputs) {
		return e.New(e.CodeProvenanceMissing, "derived output lacks exact observed sibling membership")
	}
	for i, input := range b.Inputs {
		if materials[i].Name != input.Name || !canonical.IsDigest(materials[i].Digest) {
			return e.New(e.CodeVerifyFailed, "derived output has a substituted sibling subject")
		}
	}
	return nil
}

// Binding is compiled before execution. Runtime package digests remain dataflow;
// changing the destination or any selected named build changes this identity.
type Binding struct {
	Encoding         string  `json:"encoding"`
	RepositoryPrefix string  `json:"repositoryPrefix"`
	Inputs           []Input `json:"inputs"`
}

func (b *Binding) Clone() *Binding {
	if b == nil {
		return nil
	}
	return &Binding{Encoding: b.Encoding, RepositoryPrefix: b.RepositoryPrefix, Inputs: slices.Clone(b.Inputs)}
}

func (b Binding) Validate() error {
	s := Selection{Encoding: b.Encoding}
	for _, input := range b.Inputs {
		s.Outputs = append(s.Outputs, input.Name)
		if (input.Kind != "helm-oci" && input.Kind != "oci") || !canonical.IsDigest(input.BuildDigest) {
			return e.New(e.CodeVerifyFailed, "derived output requires exact chart/image build identities")
		}
	}
	if err := s.Validate(); err != nil {
		return err
	}
	repository, err := name.NewRepository(b.RepositoryPrefix, name.StrictValidation)
	if err != nil || repository.Name() != b.RepositoryPrefix || strings.ContainsAny(b.RepositoryPrefix, "@?#") {
		return e.New(e.CodeVerifyFailed, "derived output requires an exact publication namespace")
	}
	return nil
}

func (b Binding) BuildDigest() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindBuild, struct {
		Kind    string  `json:"kind"`
		Binding Binding `json:"binding"`
	}{"release-derived-output/v1", b})
}

// ValidateOutput prevents a derived marker from waiving ordinary producer
// evidence or being attached to an unrelated format or logical identity.
func (b Binding) ValidateOutput(output, kind, digest string, components []string) error {
	expected, err := b.BuildDigest()
	if err != nil {
		return err
	}
	if !outputName.MatchString(output) || kind != "bundle-oci" || digest != expected || len(components) != 0 {
		return e.New(e.CodeVerifyFailed, "derived output differs from its publication-bound build")
	}
	for _, input := range b.Inputs {
		if input.Name == output {
			return e.New(e.CodeVerifyFailed, "derived output cannot describe itself")
		}
	}
	return nil
}

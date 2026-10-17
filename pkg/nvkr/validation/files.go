// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package validation routes explicitly configured external checks to their
// owning adapters. Report descriptions grant no qualification or passing verdict.
package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/conformance"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

const configurationByteLimit = 1 << 20

// Configuration selects a published source or complete local diagnostic Run
// and its independently configured checks.
// Credential scope remains in runtime options, never in this profile.
type Configuration struct {
	Source Source  `json:"source"`
	Checks []Check `json:"checks"`
}
type Source struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference,omitempty"`
	Policy    string `json:"policy,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}
type Check struct {
	Name    string `json:"name"`
	Suite   string `json:"suite"`
	Harness string `json:"harness"`
	Config  string `json:"config"`
}

// RunFiles runs configured admitted checks, or one explicitly selected name.
// Admission validates the entire profile's selections before provider or harness
// work. Each selected producer configuration is admitted when that check starts.
// Only kube-conformance/hydrophone is implemented; other adapters remain unsupported.
func RunFiles(ctx context.Context, checkName, configPath string, options lifecycle.QualificationRuntimeOptions) ([]testresults.Output, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "validation requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "validation canceled before admission")
	}
	if !pathText(configPath) || !pathText(options.WorkspaceRoot) {
		return nil, e.New(e.CodeUsage, "validation requires configuration and workspace paths")
	}
	profile, checks, err := loadConfiguration(ctx, checkName, configPath)
	if err != nil {
		return nil, err
	}
	if profile.Source.Kind == "local" {
		return runSelected(ctx, profile.Source, checks, options, func(ctx context.Context, _, _, config string, options lifecycle.QualificationRuntimeOptions) (testresults.Output, error) {
			return conformance.RunLocalFiles(ctx, profile.Source.Workspace, config, options)
		})
	}
	return runSelected(ctx, profile.Source, checks, options, conformance.RunReferenceFiles)
}

type checkRunner func(context.Context, string, string, string, lifecycle.QualificationRuntimeOptions) (testresults.Output, error)

// The private dispatch seam tests output retention without issuing fake source
// receipts or exposing caller-replaceable production authentication.
func runSelected(ctx context.Context, source Source, checks []Check, options lifecycle.QualificationRuntimeOptions, run checkRunner) ([]testresults.Output, error) {
	outputs := make([]testresults.Output, 0, len(checks))
	for _, check := range checks {
		output, err := run(ctx, source.Reference, source.Policy, check.Config, options)
		if output.SchemaVersion != "" {
			if err != nil {
				output.CaptureStatus = testresults.CapturePartial
			}
			outputs = append(outputs, output)
		}
		if err != nil {
			return outputs, err
		}
	}
	return outputs, nil
}

func loadConfiguration(ctx context.Context, checkName, path string) (Configuration, []Check, error) {
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "validation configuration", MaxBytes: configurationByteLimit})
	if err != nil {
		return Configuration{}, nil, configurationError(ctx, err, "opening validation configuration")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, configurationByteLimit)
	if err != nil {
		return Configuration{}, nil, configurationError(ctx, err, "reading validation configuration")
	}
	if err = canonical.ValidateJSONDocument(raw); err != nil {
		return Configuration{}, nil, e.Wrap(e.CodeUsage, err, "validation configuration requires one unambiguous JSON document")
	}
	var profile Configuration
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&profile); err != nil {
		return Configuration{}, nil, e.Wrap(e.CodeUsage, err, "decoding validation configuration")
	}
	if err = profile.validate(); err != nil {
		return Configuration{}, nil, err
	}
	if checkName != "" && !safetext.Valid(checkName, 256, false) {
		return Configuration{}, nil, e.New(e.CodeUsage, "validation check selection requires safe text")
	}
	selected := []Check{}
	for _, check := range profile.Checks {
		if checkName == "" || check.Name == checkName {
			selected = append(selected, check)
		}
	}
	if len(selected) == 0 {
		return Configuration{}, nil, e.New(e.CodeUnsupported, "validation check %q is not configured", checkName)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Configuration{}, nil, e.Wrap(e.CodeUsage, err, "resolving validation configuration")
	}
	resolve := func(value string) string {
		if filepath.IsAbs(value) {
			return value
		}
		return filepath.Join(filepath.Dir(absolute), value)
	}
	if profile.Source.Policy != "" {
		profile.Source.Policy = resolve(profile.Source.Policy)
	}
	for i := range selected {
		selected[i].Config = resolve(selected[i].Config)
	}
	return profile, selected, nil
}
func (profile Configuration) validate() error {
	if profile.Source.Kind == "local" {
		if !pathText(profile.Source.Workspace) || profile.Source.Reference != "" || profile.Source.Policy != "" {
			return e.New(e.CodeUsage, "local validation requires an explicit workspace without publication or policy fields")
		}
	} else if err := profile.validatePublishedSource(); err != nil {
		return err
	}
	if len(profile.Checks) == 0 || len(profile.Checks) > 64 {
		return e.New(e.CodeUsage, "validation profile requires 1..64 configured checks")
	}
	seen := map[string]bool{}
	for _, check := range profile.Checks {
		if !safetext.Valid(check.Name, 256, false) || strings.TrimSpace(check.Name) != check.Name {
			return e.New(e.CodeUsage, "validation check requires a safe instance name")
		}
		if check.Suite != "kube-conformance" {
			return e.New(e.CodeUnsupported, "validation suite %q is unsupported", check.Suite)
		}
		if check.Harness != "hydrophone" {
			return e.New(e.CodeUnsupported, "validation harness %q is unsupported for %s", check.Harness, check.Name)
		}
		if seen[check.Name] {
			return e.New(e.CodeUsage, "validation check names must be unique")
		}
		seen[check.Name] = true
		if !pathText(check.Config) {
			return e.New(e.CodeUsage, "validation check requires a bounded configuration path")
		}
	}
	return nil
}

func (profile Configuration) validatePublishedSource() error {
	if profile.Source.Kind != "published" {
		return e.New(e.CodeUnsupported, "validation source kind %q is unsupported", profile.Source.Kind)
	}
	if profile.Source.Workspace != "" {
		return e.New(e.CodeUsage, "published validation cannot include a local workspace")
	}
	if !pathText(profile.Source.Reference) || strings.Contains(profile.Source.Reference, "://") {
		return e.New(e.CodeUsage, "validation source requires an immutable OCI publication reference")
	}
	if err := delivery.ValidatePublicationReference(profile.Source.Reference); err != nil {
		return e.Wrap(e.CodeUsage, err, "validation source reference")
	}
	if !pathText(profile.Source.Policy) {
		return e.New(e.CodeUsage, "validation source requires a bounded policy path")
	}
	return nil
}
func pathText(value string) bool {
	return safetext.Valid(value, 4096, false) && strings.TrimSpace(value) == value
}
func configurationError(ctx context.Context, err error, message string) error {
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "%s", message)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return e.Wrap(e.CodeTimeout, err, "%s", message)
	}
	return e.Wrap(e.CodeUsage, err, "%s", message)
}

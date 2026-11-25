// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkequalify

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const configByteLimit = 1 << 20

// File names a planner directory holds when its configuration names none.
const (
	defaultSubject  = "subject.yaml"
	defaultPolicy   = "policy.yaml"
	defaultRegistry = "registry.yaml"
)

// Config is the planner's own file. The three NVKE-schema documents it names
// resolve beside it.
type Config struct {
	// Subject is the subject NVKE's planner plans for.
	Subject string `json:"subject"`

	// Scope selects the streams to plan.
	Scope string `json:"scope"`

	// Validator names the suite each target runs.
	Validator string `json:"validator"`

	// SubjectSpec, Policy and Registry are file names beside the configuration.
	SubjectSpec string `json:"subjectSpec,omitempty"`
	Policy      string `json:"policy,omitempty"`
	Registry    string `json:"registry,omitempty"`
}

// loadConfig reads and validates the planner's configuration, and returns it
// with the paths of its three documents resolved.
func loadConfig(path string) (Config, files, error) {
	raw, err := readBounded(path)
	if err != nil {
		return Config{}, files{}, e.Wrap(e.CodeUsage, err, "reading the %s planner configuration", Name)
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, files{}, e.Wrap(e.CodeUsage, err, "decoding the %s planner configuration", Name)
	}
	if decoder.More() {
		return Config{}, files{}, e.New(e.CodeUsage, "the %s planner configuration requires exactly one document", Name)
	}
	for field, value := range map[string]string{"subject": cfg.Subject, "scope": cfg.Scope, "validator": cfg.Validator} {
		if value == "" {
			return Config{}, files{}, e.New(e.CodeUsage, "the %s planner configuration requires %q", Name, field)
		}
	}
	dir := filepath.Dir(path)
	resolved := files{}
	for _, f := range []struct {
		field, value, fallback string
		out                    *string
	}{
		{"subjectSpec", cfg.SubjectSpec, defaultSubject, &resolved.subject},
		{"policy", cfg.Policy, defaultPolicy, &resolved.policy},
		{"registry", cfg.Registry, defaultRegistry, &resolved.registry},
	} {
		name := f.value
		if name == "" {
			name = f.fallback
		}
		if !filepath.IsLocal(name) {
			return Config{}, files{}, e.New(e.CodeUsage, "%q in the %s planner configuration must name a file beside it", f.field, Name)
		}
		*f.out = filepath.Join(dir, name)
	}
	return cfg, resolved, nil
}

// files holds the resolved paths of the planner's NVKE-schema documents.
type files struct {
	subject, policy, registry string
}

func readBounded(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, e.New(e.CodeUsage, "%s is not a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, configByteLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > configByteLimit {
		return nil, e.New(e.CodeUsage, "%s exceeds %d bytes", filepath.Base(path), configByteLimit)
	}
	return raw, nil
}

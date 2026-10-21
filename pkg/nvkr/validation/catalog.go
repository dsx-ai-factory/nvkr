// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
)

// These provisional schemas implement ADR-24; they are not stable compatibility promises.
type envelope struct {
	APIVersion string `json:"apiVersion" yaml:"apiVersion"`
	Kind       string `json:"kind" yaml:"kind"`
}
type catalogIndex struct {
	envelope `yaml:",inline"`
	Entries  []string `json:"entries" yaml:"entries"`
}

// Validator describes requirements, never runtime readiness or evidence authority.
type Validator struct {
	envelope    `yaml:",inline"`
	ID          string                      `json:"id" yaml:"id"`
	Description string                      `json:"description" yaml:"description"`
	Adapter     string                      `json:"adapter" yaml:"adapter"`
	Suite       string                      `json:"suite" yaml:"suite"`
	Inputs      map[string]InputRequirement `json:"inputs" yaml:"inputs"`
	SuiteSource SuiteSource                 `json:"suiteSource" yaml:"suiteSource"`
	Platforms   []string                    `json:"platforms" yaml:"platforms"`
	Requires    []string                    `json:"requires" yaml:"requires"`
	Timeout     string                      `json:"timeout" yaml:"timeout"`
	Coverage    string                      `json:"coverage" yaml:"coverage"`
	Limitations []string                    `json:"limitations" yaml:"limitations"`
}
type InputRequirement struct {
	Component string `json:"component" yaml:"component"`
	Format    string `json:"format" yaml:"format"`
}
type SuiteSource struct {
	Mode      string `json:"mode" yaml:"mode"`
	FromInput string `json:"fromInput" yaml:"fromInput"`
}
type profileSource struct {
	Kind      string `json:"kind" yaml:"kind"`
	Plan      string `json:"plan,omitempty" yaml:"plan,omitempty"`
	Reference string `json:"reference,omitempty" yaml:"reference,omitempty"`
	Policy    string `json:"policy,omitempty" yaml:"policy,omitempty"`
}
type binding struct {
	Output   string `json:"output" yaml:"output"`
	Artifact string `json:"artifact" yaml:"artifact"`
}
type profileCheck struct {
	Name      string             `json:"name" yaml:"name"`
	Validator string             `json:"validator" yaml:"validator"`
	Inputs    map[string]binding `json:"inputs" yaml:"inputs"`
}
type profile struct {
	envelope `yaml:",inline"`
	Catalog  string         `json:"catalog" yaml:"catalog"`
	Source   profileSource  `json:"source" yaml:"source"`
	Checks   []profileCheck `json:"checks" yaml:"checks"`
}

// Catalog holds validated metadata and exact source-document digests privately.
type Catalog struct {
	entries   map[string]Validator
	documents map[string]string
}

// Description distinguishes installed capability from unverified runtime prerequisites.
type Description struct {
	AdapterLimitations []string  `json:"adapterLimitations,omitempty"`
	Validator          Validator `json:"validator"`
	Available          bool      `json:"available"`
	UnavailableReason  string    `json:"unavailableReason,omitempty"`
	RuntimeReadiness   string    `json:"runtimeReadiness"`
}

// Runtime controls output retention, not suite or subject selection.
type Runtime struct {
	WorkspaceRoot  string
	Keep           bool
	Stdout, Stderr io.Writer
}

// adapter is a compiled, versioned protocol, not a user-installable plugin runtime.
// prepare must only admit static inputs; its returned closure owns executable work.
type adapter interface {
	validate(Validator) error
	limitations() []string
	prepare(context.Context, Validator, profileSource, profileCheck, Runtime, []byte) (func(context.Context) error, any, error)
}

var adapterProtocol = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?/v[1-9][0-9]{0,3}$`)

var segment = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

func validID(id string) bool {
	parts := strings.Split(id, "/")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if !segment.MatchString(p) {
			return false
		}
	}
	return true
}
func usage(message string) error { return e.New(e.CodeUsage, "%s", message) }
func validText(s string) bool    { return safetext.Valid(s, 4096, false) }
func unique(values []string) bool {
	seen := map[string]bool{}
	for _, s := range values {
		if !validText(s) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
func (v Validator) validate() error {
	if v.APIVersion != "nvkr.dev/validator/v1" || v.Kind != "Validator" || !validID(v.ID) || !validText(v.Description) ||
		!adapterProtocol.MatchString(v.Adapter) || !segment.MatchString(v.Suite) || len(v.Inputs) == 0 || len(v.Inputs) > 64 {
		return usage("invalid validator envelope, identity or inputs")
	}
	for name, role := range v.Inputs {
		if !segment.MatchString(name) || !validText(role.Component) || !validText(role.Format) {
			return usage("invalid validator role")
		}
	}
	if _, ok := v.Inputs[v.SuiteSource.FromInput]; !ok || v.SuiteSource.Mode != "build-source" {
		return usage("unsupported suite-source relationship")
	}
	timeout, err := time.ParseDuration(v.Timeout)
	if err != nil || timeout <= 0 || len(v.Platforms) == 0 || !unique(v.Platforms) || !unique(v.Requires) || len(v.Limitations) > 64 || !unique(v.Limitations) {
		return usage("invalid validator requirements or timeout")
	}
	if v.Coverage != "artifact-contract" && v.Coverage != "source" && v.Coverage != "mixed" {
		return usage("invalid coverage category")
	}
	sort.Strings(v.Platforms)
	sort.Strings(v.Requires)
	sort.Strings(v.Limitations)
	return nil
}

// LoadCatalog reads only explicitly indexed documents. It performs no acquisition or execution.
func LoadCatalog(ctx context.Context, path string) (*Catalog, error) {
	if ctx == nil {
		return nil, usage("catalog requires context")
	}
	root, err := openCatalogRoot(ctx, filepath.Dir(path))
	if err != nil {
		return nil, configurationError(ctx, err, "opening catalog root")
	}
	defer func() { _ = root.Close() }()
	return loadCatalogRoot(ctx, path, root)
}

// loadCatalogRoot reads the index and entries through the same held directory.
func loadCatalogRoot(ctx context.Context, path string, root *catalogRoot) (*Catalog, error) {
	documents := map[string]string{}
	var index catalogIndex
	budget := 16 << 20
	if err := readDocumentAt(ctx, root, path, &index, documents, &budget); err != nil {
		return nil, err
	}
	if index.APIVersion != "nvkr.dev/validator-catalog/v1" || index.Kind != "ValidatorCatalog" || len(index.Entries) == 0 || len(index.Entries) > 1024 || !unique(index.Entries) {
		return nil, usage("invalid validator catalog index")
	}
	result := &Catalog{entries: map[string]Validator{}, documents: documents}
	for _, path := range index.Entries {
		if !safeEntryPath(path) {
			return nil, usage("catalog entry path must remain beneath its root without symlinks")
		}
		raw, err := root.read(ctx, path, 1<<20)
		if err != nil {
			return nil, configurationError(ctx, err, "reading validator entry")
		}
		var v Validator
		if err := decodeDocument(raw, &v); err != nil {
			return nil, err
		}
		budget -= len(raw)
		if budget < 0 {
			return nil, usage("catalog exceeds 16 MiB")
		}
		// Entry keys are root-relative; source digests do not claim semantic identity.
		documents["entry:"+path] = blobDigest(raw)
		if err := v.validate(); err != nil {
			return nil, err
		}
		if _, ok := result.entries[v.ID]; ok {
			return nil, usage("duplicate validator ID")
		}
		result.entries[v.ID] = v
	}
	return result, nil
}
func (c *Catalog) descriptions(registry map[string]adapter) []Description {
	ids := make([]string, 0, len(c.entries))
	for id := range c.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]Description, 0, len(ids))
	for _, id := range ids {
		v := c.entries[id]
		d := Description{Validator: v, RuntimeReadiness: "unverified"}
		a, ok := registry[v.Adapter]
		if ok {
			d.AdapterLimitations = a.limitations()
		}
		if !ok {
			d.UnavailableReason = "adapter is not installed"
		} else if err := a.validate(v); err != nil {
			d.UnavailableReason = err.Error()
		} else {
			d.Available = true
		}
		result = append(result, d)
	}
	// Detach all collections so callers cannot mutate the loaded catalog.
	for i := range result {
		v := &result[i].Validator
		v.Inputs = maps.Clone(v.Inputs)
		v.Platforms = slices.Clone(v.Platforms)
		v.Requires = slices.Clone(v.Requires)
		v.Limitations = slices.Clone(v.Limitations)
	}
	return result
}

// List returns deterministic descriptions without probing sources, Docker or infrastructure.
func (c *Catalog) List() []Description { return c.descriptions(installedAdapters()) }

// Describe requires an exact ID; no aliases or fallback are supported.
func (c *Catalog) Describe(id string) (Description, error) {
	for _, d := range c.List() {
		if d.Validator.ID == id {
			return d, nil
		}
	}
	return Description{}, e.New(e.CodeUnsupported, "unknown validator %q", id)
}

// Invocation owns a fully admitted, immutable selection. Configuration is descriptive only.
type Invocation struct {
	run           []admittedCheck
	configuration []byte
}

type admittedCheck struct {
	name string
	run  func(context.Context) error
}

// Configuration returns canonical resolved selection and document digests as a copy.
func (i *Invocation) Configuration() []byte { return append([]byte(nil), i.configuration...) }

// Run executes in profile order and stops on the first failure. It never reloads configuration.
func (i *Invocation) Run(ctx context.Context) error {
	if i == nil || len(i.run) == 0 || ctx == nil {
		return usage("missing admitted validation invocation")
	}
	for _, check := range i.run {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "validation canceled")
		}
		if err := check.run(ctx); err != nil {
			code := e.CodeInternal
			var coded *e.Error
			if errors.As(err, &coded) {
				code = coded.Code
			}
			return e.Wrap(code, err, "check %q", check.name)
		}
	}
	return nil
}

// AdmitProfile validates every check before any workspace, acquisition or executable work.
func AdmitProfile(ctx context.Context, path string, runtime Runtime) (*Invocation, error) {
	return admitProfile(ctx, path, runtime, installedAdapters())
}
func admitProfile(ctx context.Context, path string, runtime Runtime, registry map[string]adapter) (*Invocation, error) {
	if ctx == nil {
		return nil, usage("validation requires context")
	}
	documents := map[string]string{}
	budget := 1 << 20
	var p profile
	if err := readDocument(ctx, path, &p, documents, &budget); err != nil {
		return nil, err
	}
	if p.APIVersion != "nvkr.dev/validation-profile/v1" || p.Kind != "ValidationProfile" || !pathText(p.Catalog) || len(p.Checks) == 0 || len(p.Checks) > 64 {
		return nil, usage("invalid versioned validation profile")
	}
	if p.Source.Kind != "local-plan" {
		return nil, e.New(e.CodeUnsupported, "new profile supports local-plan only; use legacy validate for published sources")
	}
	if !pathText(p.Source.Plan) || p.Source.Reference != "" || p.Source.Policy != "" {
		return nil, usage("local-plan fields must not mix trust modes")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, e.Wrap(e.CodeUsage, err, "resolving profile")
	}
	resolve := func(s string) string {
		if filepath.IsAbs(s) {
			return s
		}
		return filepath.Join(filepath.Dir(absolute), s)
	}
	p.Catalog = resolve(p.Catalog)
	p.Source.Plan = resolve(p.Source.Plan)
	catalog, err := LoadCatalog(ctx, p.Catalog)
	if err != nil {
		return nil, err
	}
	for k, v := range catalog.documents {
		documents[k] = v
	}
	names := map[string]bool{}
	// Validate all references and role sets before calling any adapter admission.
	for i := range p.Checks {
		check := &p.Checks[i]
		if !segment.MatchString(check.Name) || names[check.Name] {
			return nil, usage("invalid or duplicate check name")
		}
		names[check.Name] = true
		v, ok := catalog.entries[check.Validator]
		if !ok {
			return nil, e.New(e.CodeUnsupported, "unknown validator %q", check.Validator)
		}
		a, ok := registry[v.Adapter]
		if !ok {
			return nil, e.New(e.CodeEngineCapability, "adapter %q is unavailable", v.Adapter)
		}
		if err := a.validate(v); err != nil {
			return nil, err
		}
		if len(check.Inputs) != len(v.Inputs) {
			return nil, usage("missing or extra input roles")
		}
		for role, b := range check.Inputs {
			if _, ok := v.Inputs[role]; !ok || !pathText(b.Artifact) || !safetext.Valid(b.Output, 256, false) {
				return nil, usage("invalid input binding")
			}
			b.Artifact = resolve(b.Artifact)
			check.Inputs[role] = b
		}
	}
	resolved := struct {
		Profile    profile           `json:"profile"`
		Validators []Validator       `json:"validators"`
		Documents  map[string]string `json:"documents"`
	}{Profile: p, Documents: documents}
	for _, check := range p.Checks {
		resolved.Validators = append(resolved.Validators, catalog.entries[check.Validator])
	}
	metadata, err := canonical.Marshal(resolved)
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding admitted selection")
	}
	invocation := &Invocation{}
	descriptions := []any{}
	// Admission closures hold factory-issued snapshots, never mutable profile paths.
	for _, check := range p.Checks {
		v := catalog.entries[check.Validator]
		run, description, err := registry[v.Adapter].prepare(ctx, v, p.Source, check, runtime, metadata)
		if err != nil {
			return nil, err
		}
		invocation.run = append(invocation.run, admittedCheck{name: check.Name, run: run})
		descriptions = append(descriptions, description)
	}
	invocation.configuration, err = canonical.Marshal(struct {
		Selection json.RawMessage `json:"selection"`
		Inputs    []any           `json:"inputs"`
	}{metadata, descriptions})
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "encoding invocation")
	}
	return invocation, nil
}

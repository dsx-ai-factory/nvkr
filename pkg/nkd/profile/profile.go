// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package profile models the NKD builder profile — the reconciliation of nkdctl's
// reproducibility contract toward NKD v1 §8.2.1/§8.2.2/§10.1. NKD v1 favors bounding
// byte-identity to a *signed builder profile* rather than claiming host-independent
// (cross-environment) bit-identity: the host kernel, container runtime, filesystem,
// virtualization, locale, and clock are treated as byte-determinative, so a build's
// reproducibility claim holds only *within a declared, digest-identified profile*. A
// host that diverges from the selected profile on a field demonstrated to change
// output cannot claim byte reproduction.
//
// The profile DIGEST is the authoritative identity (a label like
// "gha-ubuntu-24.04-amd64-v1" is not; NKD v1 §8.2.1). Preflight runs before inputs
// are resolved and fails closed when a required (hard-fail) host property is absent
// or differs. A profile is not host-independence machinery — it makes the host
// dependence explicit, pinned, and checkable.
package profile

import (
	"context"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// SchemaVersion identifies the builder-profile shape.
const SchemaVersion = "nkd.dev/builder-profile/v1"

// Host-control class (NKD v1 §8.2.2): who controls the pinned host image. At least
// one operator-controlled profile MUST exist for every released build, so a release
// is never reachable only through infrastructure that both NVIDIA and the operator
// lack control over (e.g. a hosted CI runner image the provider rotates without
// notice). Preflight is fail-closed, so an unbounded provider rotation would
// otherwise halt every operator on that profile at once.
const (
	OperatorControlled = "operator-controlled"
	ProviderControlled = "provider-controlled"
)

// MeasurementStage names the only stage at which a profile field may be
// observed. Keeping this classification in the signed profile prevents an
// execution identity from being smuggled through the generic host-fact seam.
type MeasurementStage string

const (
	MeasurementHost      MeasurementStage = "host"
	MeasurementExecution MeasurementStage = "execution"
)

// Field is one pinned profile property, classified by the stage that can measure
// it. HardFail marks a host field whose divergence stops host preflight; execution
// identity fields are always hard-fail by classification (ADR-07 §2).
type Field struct {
	Name             string           `json:"name"`
	Value            string           `json:"value"`
	HardFail         bool             `json:"hardFail"`
	MeasurementStage MeasurementStage `json:"measurementStage,omitempty"`
}

// BuilderProfile is a signed, digest-identified declaration of the host properties a
// reference build was produced under. Identity/Version/HostControlClass are labels;
// the authoritative identity is Digest(). NVIDIA (or any publisher) publishes exactly
// one reference build + digest vector per supported profile (NKD v1 §8.2.1 v1 scope).
type BuilderProfile struct {
	SchemaVersion    string `json:"schemaVersion"`
	Identity         string `json:"identity"`         // human label, e.g. gha-ubuntu-24.04-amd64-v1
	Version          string `json:"version"`          // profile revision
	HostControlClass string `json:"hostControlClass"` // operator-controlled | provider-controlled
	// Provides is the set of builders this profile supplies (ADR-07 §5). Matching is
	// CAPABILITY-based: any profile that provides the selected build's required builders
	// qualifies — the publisher pins no fixed profile identity, so an operator can
	// always author a conforming profile. Empty defaults to ["oci-hermetic"].
	Provides []string `json:"provides,omitempty"`
	Fields   []Field  `json:"fields"` // pinned host and execution properties (sorted by name for determinism)
}

// ProvidedBuilders is the profile's builder set, defaulting to oci-hermetic (the
// baseline every profile supplies) when unspecified.
func (p BuilderProfile) ProvidedBuilders() []string {
	if len(p.Provides) == 0 {
		return []string{"oci-hermetic"}
	}
	return append([]string(nil), p.Provides...)
}

// RequireBuilders fails closed (CodeBuilderNotProvided) if the profile does not
// provide every builder the selected build requires (ADR-07 §5). This is a fail-closed
// preflight condition — e.g. a build with a nix component needs a profile that
// provides nix.
func (p BuilderProfile) RequireBuilders(required []string) error {
	have := map[string]bool{}
	for _, b := range p.ProvidedBuilders() {
		have[b] = true
	}
	for _, r := range required {
		if !have[r] {
			return e.New(e.CodeBuilderNotProvided,
				"builder profile %q does not provide builder %q required by the selected build (provides: %v)", p.Identity, r, p.ProvidedBuilders())
		}
	}
	return nil
}

// Builder-identity field names that are FAIL-CLOSED by classification (ADR-07 §2,
// ADR-18 §4): they are definitionally output-affecting, so their divergence is fatal
// without an empirical demonstration.
const (
	FieldRunnerDigest             = "builder.runnerDigest"             // exact from-source nkd-exec bytes (ADR-18)
	FieldBootstrapToolchainDigest = "builder.bootstrapToolchainDigest" // exact bootstrap image manifest
	FieldRuntimeIdentity          = "builder.runtimeIdentity"          // exact runtime/engine implementation identity
	FieldNixVersion               = "builder.nixVersion"               // exact observed Nix version
	FieldNixExecutableDigest      = "builder.nixExecutableDigest"      // exact observed Nix executable bytes
	FieldSandboxMode              = "builder.sandboxMode"              // legacy/common expected sandbox mode
	FieldOCISandboxMode           = "builder.oci.sandboxMode"          // expected OCI sandbox configuration
	FieldNixSandboxMode           = "builder.nix.sandboxMode"          // expected Nix sandbox/privilege configuration
)

// Repin upserts a pinned field (used by `profile repin`), keeping fields sorted. A
// builder-identity field (runner/toolchain digest) is set hard-fail. Changing it
// changes the profile digest — a runner/tool upgrade is an environment change routed
// through profile repin, never through catalog bump (ADR-21 §5, ADR-22 §6).
func (p *BuilderProfile) Repin(name, value string, hardFail bool) {
	stage := MeasurementStage("")
	if isExecutionIdentityField(name) {
		stage = MeasurementExecution
		hardFail = true
	}
	for i := range p.Fields {
		if p.Fields[i].Name == name {
			p.Fields[i].Value = value
			p.Fields[i].HardFail = hardFail
			if stage != "" {
				p.Fields[i].MeasurementStage = stage
			}
			return
		}
	}
	p.Fields = append(p.Fields, Field{Name: name, Value: value, HardFail: hardFail, MeasurementStage: stage})
	sort.Slice(p.Fields, func(i, j int) bool { return p.Fields[i].Name < p.Fields[j].Name })
}

// canonical returns a copy with fields sorted by name, so serialization and the
// digest are independent of authoring order.
func (p BuilderProfile) canonical() BuilderProfile {
	c := p
	c.Fields = append([]Field(nil), p.Fields...)
	sort.Slice(c.Fields, func(i, j int) bool { return c.Fields[i].Name < c.Fields[j].Name })
	if len(p.Provides) > 0 {
		c.Provides = append([]string(nil), p.Provides...)
		sort.Strings(c.Provides)
	}
	return c
}

// Marshal returns the exact canonical JSON signing/authentication bytes. It emits
// no indentation or trailing newline; callers must sign and publish these bytes,
// not a display serialization.
func (p BuilderProfile) Marshal() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	b, err := canonical.Marshal(p.canonical())
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "serializing canonical builder profile %q", p.Identity)
	}
	return b, nil
}

// Digest is the authoritative profile identity: a domain-separated sha256 over the
// canonical JCS form (ADR-01), computed through the one canonical codec shared with
// the logical build identity and every other digest-addressed object. All comparison,
// preflight, and telemetry key on this, never on the Identity label (NKD v1 §8.2.1).
func (p BuilderProfile) Digest() (string, error) {
	return canonical.Digest(canonical.KindBuilderProfile, p.canonical())
}

// Validate fails closed on a structurally invalid profile.
func (p BuilderProfile) Validate() error {
	if p.SchemaVersion != SchemaVersion {
		return e.New(e.CodeInvalidCatalog, "builder profile %q: schemaVersion %q must be %q", p.Identity, p.SchemaVersion, SchemaVersion)
	}
	if strings.TrimSpace(p.Identity) == "" {
		return e.New(e.CodeInvalidCatalog, "builder profile has no identity")
	}
	if p.HostControlClass != OperatorControlled && p.HostControlClass != ProviderControlled {
		return e.New(e.CodeInvalidCatalog,
			"builder profile %q: hostControlClass %q must be %q or %q", p.Identity, p.HostControlClass, OperatorControlled, ProviderControlled)
	}
	if len(p.Fields) == 0 {
		return e.New(e.CodeInvalidCatalog, "builder profile %q declares no pinned host fields", p.Identity)
	}
	seen := map[string]bool{}
	for _, f := range p.Fields {
		if f.Name == "" {
			return e.New(e.CodeInvalidCatalog, "builder profile %q has a field with no name", p.Identity)
		}
		if seen[f.Name] {
			return e.New(e.CodeInvalidCatalog, "builder profile %q has duplicate field %q", p.Identity, f.Name)
		}
		seen[f.Name] = true
		if strings.TrimSpace(f.Value) == "" {
			return e.New(e.CodeInvalidCatalog, "builder profile %q field %q has no value", p.Identity, f.Name)
		}
		if f.MeasurementStage != "" && f.MeasurementStage != MeasurementHost && f.MeasurementStage != MeasurementExecution {
			return e.New(e.CodeInvalidCatalog, "builder profile %q field %q has unknown measurementStage %q", p.Identity, f.Name, f.MeasurementStage)
		}
		if isExecutionIdentityField(f.Name) {
			if f.MeasurementStage == MeasurementHost {
				return e.New(e.CodeInvalidCatalog, "builder profile %q execution identity %q cannot be classified as a host fact", p.Identity, f.Name)
			}
			if !f.HardFail {
				return e.New(e.CodeInvalidCatalog, "builder profile %q execution identity %q must be hard-fail", p.Identity, f.Name)
			}
			if isDigestIdentityField(f.Name) {
				if err := requireDigest(f.Value); err != nil {
					return e.New(e.CodeInvalidCatalog, "builder profile %q execution identity %q is not an exact digest: %v", p.Identity, f.Name, err)
				}
			}
		} else if f.MeasurementStage == MeasurementExecution {
			return e.New(e.CodeInvalidCatalog, "builder profile %q field %q has no execution-identity validator", p.Identity, f.Name)
		}
	}
	return nil
}

func isDigestIdentityField(name string) bool {
	switch name {
	case FieldRunnerDigest, FieldBootstrapToolchainDigest,
		FieldRuntimeIdentity, FieldNixExecutableDigest:
		return true
	default:
		return false
	}
}

func isExecutionIdentityField(name string) bool {
	switch name {
	case FieldRunnerDigest, FieldBootstrapToolchainDigest,
		FieldRuntimeIdentity, FieldNixVersion,
		FieldNixExecutableDigest, FieldSandboxMode, FieldOCISandboxMode,
		FieldNixSandboxMode:
		return true
	default:
		return false
	}
}

func (f Field) measurementStage() MeasurementStage {
	if isExecutionIdentityField(f.Name) {
		return MeasurementExecution
	}
	if f.MeasurementStage == MeasurementExecution {
		return MeasurementExecution
	}
	return MeasurementHost
}

// Divergence records one host property that did not match the profile at preflight.
type Divergence struct {
	Field    string
	Want     string
	Got      string // "<absent>" when the host did not report the property
	HardFail bool
}

// VerifyHost compares observable host facts to the profile and returns an
// unforgeable receipt required by execution-identity preflight. Execution identity
// fields are deliberately excluded even when the caller supplies matching values.
func (p BuilderProfile) VerifyHost(ctx context.Context, observed map[string]string) (HostPreflightReceipt, []Divergence, error) {
	if err := contextFailure(ctx, "builder-profile host preflight"); err != nil {
		return HostPreflightReceipt{}, nil, err
	}
	if err := p.Validate(); err != nil {
		return HostPreflightReceipt{}, nil, err
	}
	profileDigest, err := p.Digest()
	if err != nil {
		return HostPreflightReceipt{}, nil, err
	}
	var divs []Divergence
	var hard []string
	var boundObservations []boundHostObservation
	for _, f := range p.canonical().Fields {
		if f.measurementStage() != MeasurementHost {
			continue
		}
		got, ok := observed[f.Name]
		boundObservations = append(boundObservations, boundHostObservation{Field: f.Name, Value: got, Present: ok})
		if ok && got == f.Value {
			continue
		}
		d := Divergence{Field: f.Name, Want: f.Value, Got: got, HardFail: f.HardFail}
		if !ok {
			d.Got = "<absent>"
		}
		divs = append(divs, d)
		if f.HardFail {
			hard = append(hard, f.Name)
		}
	}
	if len(hard) > 0 {
		sort.Strings(hard)
		return HostPreflightReceipt{}, divs, e.New(e.CodePreflightFailed,
			"host diverges from builder profile %q on hard-fail host field(s) %s; this host cannot claim byte reproduction under this profile", p.Identity, strings.Join(hard, ", "))
	}
	if err := contextFailure(ctx, "builder-profile host preflight"); err != nil {
		return HostPreflightReceipt{}, divs, err
	}
	observationBinding, err := hostObservationBinding(boundObservations)
	if err != nil {
		return HostPreflightReceipt{}, divs, err
	}
	return HostPreflightReceipt{profileDigest: profileDigest, observationBinding: observationBinding, valid: true}, divs, nil
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"regexp"
	"slices"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

var classNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

// ResourceCapacity is the maximum capacity one execution class can allocate to
// a step. Zero means that the class does not advertise that resource; it never
// means unlimited capacity.
type ResourceCapacity struct {
	CPUMillis           uint64 `json:"cpuMillis,omitempty"`
	MemoryMiB           uint64 `json:"memoryMiB,omitempty"`
	EphemeralStorageMiB uint64 `json:"ephemeralStorageMiB,omitempty"`
}

// Class is one Environment-advertised execution route. It is a declaration used
// for deterministic planning, not proof of the host selected at runtime.
type Class struct {
	Name              string           `json:"name"`
	RunsOn            string           `json:"runsOn"`
	HostPlatform      HostPlatform     `json:"hostPlatform"`
	Resources         ResourceCapacity `json:"resources"`
	Capabilities      []CapabilityName `json:"capabilities,omitempty"`
	MaxTimeoutSeconds uint64           `json:"maxTimeoutSeconds"`
}

// CapabilityResolution records the effective exposure chosen during planning.
// In particular, a preferred capability is resolved before runtime and cannot
// opportunistically change after a job starts.
type CapabilityResolution struct {
	Name    CapabilityName `json:"name"`
	Enabled bool           `json:"enabled"`
}

// Binding is the exact execution class and capability resolution selected for
// one step. The class is copied into the executable Plan so an isolated runner
// does not have to reconstruct Environment selection from a route label.
type Binding struct {
	Class        Class                  `json:"class"`
	Capabilities []CapabilityResolution `json:"capabilities,omitempty"`
}

// MatchOptions carries compiler-owned hard constraints which cannot be inferred
// from a generic Requirements value. VM lowering uses RequiredHostPlatform for
// required-KVM guest/host affinity; ordinary builders leave it empty.
type MatchOptions struct {
	RequiredHostPlatform HostPlatform
}

// ValidateClasses validates canonical Environment authoring order and each class.
func ValidateClasses(classes []Class) error {
	if classes != nil && len(classes) == 0 {
		return e.New(e.CodeInvalidCatalog,
			"executionClasses must not be empty when declared")
	}
	previous := ""
	routes := make(map[string]string, len(classes))
	for _, class := range classes {
		if err := class.Validate(); err != nil {
			return err
		}
		if previous != "" && class.Name <= previous {
			return e.New(e.CodeInvalidCatalog,
				"execution classes must be sorted by unique name")
		}
		if owner, duplicate := routes[class.RunsOn]; duplicate {
			return e.New(e.CodeInvalidCatalog,
				"execution classes %q and %q declare the same runsOn route %q",
				owner, class.Name, class.RunsOn)
		}
		routes[class.RunsOn] = class.Name
		previous = class.Name
	}
	return nil
}

// Validate checks one execution-class declaration. It deliberately does not
// claim that a provider route actually supplies these values.
func (class Class) Validate() error {
	if !classNamePattern.MatchString(class.Name) {
		return e.New(e.CodeInvalidCatalog,
			"execution class name %q is not canonical", class.Name)
	}
	if strings.TrimSpace(class.RunsOn) == "" || class.RunsOn != strings.TrimSpace(class.RunsOn) ||
		strings.ContainsAny(class.RunsOn, "\r\n\x00") {
		return e.New(e.CodeInvalidCatalog,
			"execution class %q requires a safe nonempty runsOn route", class.Name)
	}
	if !supportedHostPlatform(class.HostPlatform) {
		return e.New(e.CodeInvalidCatalog,
			"execution class %q has unsupported host platform %q", class.Name, class.HostPlatform)
	}
	if class.MaxTimeoutSeconds == 0 {
		return e.New(e.CodeInvalidCatalog,
			"execution class %q requires positive maxTimeoutSeconds", class.Name)
	}
	if class.MaxTimeoutSeconds > maxTimeoutSeconds {
		return e.New(e.CodeInvalidCatalog,
			"execution class %q maxTimeoutSeconds exceeds the supported duration", class.Name)
	}
	previous := CapabilityName("")
	for _, capability := range class.Capabilities {
		if capability != CapabilityKVM {
			return e.New(e.CodeInvalidCatalog,
				"execution class %q exposes unsupported capability %q", class.Name, capability)
		}
		if previous != "" && capability <= previous {
			return e.New(e.CodeInvalidCatalog,
				"execution class %q capabilities must be sorted and duplicate-free", class.Name)
		}
		previous = capability
	}
	return nil
}

// Match selects one exact satisfying class. Preferred capabilities are scored
// first; equal scores are resolved by canonical class name, independent of input
// iteration order.
func Match(requirements *Requirements, classes []Class, options MatchOptions) (*Binding, error) {
	if requirements == nil {
		return nil, nil
	}
	if err := requirements.Validate(); err != nil {
		return nil, err
	}
	if err := ValidateClasses(classes); err != nil {
		return nil, err
	}
	if options.RequiredHostPlatform != "" && !supportedHostPlatform(options.RequiredHostPlatform) {
		return nil, e.New(e.CodeInvalidCatalog,
			"required execution host platform %q is unsupported", options.RequiredHostPlatform)
	}

	type candidate struct {
		class Class
		score int
	}
	candidates := make([]candidate, 0, len(classes))
	for _, class := range classes {
		if !classSatisfies(requirements, class, options) {
			continue
		}
		score := 0
		for _, requested := range requirements.Capabilities {
			if requested.Policy == CapabilityPreferred && exposes(class, requested.Name) {
				score++
			}
		}
		candidates = append(candidates, candidate{class: class, score: score})
	}
	if len(candidates) == 0 {
		return nil, e.New(e.CodeEngineCapability,
			"no execution class satisfies the requested platform, resources, capabilities, and timeout")
	}
	slices.SortFunc(candidates, func(left, right candidate) int {
		if left.score != right.score {
			return right.score - left.score
		}
		return strings.Compare(left.class.Name, right.class.Name)
	})

	selected := cloneClass(candidates[0].class)
	binding := &Binding{Class: selected}
	for _, requested := range requirements.Capabilities {
		binding.Capabilities = append(binding.Capabilities, CapabilityResolution{
			Name: requested.Name, Enabled: exposes(selected, requested.Name),
		})
	}
	return binding, nil
}

// ValidateFor checks that a serialized binding is internally consistent with its
// embedded class and request. Deterministic selection and Environment membership
// are checked by the Plan owner, which has the complete class inventory.
func (binding *Binding) ValidateFor(requirements *Requirements) error {
	if binding == nil {
		if requirements != nil {
			return e.New(e.CodeEngineCapability,
				"execution requirements have no selected execution class")
		}
		return nil
	}
	if requirements == nil {
		return e.New(e.CodeInvalidCatalog,
			"execution binding is present without execution requirements")
	}
	if err := requirements.Validate(); err != nil {
		return err
	}
	if err := binding.Class.Validate(); err != nil {
		return err
	}
	if len(binding.Capabilities) != len(requirements.Capabilities) {
		return e.New(e.CodeInvalidCatalog,
			"execution binding capability resolution is incomplete")
	}
	for index, requested := range requirements.Capabilities {
		resolved := binding.Capabilities[index]
		if resolved.Name != requested.Name || resolved.Enabled != exposes(binding.Class, requested.Name) {
			return e.New(e.CodeInvalidCatalog,
				"execution binding capability %q does not match its selected class", requested.Name)
		}
		switch requested.Policy {
		case CapabilityRequired:
			if !resolved.Enabled {
				return e.New(e.CodeEngineCapability,
					"execution binding does not expose required capability %q", requested.Name)
			}
		case CapabilityForbidden:
			if resolved.Enabled {
				return e.New(e.CodeEngineCapability,
					"execution binding exposes forbidden capability %q", requested.Name)
			}
		}
	}
	if !classSatisfies(requirements, binding.Class, MatchOptions{}) {
		return e.New(e.CodeEngineCapability,
			"selected execution class %q no longer satisfies its request", binding.Class.Name)
	}
	return nil
}

// CloneBinding returns a caller-owned deep copy.
func CloneBinding(binding *Binding) *Binding {
	if binding == nil {
		return nil
	}
	return &Binding{
		Class:        cloneClass(binding.Class),
		Capabilities: slices.Clone(binding.Capabilities),
	}
}

// CloneClasses returns a caller-owned deep copy of an execution-class inventory.
func CloneClasses(classes []Class) []Class {
	if classes == nil {
		return nil
	}
	cloned := make([]Class, len(classes))
	for index := range classes {
		cloned[index] = cloneClass(classes[index])
	}
	return cloned
}

// EqualBinding compares the complete selected execution class and resolution.
func EqualBinding(left, right *Binding) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return equalClass(left.Class, right.Class) && slices.Equal(left.Capabilities, right.Capabilities)
}

func classSatisfies(requirements *Requirements, class Class, options MatchOptions) bool {
	if options.RequiredHostPlatform != "" && class.HostPlatform != options.RequiredHostPlatform {
		return false
	}
	if len(requirements.AllowedHostPlatforms) > 0 &&
		!slices.Contains(requirements.AllowedHostPlatforms, class.HostPlatform) {
		return false
	}
	if request := requirements.Resources; request != nil &&
		(class.Resources.CPUMillis < request.CPUMillis ||
			class.Resources.MemoryMiB < request.MemoryMiB ||
			class.Resources.EphemeralStorageMiB < request.EphemeralStorageMiB) {
		return false
	}
	if requirements.TimeoutSeconds > class.MaxTimeoutSeconds {
		return false
	}
	for _, requested := range requirements.Capabilities {
		exposed := exposes(class, requested.Name)
		if requested.Policy == CapabilityRequired && !exposed ||
			requested.Policy == CapabilityForbidden && exposed {
			return false
		}
	}
	return true
}

func exposes(class Class, capability CapabilityName) bool {
	return slices.Contains(class.Capabilities, capability)
}

func supportedHostPlatform(platform HostPlatform) bool {
	return platform == HostPlatformLinuxAMD64 || platform == HostPlatformLinuxARM64
}

func cloneClass(class Class) Class {
	class.Capabilities = slices.Clone(class.Capabilities)
	return class
}

func equalClass(left, right Class) bool {
	return left.Name == right.Name &&
		left.RunsOn == right.RunsOn &&
		left.HostPlatform == right.HostPlatform &&
		left.Resources == right.Resources &&
		slices.Equal(left.Capabilities, right.Capabilities) &&
		left.MaxTimeoutSeconds == right.MaxTimeoutSeconds
}

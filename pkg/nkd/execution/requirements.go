// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package execution owns engine-neutral requirements for an executable NKD step.
// Requirements describe minimum runner capacity; they are not proof that a selected
// runner provides that capacity and they are not observations of a completed Run.
package execution

import (
	"slices"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// maxTimeoutSeconds is the largest whole-second timeout that a future runtime
// adapter can safely convert to a time.Duration after validation succeeds.
const maxTimeoutSeconds uint64 = (1<<63 - 1) / 1_000_000_000

// HostPlatform is the execution host operating system and architecture. It is
// deliberately distinct from an IR node's target/toolchain platform.
type HostPlatform string

const (
	HostPlatformLinuxAMD64 HostPlatform = "linux/amd64"
	HostPlatformLinuxARM64 HostPlatform = "linux/arm64"
)

// CapabilityName identifies a typed facility that an executor can expose to or
// withhold from a step. Unknown names fail closed until their semantics are added
// to this package and the Environment matcher.
type CapabilityName string

const (
	CapabilityKVM CapabilityName = "kvm"
)

// CapabilityPolicy states how the selected execution class must handle a typed
// capability. Preferred is not opportunistic runtime fallback: the Plan must bind
// the selected mode before work begins.
type CapabilityPolicy string

const (
	CapabilityRequired  CapabilityPolicy = "required"
	CapabilityPreferred CapabilityPolicy = "preferred"
	CapabilityForbidden CapabilityPolicy = "forbidden"
)

// CapabilityRequirement is one sorted, unique typed host-capability request.
type CapabilityRequirement struct {
	Name   CapabilityName   `json:"name"`
	Policy CapabilityPolicy `json:"policy"`
}

// ResourceRequest is the minimum allocatable host capacity requested by one step.
// Zero means that the producer does not constrain that resource. A present request
// must constrain at least one resource. Adapters must never clamp or silently drop a
// requested value.
type ResourceRequest struct {
	CPUMillis           uint64 `json:"cpuMillis,omitempty"`
	MemoryMiB           uint64 `json:"memoryMiB,omitempty"`
	EphemeralStorageMiB uint64 `json:"ephemeralStorageMiB,omitempty"`
}

// Requirements is the reusable per-step execution contract. The currently admitted
// authoring path is a catalog build producer projected onto build IR and Plan nodes.
// It describes requested capacity, eligible host platforms, typed host capabilities,
// and the executor-owned wall-time budget. Other step kinds belong here only after
// their compiler projection, Environment admission, and runtime-observation contracts
// are defined.
type Requirements struct {
	Resources            *ResourceRequest        `json:"resources,omitempty"`
	AllowedHostPlatforms []HostPlatform          `json:"allowedHostPlatforms,omitempty"`
	Capabilities         []CapabilityRequirement `json:"capabilities,omitempty"`
	TimeoutSeconds       uint64                  `json:"timeoutSeconds,omitempty"`
}

// Validate checks only the request shape. Deterministic execution-class selection is
// compiler-owned; runtime realization, capability proof, and enforcement remain
// separate adapter responsibilities.
func (requirements *Requirements) Validate() error {
	if requirements == nil {
		return nil
	}
	hasConstraint := false
	if requirements.Resources != nil {
		hasConstraint = true
		resources := requirements.Resources
		if resources.CPUMillis == 0 && resources.MemoryMiB == 0 && resources.EphemeralStorageMiB == 0 {
			return e.New(e.CodeInvalidCatalog,
				"execution resource request must constrain CPU, memory, or ephemeral storage")
		}
	}

	if requirements.AllowedHostPlatforms != nil {
		if len(requirements.AllowedHostPlatforms) == 0 {
			return e.New(e.CodeInvalidCatalog,
				"execution allowedHostPlatforms must not be empty when declared")
		}
		hasConstraint = true
		previous := HostPlatform("")
		for _, platform := range requirements.AllowedHostPlatforms {
			switch platform {
			case HostPlatformLinuxAMD64, HostPlatformLinuxARM64:
			default:
				return e.New(e.CodeInvalidCatalog,
					"execution host platform %q is unsupported", platform)
			}
			if previous != "" && platform <= previous {
				return e.New(e.CodeInvalidCatalog,
					"execution allowedHostPlatforms must be sorted and duplicate-free")
			}
			previous = platform
		}
	}

	if requirements.Capabilities != nil {
		if len(requirements.Capabilities) == 0 {
			return e.New(e.CodeInvalidCatalog,
				"execution capabilities must not be empty when declared")
		}
		hasConstraint = true
		previous := CapabilityName("")
		for _, capability := range requirements.Capabilities {
			if capability.Name != CapabilityKVM {
				return e.New(e.CodeInvalidCatalog,
					"execution capability %q is unsupported", capability.Name)
			}
			switch capability.Policy {
			case CapabilityRequired, CapabilityPreferred, CapabilityForbidden:
			default:
				return e.New(e.CodeInvalidCatalog,
					"execution capability %q has unsupported policy %q", capability.Name, capability.Policy)
			}
			if previous != "" && capability.Name <= previous {
				return e.New(e.CodeInvalidCatalog,
					"execution capabilities must be sorted by unique name")
			}
			previous = capability.Name
		}
	}

	if requirements.TimeoutSeconds > 0 {
		if requirements.TimeoutSeconds > maxTimeoutSeconds {
			return e.New(e.CodeInvalidCatalog,
				"execution timeoutSeconds exceeds the supported duration")
		}
		hasConstraint = true
	}
	if !hasConstraint {
		return e.New(e.CodeInvalidCatalog,
			"execution requirements must declare at least one supported constraint")
	}
	return nil
}

// Clone returns a caller-owned deep copy.
func Clone(requirements *Requirements) *Requirements {
	if requirements == nil {
		return nil
	}
	cloned := *requirements
	if requirements.Resources != nil {
		resources := *requirements.Resources
		cloned.Resources = &resources
	}
	cloned.AllowedHostPlatforms = slices.Clone(requirements.AllowedHostPlatforms)
	cloned.Capabilities = slices.Clone(requirements.Capabilities)
	return &cloned
}

// Equal compares exact declared requirements. It does not perform capacity
// satisfaction; Match owns greater-than-or-equal class selection.
func Equal(left, right *Requirements) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if (left.Resources == nil) != (right.Resources == nil) {
		return false
	}
	if left.Resources != nil && *left.Resources != *right.Resources {
		return false
	}
	return slices.Equal(left.AllowedHostPlatforms, right.AllowedHostPlatforms) &&
		slices.Equal(left.Capabilities, right.Capabilities) &&
		left.TimeoutSeconds == right.TimeoutSeconds
}

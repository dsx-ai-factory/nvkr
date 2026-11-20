// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

const BootSelectionSchemaVersion = "nvkr.dev/boot-selection/v1"

// BootSelection describes the required transport subset, independently of the
// complete Definition membership. It grants no artifact or compatibility trust:
// lifecycle must authenticate each exact output, member, platform and profile.
type BootSelection struct {
	SchemaVersion string       `json:"schemaVersion"`
	Disks         []BootDisk   `json:"disks"`
	Imagers       []BootImager `json:"imagers"`
}

type BootDisk struct {
	Output   string `json:"output"`
	Platform string `json:"platform"`
}

// BootImager names one regular EFI member in an original signed Binary Bundle.
// Profile is an exact expected producer compatibility claim, not a filename or
// an inferred host default. A matching claim does not prove a boot exercise.
type BootImager struct {
	Output   string `json:"output"`
	Member   string `json:"member"`
	Platform string `json:"platform"`
	Profile  string `json:"profile"`
}

func (selection BootSelection) Clone() BootSelection {
	selection.Disks = slices.Clone(selection.Disks)
	selection.Imagers = slices.Clone(selection.Imagers)
	return selection
}

func (imager BootImager) Validate() error {
	if err := target.ValidatePlatform(imager.Platform); err != nil {
		return err
	}
	if !namePattern.MatchString(imager.Output) || len(imager.Output) > 128 ||
		!namePattern.MatchString(imager.Member) || len(imager.Member) > 128 || strings.ContainsAny(imager.Member, "/\\") ||
		!namePattern.MatchString(imager.Profile) || len(imager.Profile) > 128 {
		return e.New(e.CodeInvalidCatalog, "boot imager requires an exact output, regular member and explicit profile")
	}
	return nil
}

// Validate admits a bounded, exact selection without I/O. Definition membership
// and signed raw-file/profile bindings are checked by their respective owners.
func (selection BootSelection) Validate() error {
	if selection.SchemaVersion != BootSelectionSchemaVersion {
		return e.New(e.CodeEngineCapability, "unsupported boot-selection schema %q", selection.SchemaVersion)
	}
	if len(selection.Disks) == 0 || len(selection.Disks) > 1024 || len(selection.Imagers) == 0 || len(selection.Imagers) > len(target.Arches()) {
		return e.New(e.CodeInvalidCatalog, "boot selection requires one to 1024 disks and one imager per selected architecture")
	}
	platforms := map[string]bool{}
	names := map[string]bool{}
	previous := ""
	for _, disk := range selection.Disks {
		if !namePattern.MatchString(disk.Output) || len(disk.Output) > 128 || disk.Output <= previous {
			return e.New(e.CodeInvalidCatalog, "boot disk outputs must have sorted unique canonical names")
		}
		if err := target.ValidatePlatform(disk.Platform); err != nil {
			return err
		}
		previous = disk.Output
		names[disk.Output] = true
		platforms[disk.Platform] = true
	}
	previous = ""
	for _, imager := range selection.Imagers {
		if err := imager.Validate(); err != nil {
			return err
		}
		if imager.Platform <= previous || !platforms[imager.Platform] {
			return e.New(e.CodeInvalidCatalog, "boot imagers must uniquely cover exactly the selected platforms in sorted order")
		}
		if names[imager.Output] {
			return e.New(e.CodeInvalidCatalog, "boot imager requires a distinct named output")
		}
		previous = imager.Platform
		names[imager.Output] = true
	}
	if len(selection.Imagers) != len(platforms) {
		return e.New(e.CodeInvalidCatalog, "boot selection omits an architecture-matched imager")
	}
	return nil
}

// ValidateOutputs joins the selection to the compiler's complete named output
// requirements. The caller still validates the Definition and producer graph;
// this check does not observe bytes or authenticate a producer's profile claim.
func (selection BootSelection) ValidateOutputs(outputs []DefinitionOutput) error {
	if err := selection.Validate(); err != nil {
		return err
	}
	byName := make(map[string]DefinitionOutput, len(outputs))
	for _, output := range outputs {
		if _, duplicate := byName[output.Name]; duplicate {
			return e.New(e.CodeInvalidCatalog, "boot selection cannot join ambiguous Definition output %q", output.Name)
		}
		byName[output.Name] = output
	}
	check := func(name, platform, format string) error {
		output, exists := byName[name]
		if !exists || output.Format != format || output.Platform != platform || len(output.Platforms) != 0 {
			return e.New(e.CodeInvalidCatalog, "boot output %q must name an exact %s output for %s", name, format, platform)
		}
		return nil
	}
	for _, disk := range selection.Disks {
		if err := check(disk.Output, disk.Platform, "disk-image"); err != nil {
			return err
		}
	}
	for _, imager := range selection.Imagers {
		if err := check(imager.Output, imager.Platform, "executable"); err != nil {
			return err
		}
		// The executable packager names each regular bundle member after its
		// selected component, independently of the producer's workspace filename.
		if !slices.Contains(byName[imager.Output].Components, imager.Member) {
			return e.New(e.CodeInvalidCatalog, "boot imager member %q is not selected by output %q", imager.Member, imager.Output)
		}
	}
	return nil
}

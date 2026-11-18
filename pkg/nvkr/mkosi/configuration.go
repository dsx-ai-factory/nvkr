// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mkosi

import (
	"maps"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// validateConfiguration admits the narrow runtime recipe, not arbitrary mkosi
// project discovery. Host paths, scripts, includes, image downloads and package
// selections must not enter through an untracked configuration side channel.
// The executor supplies root-specific values and these invariant defaults
// explicitly; omission here cannot restore mkosi's ambient defaults.
func validateConfiguration(configuration string) error {
	allowed := map[string]string{
		"Output/Format": "oci", "Output/CompressOutput": "no", "Output/SplitArtifacts": "", "Output/ManifestFormat": "json",
		"Content/Bootable": "no", "Content/SourceDateEpoch": "0", "Content/WithDocs": "yes", "Content/WithRecommends": "no",
		"Content/CleanPackageMetadata": "no", "Content/RemoveFiles": "/var/cache/* /var/log/*",
		"Build/BuildSources": "", "Build/Incremental": "no", "Build/CacheKey": "nvkr-vm-runtime",
	}
	sections, settings := map[string]bool{}, map[string]string{}
	section, previous := "", ""
	for _, line := range strings.Split(configuration, "\n") {
		value := strings.TrimSpace(line)
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		if strings.HasPrefix(value, "[") {
			if value != "[Output]" && value != "[Content]" && value != "[Build]" {
				return e.New(e.CodeInvalidCatalog, "mkosi configuration has an unsupported section")
			}
			section = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
			if sections[section] {
				return e.New(e.CodeInvalidCatalog, "mkosi configuration repeats a section")
			}
			sections[section], previous = true, ""
			continue
		}
		key, setting, assignment := strings.Cut(value, "=")
		if !assignment {
			if previous != "Content/RemoveFiles" || (line[0] != ' ' && line[0] != '\t') {
				return e.New(e.CodeInvalidCatalog, "mkosi configuration has an unsupported continuation")
			}
			settings[previous] += " " + value
			continue
		}
		key = section + "/" + strings.TrimSpace(key)
		if _, ok := allowed[key]; !ok {
			return e.New(e.CodeInvalidCatalog, "mkosi configuration setting %q is not admitted", key)
		}
		if _, repeated := settings[key]; repeated {
			return e.New(e.CodeInvalidCatalog, "mkosi configuration repeats setting %q", key)
		}
		settings[key], previous = strings.TrimSpace(setting), key
	}
	if settings["Output/Format"] != "oci" {
		return e.New(e.CodeInvalidCatalog, "mkosi runtime configuration requires OCI output")
	}
	for _, key := range slices.Sorted(maps.Keys(settings)) {
		value := settings[key]
		if strings.Join(strings.Fields(value), " ") != allowed[key] {
			return e.New(e.CodeInvalidCatalog, "mkosi configuration setting %q changes the admitted runtime recipe", key)
		}
	}
	return nil
}

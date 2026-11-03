// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Pinned-tool output forms (ADR-27 §5). They are valid only for builderType
// pinned-tool; no other builder can declare them.
const (
	PinnedToolOutputFile      = "file"
	PinnedToolOutputOCILayout = "oci-layout"
)

const (
	maxPinnedToolArgs     = 256
	maxPinnedToolArgBytes = 4096
	maxPinnedToolEnv      = 64
)

var (
	// pinnedToolExecutorEnv names are fixed by the executor (ADR-27 §3); a literal
	// producer variable cannot redefine them.
	pinnedToolExecutorEnv = map[string]bool{"HOME": true, "TMPDIR": true, "TZ": true, "LC_ALL": true}
	pinnedToolEnvName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// A token consumes a quoted string whole, so a binding may contain "}}".
	pinnedToolToken    = regexp.MustCompile(`\{\{(?:[^"]|"(?:[^"\\]|\\.)*")*?\}\}`)
	pinnedToolMaterial = regexp.MustCompile(`^\{\{\.Material ("(?:[^"\\]|\\.)*")\}\}$`)
)

// ToolArgValues supplies the sandbox values a pinned-tool argument vector's typed
// tokens resolve to. Material returns the staged path of a declared binding.
type ToolArgValues struct {
	Output   string
	Arch     string
	Material func(binding string) (string, error)
}

// RenderToolArgs is the single token grammar for pinned-tool arguments (ADR-27 §1):
// {{.Output}}, {{.Arch}} and {{.Material "<binding>"}}. Any other template syntax
// fails closed with invalid-catalog. The returned vector contains only the supplied
// values, so the compiler controls every path it can name.
func RenderToolArgs(args []string, values ToolArgValues) ([]string, error) {
	rendered := make([]string, 0, len(args))
	for index, argument := range args {
		var renderErr error
		out := pinnedToolToken.ReplaceAllStringFunc(argument, func(token string) string {
			if renderErr != nil {
				return ""
			}
			switch token {
			case "{{.Output}}":
				return values.Output
			case "{{.Arch}}":
				return values.Arch
			}
			match := pinnedToolMaterial.FindStringSubmatch(token)
			if match == nil {
				renderErr = e.New(e.CodeInvalidCatalog, "toolArgs[%d] uses unknown template %q", index, token)
				return ""
			}
			binding, err := strconv.Unquote(match[1])
			if err != nil {
				renderErr = e.Wrap(e.CodeInvalidCatalog, err, "toolArgs[%d] material binding %s", index, match[1])
				return ""
			}
			path, err := values.Material(binding)
			if err != nil {
				renderErr = err
				return ""
			}
			return path
		})
		if renderErr != nil {
			return nil, renderErr
		}
		if strings.Contains(pinnedToolToken.ReplaceAllString(argument, ""), "{{") ||
			strings.Contains(pinnedToolToken.ReplaceAllString(argument, ""), "}}") {
			return nil, e.New(e.CodeInvalidCatalog, "toolArgs[%d] has unbalanced template delimiters", index)
		}
		rendered = append(rendered, out)
	}
	return rendered, nil
}

// resolvePinnedTool binds a pinned-tool producer to its named source's tool pin. The
// component has no source fetch: the tool source supplies only the executable.
func resolvePinnedTool(name string, raw rawComponent, sources map[string]Source, lock *PinLock, c Component) (Component, error) {
	if raw.FromSource || raw.Source != "" || raw.Prebuilt != nil || len(raw.VersionStamp) != 0 || len(raw.Patches) != 0 {
		return c, e.New(e.CodeInvalidCatalog,
			"pinned-tool component %q must not declare fromSource, source, prebuilt, versionStamp or patches", name)
	}
	if raw.Tool == "" {
		return c, e.New(e.CodeInvalidCatalog, "pinned-tool component %q names no tool source", name)
	}
	src, ok := sources[raw.Tool]
	if !ok {
		return c, e.New(e.CodeInvalidCatalog, "pinned-tool component %q references unknown tool source %q", name, raw.Tool)
	}
	if src.Tool == nil {
		return c, e.New(e.CodeUnpinnedSource, "pinned-tool component %q: source %q has no tool declaration", name, raw.Tool)
	}
	pin, ok := lock.Pins[raw.Tool]
	if !ok || pin.Tool == nil {
		return c, e.New(e.CodeUnpinnedSource,
			"pinned-tool component %q: tool source %q has no lock record (run: nvkr catalog pin %s)", name, raw.Tool, raw.Tool)
	}
	if err := checkToolPin(src.Tool, pin); err != nil {
		return c, e.Wrap(e.CodeUnpinnedSource, err, "pinned-tool component %q tool %q", name, raw.Tool)
	}
	tool := *pin.Tool
	c.Tool = raw.Tool
	c.ToolExecutable = &tool
	c.ToolArgs = slices.Clone(raw.ToolArgs)
	c.ToolEnv = cloneStringMap(raw.ToolEnv)
	c.ToolchainImage = raw.ToolchainImage
	c.Tag = pin.Tag
	return c, nil
}

// validatePinnedTool admits a resolved pinned-tool producer (ADR-27 §1, §3–§5, §11).
func validatePinnedTool(name string, c Component) error {
	if c.FromSource || c.Prebuilt != nil || strings.TrimSpace(c.ExclusionReason) != "" {
		return e.New(e.CodeInvalidCatalog, "pinned-tool component %q must not declare another lane", name)
	}
	if c.BuildNetwork != "" {
		return e.New(e.CodeInvalidCatalog, "pinned-tool component %q may not request build network", name)
	}
	if c.Embeds != nil || c.Helm != nil || c.Flake != nil || c.CGO || c.Disk != nil || c.Packer != nil ||
		len(c.BuildCommand) != 0 || len(c.BuildCommandByPlatform) != 0 || c.WriteDir != "" ||
		len(c.VendorCommand) != 0 || len(c.Patches) != 0 || len(c.VersionStamp) != 0 || len(c.ToolchainImages) != 0 ||
		c.Repo != "" || c.Commit != "" || c.TreeHash != "" || c.SourceDate != 0 {
		return e.New(e.CodeInvalidCatalog,
			"pinned-tool component %q may declare only tool, toolArgs, toolEnv, materials, toolchainImage, platforms, outputType and artifactPath", name)
	}
	if c.BuildRequirements != nil {
		if err := c.BuildRequirements.Validate(); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "pinned-tool component %q has invalid buildRequirements", name)
		}
	}
	if c.Tool == "" || c.ToolExecutable == nil {
		return e.New(e.CodeUnpinnedSource, "pinned-tool component %q has no resolved tool pin", name)
	}
	if err := validateToolPin(*c.ToolExecutable); err != nil {
		return e.Wrap(e.CodeUnpinnedSource, err, "pinned-tool component %q tool %q", name, c.Tool)
	}
	toolPlatform := c.ToolExecutable.Platform
	if !strings.HasPrefix(toolPlatform, "linux/") || len(c.Platforms) != 1 || c.Platforms[0] != toolPlatform {
		return e.New(e.CodeEngineCapability,
			"pinned-tool component %q: tool platform %q must equal its single target platform %v", name, toolPlatform, c.Platforms)
	}
	if c.ToolchainImage == "" {
		return e.New(e.CodeTagToolchain, "pinned-tool component %q requires a digest-pinned toolchainImage", name)
	}
	if err := checkOCIRefScheme(name, "toolchain", c.ToolchainImage); err != nil {
		return err
	}
	if !digestPinned.MatchString(c.ToolchainImage) || !strings.Contains(c.ToolchainImage, "@sha256:") {
		return e.New(e.CodeTagToolchain, "pinned-tool component %q toolchain %q is not pinned by name@sha256: digest", name, c.ToolchainImage)
	}
	switch c.OutputType {
	case PinnedToolOutputFile:
		if !validToolOutputName(c.ArtifactPath) {
			return e.New(e.CodeInvalidCatalog,
				"pinned-tool component %q: file output requires artifactPath naming one file in the output directory", name)
		}
	case PinnedToolOutputOCILayout:
		if c.ArtifactPath != "" {
			return e.New(e.CodeInvalidCatalog, "pinned-tool component %q: oci-layout output has no artifactPath", name)
		}
	default:
		return e.New(e.CodeInvalidCatalog, "pinned-tool component %q: outputType %q is not file or oci-layout", name, c.OutputType)
	}
	if err := validateToolEnv(name, c.ToolEnv); err != nil {
		return err
	}
	if err := validateMaterialBindings(name, c); err != nil {
		return err
	}
	return validateToolArgs(name, c.ToolArgs, c.Materials)
}

func validToolOutputName(name string) bool {
	return safeToolPath(name) && !strings.Contains(name, "/") && len(name) <= 255
}

func validateToolEnv(name string, env map[string]string) error {
	if len(env) > maxPinnedToolEnv {
		return e.New(e.CodeInvalidCatalog, "pinned-tool component %q declares too many toolEnv variables", name)
	}
	for _, key := range sortedStringKeys(env) {
		value := env[key]
		if !pinnedToolEnvName.MatchString(key) {
			return e.New(e.CodeInvalidCatalog, "pinned-tool component %q: invalid toolEnv name %q", name, key)
		}
		if pinnedToolExecutorEnv[key] {
			return e.New(e.CodeInvalidCatalog, "pinned-tool component %q: toolEnv may not redefine executor-owned %s", name, key)
		}
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || len(value) > maxPinnedToolArgBytes ||
			strings.Contains(value, "{{") {
			return e.New(e.CodeInvalidCatalog, "pinned-tool component %q: toolEnv %s must be a bounded literal value", name, key)
		}
	}
	return nil
}

// validateToolArgs checks the token grammar with sentinel values and requires every
// material binding to be named by at least one token, and every token to name one.
func validateToolArgs(name string, args []string, materials map[string]string) error {
	if len(args) > maxPinnedToolArgs {
		return e.New(e.CodeInvalidCatalog, "pinned-tool component %q declares too many toolArgs", name)
	}
	for index, argument := range args {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, '\x00') || len(argument) > maxPinnedToolArgBytes {
			return e.New(e.CodeInvalidCatalog, "pinned-tool component %q: toolArgs[%d] is not bounded UTF-8 text", name, index)
		}
	}
	used := make(map[string]bool, len(materials))
	_, err := RenderToolArgs(args, ToolArgValues{Output: "/output", Arch: "arch", Material: func(binding string) (string, error) {
		if _, ok := materials[binding]; !ok {
			return "", e.New(e.CodeInvalidCatalog, "toolArgs name undeclared material %q", binding)
		}
		used[binding] = true
		return "/material", nil
	}})
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "pinned-tool component %q", name)
	}
	for _, binding := range sortedStringKeys(materials) {
		if !used[binding] {
			return e.New(e.CodeInvalidCatalog, "pinned-tool component %q: material %q has no toolArgs token", name, binding)
		}
	}
	return nil
}

// pinnedToolMaterialForm reports whether a component's output is one regular file
// that a pinned-tool producer may stage as a material (ADR-27 §4).
func pinnedToolMaterialForm(c Component) bool {
	switch c.OutputType {
	case PinnedToolOutputFile, "raw", "tarball", "host-package", "disk-image":
		return true
	default:
		return false
	}
}

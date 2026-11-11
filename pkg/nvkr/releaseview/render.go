// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package releaseview presents checked publication descriptions. These views
// carry no authority and cannot authorize execution, publication or consumption.
package releaseview

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

// Options selects a presentation and an optional output subset. Output, Kind and
// Platform intersect: an output is shown only when it satisfies every one given.
type Options struct {
	Format   string
	Output   string
	Kind     string
	Platform string
}

// Kinds lists the exact output kinds accepted by a kind filter, in sorted order.
func Kinds() []string {
	kinds := []string{definition.OutputExecutable, definition.OutputOCI, definition.OutputAPT, definition.OutputHelm,
		definition.OutputBundle, definition.OutputDiskImage, definition.OutputDebianPackage}
	slices.Sort(kinds)
	return kinds
}

// Validate rejects unsupported presentation before a CLI starts acquisition.
func (options Options) Validate() error {
	switch options.Format {
	case "", "text", "json", "mermaid":
	default:
		return e.New(e.CodeUsage, "release view format must be text, json or mermaid")
	}
	if strings.IndexFunc(options.Output, unicode.IsControl) >= 0 {
		return e.New(e.CodeUsage, "release output selector contains control characters")
	}
	if options.Kind != "" && !slices.Contains(Kinds(), options.Kind) {
		return e.New(e.CodeUsage, "release kind filter must be one of %s", strings.Join(Kinds(), ", "))
	}
	if options.Platform != "" && target.ValidateExecutablePlatform(options.Platform) != nil {
		return e.New(e.CodeUsage, "release platform filter must be linux/amd64, linux/arm64 or darwin/arm64")
	}
	return nil
}

func (options Options) filtered() bool {
	return options.Output != "" || options.Kind != "" || options.Platform != ""
}

// matches reports whether an output satisfies every requested filter. A native
// output supports its recorded platform; an aggregate image supports each child
// platform it contains. Missing platform metadata is unknown and never matches.
func (options Options) matches(output delivery.PublicationOutput) bool {
	if options.Output != "" && output.Name != options.Output {
		return false
	}
	if options.Kind != "" && output.Kind != options.Kind {
		return false
	}
	if options.Platform == "" {
		return true
	}
	if output.ImageIndex != nil {
		return slices.ContainsFunc(output.ImageIndex.Children, func(child delivery.ImageIndexChildBinding) bool {
			return child.Platform == options.Platform
		})
	}
	return output.OS != "" && output.OS+"/"+output.Arch == options.Platform
}

type view struct {
	Inspection       string `json:"inspection"`
	SelectedOutput   string `json:"selectedOutput,omitempty"`
	SelectedKind     string `json:"selectedKind,omitempty"`
	SelectedPlatform string `json:"selectedPlatform,omitempty"`
	TotalOutputs     int    `json:"totalOutputs"`
	delivery.PublicationDescription
}

// filtered reports whether any selection narrowed the view.
func (selected view) filtered() bool {
	return selected.SelectedOutput != "" || selected.SelectedKind != "" || selected.SelectedPlatform != ""
}

// scope names the counts and active filters of a narrowed view, so a partial
// diagram cannot pass for the whole Release. An unfiltered view has none.
func (selected view) scope() string {
	if !selected.filtered() {
		return ""
	}
	parts := []string{fmt.Sprintf("outputs %d of %d", len(selected.Outputs), selected.TotalOutputs)}
	if selected.SelectedOutput != "" {
		parts = append(parts, "output "+selected.SelectedOutput)
	}
	if selected.SelectedKind != "" {
		parts = append(parts, "kind "+selected.SelectedKind)
	}
	if selected.SelectedPlatform != "" {
		parts = append(parts, "platform "+selected.SelectedPlatform)
	}
	return strings.Join(parts, " | ")
}

// Render selects a presentation only after the owner checked all metadata. Its
// inspection labels describe that prerequisite; rendering does not authenticate
// caller-composed values. JSON follows DescriptionSchemaVersion and is readable
// presentation, not canonical identity input or a receipt.
func Render(description delivery.PublicationDescription, options Options) ([]byte, error) {
	selected, err := selectView(description, options)
	if err != nil {
		return nil, err
	}
	switch options.Format {
	case "json":
		raw, err := json.MarshalIndent(describe(selected), "", "  ")
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "encoding release view")
		}
		return append(escapeJSONDisplayControls(raw), '\n'), nil
	case "mermaid":
		return []byte(renderMermaid(selected)), nil
	default:
		return []byte(renderText(selected)), nil
	}
}

func selectView(description delivery.PublicationDescription, options Options) (view, error) {
	if err := options.Validate(); err != nil {
		return view{}, err
	}
	selected := view{Inspection: "metadata inspection; payloads, signatures and policy not verified", SelectedOutput: options.Output,
		SelectedKind: options.Kind, SelectedPlatform: options.Platform,
		TotalOutputs: len(description.Outputs), PublicationDescription: description}
	if !options.filtered() {
		return selected, nil
	}
	selected.Outputs = nil
	selected.Evidence = []delivery.PublicationEvidence{}
	retained := map[string]bool{}
	named := 0
	for _, output := range description.Outputs {
		if output.Name == options.Output {
			named++
		}
		if options.matches(output) {
			selected.Outputs = append(selected.Outputs, output)
			retained[output.Name] = true
		}
	}
	if options.Output != "" && named != 1 {
		return view{}, e.New(e.CodeUsage, "release has no unique output %q", options.Output)
	}
	if len(selected.Outputs) == 0 {
		return view{}, e.New(e.CodeUsage, "release has no output matching %s", options.describeFilters())
	}
	for _, evidence := range description.Evidence {
		if slices.ContainsFunc(evidence.Binding.Outputs, func(name string) bool { return retained[name] }) {
			selected.Evidence = append(selected.Evidence, evidence)
		}
	}
	return selected, nil
}

// describeFilters names the active filters for a selection diagnostic.
func (options Options) describeFilters() string {
	var filters []string
	for _, filter := range []struct{ name, value string }{{"output", options.Output}, {"kind", options.Kind}, {"platform", options.Platform}} {
		if filter.value != "" {
			filters = append(filters, filter.name+" "+strconv.Quote(filter.value))
		}
	}
	return strings.Join(filters, ", ")
}

func renderText(selected view) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Release %s\nInspection: %s\nDeclared qualification: %s\n", terminal(selected.Reference), selected.Inspection, terminal(selected.DeclaredQualification))
	fmt.Fprintf(&out, "References: %s\n", terminal(selected.ReferenceScope))
	fmt.Fprintf(&out, "Content: %s\nDefinition: %s\nEnvironment: %s\nPlan: %s\n",
		terminal(selected.Metadata.ContentSubject), terminal(selected.Metadata.DefinitionDigest),
		terminal(selected.Metadata.EnvironmentDigest), terminal(selected.Metadata.PlanDigest))
	fmt.Fprintf(&out, "Outputs: %d of %d\n", len(selected.Outputs), selected.TotalOutputs)
	if selected.SelectedKind != "" {
		fmt.Fprintf(&out, "Kind filter: %s\n", terminal(selected.SelectedKind))
	}
	if selected.SelectedPlatform != "" {
		fmt.Fprintf(&out, "Platform filter: %s (outputs without platform metadata excluded)\n", terminal(selected.SelectedPlatform))
	}
	for _, output := range selected.Outputs {
		platform := ""
		if output.OS != "" || output.Arch != "" {
			platform = " " + output.OS + "/" + output.Arch
		}
		fmt.Fprintf(&out, "\n%s (%s%s)\n  Derived artifact reference: %s\n  Build: %s\n", terminal(output.Name), terminal(output.Kind), terminal(platform), terminal(output.Reference), terminal(output.BuildDigest))
		if output.Disk != nil {
			mode := output.Disk.EvidenceMode
			if mode == "" {
				mode = "inspected"
			}
			fmt.Fprintf(&out, "  Declared disk evidence: %s (metadata; consumer policy not applied)\n", terminal(mode))
		}
		if output.ImageIndex != nil {
			fmt.Fprintf(&out, "  Required platforms: %s\n", terminal(strings.Join(output.ImageIndex.RequiredPlatforms, ", ")))
			for _, child := range output.ImageIndex.Children {
				fmt.Fprintf(&out, "  Platform: %s\n    Manifest: %s\n    Build: %s\n    Component: %s\n    Inspected export: %s\n",
					terminal(child.Platform), terminal(child.ManifestDigest), terminal(child.BuildDigest), terminal(child.Component), terminal(child.BuildSubject))
			}
		}
		for _, subject := range output.BuildSubjects {
			fmt.Fprintf(&out, "  Component: %s @ %s\n", terminal(subject.Component), terminal(subject.Digest))
		}
		for _, evidence := range selected.Evidence {
			binding := evidence.Binding
			if !slices.Contains(binding.Outputs, output.Name) {
				continue
			}
			fmt.Fprintf(&out, "  Evidence: %s (%s, %s)\n    Derived reference: %s\n    Subject: %s %s\n", terminal(evidence.Name), terminal(binding.Component), terminal(binding.Stage), terminal(evidence.Reference), terminal(binding.SubjectKind), terminal(binding.SubjectDigest))
			if binding.Platform != "" {
				fmt.Fprintf(&out, "    Platform: %s\n", terminal(binding.Platform))
			}
			fmt.Fprintf(&out, "    Members: %s\n", terminal(strings.Join(binding.Outputs, ", ")))
		}
	}
	return out.String()
}

func renderMermaid(selected view) string {
	var out strings.Builder
	fmt.Fprintf(&out, "flowchart TD\n  %%%% %s.\n", selected.Inspection)
	label := "Release " + shortDigest(selected.RootDigest) + " | metadata inspection"
	if scope := selected.scope(); scope != "" {
		label += " | " + scope
	}
	fmt.Fprintf(&out, "  root[\"%s\"]\n", mermaid(label))
	outputs := make(map[string]string, len(selected.Outputs))
	type imageChildKey struct{ output, platform, digest string }
	imageChildren := make(map[imageChildKey]string)
	for i, output := range selected.Outputs {
		id := fmt.Sprintf("o%d", i)
		outputs[output.Name] = id
		label := output.Name + " | " + output.Kind
		if output.OS != "" || output.Arch != "" {
			label += " | " + output.OS + "/" + output.Arch
		}
		if output.Disk != nil {
			mode := output.Disk.EvidenceMode
			if mode == "" {
				mode = "inspected"
			}
			label += " | declared disk evidence: " + mode
		}
		fmt.Fprintf(&out, "  %s[\"%s\"]\n  root --> %s\n", id, mermaid(label), id)
		if output.ImageIndex != nil {
			for j, child := range output.ImageIndex.Children {
				childID := fmt.Sprintf("%s_p%d", id, j)
				imageChildren[imageChildKey{output.Name, child.Platform, child.ManifestDigest}] = childID
				fmt.Fprintf(&out, "  %s[\"%s\"]\n  %s -->|contains platform| %s\n", childID,
					mermaid(child.Platform+" | "+child.Component+" | "+shortDigest(child.ManifestDigest)), id, childID)
			}
		}
		for j, subject := range output.BuildSubjects {
			fmt.Fprintf(&out, "  %s_c%d[\"%s\"]\n  %s -->|contains| %s_c%d\n", id, j, mermaid(subject.Component+" | "+shortDigest(subject.Digest)), id, id, j)
		}
	}
	for i, evidence := range selected.Evidence {
		binding := evidence.Binding
		label := evidence.Kind + " | " + binding.Component + " | " + binding.Stage
		if binding.Platform != "" {
			label += " | " + binding.Platform
		}
		fmt.Fprintf(&out, "  e%d[\"%s\"]\n", i, mermaid(label))
		linked := false
		if binding.SubjectKind == "oci-manifest" && binding.Platform != "" {
			for _, member := range binding.Outputs {
				if childID, ok := imageChildren[imageChildKey{member, binding.Platform, binding.SubjectDigest}]; ok {
					fmt.Fprintf(&out, "  e%d -->|inventories| %s\n", i, childID)
					linked = true
				}
			}
		}
		if !linked {
			fmt.Fprintf(&out, "  e%d_s[\"%s\"]\n  e%d -->|inventories| e%d_s\n", i, mermaid(binding.SubjectKind+" | "+shortDigest(binding.SubjectDigest)), i, i)
		}
		for _, member := range binding.Outputs {
			if id, ok := outputs[member]; ok {
				fmt.Fprintf(&out, "  e%d -->|supports| %s\n", i, id)
			}
		}
	}
	return out.String()
}

// MarshalIndent already escapes C0 controls inside strings. Escape remaining
// nonprinting Unicode without changing decoded values or structural whitespace.
func escapeJSONDisplayControls(raw []byte) []byte {
	var out strings.Builder
	out.Grow(len(raw))
	for _, char := range string(raw) {
		if char >= 0x7f && !strconv.IsPrint(char) {
			if char <= 0xffff {
				fmt.Fprintf(&out, `\u%04x`, char)
			} else {
				high, low := utf16.EncodeRune(char)
				fmt.Fprintf(&out, `\u%04x\u%04x`, high, low)
			}
			continue
		}
		out.WriteRune(char)
	}
	return []byte(out.String())
}

func shortDigest(value string) string {
	count := 0
	for index := range value {
		if count == 19 {
			return value[:index]
		}
		count++
	}
	return value
}

// Labels come from registry bytes. Quote controls for terminals and encode
// every non-alphanumeric Mermaid rune so input cannot inject diagram syntax.
func terminal(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

func mermaid(value string) string {
	var out strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == ' ' || char == '-' || char == '_' || char == '/' || char == '.' || char == ':' {
			out.WriteRune(char)
		} else {
			fmt.Fprintf(&out, "#%d;", char)
		}
	}
	return out.String()
}

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

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

type Options struct {
	Format string
	Output string
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
	return nil
}

type view struct {
	Inspection     string `json:"inspection"`
	SelectedOutput string `json:"selectedOutput,omitempty"`
	TotalOutputs   int    `json:"totalOutputs"`
	delivery.PublicationDescription
}

// Render selects a presentation only after the owner checked the full closure.
// JSON is readable presentation, not canonical identity input or a receipt.
func Render(description delivery.PublicationDescription, options Options) ([]byte, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	selected := view{Inspection: "integrity-only; signatures and policy not verified", SelectedOutput: options.Output,
		TotalOutputs: len(description.Outputs), PublicationDescription: description}
	if options.Output != "" {
		selected.Outputs = nil
		selected.Evidence = []delivery.PublicationEvidence{}
		for _, output := range description.Outputs {
			if output.Name == options.Output {
				selected.Outputs = append(selected.Outputs, output)
			}
		}
		if len(selected.Outputs) != 1 {
			return nil, e.New(e.CodeUsage, "release has no unique output %q", options.Output)
		}
		for _, evidence := range description.Evidence {
			if slices.Contains(evidence.Binding.Outputs, options.Output) {
				selected.Evidence = append(selected.Evidence, evidence)
			}
		}
	}
	switch options.Format {
	case "json":
		raw, err := json.MarshalIndent(selected, "", "  ")
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

func renderText(selected view) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Release %s\nInspection: %s\nDeclared qualification: %s\n", terminal(selected.Reference), selected.Inspection, terminal(selected.DeclaredQualification))
	fmt.Fprintf(&out, "References: %s\n", terminal(selected.ReferenceScope))
	fmt.Fprintf(&out, "Content: %s\nDefinition: %s\nEnvironment: %s\nPlan: %s\n",
		terminal(selected.Metadata.ContentSubject), terminal(selected.Metadata.DefinitionDigest),
		terminal(selected.Metadata.EnvironmentDigest), terminal(selected.Metadata.PlanDigest))
	fmt.Fprintf(&out, "Outputs: %d of %d\n", len(selected.Outputs), selected.TotalOutputs)
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
		for _, subject := range output.BuildSubjects {
			fmt.Fprintf(&out, "  Component: %s @ %s\n", terminal(subject.Component), terminal(subject.Digest))
		}
		for _, evidence := range selected.Evidence {
			binding := evidence.Binding
			if !slices.Contains(binding.Outputs, output.Name) {
				continue
			}
			fmt.Fprintf(&out, "  Evidence: %s (%s, %s)\n    Derived reference: %s\n    Subject: %s %s\n", terminal(evidence.Name), terminal(binding.Component), terminal(binding.Stage), terminal(evidence.Reference), terminal(binding.SubjectKind), terminal(binding.SubjectDigest))
			fmt.Fprintf(&out, "    Members: %s\n", terminal(strings.Join(binding.Outputs, ", ")))
		}
	}
	return out.String()
}

func renderMermaid(selected view) string {
	var out strings.Builder
	out.WriteString("flowchart TD\n  %% Complete closure checked; signatures and policy not verified.\n")
	fmt.Fprintf(&out, "  root[\"%s\"]\n", mermaid("Release "+shortDigest(selected.RootDigest)+" | integrity only"))
	outputs := make(map[string]string, len(selected.Outputs))
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
		for j, subject := range output.BuildSubjects {
			fmt.Fprintf(&out, "  %s_c%d[\"%s\"]\n  %s -->|contains| %s_c%d\n", id, j, mermaid(subject.Component+" | "+shortDigest(subject.Digest)), id, id, j)
		}
	}
	for i, evidence := range selected.Evidence {
		binding := evidence.Binding
		fmt.Fprintf(&out, "  e%d[\"%s\"]\n", i, mermaid(evidence.Kind+" | "+binding.Component+" | "+binding.Stage))
		fmt.Fprintf(&out, "  e%d_s[\"%s\"]\n  e%d -->|inventories| e%d_s\n", i, mermaid(binding.SubjectKind+" | "+shortDigest(binding.SubjectDigest)), i, i)
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

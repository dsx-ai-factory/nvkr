// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseview

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ComparisonSchemaVersion identifies the external release comparison JSON. Like
// the description, its bytes are presentation, not identity input or a receipt.
const ComparisonSchemaVersion = "nvkr.dev/release-comparison/v1"

// Comparison is an artifact-inventory comparison of two checked release
// descriptions. Outputs match by exact name only; equal Build Digests never pair
// differently named outputs. Evidence documents, signatures, policy acceptance
// and deployment compatibility are neither compared nor established.
type Comparison struct {
	SchemaVersion string            `json:"schemaVersion"`
	Scope         string            `json:"scope"`
	NotCompared   []string          `json:"notCompared"`
	Before        ComparedRelease   `json:"before"`
	After         ComparedRelease   `json:"after"`
	Added         []Output          `json:"added"`
	Removed       []Output          `json:"removed"`
	Changed       []OutputChange    `json:"changed"`
	Unchanged     int               `json:"unchanged"`
	Inspection    InspectionDetails `json:"inspectionDetails"`
}

// ComparedRelease identifies one compared side without repeating its inventory.
type ComparedRelease struct {
	Reference    string `json:"reference"`
	RootDigest   string `json:"rootDigest"`
	TotalOutputs int    `json:"totalOutputs"`
}

// OutputChange retains complete before and after values for one named output.
// ReferenceOnly marks a changed derived transport reference with otherwise equal
// metadata, so it is never reported as a changed payload digest.
type OutputChange struct {
	Name          string   `json:"name"`
	Fields        []string `json:"fields"`
	ReferenceOnly bool     `json:"referenceOnly"`
	Before        Output   `json:"before"`
	After         Output   `json:"after"`
}

// Equal reports whether both inventories contain the same named outputs with
// the same compared metadata, including derived references.
func (comparison Comparison) Equal() bool {
	return len(comparison.Added) == 0 && len(comparison.Removed) == 0 && len(comparison.Changed) == 0
}

// Compare projects both checked publications and compares every output by exact
// name. Like Describe, it relies on the owner's complete metadata inspection.
func Compare(before, after delivery.PublicationDescription) (Comparison, error) {
	left, err := Describe(before, Options{})
	if err != nil {
		return Comparison{}, err
	}
	right, err := Describe(after, Options{})
	if err != nil {
		return Comparison{}, err
	}
	result := Comparison{
		SchemaVersion: ComparisonSchemaVersion, Scope: "artifact-inventory",
		NotCompared: []string{"deployment-compatibility", "evidence-documents", "policy-acceptance", "signatures"},
		Before:      ComparedRelease{Reference: left.Reference, RootDigest: left.RootDigest, TotalOutputs: left.TotalOutputs},
		After:       ComparedRelease{Reference: right.Reference, RootDigest: right.RootDigest, TotalOutputs: right.TotalOutputs},
		Added:       []Output{}, Removed: []Output{}, Changed: []OutputChange{},
		Inspection: left.InspectionDetails,
	}
	previous, err := outputsByName(left.Outputs)
	if err != nil {
		return Comparison{}, err
	}
	next, err := outputsByName(right.Outputs)
	if err != nil {
		return Comparison{}, err
	}
	for _, output := range left.Outputs {
		if _, kept := next[output.Name]; !kept {
			result.Removed = append(result.Removed, output)
		}
	}
	for _, output := range right.Outputs {
		old, kept := previous[output.Name]
		if !kept {
			result.Added = append(result.Added, output)
			continue
		}
		fields, err := changedFields(old, output)
		if err != nil {
			return Comparison{}, err
		}
		if len(fields) == 0 {
			result.Unchanged++
			continue
		}
		result.Changed = append(result.Changed, OutputChange{Name: output.Name, Fields: fields,
			ReferenceOnly: slices.Equal(fields, []string{"reference"}), Before: old, After: output})
	}
	// Describe already sorts outputs by name; keep the contract explicit.
	slices.SortFunc(result.Added, func(a, b Output) int { return cmp.Compare(a.Name, b.Name) })
	slices.SortFunc(result.Removed, func(a, b Output) int { return cmp.Compare(a.Name, b.Name) })
	slices.SortFunc(result.Changed, func(a, b OutputChange) int { return cmp.Compare(a.Name, b.Name) })
	return result, nil
}

// A checked publication has unique output names. Reject a duplicate rather than
// silently comparing only one of two same-named outputs.
func outputsByName(outputs []Output) (map[string]Output, error) {
	byName := make(map[string]Output, len(outputs))
	for _, output := range outputs {
		if _, duplicate := byName[output.Name]; duplicate {
			return nil, e.New(e.CodeVerifyFailed, "release description repeats output %q", output.Name)
		}
		byName[output.Name] = output
	}
	return byName, nil
}

// changedFields compares the projected JSON fields, so every field the external
// description carries is compared and a new field cannot be silently skipped.
func changedFields(before, after Output) ([]string, error) {
	left, err := outputFields(before)
	if err != nil {
		return nil, err
	}
	right, err := outputFields(after)
	if err != nil {
		return nil, err
	}
	var fields []string
	for name, value := range left {
		if other, found := right[name]; !found || !bytes.Equal(value, other) {
			fields = append(fields, name)
		}
	}
	for name := range right {
		if _, found := left[name]; !found {
			fields = append(fields, name)
		}
	}
	slices.Sort(fields)
	return fields, nil
}

func outputFields(output Output) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(output)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "encoding release output")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding release output")
	}
	return fields, nil
}

// ValidateComparisonFormat rejects unsupported comparison encodings before
// either release is inspected.
func ValidateComparisonFormat(format string) error {
	if format != "" && format != "text" && format != "json" {
		return e.New(e.CodeUsage, "release comparison format must be text or json")
	}
	return nil
}

// Render encodes the comparison as indented JSON or a terminal-safe summary.
func (comparison Comparison) Render(format string) ([]byte, error) {
	if err := ValidateComparisonFormat(format); err != nil {
		return nil, err
	}
	if format == "json" {
		raw, err := json.MarshalIndent(comparison, "", "  ")
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "encoding release comparison")
		}
		return append(escapeJSONDisplayControls(raw), '\n'), nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Release comparison: artifact inventory; metadata only\nNot compared: %s\n", strings.Join(comparison.NotCompared, ", "))
	fmt.Fprintf(&out, "Before: %s (%d outputs)\nAfter: %s (%d outputs)\n",
		terminal(comparison.Before.Reference), comparison.Before.TotalOutputs, terminal(comparison.After.Reference), comparison.After.TotalOutputs)
	referenceOnly := 0
	for _, change := range comparison.Changed {
		if change.ReferenceOnly {
			referenceOnly++
		}
	}
	fmt.Fprintf(&out, "Added: %d, removed: %d, changed: %d (%d reference only), unchanged: %d\n",
		len(comparison.Added), len(comparison.Removed), len(comparison.Changed), referenceOnly, comparison.Unchanged)
	for _, output := range comparison.Added {
		fmt.Fprintf(&out, "\n+ %s (%s) %s\n", terminal(output.Name), terminal(output.Kind), terminal(output.Digest))
	}
	for _, output := range comparison.Removed {
		fmt.Fprintf(&out, "\n- %s (%s) %s\n", terminal(output.Name), terminal(output.Kind), terminal(output.Digest))
	}
	for _, change := range comparison.Changed {
		if change.ReferenceOnly {
			fmt.Fprintf(&out, "\n~ %s: derived reference only\n", terminal(change.Name))
		} else {
			fmt.Fprintf(&out, "\n~ %s: %s\n", terminal(change.Name), strings.Join(change.Fields, ", "))
		}
		for _, pair := range change.FieldChanges() {
			fmt.Fprintf(&out, "  %s: %s -> %s\n", pair.Field, terminal(pair.Before), terminal(pair.After))
		}
	}
	return []byte(out.String()), nil
}

// FieldChange is one changed field of a compared output with its compact
// before and after values. "(absent)" marks a field missing on one side.
type FieldChange struct {
	Field, Before, After string
}

// FieldChanges pairs each changed field with its compact before and after
// values, in the order of Fields, so every presentation shows the same values.
func (change OutputChange) FieldChanges() []FieldChange {
	// Both sides are Outputs this package encoded, so encoding cannot fail.
	before, _ := outputFields(change.Before)
	after, _ := outputFields(change.After)
	pairs := make([]FieldChange, 0, len(change.Fields))
	for _, field := range change.Fields {
		pairs = append(pairs, FieldChange{Field: field, Before: compactField(before[field]), After: compactField(after[field])})
	}
	return pairs
}

func compactField(raw json.RawMessage) string {
	if raw == nil {
		return "(absent)"
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return string(raw)
}

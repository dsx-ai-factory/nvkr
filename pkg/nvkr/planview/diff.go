// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package planview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Comparison describes all differences between two admitted canonical Plans.
// A changed Build Digest means changed logical inputs, not observed output bytes.
type Comparison struct {
	SchemaVersion   string        `json:"schemaVersion"`
	Before          string        `json:"before"`
	After           string        `json:"after"`
	Authority       string        `json:"authority"`
	Builds          []BuildChange `json:"builds"`
	UnchangedBuilds int           `json:"unchangedBuilds"`
	Changes         []Change      `json:"changes"`
}

type BuildChange struct {
	Name   string `json:"name"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type Change struct {
	Category string          `json:"category"`
	Path     string          `json:"path"`
	Before   json.RawMessage `json:"before,omitempty"`
	After    json.RawMessage `json:"after,omitempty"`
}

// Compare matches builds by output name, named schema collections by exact keys,
// and ordered values (including argv and patches) by index. Added/removed values
// retain their complete content. No field is dropped as irrelevant churn.
func Compare(before, after *Document) (*Comparison, error) {
	if err := before.valid(); err != nil {
		return nil, err
	}
	if err := after.valid(); err != nil {
		return nil, err
	}
	result := &Comparison{SchemaVersion: "nvkr.dev/plan-comparison/v1", Before: before.digest, After: after.digest, Authority: "description-only", Builds: []BuildChange{}, Changes: []Change{}}
	oldBuilds, newBuilds := map[string]string{}, map[string]string{}
	for _, build := range before.plan.Builds {
		oldBuilds[build.Name] = build.BuildDigest
	}
	for _, build := range after.plan.Builds {
		newBuilds[build.Name] = build.BuildDigest
	}
	for _, name := range unionKeys(oldBuilds, newBuilds) {
		if oldBuilds[name] == newBuilds[name] {
			result.UnchangedBuilds++
			continue
		}
		result.Builds = append(result.Builds, BuildChange{Name: name, Before: oldBuilds[name], After: newBuilds[name]})
	}
	left, err := decodeTree(before.raw)
	if err != nil {
		return nil, err
	}
	right, err := decodeTree(after.raw)
	if err != nil {
		return nil, err
	}
	result.compare("", left, true, right, true)
	sort.Slice(result.Changes, func(i, j int) bool {
		a, b := result.Changes[i], result.Changes[j]
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		return a.Path < b.Path
	})
	return result, nil
}

func decodeTree(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "decoding validated plan view")
	}
	return value, nil
}

func (c *Comparison) compare(path string, left any, leftExists bool, right any, rightExists bool) {
	if leftExists == rightExists && reflect.DeepEqual(left, right) {
		return
	}
	if leftExists && rightExists {
		leftMap, leftOK := left.(map[string]any)
		rightMap, rightOK := right.(map[string]any)
		if leftOK && rightOK {
			for _, key := range unionKeys(leftMap, rightMap) {
				lv, le := leftMap[key]
				rv, re := rightMap[key]
				c.compare(path+"/"+pointer(key), lv, le, rv, re)
			}
			return
		}
		leftArray, leftOK := left.([]any)
		rightArray, rightOK := right.([]any)
		if leftOK && rightOK {
			c.compareArray(path, leftArray, rightArray)
			return
		}
	}
	c.add(path, left, leftExists, right, rightExists)
}

func (c *Comparison) compareArray(path string, left, right []any) {
	key := collectionKey(path)
	lm, lo, lok := namedItems(left, key)
	rm, ro, rok := namedItems(right, key)
	if key != "" && lok && rok {
		for _, name := range unionKeys(lm, rm) {
			lv, le := lm[name]
			rv, re := rm[name]
			c.compare(path+"/@"+key+"="+pointer(name), lv, le, rv, re)
		}
		// Preserve reorder evidence without interpreting insertions as shifts of every
		// later entry. Only the relative order of surviving names can change here.
		commonLeft, commonRight := []string{}, []string{}
		for _, name := range lo {
			if _, ok := rm[name]; ok {
				commonLeft = append(commonLeft, name)
			}
		}
		for _, name := range ro {
			if _, ok := lm[name]; ok {
				commonRight = append(commonRight, name)
			}
		}
		if !reflect.DeepEqual(commonLeft, commonRight) {
			c.add(path+"/@order", commonLeft, true, commonRight, true)
		}
		return
	}
	count := max(len(left), len(right))
	for i := 0; i < count; i++ {
		var lv, rv any
		le, re := i < len(left), i < len(right)
		if le {
			lv = left[i]
		}
		if re {
			rv = right[i]
		}
		c.compare(fmt.Sprintf("%s/%d", path, i), lv, le, rv, re)
	}
}

func collectionKey(path string) string {
	// Explicit schema set collections only. Patches and command arguments are
	// ordered even if a patch happens to carry a unique name.
	switch path[strings.LastIndex(path, "/")+1:] {
	case "builds", "lifecycleOutputs", "outputs", "routes", "env", "variables", "executionClasses":
		return "name"
	case "resolvedComponents":
		return "Name"
	case "nodes", "artifacts":
		return "id"
	case "materials":
		return "binding"
	default:
		return ""
	}
}

func namedItems(values []any, key string) (map[string]any, []string, bool) {
	result := map[string]any{}
	order := make([]string, 0, len(values))
	if key == "" {
		return nil, nil, false
	}
	for _, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, nil, false
		}
		name, ok := object[key].(string)
		if !ok || name == "" {
			return nil, nil, false
		}
		if _, exists := result[name]; exists {
			return nil, nil, false
		}
		result[name] = value
		order = append(order, name)
	}
	return result, order, true
}

func (c *Comparison) add(path string, left any, leftExists bool, right any, rightExists bool) {
	change := Change{Category: category(path), Path: path}
	// Trees originate only in json.Decoder with UseNumber. All retained values are
	// JSON-compatible; encoding them cannot fail or coerce integer precision.
	if leftExists {
		change.Before, _ = canonical.Marshal(left)
	}
	if rightExists {
		change.After, _ = canonical.Marshal(right)
	}
	c.Changes = append(c.Changes, change)
}

// Category rules name complete schema fields. Named collection selectors match
// exactly one path segment, so similarly named producer map keys cannot acquire
// a different category. More specific rules precede their containing fields.
// Tests check these paths and selectors against the serialized Plan schema.
var comparisonCategories = []struct {
	path     string
	category string
}{
	{"/definitionDigest", "derived identities"},
	{"/environmentDigest", "derived identities"},
	{"/catalogDigest", "derived identities"},
	{"/builds/@name=/buildDigest", "derived identities"},
	{"/builds/@name=/catalogDigest", "derived identities"},
	{"/lifecycleOutputs/@name=/buildDigest", "derived identities"},
	{"/builds/@name=/executionGraph/nodes/@id=/requirements", "environment and execution"},
	{"/builds/@name=/executionGraph/nodes/@id=/executionBinding", "environment and execution"},
	{"/graph/nodes/@id=/requirements", "environment and execution"},
	{"/graph/nodes/@id=/executionBinding", "environment and execution"},
	{"/graph/nodes/@id=/runsOn", "environment and execution"},
	{"/builds/@name=/catalogSnapshot", "catalog inputs"},
	{"/builds/@name=/resolvedComponents", "catalog inputs"},
	{"/builds/@name=/appliedOverlays", "catalog inputs"},
	{"/catalogSnapshot", "catalog inputs"},
	{"/appliedOverlays", "catalog inputs"},
	{"/definition", "definition"},
	{"/environment", "environment and execution"},
	{"/builds", "producer instructions"},
	{"/graph/artifacts", "artifact bindings"},
	{"/graph/nodes", "operation bindings"},
	{"/routes", "publication routes"},
	{"/targets", "publication routes"},
	{"/lifecycleOutputs", "required outputs"},
	{"/actions", "provider tools"},
}

func category(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, rule := range comparisonCategories {
		fields := strings.Split(strings.TrimPrefix(rule.path, "/"), "/")
		if len(parts) < len(fields) {
			continue
		}
		matches := true
		for index, field := range fields {
			if strings.HasPrefix(field, "@") {
				if !strings.HasPrefix(parts[index], field) || len(parts[index]) == len(field) {
					matches = false
					break
				}
			} else if parts[index] != field {
				matches = false
				break
			}
		}
		if matches {
			return rule.category
		}
	}
	return "plan metadata"
}

func unionKeys[V any](left, right map[string]V) []string {
	members := make(map[string]struct{})
	for key := range left {
		members[key] = struct{}{}
	}
	for key := range right {
		members[key] = struct{}{}
	}
	return keys(members)
}

// Equal reports whether the compared canonical Plans have the same identity.
// It describes the inputs only, not observed artifacts or release acceptance.
func (c *Comparison) Equal() bool {
	return c != nil && c.Before == c.After
}

func pointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

// Render emits one canonical descriptive JSON document or a compact text report.
// Text retains every changed path; long values are explicitly abbreviated and the
// JSON form retains them in full. Equal plans succeed without a difference error.
func (c *Comparison) Render(format string) ([]byte, error) {
	if err := ValidateComparisonFormat(format); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, e.New(e.CodeUsage, "comparison requires inspected plans")
	}
	if format == "json" {
		raw, err := canonical.Marshal(c)
		if err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "serializing plan comparison")
		}
		return raw, nil
	}
	var out strings.Builder
	c.writeTextHeader(&out)
	if c.Equal() {
		return []byte(out.String()), nil
	}
	fmt.Fprintf(&out, "\nChanged fields: %d (long values abbreviated; --format json retains full values)\n", len(c.Changes))
	previous := ""
	for _, change := range c.Changes {
		if change.Category != previous {
			fmt.Fprintf(&out, "\n%s\n", change.Category)
			previous = change.Category
		}
		fmt.Fprintf(&out, "  %q\n    %s -> %s\n", change.Path, compact(change.Before), compact(change.After))
	}
	return []byte(out.String()), nil
}

func (c *Comparison) writeTextHeader(out *strings.Builder) {
	fmt.Fprintf(out, "before: %s\nafter:  %s\n", c.Before, c.After)
	out.WriteString("Logical input changes only; output bytes and release acceptance are not observed.\n")
	if c.Equal() {
		out.WriteString("No changes.\n")
		return
	}
	fmt.Fprintf(out, "\nBuild identities: %d changed/added/removed; %d unchanged\n", len(c.Builds), c.UnchangedBuilds)
	for _, build := range c.Builds {
		fmt.Fprintf(out, "  %s: %s -> %s\n", build.Name, orAbsent(build.Before), orAbsent(build.After))
	}
}

func ValidateComparisonFormat(format string) error {
	if format != "text" && format != "json" {
		return e.New(e.CodeUsage, "unknown comparison format %q (use text or json)", format)
	}
	return nil
}
func compact(raw []byte) string {
	if len(raw) == 0 {
		return "(absent)"
	}
	if len(raw) <= 180 {
		return jsonDisplay(json.RawMessage(raw))
	}
	// Quote the raw JSON prefix to retain its escapes, and keep the byte budget
	// without cutting a Unicode code point in half.
	end := 140
	for end > 0 && !utf8.RuneStart(raw[end]) {
		end--
	}
	return fmt.Sprintf("%q … (%d bytes)", string(raw[:end]), len(raw))
}
func orAbsent(value string) string {
	if value == "" {
		return "(absent)"
	}
	return value
}

// Summary renders a review-sized comparison. It counts every change and shows
// authored inputs directly; repeated snapshots, derived identities, producer and
// artifact wiring remain available through Render("text") or Render("json").
func (c *Comparison) Summary() ([]byte, error) {
	if c == nil {
		return nil, e.New(e.CodeUsage, "comparison requires inspected plans")
	}
	var out strings.Builder
	c.writeTextHeader(&out)
	if c.Equal() {
		return []byte(out.String()), nil
	}
	counts := map[string]int{}
	for _, change := range c.Changes {
		counts[change.Category]++
	}
	fmt.Fprintf(&out, "\nChanged fields: %d\n", len(c.Changes))
	for _, category := range keys(counts) {
		fmt.Fprintf(&out, "  %s: %d\n", category, counts[category])
	}
	out.WriteString("\nDirect inputs and routes\n")
	shown := 0
	for _, change := range c.Changes {
		if strings.HasPrefix(change.Path, "/definition/") || strings.HasPrefix(change.Path, "/environment/") || strings.HasPrefix(change.Path, "/catalogSnapshot/") || change.Category == "publication routes" || change.Category == "plan metadata" || change.Category == "provider tools" {
			fmt.Fprintf(&out, "  %q\n    %s -> %s\n", change.Path, compact(change.Before), compact(change.After))
			shown++
		}
	}
	if shown == 0 {
		out.WriteString("  no changes in the shared inputs; inspect per-output catalogs and generated bindings in the full report\n")
	}
	out.WriteString("\nUse --details for every changed path, or --format json for complete values.\n")
	return []byte(out.String()), nil
}

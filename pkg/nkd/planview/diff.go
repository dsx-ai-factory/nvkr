package planview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
	result := &Comparison{SchemaVersion: "nkd.dev/plan-comparison/v1", Before: before.digest, After: after.digest, Authority: "description-only", Builds: []BuildChange{}, Changes: []Change{}}
	oldBuilds, newBuilds := map[string]string{}, map[string]string{}
	for _, build := range before.plan.Builds {
		oldBuilds[build.Name] = build.BuildDigest
	}
	for _, build := range after.plan.Builds {
		newBuilds[build.Name] = build.BuildDigest
	}
	names := map[string]bool{}
	for name := range oldBuilds {
		names[name] = true
	}
	for name := range newBuilds {
		names[name] = true
	}
	for _, name := range keys(names) {
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
			members := map[string]bool{}
			for key := range leftMap {
				members[key] = true
			}
			for key := range rightMap {
				members[key] = true
			}
			for _, key := range keys(members) {
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
		names := map[string]bool{}
		for name := range lm {
			names[name] = true
		}
		for name := range rm {
			names[name] = true
		}
		for _, name := range keys(names) {
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

func category(path string) string {
	leaf := path[strings.LastIndex(path, "/")+1:]
	switch leaf {
	case "buildDigest", "catalogDigest", "definitionDigest", "environmentDigest":
		return "derived identities"
	}
	switch {
	case strings.HasPrefix(path, "/definition/"):
		return "definition"
	case strings.HasPrefix(path, "/environment/"):
		return "environment and execution"
	case strings.HasPrefix(path, "/catalogSnapshot/") || strings.Contains(path, "/catalogSnapshot/") || strings.Contains(path, "/resolvedComponents/") || strings.Contains(path, "/appliedOverlays"):
		return "catalog inputs"
	case strings.HasPrefix(path, "/builds/"):
		if strings.Contains(path, "/requirements/") || strings.Contains(path, "/executionBinding/") {
			return "environment and execution"
		}
		return "producer instructions"
	case strings.HasPrefix(path, "/graph/artifacts/"):
		return "artifact bindings"
	case strings.HasPrefix(path, "/graph/nodes/"):
		return "operation bindings"
	case strings.HasPrefix(path, "/routes") || strings.HasPrefix(path, "/targets"):
		return "publication routes"
	case strings.HasPrefix(path, "/lifecycleOutputs"):
		return "required outputs"
	case strings.HasPrefix(path, "/actions"):
		return "provider tools"
	default:
		return "plan metadata"
	}
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
	fmt.Fprintf(&out, "before: %s\nafter:  %s\n", c.Before, c.After)
	out.WriteString("Logical input changes only; output bytes and release acceptance are not observed.\n")
	if c.Before == c.After {
		out.WriteString("No changes.\n")
		return []byte(out.String()), nil
	}
	fmt.Fprintf(&out, "\nBuild identities: %d changed/added/removed; %d unchanged\n", len(c.Builds), c.UnchangedBuilds)
	for _, build := range c.Builds {
		fmt.Fprintf(&out, "  %s: %s -> %s\n", build.Name, orAbsent(build.Before), orAbsent(build.After))
	}
	fmt.Fprintf(&out, "\nChanged fields: %d (long values abbreviated; --format json retains full values)\n", len(c.Changes))
	previous := ""
	for _, change := range c.Changes {
		if change.Category != previous {
			fmt.Fprintf(&out, "\n%s\n", change.Category)
			previous = change.Category
		}
		fmt.Fprintf(&out, "  %s\n    %s -> %s\n", change.Path, compact(change.Before), compact(change.After))
	}
	return []byte(out.String()), nil
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
		return string(raw)
	}
	// Keep JSON escape sequences intact by quoting the abbreviated raw prefix.
	return fmt.Sprintf("%q … (%d bytes)", string(raw[:140]), len(raw))
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
	fmt.Fprintf(&out, "before: %s\nafter:  %s\n", c.Before, c.After)
	out.WriteString("Logical input changes only; output bytes and release acceptance are not observed.\n")
	if c.Before == c.After {
		out.WriteString("No changes.\n")
		return []byte(out.String()), nil
	}
	fmt.Fprintf(&out, "\nBuild identities: %d changed/added/removed; %d unchanged\n", len(c.Builds), c.UnchangedBuilds)
	for _, build := range c.Builds {
		fmt.Fprintf(&out, "  %s: %s -> %s\n", build.Name, orAbsent(build.Before), orAbsent(build.After))
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
			fmt.Fprintf(&out, "  %s\n    %s -> %s\n", change.Path, compact(change.Before), compact(change.After))
			shown++
		}
	}
	if shown == 0 {
		out.WriteString("  no changes in the shared inputs; inspect per-output catalogs and generated bindings in the full report\n")
	}
	out.WriteString("\nUse --details for every changed path, or --format json for complete values.\n")
	return []byte(out.String()), nil
}

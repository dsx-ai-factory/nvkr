package planview

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

func TestCompareDistinguishesBuildIdentityFromExecutionAndTransport(t *testing.T) {
	before := inspectPlan(t, comparisonPlan(t, nil))
	for _, test := range []struct {
		name               string
		change             func(*catalog.Catalog, *definition.Definition, *definition.Environment)
		builds             int
		category, contains string
	}{
		{"unchanged", nil, 0, "", ""},
		{"runner", func(_ *catalog.Catalog, _ *definition.Definition, env *definition.Environment) {
			env.RunsOn = "ubuntu-24.04"
		}, 0, "environment and execution", "runsOn"},
		{"source pin", func(cat *catalog.Catalog, _ *definition.Definition, _ *definition.Environment) {
			component := cat.Components["containerd"]
			component.Commit = strings.Repeat("b", 40)
			component.TreeHash = strings.Repeat("c", 40)
			cat.Components["containerd"] = component
		}, 1, "producer instructions", "sourceCommit"},
		{"source transport", func(cat *catalog.Catalog, _ *definition.Definition, _ *definition.Environment) {
			component := cat.Components["containerd"]
			component.Repo = "https://mirror.invalid/containerd"
			cat.Components["containerd"] = component
		}, 0, "producer instructions", "repo"},
		{"output addition", func(_ *catalog.Catalog, def *definition.Definition, _ *definition.Environment) {
			def.Outputs = append(def.Outputs, definition.DefinitionOutput{Name: "z-extra", Platform: "linux/amd64", Format: definition.OutputExecutable, Components: []string{"crictl"}})
		}, 1, "required outputs", "@name=z-extra"},
		{"output removal", func(_ *catalog.Catalog, def *definition.Definition, _ *definition.Environment) {
			def.Outputs = def.Outputs[:1]
		}, 1, "required outputs", "@name=utility"},
	} {
		t.Run(test.name, func(t *testing.T) {
			after := inspectPlan(t, comparisonPlan(t, test.change))
			comparison, err := Compare(before, after)
			if err != nil {
				t.Fatal(err)
			}
			if len(comparison.Builds) != test.builds {
				t.Fatalf("changed builds=%+v, want %d", comparison.Builds, test.builds)
			}
			if test.change == nil {
				if len(comparison.Changes) != 0 || comparison.Before != comparison.After {
					t.Fatalf("false changes: %+v", comparison)
				}
				return
			}
			if comparison.Before == comparison.After || len(comparison.Changes) == 0 {
				t.Fatal("changed plan reported no difference")
			}
			found := false
			for _, change := range comparison.Changes {
				if change.Category == test.category && strings.Contains(change.Path, test.contains) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing change %s/%s: %+v", test.category, test.contains, comparison.Changes)
			}
			if test.builds == 0 && comparison.UnchangedBuilds != len(before.plan.Builds) {
				t.Fatal("execution/transport change misreported logical input change")
			}
			raw, err := comparison.Render("json")
			if err != nil {
				t.Fatal(err)
			}
			var restored Comparison
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(comparison, &restored) {
				t.Fatal("JSON lost comparison fields")
			}
			text, err := comparison.Render("text")
			if err != nil || !strings.Contains(string(text), test.contains) {
				t.Fatalf("text report lost change: %s %v", text, err)
			}
		})
	}
}

func TestCompareExecutionClassesByName(t *testing.T) {
	alpha := execution.Class{Name: "alpha", RunsOn: "ubuntu-22.04", HostPlatform: execution.HostPlatformLinuxAMD64, MaxTimeoutSeconds: 600}
	beta := execution.Class{Name: "beta", RunsOn: "ubuntu-24.04", HostPlatform: execution.HostPlatformLinuxAMD64, MaxTimeoutSeconds: 600}
	updatedBeta := beta
	updatedBeta.Resources.MemoryMiB = 2048
	withClasses := func(classes []execution.Class) *Document {
		return inspectPlan(t, comparisonPlan(t, func(_ *catalog.Catalog, _ *definition.Definition, env *definition.Environment) {
			env.ExecutionClasses = classes
		}))
	}
	for _, test := range []struct {
		name          string
		before, after []execution.Class
		path          string
		old, new      any
	}{
		{"insertion", []execution.Class{beta}, []execution.Class{alpha, beta}, "/environment/executionClasses/@name=alpha", nil, alpha},
		{"removal", []execution.Class{alpha, beta}, []execution.Class{beta}, "/environment/executionClasses/@name=alpha", alpha, nil},
		{"field change", []execution.Class{alpha, beta}, []execution.Class{alpha, updatedBeta}, "/environment/executionClasses/@name=beta/resources/memoryMiB", nil, uint64(2048)},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, after := withClasses(test.before), withClasses(test.after)
			comparison, err := Compare(before, after)
			if err != nil {
				t.Fatal(err)
			}
			if len(comparison.Builds) != 0 || comparison.UnchangedBuilds != len(before.plan.Builds) {
				t.Fatalf("execution class change altered logical build identities: %+v", comparison)
			}
			var changes []Change
			for _, change := range comparison.Changes {
				if strings.HasPrefix(change.Path, "/environment/executionClasses/") {
					changes = append(changes, change)
				}
			}
			want := Change{Category: "environment and execution", Path: test.path}
			if test.old != nil {
				want.Before, err = canonical.Marshal(test.old)
				if err != nil {
					t.Fatal(err)
				}
			}
			if test.new != nil {
				want.After, err = canonical.Marshal(test.new)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(changes, []Change{want}) {
				t.Fatalf("execution class changes=%+v, want %+v", changes, want)
			}
		})
	}
}

func TestSemanticCollectionsPreservePrecisionOrderAndAbsentValues(t *testing.T) {
	for _, test := range []struct {
		name, left, right, want string
		count                   int
	}{
		{"named insertion", `{"builds":[{"name":"b","value":1}]}`, `{"builds":[{"name":"a","value":2},{"name":"b","value":1}]}`, "/builds/@name=a", 1},
		{"named reorder", `{"builds":[{"name":"a"},{"name":"b"}]}`, `{"builds":[{"name":"b"},{"name":"a"}]}`, "/builds/@order", 1},
		{"argv reorder", `{"command":["first","second"]}`, `{"command":["second","first"]}`, "/command/0", 2},
		{"patch order", `{"patches":[{"name":"a"},{"name":"b"}]}`, `{"patches":[{"name":"b"},{"name":"a"}]}`, "/patches/0/name", 2},
		{"large integer", `{"size":9007199254740992}`, `{"size":9007199254740993}`, "/size", 1},
		{"null vs absent", `{"x":null}`, `{}`, "/x", 1},
		{"escaped selector", `{"nodes":[{"id":"a/b~c","x":1}]}`, `{"nodes":[{"id":"a/b~c","x":2}]}`, "/nodes/@id=a~1b~0c/x", 1},
		{"named component", `{"resolvedComponents":[{"Name":"a","tag":"v1"}]}`, `{"resolvedComponents":[{"Name":"a","tag":"v2"}]}`, "/resolvedComponents/@Name=a/tag", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			left, err := decodeTree([]byte(test.left))
			if err != nil {
				t.Fatal(err)
			}
			right, err := decodeTree([]byte(test.right))
			if err != nil {
				t.Fatal(err)
			}
			c := Comparison{}
			c.compare("", left, true, right, true)
			if len(c.Changes) != test.count {
				t.Fatalf("changes=%+v", c.Changes)
			}
			found := false
			for _, change := range c.Changes {
				if change.Path == test.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s: %+v", test.want, c.Changes)
			}
			if test.name == "large integer" && string(c.Changes[0].After) != "9007199254740993" {
				t.Fatal("integer rounded")
			}
			if test.name == "null vs absent" && (string(c.Changes[0].Before) != "null" || c.Changes[0].After != nil) {
				t.Fatal("absence collapsed into null")
			}
		})
	}
}

func TestComparisonAdmissionAndFormats(t *testing.T) {
	doc := inspectPlan(t, comparisonPlan(t, nil))
	for _, pair := range [][2]*Document{{nil, doc}, {doc, nil}, {&Document{}, doc}} {
		comparison, err := Compare(pair[0], pair[1])
		if comparison != nil {
			t.Fatal("invalid input produced comparison")
		}
		assertCode(t, err, e.CodeUsage)
	}
	c, err := Compare(doc, doc)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Render("yaml")
	assertCode(t, err, e.CodeUsage)
	var missing *Comparison
	_, err = missing.Render("json")
	assertCode(t, err, e.CodeUsage)
}

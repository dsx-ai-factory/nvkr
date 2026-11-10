// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseview

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCompareInventoriesByExactOutputName(t *testing.T) {
	digest := func(value string) string { return "sha256:" + strings.Repeat(value, 64) }
	find := func(publication *delivery.PublicationDescription, name string) *delivery.PublicationOutput {
		for index := range publication.Outputs {
			if publication.Outputs[index].Name == name {
				return &publication.Outputs[index]
			}
		}
		t.Fatalf("fixture has no output %q", name)
		return nil
	}
	for _, test := range []struct {
		name           string
		mutate         func(*delivery.PublicationDescription)
		added, removed []string
		changed        map[string][]string
		referenceOnly  []string
		unchanged      int
	}{
		{name: "identical", mutate: func(*delivery.PublicationDescription) {}, unchanged: 6},
		{name: "added and removed", mutate: func(after *delivery.PublicationDescription) {
			find(after, "apt").Name = "apt-next"
		}, added: []string{"apt-next"}, removed: []string{"apt"}, unchanged: 5},
		{name: "equal Build Digests never pair different names", mutate: func(after *delivery.PublicationDescription) {
			find(after, "tools").Name = "tools-v2"
			find(after, "image").Name = "image-v2"
		}, added: []string{"image-v2", "tools-v2"}, removed: []string{"image", "tools"}, unchanged: 4},
		{name: "native digest", mutate: func(after *delivery.PublicationDescription) {
			find(after, "image").Digest = digest("9")
		}, changed: map[string][]string{"image": {"digest"}}, unchanged: 5},
		{name: "image child and platform set", mutate: func(after *delivery.PublicationDescription) {
			index := find(after, "multi").ImageIndex
			index.RequiredPlatforms = []string{"linux/amd64"}
			index.Children = index.Children[:1]
		}, changed: map[string][]string{"multi": {"imageIndex"}}, unchanged: 5},
		{name: "chart version", mutate: func(after *delivery.PublicationDescription) {
			find(after, "chart").ChartVersion = "8.3.0"
		}, changed: map[string][]string{"chart": {"chartVersion"}}, unchanged: 5},
		{name: "transport reference only", mutate: func(after *delivery.PublicationDescription) {
			output := find(after, "disk")
			output.Reference = strings.Replace(output.Reference, "registry.example/", "mirror.example/", 1)
		}, changed: map[string][]string{"disk": {"reference"}}, referenceOnly: []string{"disk"}, unchanged: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, after := descriptionFixture(), descriptionFixture()
			test.mutate(&after)
			comparison, err := Compare(before, after)
			if err != nil {
				t.Fatal(err)
			}
			names := func(outputs []Output) []string {
				result := []string{}
				for _, output := range outputs {
					result = append(result, output.Name)
				}
				return result
			}
			changed, referenceOnly := map[string][]string{}, []string{}
			for _, change := range comparison.Changed {
				changed[change.Name] = change.Fields
				if change.ReferenceOnly {
					referenceOnly = append(referenceOnly, change.Name)
				}
				if reflect.DeepEqual(change.Before, change.After) {
					t.Fatalf("change %q lost its complete before/after values", change.Name)
				}
			}
			want := func(values []string) []string {
				if values == nil {
					return []string{}
				}
				return values
			}
			if test.changed == nil {
				test.changed = map[string][]string{}
			}
			if !reflect.DeepEqual(names(comparison.Added), want(test.added)) || !reflect.DeepEqual(names(comparison.Removed), want(test.removed)) ||
				!reflect.DeepEqual(changed, test.changed) || !reflect.DeepEqual(referenceOnly, want(test.referenceOnly)) || comparison.Unchanged != test.unchanged {
				t.Fatalf("comparison = added %v removed %v changed %v referenceOnly %v unchanged %d",
					names(comparison.Added), names(comparison.Removed), changed, referenceOnly, comparison.Unchanged)
			}
			if comparison.Equal() != (test.name == "identical") {
				t.Fatalf("Equal() = %v", comparison.Equal())
			}
		})
	}
}

func TestCompareJSONStatesMetadataOnlyScope(t *testing.T) {
	after := descriptionFixture()
	after.Outputs = after.Outputs[1:]
	comparison, err := Compare(descriptionFixture(), after)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := comparison.Render("json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["schemaVersion"] != ComparisonSchemaVersion || got["scope"] != "artifact-inventory" ||
		!reflect.DeepEqual(got["notCompared"], []any{"deployment-compatibility", "evidence-documents", "policy-acceptance", "signatures"}) ||
		got["inspectionDetails"].(map[string]any)["scope"] != "metadata-only" {
		t.Fatalf("comparison hid its scope: %s", raw)
	}
	if strings.Contains(string(raw), ": null") {
		t.Fatalf("empty collections must be arrays: %s", raw)
	}
	text, err := comparison.Render("text")
	if err != nil || !strings.Contains(string(text), "Not compared: deployment-compatibility, evidence-documents, policy-acceptance, signatures\n") ||
		!strings.Contains(string(text), "Added: 0, removed: 1, changed: 0 (0 reference only), unchanged: 5\n") || !strings.Contains(string(text), "\n- apt (apt-repository)") {
		t.Fatalf("text comparison differs: %s, %v", text, err)
	}
	if _, err := comparison.Render("mermaid"); !isUsage(err) {
		t.Fatalf("unsupported comparison format: %v", err)
	}
}

func TestCompareTextShowsFieldValues(t *testing.T) {
	after := descriptionFixture()
	after.Outputs[1].ChartVersion = "8.3.0"
	after.Outputs[2].Reference = "mirror.example/product/disk@" + after.Outputs[2].Digest
	comparison, err := Compare(descriptionFixture(), after)
	if err != nil {
		t.Fatal(err)
	}
	text, err := comparison.Render("text")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"~ chart: chartVersion\n  chartVersion: 8.2.1 -> 8.3.0\n", "~ disk: derived reference only\n", "changed: 2 (1 reference only)"} {
		if !strings.Contains(string(text), fragment) {
			t.Fatalf("text comparison omitted %q: %s", fragment, text)
		}
	}
}

func TestCompareRejectsRepeatedOutputNames(t *testing.T) {
	after := descriptionFixture()
	after.Outputs = append(after.Outputs, after.Outputs[0])
	comparison, err := Compare(descriptionFixture(), after)
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed || !reflect.DeepEqual(comparison, Comparison{}) {
		t.Fatalf("repeated output name produced a comparison: %+v, %v", comparison, err)
	}
}

func isUsage(err error) bool {
	var coded *e.Error
	return errors.As(err, &coded) && coded.Code == e.CodeUsage
}

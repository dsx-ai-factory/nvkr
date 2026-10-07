package factory

import (
	"os"
	"slices"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestMergeGateActionlintConfigurationTriggersCheck(t *testing.T) {
	raw, err := os.ReadFile("../../../.github/workflows/merge-gate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				ID   string `json:"id"`
				With struct {
					Filters string `json:"filters"`
				} `json:"with"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, step := range workflow.Jobs["check-paths"].Steps {
		if step.ID != "changes" {
			continue
		}
		var filters map[string][]string
		if err := yaml.Unmarshal([]byte(step.With.Filters), &filters); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(filters["actions"], ".github/actionlint.yaml") {
			t.Fatal("actionlint config-only changes skip workflow lint")
		}
		return
	}
	t.Fatal("missing PR path classifier")
}

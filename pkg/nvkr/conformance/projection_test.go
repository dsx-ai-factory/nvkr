// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

func partialProjection() testresults.Output {
	return testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CapturePartial, Producer: testresults.ProducerIdentity{Name: "adapter", Revision: testresults.Metadata{Status: "unknown"}}, Suite: testresults.SuiteSelection{Name: "suite", Revision: testresults.Metadata{Status: "unknown"}, Selection: []string{"full"}}}
}
func TestProjectionBudgetRefusesBeforeHarness(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		edit       func(*Options)
	}{
		{"500 nodes two phases", "node-budget", func(*Options) {}},
		{"cumulative replicas", "success", func(o *Options) {
			o.Targets[0].RequiredReplicas = 3000
			other := o.Targets[0]
			other.LabelSelector = "app=other"
			o.Targets = append(o.Targets, other)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := buildFixture(t, tc.mode)
			tc.edit(&o)
			output, err := produce(t, o)
			requireCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), "projection exceeds") {
				t.Fatalf("wrong refusal %v", err)
			}
			if output.CaptureStatus != testresults.CapturePartial || len(output.Reports) != 0 {
				t.Fatal("budget refusal fabricated capture")
			}
			if _, err = testresults.Encode(output); err != nil {
				t.Fatalf("refusal lost encodable partial: %v", err)
			}
			if _, err = os.Stat(filepath.Join(filepath.Dir(o.Kubeconfig.Path), "state.json.startup")); !os.IsNotExist(err) {
				t.Fatalf("budget refusal started harness: %v", err)
			}
			if _, err = os.Stat(filepath.Join(filepath.Dir(o.Kubeconfig.Path), "state.json")); !os.IsNotExist(err) {
				t.Fatalf("budget refusal created suite resources: %v", err)
			}
		})
	}
}
func TestTerminalProjectionBudgetRetainsReport(t *testing.T) {
	o := buildFixture(t, "terminal-budget")
	output, err := produce(t, o)
	requireCode(t, err, e.CodeEngineCapability)
	if output.CaptureStatus != testresults.CapturePartial || len(output.Reports) != 1 {
		t.Fatal("terminal budget refusal lost capture")
	}
	if _, err = testresults.Encode(output); err != nil {
		t.Fatalf("partial is not encodable: %v", err)
	}
	if _, err = os.ReadFile(output.Reports[0].Reference); err != nil {
		t.Fatal("original report lost")
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(o.Kubeconfig.Path), "state.json.delete")); err != nil {
		t.Fatal("terminal refusal abandoned cleanup")
	}
}
func TestProjectionByteBudgetAndInvalidPhasePreserveLastValidPartial(t *testing.T) {
	output := partialProjection()
	before := make([]testresults.EnvironmentFact, 4000)
	for i := range before {
		before[i] = testresults.EnvironmentFact{Scope: "deployment", Name: fmt.Sprintf("replica.%d", i), Phase: "before", Metadata: testresults.Metadata{Status: "observed", Value: strings.Repeat("x", 2300)}}
	}
	if err := appendProjection(&output, before); err != nil {
		t.Fatal(err)
	}
	err := admitTerminalProjection(output, before, nil, "/original.xml")
	requireCode(t, err, e.CodeEngineCapability)
	if !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("wrong budget boundary: %v", err)
	}
	for _, facts := range [][]testresults.EnvironmentFact{before, {{Scope: "cluster", Name: "version", Phase: "after", Metadata: testresults.Metadata{Status: "observed", Value: strings.Repeat("x", 4097)}}}} {
		err = appendProjection(&output, facts)
		requireCode(t, err, e.CodeEngineCapability)
		if len(output.Environment) != len(before) {
			t.Fatal("rejected phase replaced last valid projection")
		}
		if _, err = testresults.Encode(output); err != nil {
			t.Fatalf("retained partial unencodable: %v", err)
		}
	}
}

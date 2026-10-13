// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testresults

import (
	"fmt"
	"strings"
	"testing"
)

func TestJUnitOutcomeDiagnosticAttributesHaveTextBudget(t *testing.T) {
	for _, outcome := range []string{"failure", "error", "skipped"} {
		t.Run(outcome, func(t *testing.T) {
			raw := fmt.Sprintf(`<testsuite name="s"><testcase name="case"><%s message="%s" type="%s"/></testcase></testsuite>`, outcome, strings.Repeat("x", 5000), strings.Repeat("y", 5000))
			report, err := ParseJUnit(t.Context(), []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if report.Summary.Total != 1 || report.Summary.Passed != 0 {
				t.Fatalf("lost diagnostic outcome: %+v", report)
			}
			raw = fmt.Sprintf(`<testsuite name="s"><testcase name="case"><%s message="%s"/></testcase></testsuite>`, outcome, strings.Repeat("x", maxTextBytes+1))
			_, err = ParseJUnit(t.Context(), []byte(raw))
			if err == nil || !strings.Contains(err.Error(), "budget") {
				t.Fatalf("unbounded diagnostic: %v", err)
			}
		})
	}
}

func TestJUnitGinkgoSuiteNodesRetainCountsWithoutTestIdentities(t *testing.T) {
	raw := `<testsuite name="suite" tests="3"><testcase classname="suite" name="[It] selected" status="passed"/><testcase classname="suite" name="[ReportBeforeSuite]" status="passed"/><testcase classname="suite" name="[ReportBeforeSuite]" status="passed"/></testsuite>`
	report, err := ParseJUnit(t.Context(), []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	hooks := 0
	for _, c := range report.Cases {
		if c.SuiteNode {
			hooks++
		}
	}
	if hooks != 2 || report.Summary != (Summary{Total: 3, Passed: 3}) {
		t.Fatalf("suite nodes=%d report=%+v", hooks, report)
	}
	failed := strings.Replace(raw, `tests="3"`, `tests="3" failures="1"`, 1)
	failed = strings.Replace(failed, `name="[ReportBeforeSuite]" status="passed"/>`, `name="[ReportBeforeSuite]" status="failed"><failure/></testcase>`, 1)
	report, err = ParseJUnit(t.Context(), []byte(failed))
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Failed != 1 {
		t.Fatal("failed hook was hidden")
	}
	for _, ordinary := range []string{"[It] selected", "ordinary"} {
		raw := fmt.Sprintf(`<testsuite name="suite"><testcase classname="suite" name="%s" status="passed"/><testcase classname="suite" name="%s" status="passed"/></testsuite>`, ordinary, ordinary)
		_, err := ParseJUnit(t.Context(), []byte(raw))
		if err == nil || !strings.Contains(err.Error(), "duplicate case identity") {
			t.Fatalf("ordinary duplicate admitted: %v", err)
		}
	}
}

func TestJUnitDuplicateUninterpretedCaseProperties(t *testing.T) {
	raw := `<testsuite name="s"><testcase name="a"><properties><property name="entry" value="one"/><property name="entry" value="two"/></properties></testcase></testsuite>`
	if _, err := ParseJUnit(t.Context(), []byte(raw)); err != nil {
		t.Fatal(err)
	}
	raw = `<testsuite name="s"><properties><property name="DryRun" value="false"/><property name="DryRun" value="true"/></properties><testcase name="a"/></testsuite>`
	_, err := ParseJUnit(t.Context(), []byte(raw))
	if err == nil || !strings.Contains(err.Error(), "duplicate property") {
		t.Fatal("duplicate control property admitted")
	}
}

func TestJUnitGinkgoTerminalStatusShapes(t *testing.T) {
	for _, tc := range []struct {
		status, outcome, kind string
		expected              Status
	}{
		{"timedout", "failure", "timedout", Failed}, {"panicked", "error", "panicked", Error}, {"interrupted", "error", "interrupted", Error}, {"aborted", "failure", "aborted", Error},
	} {
		t.Run(tc.status, func(t *testing.T) {
			raw := fmt.Sprintf(`<testsuite name="s"><testcase name="a" status="%s"><%s type="%s"/></testcase></testsuite>`, tc.status, tc.outcome, tc.kind)
			report, err := ParseJUnit(t.Context(), []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if report.Cases[0].Status != tc.expected {
				t.Fatalf("status=%s", report.Cases[0].Status)
			}
		})
	}
	for _, raw := range []string{`<testsuite name="s" tests=""><testcase name="a"/></testsuite>`, `<testsuite name="s"><properties><property name="SuiteSucceeded" value="perhaps"/></properties><testcase name="a"/></testsuite>`} {
		_, err := ParseJUnit(t.Context(), []byte(raw))
		if err == nil {
			t.Fatal("invalid count/success accepted")
		}
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testresults

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestParseJUnitProfiles(t *testing.T) {
	tests := []struct {
		name, raw string
		summary   Summary
	}{
		{"empty suite", `<testsuite name="s" tests="0" failures="0" errors="0"/>`, Summary{}},
		{"empty suites", `<testsuites tests="0"/>`, Summary{}},
		{"single suite", `<testsuite name="suite" tests="2" failures="1"><testcase classname="c" name="z"/><testcase classname="c" name="a"><failure message="bad">trace</failure></testcase></testsuite>`, Summary{Total: 2, Passed: 1, Failed: 1}},
		{"separate error and skip", `<testsuites tests="3" errors="1" failures="0" skipped="1"><testsuite name="suite"><testcase name="pass"/><testcase name="skip"><skipped/></testcase><testcase name="error"><error>panic</error></testcase></testsuite></testsuites>`, Summary{Total: 3, Passed: 1, Errors: 1, Skipped: 1}},
		// Representative original fixture based on Ginkgo reporters/junit_report.go
		// field layout; it is not copied upstream source or external run evidence.
		{"ginkgo pending skipped aborted", `<?xml version="1.0" encoding="UTF-8"?><testsuites tests="4" disabled="2" errors="1" failures="0" time="1.2"><testsuite name="Kubernetes e2e suite" package="k8s.io/kubernetes/test/e2e" tests="4" disabled="1" skipped="1" errors="1" failures="0" time="1.2" timestamp="2026-10-13T00:00:00"><properties><property name="SuiteSucceeded" value="false"/><property name="DryRun" value="false"/></properties><testcase name="[It] pass" classname="Kubernetes e2e suite" status="passed" time="0.1"><system-out>output</system-out><system-err><![CDATA[timeline & detail]]></system-err><properties><property name="entry" value="{}"/></properties></testcase><testcase name="[It] pending" status="pending"><skipped message="pending"/></testcase><testcase name="[It] skip" status="skipped"><skipped message="skip"/></testcase><testcase name="[It] abort" status="aborted"><failure type="aborted">aborted</failure></testcase></testsuite></testsuites>`, Summary{Total: 4, Passed: 1, Errors: 1, Skipped: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(tt.raw)
			before := append([]byte(nil), raw...)
			got, err := ParseJUnit(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if got.Summary != tt.summary {
				t.Fatalf("summary = %#v want %#v", got.Summary, tt.summary)
			}
			if !reflect.DeepEqual(raw, before) {
				t.Fatal("original bytes changed")
			}
			again, err := ParseJUnit(context.Background(), raw)
			if err != nil || !reflect.DeepEqual(again, got) {
				t.Fatalf("nondeterminism: %v", err)
			}
		})
	}
}
func TestParseJUnitRejects(t *testing.T) {
	valid := `<testsuite name="s"><testcase name="a"/></testsuite>`
	tests := []struct{ name, raw string }{
		{"two declarations", `<?xml version="1.0"?><?xml version="1.0"?><testsuite name="s"><testcase name="a"/></testsuite>`},
		{"contradictory property body", `<testsuite name="s"><properties><property name="DryRun" value="false">true</property></properties><testcase name="a"/></testsuite>`},
		{"empty", ``},
		{"truncated", `<testsuite name="s"><testcase name="a">`},
		{"unknown root", `<root name="s"><testcase name="a"/></root>`},
		{"two roots", valid + valid},
		{"namespace", `<testsuite xmlns="urn:test" name="s"><testcase name="a"/></testsuite>`},
		{"DTD", `<!DOCTYPE testsuite [<!ENTITY x SYSTEM "file:///etc/passwd">]>` + valid},
		{"entity", `<testsuite name="s"><testcase name="&unknown;"/></testsuite>`},
		{"duplicate attribute", `<testsuite name="s" name="z"><testcase name="a"/></testsuite>`},
		{"hidden outcome attr", `<testsuite name="s"><testcase name="a" result="failed"/></testsuite>`},
		{"contradictory status", `<testsuite name="s"><testcase name="a" status="failed"/></testsuite>`},
		{"unknown status", `<testsuite name="s"><testcase name="a" status="notrun"/></testsuite>`},
		{"two outcomes", `<testsuite name="s"><testcase name="a"><failure/><skipped/></testcase></testsuite>`},
		{"repeated outcome", `<testsuite name="s"><testcase name="a"><failure/><failure/></testcase></testsuite>`},
		{"duplicates", `<testsuite name="s"><testcase name="a"/><testcase name="a"/></testsuite>`},
		{"duplicates across suites", `<testsuites>` + valid + valid + `</testsuites>`},
		{"nested suite", `<testsuite name="s">` + valid + `</testsuite>`},
		{"hidden case", `<testsuite name="s"><system-out><testcase name="a"/></system-out></testsuite>`},
		{"false claimed tests", `<testsuite name="s" tests="2"><testcase name="a"/></testsuite>`},
		{"false claimed failures", `<testsuite name="s" failures="0"><testcase name="a"><failure/></testcase></testsuite>`},
		{"root false failures", `<testsuites failures="0"><testsuite name="s"><testcase name="a"><failure/></testcase></testsuite></testsuites>`},
		{"fraction", `<testsuite name="s" tests="1.0"><testcase name="a"/></testsuite>`},
		{"exponent", `<testsuite name="s" tests="1e0"><testcase name="a"/></testsuite>`},
		{"negative", `<testsuite name="s" tests="-1"><testcase name="a"/></testsuite>`},
		{"overflow", `<testsuite name="s" tests="999999999999999999999999999999"><testcase name="a"/></testsuite>`},
		{"blank suite", `<testsuite name=" "><testcase name="a"/></testsuite>`},
		{"blank case", `<testsuite name="s"><testcase name=" "/></testsuite>`},
		{"property body cannot hide verdict", `<testsuite name="s"><properties><property name="DryRun">true</property></properties><testcase name="a"/></testsuite>`},
		{"focus", `<testsuite name="s"><properties><property name="SuiteHasProgrammaticFocus" value="true"/></properties><testcase name="a"/></testsuite>`},
		{"dryrun", `<testsuite name="s"><properties><property name="DryRun" value="true"/></properties><testcase name="a"/></testsuite>`},
		{"suite failure", `<testsuite name="s"><properties><property name="SuiteSucceeded" value="false"/></properties><testcase name="a"/></testsuite>`},
		{"suite false pass", `<testsuite name="s"><properties><property name="SuiteSucceeded" value="true"/></properties><testcase name="a"><failure/></testcase></testsuite>`},
		{"special failure", `<testsuite name="s"><properties><property name="SpecialSuiteFailureReason" value="focus"/></properties><testcase name="a"/></testsuite>`},
		{"nested outcome", `<testsuite name="s"><testcase name="a"><failure><error/></failure></testcase></testsuite>`},
		{"PI within suite", `<testsuite name="s"><?process data?><testcase name="a"/></testsuite>`},
		{"trailing text", valid + `x`},
		{"name budget", `<testsuite name="s"><testcase name="` + strings.Repeat("a", maxValueBytes+1) + `"/></testsuite>`},
		{"suite budget", `<testsuites>` + strings.Repeat(`<testsuite name="s"/>`, MaxSuites+1) + `</testsuites>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseJUnit(context.Background(), []byte(tt.raw))
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("error = %v", err)
			}
			if !reflect.DeepEqual(got, Report{}) {
				t.Fatal("partial report returned")
			}
		})
	}
}
func TestParseJUnitContextAndSorting(t *testing.T) {
	//nolint:staticcheck // Exercise the explicit nil-context admission contract.
	_, nilErr := ParseJUnit(nil, []byte(`<testsuites tests="0"/>`))
	var nilCoded *e.Error
	if !errors.As(nilErr, &nilCoded) || nilCoded.Code != e.CodeUsage {
		t.Fatalf("nil context = %v", nilErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ParseJUnit(ctx, []byte(`<testsuite name="s"><testcase name="a"/></testsuite>`))
	var canceled *e.Error
	if !errors.As(err, &canceled) || canceled.Code != e.CodeTimeout || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	got, err := ParseJUnit(context.Background(), []byte(`<testsuites><testsuite name="z"><testcase name="b"/></testsuite><testsuite name="a"><testcase classname="z" name="z"/><testcase classname="a" name="a"/></testsuite></testsuites>`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Cases[0].Suite != "a" || got.Cases[0].Class != "a" || got.Cases[2].Suite != "z" {
		t.Fatalf("cases not sorted: %#v", got.Cases)
	}
}

func TestParseJUnitBudgets(t *testing.T) {
	tests := []struct {
		name   string
		raw    func() []byte
		reason string
	}{
		{"bytes", func() []byte { return make([]byte, MaxBytes+1) }, "byte budget"},
		{"text", func() []byte {
			return []byte(`<testsuite name="s"><testcase name="a"><system-out>` + strings.Repeat("x", maxTextBytes+1) + `</system-out></testcase></testsuite>`)
		}, "text budget"},
		{"cases", func() []byte {
			var b strings.Builder
			b.WriteString(`<testsuite name="s">`)
			for i := 0; i <= MaxCases; i++ {
				b.WriteString(`<testcase name="`)
				b.WriteString(strconv.Itoa(i))
				b.WriteString(`"/>`)
			}
			b.WriteString(`</testsuite>`)
			return []byte(b.String())
		}, "case budget"},
		{"tokens", func() []byte {
			return []byte(strings.Repeat(`<!--x-->`, maxTokens) + `<testsuite name="s"><testcase name="a"/></testsuite>`)
		}, "token budget"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseJUnit(context.Background(), tt.raw())
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("expected %s, got %v", tt.reason, err)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package testresults projects a bounded, explicit JUnit profile into descriptive
// counts. Parsing supplies no authentication, deployment binding or qualification.
// XML syntax belongs to encoding/xml; unsupported profiles fail closed.
package testresults

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	MaxBytes      = 64 << 20
	MaxCases      = 100000
	MaxSuites     = 1024
	maxTokens     = 1000000
	maxValueBytes = 4096
	maxTextBytes  = 16 << 20
)

type Status string

const (
	Passed  Status = "passed"
	Failed  Status = "failed"
	Error   Status = "error"
	Skipped Status = "skipped"
)

type Summary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errors  int `json:"errors"`
	Skipped int `json:"skipped"`
}
type Case struct {
	Suite  string `json:"suite"`
	Class  string `json:"class"`
	Name   string `json:"name"`
	Status Status `json:"status"`
}
type Report struct {
	Summary Summary `json:"summary"`
	Cases   []Case  `json:"cases"`
}
type parser struct {
	ctx            context.Context
	decoder        *xml.Decoder
	tokens, suites int
	report         Report
	identities     map[[3]string]bool
}

// ParseJUnit supports one testsuite or testsuites containing flat named suites,
// named cases, properties, system-out/system-err and one outcome per case. The
// Ginkgo status/disabled conventions are admitted explicitly; no universal JUnit
// schema is implied. Original bytes and diagnostics are not modified or retained.
func ParseJUnit(ctx context.Context, raw []byte) (Report, error) {
	if ctx == nil {
		return Report{}, e.New(e.CodeUsage, "JUnit requires a non-nil context")
	}
	if len(raw) == 0 || len(raw) > MaxBytes {
		return Report{}, invalid("byte budget")
	}
	p := parser{ctx: ctx, decoder: xml.NewDecoder(bytes.NewReader(raw)), identities: make(map[[3]string]bool)}
	started := false
	declaration := false
	for {
		token, err := p.token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Report{}, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if started {
				return Report{}, invalid("multiple roots")
			}
			started = true
			switch t.Name.Local {
			case "testsuite":
				_, _, err = p.suite(t)
			case "testsuites":
				err = p.container(t)
			default:
				err = invalid("unsupported root")
			}
			if err != nil {
				return Report{}, err
			}
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return Report{}, invalid("text outside root")
			}
		case xml.ProcInst:
			if started || declaration || t.Target != "xml" {
				return Report{}, invalid("processing instruction")
			}
			declaration = true
		}
	}
	if !started {
		return Report{}, invalid("no test cases")
	}
	sort.Slice(p.report.Cases, func(i, j int) bool {
		a, b := p.report.Cases[i], p.report.Cases[j]
		if a.Suite != b.Suite {
			return a.Suite < b.Suite
		}
		if a.Class != b.Class {
			return a.Class < b.Class
		}
		return a.Name < b.Name
	})
	return p.report, nil
}
func invalid(reason string) error {
	return e.New(e.CodeVerifyFailed, "JUnit profile rejected: %s", reason)
}
func (p *parser) token() (xml.Token, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "JUnit parsing canceled")
	}
	p.tokens++
	if p.tokens > maxTokens {
		return nil, invalid("token budget")
	}
	t, err := p.decoder.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, invalid("malformed XML")
	}
	switch v := t.(type) {
	case xml.Directive:
		return nil, invalid("XML directive")
	case xml.StartElement:
		if v.Name.Space != "" || len(v.Name.Local) > maxValueBytes {
			return nil, invalid("element namespace or name")
		}
		seen := map[string]bool{}
		for _, a := range v.Attr {
			if a.Name.Space != "" || seen[a.Name.Local] || len(a.Value) > maxValueBytes {
				return nil, invalid("attribute namespace, duplicate or budget")
			}
			seen[a.Name.Local] = true
		}
	case xml.CharData:
		if len(v) > maxTextBytes {
			return nil, invalid("text budget")
		}
	}
	return t, nil
}
func attributes(start xml.StartElement, allowed string) (map[string]string, error) {
	out := map[string]string{}
	for _, a := range start.Attr {
		if !strings.Contains(" "+allowed+" ", " "+a.Name.Local+" ") {
			return nil, invalid("unsupported attribute")
		}
		out[a.Name.Local] = a.Value
	}
	return out, nil
}
func (p *parser) container(start xml.StartElement) error {
	attrs, err := attributes(start, "name tests failures errors skipped disabled time")
	if err != nil {
		return err
	}
	for {
		t, err := p.token()
		if err != nil {
			return err
		}
		switch v := t.(type) {
		case xml.StartElement:
			if v.Name.Local != "testsuite" {
				return invalid("unexpected container child")
			}
			if _, _, err = p.suite(v); err != nil {
				return err
			}
		case xml.EndElement:
			return totals(attrs, p.report.Summary, p.report.Summary.Skipped, true)
		case xml.CharData:
			if strings.TrimSpace(string(v)) != "" {
				return invalid("container text")
			}
		case xml.ProcInst:
			return invalid("processing instruction")
		}
	}
}
func (p *parser) suite(start xml.StartElement) (Summary, int, error) {
	var summary Summary
	pending := 0
	attrs, err := attributes(start, "name tests failures errors skipped disabled time timestamp hostname package id")
	if err != nil {
		return summary, 0, err
	}
	name := attrs["name"]
	if strings.TrimSpace(name) == "" {
		return summary, 0, invalid("unnamed suite")
	}
	p.suites++
	if p.suites > MaxSuites {
		return summary, 0, invalid("suite budget")
	}
	properties := map[string]string{}
	for {
		t, err := p.token()
		if err != nil {
			return summary, 0, err
		}
		switch v := t.(type) {
		case xml.StartElement:
			switch v.Name.Local {
			case "testcase":
				c, isPending, err := p.testcase(v, name)
				if err != nil {
					return summary, 0, err
				}
				key := [3]string{c.Suite, c.Class, c.Name}
				if p.identities[key] {
					return summary, 0, invalid("duplicate case identity")
				}
				p.identities[key] = true
				if len(p.report.Cases) >= MaxCases {
					return summary, 0, invalid("case budget")
				}
				p.report.Cases = append(p.report.Cases, c)
				add(&summary, c.Status)
				add(&p.report.Summary, c.Status)
				if isPending {
					pending++
				}
			case "properties":
				if err = p.properties(v, properties); err != nil {
					return summary, 0, err
				}
			case "system-out", "system-err":
				if err = p.text(v); err != nil {
					return summary, 0, err
				}
			default:
				return summary, 0, invalid("unexpected suite child")
			}
		case xml.EndElement:
			if properties["DryRun"] != "" && properties["DryRun"] != "false" {
				return summary, 0, invalid("dry-run suite")
			}
			if value, ok := properties["SuiteSucceeded"]; ok {
				if value != "true" && value != "false" {
					return summary, 0, invalid("invalid suite success property")
				}
				if value == "false" && summary.Failed+summary.Errors == 0 {
					return summary, 0, invalid("suite failure missing case outcome")
				}
				if value == "true" && summary.Failed+summary.Errors > 0 {
					return summary, 0, invalid("contradictory suite success")
				}
			}
			if value, ok := properties["SuiteHasProgrammaticFocus"]; ok && value != "false" {
				return summary, 0, invalid("programmatic focus")
			}
			if properties["SpecialSuiteFailureReason"] != "" {
				return summary, 0, invalid("special suite failure requires unsupported suite-level outcome")
			}
			return summary, pending, totals(attrs, summary, pending, false)
		case xml.CharData:
			if strings.TrimSpace(string(v)) != "" {
				return summary, 0, invalid("suite text")
			}
		case xml.ProcInst:
			return summary, 0, invalid("processing instruction")
		}
	}
}
func (p *parser) testcase(start xml.StartElement, suite string) (Case, bool, error) {
	attrs, err := attributes(start, "name classname status time owner assertions file line")
	if err != nil {
		return Case{}, false, err
	}
	c := Case{Suite: suite, Class: attrs["classname"], Name: attrs["name"], Status: Passed}
	if strings.TrimSpace(c.Name) == "" {
		return c, false, invalid("unnamed case")
	}
	outcome := ""
	properties := map[string]string{}
	for {
		t, err := p.token()
		if err != nil {
			return c, false, err
		}
		switch v := t.(type) {
		case xml.StartElement:
			switch v.Name.Local {
			case "skipped", "failure", "error":
				if outcome != "" {
					return c, false, invalid("multiple case outcomes")
				}
				outcome = v.Name.Local
				a, err := attributes(v, "message type")
				if err != nil {
					return c, false, err
				}
				switch outcome {
				case "skipped":
					c.Status = Skipped
				case "failure":
					c.Status = Failed
					if a["type"] == "aborted" {
						c.Status = Error
					}
				case "error":
					c.Status = Error
				}
				if err = p.textBody(v); err != nil {
					return c, false, err
				}
			case "system-out", "system-err":
				if err = p.text(v); err != nil {
					return c, false, err
				}
			case "properties":
				if err = p.properties(v, properties); err != nil {
					return c, false, err
				}
			default:
				return c, false, invalid("unexpected case child")
			}
		case xml.EndElement:
			status := attrs["status"]
			if status != "" {
				expected := Passed
				switch status {
				case "passed":
				case "failed", "timedout":
					expected = Failed
				case "panicked", "interrupted", "aborted":
					expected = Error
				case "pending", "skipped":
					expected = Skipped
				default:
					return c, false, invalid("unsupported case status")
				}
				if expected != c.Status {
					return c, false, invalid("contradictory case status")
				}
			}
			return c, status == "pending", nil
		case xml.CharData:
			if strings.TrimSpace(string(v)) != "" {
				return c, false, invalid("case text")
			}
		case xml.ProcInst:
			return c, false, invalid("processing instruction")
		}
	}
}
func (p *parser) text(start xml.StartElement) error {
	if _, err := attributes(start, ""); err != nil {
		return err
	}
	return p.textBody(start)
}
func (p *parser) textBody(_ xml.StartElement) error {
	size := 0
	for {
		t, err := p.token()
		if err != nil {
			return err
		}
		switch v := t.(type) {
		case xml.EndElement:
			return nil
		case xml.CharData:
			size += len(v)
			if size > maxTextBytes {
				return invalid("text budget")
			}
		case xml.Comment:
		default:
			return invalid("nested text content")
		}
	}
}
func (p *parser) properties(start xml.StartElement, values map[string]string) error {
	if _, err := attributes(start, ""); err != nil {
		return err
	}
	for {
		t, err := p.token()
		if err != nil {
			return err
		}
		switch v := t.(type) {
		case xml.StartElement:
			if v.Name.Local != "property" {
				return invalid("unexpected property child")
			}
			a, err := attributes(v, "name value")
			if err != nil {
				return err
			}
			if _, ok := a["value"]; !ok {
				return invalid("property requires explicit value attribute")
			}
			if a["name"] == "" {
				return invalid("unnamed property")
			}
			if _, exists := values[a["name"]]; exists {
				return invalid("duplicate property")
			}
			values[a["name"]] = a["value"]
			if err = p.propertyBody(); err != nil {
				return err
			}
		case xml.EndElement:
			return nil
		case xml.CharData:
			if strings.TrimSpace(string(v)) != "" {
				return invalid("properties text")
			}
		case xml.ProcInst:
			return invalid("processing instruction")
		}
	}
}
func (p *parser) propertyBody() error {
	for {
		token, err := p.token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.EndElement:
			return nil
		case xml.CharData:
			if strings.TrimSpace(string(value)) != "" {
				return invalid("property body is unsupported")
			}
		case xml.Comment:
		default:
			return invalid("nested property content")
		}
	}
}

func totals(attrs map[string]string, s Summary, pending int, root bool) error {
	expected := map[string]int{"tests": s.Total, "failures": s.Failed, "errors": s.Errors, "skipped": s.Skipped, "disabled": pending}
	// Ginkgo suite skipped excludes pending; testsuites disabled combines both.
	if !root {
		if _, hasDisabled := attrs["disabled"]; hasDisabled {
			expected["skipped"] -= pending
		}
	}
	for key, want := range expected {
		raw, exists := attrs[key]
		if !exists {
			continue
		}
		if raw == "" {
			return invalid("empty declared count")
		}
		for _, r := range raw {
			if r < '0' || r > '9' {
				return invalid("noninteger declared count")
			}
		}
		count, err := strconv.Atoi(raw)
		if err != nil || count != want {
			return invalid("contradictory declared count")
		}
	}
	return nil
}
func add(s *Summary, status Status) {
	s.Total++
	switch status {
	case Passed:
		s.Passed++
	case Failed:
		s.Failed++
	case Error:
		s.Errors++
	case Skipped:
		s.Skipped++
	}
}

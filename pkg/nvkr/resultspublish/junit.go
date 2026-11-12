// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package resultspublish

import (
	"encoding/xml"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
)

// manifestCase is the testcase for release manifest issuance.
const manifestCase = "release-manifest/issue"

type junitSuite struct {
	XMLName  xml.Name    `xml:"testsuite"`
	Name     string      `xml:"name,attr"`
	Tests    int         `xml:"tests,attr"`
	Failures int         `xml:"failures,attr"`
	Skipped  int         `xml:"skipped,attr"`
	Cases    []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name    string        `xml:"name,attr"`
	Failure *junitMessage `xml:"failure,omitempty"`
	Skipped *junitMessage `xml:"skipped,omitempty"`
}

// junitMessage carries its text as both the attribute and the body, because JUnit
// readers differ on which one they show.
type junitMessage struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

func message(text string) *junitMessage {
	text = qualificationoutcome.Scrub(text, nil)
	return &junitMessage{Message: text, Text: text}
}

// junitXML projects an outcome's typed fields, never its opaque result, into one
// TestGrid row per check and one for manifest issuance:
//   - a check is <output>/<check>, with its subject appended only where two checks
//     would otherwise share a row, since TestGrid merges same-named rows;
//   - not-applicable is skipped, and a failed check carries its scrubbed message;
//   - an errored outcome is one row named for its stage;
//   - a failed qualification with no failed check gets a qualify row for its
//     error, so the column says why it failed.
func junitXML(outcome qualificationoutcome.Outcome, definition string) ([]byte, error) {
	suite := junitSuite{Name: definition}
	if outcome.Status == qualificationoutcome.StatusErrored {
		suite.Cases = []junitCase{{Name: string(outcome.Stage), Failure: message(erroredText(outcome))}}
	} else {
		suite.Cases = checkCases(outcome.Checks)
		failedCheck := false
		for _, check := range outcome.Checks {
			failedCheck = failedCheck || check.Status == "fail"
		}
		switch {
		case outcome.Status == qualificationoutcome.StatusPassed:
			suite.Cases = append(suite.Cases, junitCase{Name: manifestCase})
		case outcome.Stage == qualificationoutcome.StageManifest:
			suite.Cases = append(suite.Cases, junitCase{Name: manifestCase, Failure: message(errorText(outcome.Error))})
		default:
			suite.Cases = append(suite.Cases, junitCase{Name: manifestCase, Skipped: message("not issued: qualification failed")})
			if !failedCheck {
				suite.Cases = append(suite.Cases, junitCase{Name: string(qualificationoutcome.StageQualify), Failure: message(errorText(outcome.Error))})
			}
		}
	}
	for _, row := range suite.Cases {
		suite.Tests++
		if row.Failure != nil {
			suite.Failures++
		}
		if row.Skipped != nil {
			suite.Skipped++
		}
	}
	raw, err := xml.MarshalIndent(suite, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(append([]byte(xml.Header), raw...), '\n'), nil
}

func checkCases(checks []qualificationoutcome.Check) []junitCase {
	names := map[string]int{}
	for _, check := range checks {
		names[check.Output+"/"+check.Check]++
	}
	cases := make([]junitCase, 0, len(checks))
	for _, check := range checks {
		row := junitCase{Name: check.Output + "/" + check.Check}
		if names[row.Name] > 1 && check.Subject != "" {
			row.Name += " " + check.Subject
		}
		text := check.Message
		if text == "" {
			text = check.Code
		}
		switch check.Status {
		case "fail":
			if text == "" {
				text = "check failed"
			}
			row.Failure = message(text)
		case "not-applicable":
			if text == "" {
				text = "not applicable"
			}
			row.Skipped = message(text)
		}
		cases = append(cases, row)
	}
	return cases
}

func erroredText(outcome qualificationoutcome.Outcome) string {
	switch {
	case outcome.Error != nil:
		return errorText(outcome.Error)
	case outcome.Stage == qualificationoutcome.StageProducer:
		return "the producer Run concluded " + outcome.Source.Conclusion + ", so nothing was qualified"
	default:
		return "the qualify job ended without recording an outcome"
	}
}

func errorText(failure *qualificationoutcome.Failure) string {
	if failure == nil {
		return "no error was recorded"
	}
	return failure.Code + ": " + failure.Message
}

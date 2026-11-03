// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationoutcome

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// MaxMessageBytes bounds every outcome message. TestGrid keeps only 140 bytes of a
// failure message, so the cut happens here, at a known place.
const MaxMessageBytes = 140

// MinSecretBytes is the shortest secret that can be redacted without erasing
// ordinary text. Callers refuse a shorter secret before any work.
const MinSecretBytes = 4

const (
	redacted = "[REDACTED]"
	// A cut keeps the head and the tail, because a wrapped error's cause comes last.
	cutMarker = " … "
	// Each pass only replaces matches or cuts, so a few passes reach a fixed point.
	maxScrubPasses = 8
)

var (
	gcsURLPattern    = regexp.MustCompile(`gs://[^\s"'<>]*`)
	principalPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	// Credentials that are not in the job's environment, such as an OIDC token
	// minted while signing, are found by shape: GitHub tokens and JWTs.
	credentialPattern = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})|eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
)

// Scrub prepares untrusted diagnostic text for an outcome. It redacts each secret
// value and each line of a multi-line one, GitHub tokens and JWTs, gs:// URLs and
// email-shaped principals, turns control characters and invalid UTF-8 into plain
// text, and keeps at most MaxMessageBytes: the head and the tail around a marker.
// A job log masks secrets, but an uploaded artifact does not. Scrub is
// idempotent, so Decode can require scrubbed text without knowing the secrets.
func Scrub(text string, secrets []string) string {
	text = scrubPass(strings.ToValidUTF8(text, "�"), secretTerms(secrets))
	// A cut can expose a match the whole text hid, such as a token that started
	// mid-word, so repeat until nothing changes.
	for range maxScrubPasses {
		next := scrubPass(text, nil)
		if next == text {
			break
		}
		text = next
	}
	return text
}

func scrubPass(text string, terms []string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, redact(text, terms))
	return abbreviate(strings.TrimSpace(text), MaxMessageBytes)
}

// RedactDocument applies Scrub's redactions to every string value in a JSON
// document and returns the document's canonical bytes. It neither cuts nor
// normalizes the remaining text, which is a complete diagnostic its producer also
// prints to the job log. Object member names are not redacted: they are schema
// names, never free text.
func RedactDocument(raw []byte, secrets []string) ([]byte, error) {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding a document for redaction")
	}
	return canonical.Marshal(redactValue(document, secretTerms(secrets)))
}

// redactedDocument reports whether raw is canonical and already free of every
// redaction RedactDocument applies without secrets.
func redactedDocument(raw []byte) bool {
	again, err := RedactDocument(raw, nil)
	return err == nil && bytes.Equal(again, raw)
}

func redactValue(value any, terms []string) any {
	switch typed := value.(type) {
	case string:
		return redact(typed, terms)
	case []any:
		for i := range typed {
			typed[i] = redactValue(typed[i], terms)
		}
		return typed
	case map[string]any:
		for key, nested := range typed {
			typed[key] = redactValue(nested, terms)
		}
		return typed
	default:
		return value
	}
}

func redact(text string, terms []string) string {
	for _, term := range terms {
		text = strings.ReplaceAll(text, term, redacted)
	}
	text = credentialPattern.ReplaceAllString(text, redacted)
	text = gcsURLPattern.ReplaceAllString(text, "gs://"+redacted)
	return principalPattern.ReplaceAllString(text, redacted)
}

// secretTerms lists each secret and each of its lines, trimmed (surrounding space
// is not secret), longest first, so a secret that contains another is redacted
// whole.
func secretTerms(secrets []string) []string {
	seen := map[string]bool{}
	var terms []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if len(value) >= MinSecretBytes && !seen[value] {
			seen[value] = true
			terms = append(terms, value)
		}
	}
	for _, secret := range secrets {
		add(secret)
		for _, line := range strings.Split(secret, "\n") {
			add(line)
		}
	}
	sort.Slice(terms, func(i, j int) bool {
		if len(terms[i]) != len(terms[j]) {
			return len(terms[i]) > len(terms[j])
		}
		return terms[i] < terms[j]
	})
	return terms
}

func scrubbed(text string) bool { return Scrub(text, nil) == text }

// abbreviate keeps the head and the tail of text within limit bytes, cutting on
// rune boundaries.
func abbreviate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	room := limit - len(cutMarker)
	head := room / 2
	for head > 0 && !utf8.RuneStart(text[head]) {
		head--
	}
	tail := len(text) - (room - room/2)
	for tail < len(text) && !utf8.RuneStart(text[tail]) {
		tail++
	}
	return strings.TrimSpace(text[:head]) + cutMarker + strings.TrimSpace(text[tail:])
}

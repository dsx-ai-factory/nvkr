// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationoutcome

import (
	"bytes"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestQualificationOutcomeScrub(t *testing.T) {
	secrets := []string{"sentinel-registry-password", "sentinel-line-one\nsentinel-line-two", " padded-secret ", "abc", ""}
	for _, tc := range []struct {
		name, text, want string
		absent           []string
	}{
		{"secret value", "login with sentinel-registry-password failed", "login with [REDACTED] failed", []string{"sentinel-registry-password"}},
		{"line of a multi-line secret", "key sentinel-line-two leaked", "key [REDACTED] leaked", []string{"sentinel-line-two"}},
		{"secret with surrounding space", "using padded-secret now", "using [REDACTED] now", []string{"padded-secret"}},
		{"bucket URL", "uploading gs://private-bucket/results/x.json failed", "uploading gs://[REDACTED] failed", []string{"private-bucket"}},
		{"email principal", "serviceAccount:publisher@project-123.iam.gserviceaccount.com denied", "serviceAccount:[REDACTED] denied", []string{"@", "project-123"}},
		{"basic-auth URL", "https://x-access-token:sentinel@github.com/x", "https://x-access-token:[REDACTED]/x", []string{"sentinel@"}},
		{"GitHub token", "token ghs_" + strings.Repeat("A1", 18) + " expired", "token [REDACTED] expired", []string{"ghs_"}},
		{"fine-grained token", "github_pat_" + strings.Repeat("b2_", 10) + " denied", "[REDACTED] denied", []string{"github_pat_"}},
		{"JWT", "identity eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJyZXBvIn0.c2lnbmF0dXJl refused", "identity [REDACTED] refused", []string{"eyJ"}},
		{"base64 that is not a JWT", "payload eyJfdHlwZSI6Imh0dHAifQ== kept", "payload eyJfdHlwZSI6Imh0dHAifQ== kept", nil},
		// Callers refuse a secret under MinSecretBytes before any work.
		{"values under the floor stay", "abc keeps its text", "abc keeps its text", nil},
		{"control characters", "first\nsecond\tthird\r", "first second third", nil},
		{"invalid UTF-8", "bad \xff byte", "bad � byte", nil},
		{"head and tail at a multibyte cut", strings.Repeat("a", MaxMessageBytes-1) + "é tail",
			strings.Repeat("a", 67) + cutMarker + strings.Repeat("a", 61) + "é tail", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Scrub(tc.text, secrets)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if len(got) > MaxMessageBytes || !utf8.ValidString(got) {
				t.Fatalf("scrubbed text is %d bytes or invalid UTF-8", len(got))
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Fatalf("%q survived scrubbing", absent)
				}
			}
			if Scrub(got, nil) != got {
				t.Fatal("Scrub is not idempotent, so Decode would refuse its output")
			}
		})
	}
}

// A wrapped error names its cause last, so the cut keeps both ends.
func TestQualificationOutcomeScrubKeepsHeadAndTail(t *testing.T) {
	got := Scrub("attaching qualification evidence for cli failed: "+strings.Repeat("retrying ", 30)+"last failure: 503 Service Unavailable", nil)
	if !strings.HasPrefix(got, "attaching qualification evidence") || !strings.HasSuffix(got, "503 Service Unavailable") ||
		!strings.Contains(got, cutMarker) || len(got) > MaxMessageBytes {
		t.Fatalf("got %q", got)
	}
	wide := Scrub(strings.Repeat("é", MaxMessageBytes), nil)
	if !utf8.ValidString(wide) || len(wide) > MaxMessageBytes || strings.Trim(wide, "é… ") != "" {
		t.Fatalf("multibyte cut produced %q", wide)
	}
}

// Every scrubbed text must scrub to itself, or Decode refuses outcomes that
// NewOutcome wrote. Random text built from the redaction patterns' pieces
// exercises cuts that land inside them.
func TestQualificationOutcomeScrubIsIdempotent(t *testing.T) {
	pieces := []string{"a", "Z", "9", " ", "\n", "é", "€", "@", ".", "-", "_", "/", ":", "[", "]", "gs://", "ghs_", "github_pat_",
		"eyJhbGciOiJ9", "eyJzdWIiOiJ9", "x@y.com", strings.Repeat("A1", 12), "[REDACTED]", "\xff"}
	random := rand.New(rand.NewPCG(279, 1))
	for range 20000 {
		var text strings.Builder
		for range random.IntN(120) {
			text.WriteString(pieces[random.IntN(len(pieces))])
		}
		once := Scrub(text.String(), nil)
		if len(once) > MaxMessageBytes || !utf8.ValidString(once) || Scrub(once, nil) != once {
			t.Fatalf("Scrub(%q) = %q is not a scrubbed fixed point", text.String(), once)
		}
	}
}

func TestQualificationOutcomeRedactDocument(t *testing.T) {
	secrets := []string{"sentinel-github-token", "sentinel-source-token"}
	subject := "ghcr.io/example/tool@sha256:" + strings.Repeat("e", 64)
	long := strings.Repeat("diagnostic ", 40)
	raw := encode(t, map[string]any{
		"checks":  []any{map[string]any{"message": "used sentinel-github-token\nthen gs://private-bucket/x"}},
		"nested":  map[string]any{"reason": "sentinel-source-token for publisher@project-123.iam.gserviceaccount.com", "size": 7},
		"oidc":    "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJyZXBvIn0.c2lnbmF0dXJl",
		"subject": subject,
		"text":    long,
	})
	redacted, err := RedactDocument(raw, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range append([]string{"private-bucket", "project-123", "eyJ"}, secrets...) {
		if bytes.Contains(redacted, []byte(absent)) {
			t.Fatalf("%s survived redaction", absent)
		}
	}
	// Other text stays whole: no cut, and newlines stay escaped newlines.
	if !bytes.Contains(redacted, []byte(`"size":7`)) || !bytes.Contains(redacted, []byte(subject)) || !bytes.Contains(redacted, []byte(long)) || !bytes.Contains(redacted, []byte(`\nthen`)) {
		t.Fatal("redaction changed text that needs no redaction")
	}
	if err := canonical.UnmarshalExact(redacted, new(map[string]any)); err != nil {
		t.Fatalf("redacted document is not canonical: %v", err)
	}
	clean := encode(t, map[string]any{"subject": subject, "text": long})
	unchanged, err := RedactDocument(clean, secrets)
	if err != nil || !bytes.Equal(unchanged, clean) {
		t.Fatalf("a canonical document with nothing to redact changed: %v", err)
	}
	_, err = RedactDocument([]byte(`{"a":1,"a":2}`), secrets)
	requireCode(t, err, e.CodeVerifyFailed)
}

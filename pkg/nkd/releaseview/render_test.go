// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseview

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestRenderKeepsOutputEvidenceMembershipAndMetadataScope(t *testing.T) {
	description := delivery.PublicationDescription{
		Reference: "registry.test/releases@sha256:" + strings.Repeat("a", 64), RootDigest: "sha256:" + strings.Repeat("a", 64),
		Outputs: []delivery.PublicationOutput{
			{Output: delivery.Output{Name: "first", Kind: "executable", Reference: "registry.test/first@sha256:exact"}},
			{Output: delivery.Output{Name: "second", Kind: "executable"}},
		},
		Evidence: []delivery.PublicationEvidence{
			{Name: "shared", Binding: delivery.EvidenceBinding{Outputs: []string{"first", "second"}, SubjectDigest: "sha256:exact", Stage: "build-output", SubjectKind: "file"}},
			{Name: "other", Binding: delivery.EvidenceBinding{Outputs: []string{"second"}}},
		},
	}
	for _, format := range []string{"", "text", "json", "mermaid"} {
		t.Run(format, func(t *testing.T) {
			raw, err := Render(description, Options{Format: format, Output: "first"})
			if err != nil || !strings.Contains(string(raw), "first") || !strings.Contains(string(raw), "signatures and policy not verified") {
				t.Fatalf("view loses scope or selected output: %s, %v", raw, err)
			}
			if strings.Contains(string(raw), "other") {
				t.Fatalf("filtered view includes unrelated evidence: %s", raw)
			}
			if !strings.Contains(string(raw), "metadata inspection; payloads, signatures and policy not verified") {
				t.Fatalf("view overclaims metadata inspection: %s", raw)
			}
			if format == "mermaid" && !strings.HasPrefix(string(raw), "flowchart TD\n  %% metadata inspection;") {
				t.Fatalf("invalid Mermaid scope comment: %s", raw)
			}
			if format == "json" {
				var got view
				if err := json.Unmarshal(raw, &got); err != nil || len(got.Outputs) != 1 || len(got.Evidence) != 1 || len(got.Evidence[0].Binding.Outputs) != 2 || got.TotalOutputs != 2 || got.SelectedOutput != "first" {
					t.Fatalf("filtered JSON hides shared membership or full scope: %s, %v", raw, err)
				}
			}
		})
	}
	if len(description.Outputs) != 2 || len(description.Evidence) != 2 {
		t.Fatal("render mutated its input")
	}
	for _, options := range []Options{{Format: "html"}, {Output: "unknown"}, {Output: "bad\x1b[31m"}} {
		raw, err := Render(description, options)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeUsage || raw != nil {
			t.Fatalf("bad presentation did not fail closed: %q, %v", raw, err)
		}
	}
}

func TestRenderEscapesUntrustedLabels(t *testing.T) {
	description := delivery.PublicationDescription{Reference: "registry\x1b[31m", Outputs: []delivery.PublicationOutput{{Output: delivery.Output{Name: "x\"]\nattack-->root<svg>"}}}}
	for _, format := range []string{"text", "json", "mermaid"} {
		raw, err := Render(description, Options{Format: format})
		if err != nil || strings.ContainsRune(string(raw), '\x1b') || strings.Contains(string(raw), "\nattack-->root") {
			t.Fatalf("unsafe %s rendering: %q, %v", format, raw, err)
		}
	}
}

func TestRenderShowsDiskEvidenceWithoutPolicyAuthority(t *testing.T) {
	for _, mode := range []string{"", "build-provenance-v1"} {
		for _, format := range []string{"text", "json", "mermaid"} {
			t.Run(mode+"/"+format, func(t *testing.T) {
				description := delivery.PublicationDescription{Outputs: []delivery.PublicationOutput{{Output: delivery.Output{Name: "disk", Kind: "disk-image", Disk: &delivery.DiskOutput{EvidenceMode: mode}}}}}
				raw, err := Render(description, Options{Format: format})
				if err != nil {
					t.Fatal(err)
				}
				want := mode
				if mode == "" {
					want = "inspected"
				}
				if format != "json" && !strings.Contains(string(raw), want) {
					t.Fatalf("disk evidence mode hidden: %s", raw)
				}
				if !strings.Contains(string(raw), "signatures and policy not verified") {
					t.Fatalf("view overclaims authority: %s", raw)
				}
				if format == "json" {
					var decoded view
					if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Outputs[0].Disk.EvidenceMode != mode {
						t.Fatalf("JSON loses original evidence mode: %s / %v", raw, err)
					}
				}
			})
		}
	}
}

func TestRenderJSONEscapesUnicodeControlsWithoutChangingValues(t *testing.T) {
	for _, test := range []struct {
		name, value, escaped string
	}{
		{"ASCII DEL", "\x7f", `\u007f`},
		{"BMP bidi override", "\u202e", `\u202e`},
		{"C1 control", "\u0085", `\u0085`},
		{"supplementary format", "\U000e0001", `\udb40\udc01`},
	} {
		t.Run(test.name, func(t *testing.T) {
			label := "café " + test.value + ` literal \u202e`
			description := delivery.PublicationDescription{
				Reference: label,
				Outputs: []delivery.PublicationOutput{{
					Output:        delivery.Output{Name: label, Reference: label},
					BuildSubjects: []delivery.BuildSubject{{Component: label}},
				}},
				Evidence: []delivery.PublicationEvidence{{Name: label, Binding: delivery.EvidenceBinding{Outputs: []string{label}, Component: label}}},
			}
			raw, err := Render(description, Options{Format: "json"})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), test.value) || !strings.Contains(string(raw), test.escaped) {
				t.Errorf("JSON contains raw control or lacks its JSON escape: %q", raw)
			}
			if !strings.Contains(string(raw), "café") {
				t.Errorf("ordinary Unicode became unreadable: %q", raw)
			}
			var decoded view
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded.PublicationDescription, description) {
				t.Fatalf("JSON escaping changed decoded values: got %+v, want %+v", decoded.PublicationDescription, description)
			}
		})
	}
}

func TestShortDigestPreservesRuneBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, value, want string
	}{
		{"empty", "", ""},
		{"short Unicode", "sha256:é", "sha256:é"},
		{"exact length", strings.Repeat("a", 19), strings.Repeat("a", 19)},
		{"long ASCII", "sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("a", 12)},
		{"long multibyte", strings.Repeat("é", 20), strings.Repeat("é", 19)},
		{"supplementary boundary", strings.Repeat("a", 18) + "😀tail", strings.Repeat("a", 18) + "😀"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shortDigest(test.value); got != test.want || !utf8.ValidString(got) {
				t.Fatalf("shortDigest(%q) = %q, want %q", test.value, got, test.want)
			}
			raw, err := Render(delivery.PublicationDescription{RootDigest: test.value}, Options{Format: "mermaid"})
			if err != nil || strings.Contains(string(raw), "#65533;") {
				t.Fatalf("digest truncation introduced replacement character: %q, %v", raw, err)
			}
		})
	}
}

func TestRenderLabelsDerivedReferences(t *testing.T) {
	const scope = "derived from supplied root reference; no registry availability claim"
	description := delivery.PublicationDescription{
		Reference: "registry.test/releases@sha256:root", ReferenceScope: scope,
		Outputs:  []delivery.PublicationOutput{{Output: delivery.Output{Name: "cli", Reference: "registry.test/cli@sha256:output"}}},
		Evidence: []delivery.PublicationEvidence{{Name: "sbom", Reference: "registry.test/cli@sha256:evidence", Binding: delivery.EvidenceBinding{Outputs: []string{"cli"}}}},
	}
	for _, format := range []string{"text", "json", "mermaid"} {
		t.Run(format, func(t *testing.T) {
			raw, err := Render(description, Options{Format: format})
			if err != nil {
				t.Fatal(err)
			}
			switch format {
			case "text":
				for _, want := range []string{"References: " + scope, "Derived artifact reference: " + description.Outputs[0].Reference, "Derived reference: " + description.Evidence[0].Reference} {
					if !strings.Contains(string(raw), want) {
						t.Errorf("text omits derived-reference scope %q: %s", want, raw)
					}
				}
			case "json":
				var decoded view
				if err := json.Unmarshal(raw, &decoded); err != nil {
					t.Fatal(err)
				}
				if decoded.ReferenceScope != scope || decoded.Outputs[0].Reference != description.Outputs[0].Reference || decoded.Evidence[0].Reference != description.Evidence[0].Reference {
					t.Fatalf("JSON loses derived-reference scope or exact locators: %s", raw)
				}
			case "mermaid":
				if strings.Contains(string(raw), "registry.test") || !strings.Contains(string(raw), "signatures and policy not verified") {
					t.Fatalf("diagram implies registry locations or loses integrity scope: %s", raw)
				}
			}
		})
	}
}

func TestRenderImageIndexShowsExactChildrenAndPlatformEvidence(t *testing.T) {
	binding := &delivery.ImageIndexBinding{RequiredPlatforms: []string{"linux/amd64", "linux/arm64"}, Children: []delivery.ImageIndexChildBinding{
		{Platform: "linux/amd64", Component: "controller", ManifestDigest: "sha256:" + strings.Repeat("a", 64), BuildDigest: "sha256:" + strings.Repeat("b", 64)},
		{Platform: "linux/arm64", Component: "controller", ManifestDigest: "sha256:" + strings.Repeat("c", 64), BuildDigest: "sha256:" + strings.Repeat("d", 64)},
	}}
	description := delivery.PublicationDescription{Outputs: []delivery.PublicationOutput{{Output: delivery.Output{Name: "cwe", Kind: "oci", ImageIndex: binding}}}}
	for _, child := range binding.Children {
		description.Evidence = append(description.Evidence, delivery.PublicationEvidence{Kind: "sbom", Binding: delivery.EvidenceBinding{Outputs: []string{"cwe"}, Platform: child.Platform, Component: child.Component, Stage: "build-output", SubjectKind: "oci-manifest", SubjectDigest: child.ManifestDigest}})
	}
	for _, format := range []string{"text", "json", "mermaid"} {
		t.Run(format, func(t *testing.T) {
			raw, err := Render(description, Options{Format: format, Output: "cwe"})
			if err != nil {
				t.Fatal(err)
			}
			for _, child := range binding.Children {
				digest := child.ManifestDigest
				if format == "mermaid" {
					digest = shortDigest(digest)
				}
				if !strings.Contains(string(raw), child.Platform) || !strings.Contains(string(raw), digest) {
					t.Fatalf("missing platform/manifest binding: %s", raw)
				}
			}
			if format == "mermaid" && (!strings.Contains(string(raw), "e0 -->|inventories| o0_p0") || !strings.Contains(string(raw), "e1 -->|inventories| o0_p1")) {
				t.Fatalf("evidence did not join its native child: %s", raw)
			}
		})
	}
}

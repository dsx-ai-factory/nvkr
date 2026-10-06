package ir

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func indexBuildFixture() ([]string, []ImageIndexBuildChild) {
	return []string{"linux/amd64", "linux/arm64"}, []ImageIndexBuildChild{
		{Platform: "linux/amd64", BuildDigest: "sha256:" + strings.Repeat("a", 64), Component: "controller"},
		{Platform: "linux/arm64", BuildDigest: "sha256:" + strings.Repeat("b", 64), Component: "controller"},
	}
}

func TestImageIndexBuildIdentityCanonicalContract(t *testing.T) {
	platforms, children := indexBuildFixture()
	got, err := ImageIndexBuildDigest(platforms, children)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit canonical projection freezes the wire shape independently of its DTO.
	want, err := canonical.Digest(canonical.KindBuild, map[string]any{
		"schemaVersion":     "nkd.dev/image-index-build/v1",
		"requiredPlatforms": []string{"linux/amd64", "linux/arm64"},
		"children": []map[string]string{
			{"platform": "linux/amd64", "buildDigest": "sha256:" + strings.Repeat("a", 64), "component": "controller"},
			{"platform": "linux/arm64", "buildDigest": "sha256:" + strings.Repeat("b", 64), "component": "controller"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("identity = %s, want %s", got, want)
	}
	for _, test := range []struct {
		name   string
		mutate func(*[]string, *[]ImageIndexBuildChild)
	}{
		{"pin", func(_ *[]string, c *[]ImageIndexBuildChild) {
			(*c)[1].BuildDigest = "sha256:" + strings.Repeat("c", 64)
		}},
		{"component", func(_ *[]string, c *[]ImageIndexBuildChild) {
			for i := range *c {
				(*c)[i].Component = "replacement"
			}
		}},
		{"membership", func(p *[]string, c *[]ImageIndexBuildChild) { *p = (*p)[:1]; *c = (*c)[:1] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, c := indexBuildFixture()
			test.mutate(&p, &c)
			changed, err := ImageIndexBuildDigest(p, c)
			if err != nil {
				t.Fatal(err)
			}
			if changed == got {
				t.Fatal("logical change did not change aggregate Build identity")
			}
		})
	}
}

func TestImageIndexBuildIdentityRejectsAmbiguousMembership(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*[]string, *[]ImageIndexBuildChild)
	}{
		{"empty", func(p *[]string, c *[]ImageIndexBuildChild) { *p = nil; *c = nil }},
		{"missing", func(_ *[]string, c *[]ImageIndexBuildChild) { *c = (*c)[:1] }},
		{"extra", func(_ *[]string, c *[]ImageIndexBuildChild) { *c = append(*c, (*c)[0]) }},
		{"unordered", func(p *[]string, c *[]ImageIndexBuildChild) {
			(*p)[0], (*p)[1] = (*p)[1], (*p)[0]
			(*c)[0], (*c)[1] = (*c)[1], (*c)[0]
		}},
		{"duplicate", func(p *[]string, c *[]ImageIndexBuildChild) { (*p)[1] = (*p)[0]; (*c)[1] = (*c)[0] }},
		{"unsupported", func(p *[]string, c *[]ImageIndexBuildChild) { (*p)[1] = "linux/riscv64"; (*c)[1].Platform = (*p)[1] }},
		{"child mismatch", func(_ *[]string, c *[]ImageIndexBuildChild) { (*c)[1].Platform = "linux/amd64" }},
		{"tag", func(_ *[]string, c *[]ImageIndexBuildChild) { (*c)[1].BuildDigest = "image:latest" }},
		{"component mismatch", func(_ *[]string, c *[]ImageIndexBuildChild) { (*c)[1].Component = "other" }},
		{"component empty", func(_ *[]string, c *[]ImageIndexBuildChild) { (*c)[1].Component = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, c := indexBuildFixture()
			test.mutate(&p, &c)
			_, err := ImageIndexBuildDigest(p, c)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
				t.Fatalf("error = %v, want %s", err, e.CodeInvalidCatalog)
			}
		})
	}
}

package sbom

import (
	"errors"
	"sort"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestDiskBuildProvenanceScopeAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*BOM) *BOM
		want   e.Code
	}{
		{"valid", func(b *BOM) *BOM { return b }, ""},
		{"historical uninspected scope", func(b *BOM) *BOM {
			for i := range b.Metadata.Props {
				if b.Metadata.Props[i].Name == "nkd:diskInspection" {
					b.Metadata.Props[i].Value = "deferred-m3"
				}
			}
			return b
		}, ""},
		{"unknown inspection scope", func(b *BOM) *BOM {
			for i := range b.Metadata.Props {
				if b.Metadata.Props[i].Name == "nkd:diskInspection" {
					b.Metadata.Props[i].Value = "inspected"
				}
			}
			return b
		}, e.CodeProvenanceMissing},
		{"nil", func(*BOM) *BOM { return nil }, e.CodeProvenanceMissing},
		{"invalid schema", func(b *BOM) *BOM { b.Version = 2; return b }, e.CodeInvalidCatalog},
		{"missing scope", func(b *BOM) *BOM {
			for i := range b.Metadata.Props {
				if b.Metadata.Props[i].Name == "nkd:diskEvidence" {
					b.Metadata.Props[i].Value = "unknown"
				}
			}
			return b
		}, e.CodeProvenanceMissing},
		{"invented inventory", func(b *BOM) *BOM { b.Components = []Component{{Type: "library", Name: "invented"}}; return b }, e.CodeProvenanceMissing},
		{"invented dependency", func(b *BOM) *BOM {
			b.Metadata.Component.BOMRef = "root"
			b.Dependencies = []Dependency{{Ref: "root", DependsOn: []string{}}}
			return b
		}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, err := NewSubject("sha256:"+strings.Repeat("a", 64), "image", "sha256:"+strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			bom, err := ForDiskBuildProvenance(base, "sha256:"+strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			for _, property := range bom.Metadata.Props {
				if property.Name == "nkd:diskInspection" && property.Value != "not-performed" {
					t.Fatalf("new producer emitted historical scope: %q", property.Value)
				}
			}
			assertDiskScopeCode(t, ValidateDiskBuildProvenance(test.change(bom)), test.want)
		})
	}
	for _, property := range []Property{
		{Name: "nkd:dependencyInspection", Value: "disk-reconciled/v1"},
		{Name: "nkd:dependencyScannerDigest", Value: "sha256:" + strings.Repeat("c", 64)},
		{Name: "nkd:diskInspectorDigest", Value: "sha256:" + strings.Repeat("c", 64)},
		{Name: "nkd:diskCodecDigest", Value: "sha256:" + strings.Repeat("c", 64)},
		{Name: "nkd:inspectionInputDigest", Value: "sha256:" + strings.Repeat("c", 64)},
	} {
		t.Run(property.Name, func(t *testing.T) {
			base, err := NewSubject("sha256:"+strings.Repeat("a", 64), "image", "sha256:"+strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			bom, err := ForDiskBuildProvenance(base, "sha256:"+strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			bom.Metadata.Props = append(bom.Metadata.Props, property)
			sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
			assertDiskScopeCode(t, ValidateDiskBuildProvenance(bom), e.CodeVerifyFailed)
		})
	}
	// A structurally valid inventory must reach the mode-specific guard, not be
	// rejected only because the generic CycloneDX document is incomplete.
	for _, method := range []string{"syft-cyclonedx/v1", "disk-reconciled/v1"} {
		t.Run("complete inventory/"+method, func(t *testing.T) {
			base, err := NewSubject("sha256:"+strings.Repeat("a", 64), "image", "sha256:"+strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			bom, err := ForDiskBuildProvenance(base, "sha256:"+strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			if method == "disk-reconciled/v1" {
				bom.Metadata.Component.Type = "operating-system"
			}
			inventory, err := WithDependencies(bom, []Component{{Type: "library", Name: "invented", Version: "1"}}, method, "sha256:"+strings.Repeat("c", 64))
			if err != nil {
				t.Fatal(err)
			}
			if err := inventory.Validate(); err != nil {
				t.Fatalf("fixture did not reach mode-specific admission: %v", err)
			}
			assertDiskScopeCode(t, ValidateDiskBuildProvenance(inventory), e.CodeVerifyFailed)
		})
	}
}

func assertDiskScopeCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error=%v, want %s", err, want)
	}
}

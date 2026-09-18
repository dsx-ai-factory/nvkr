package target

import (
	"errors"
	"reflect"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPlatformAdmissionRequiresConcreteSupportedTarget(t *testing.T) {
	for _, test := range []struct{ platform, arch string }{
		{"linux/amd64", "amd64"}, {"linux/arm64", "arm64"},
		{"", ""}, {"any", ""}, {"linux/any", ""}, {"linux/riscv64", ""},
		{"darwin/arm64", ""}, {"windows/amd64", ""}, {" linux/amd64", ""},
		{"linux/arm64/v8", ""}, {"LINUX/amd64", ""},
	} {
		t.Run(test.platform, func(t *testing.T) {
			err := ValidatePlatform(test.platform)
			if test.arch == "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
					t.Fatalf("platform admission = %v, want %s", err, e.CodeInvalidCatalog)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := Arch(test.platform); got != test.arch {
				t.Fatalf("architecture = %q, want %q", got, test.arch)
			}
		})
	}
}

func TestArchitectureDomainIsOwned(t *testing.T) {
	want := []string{"amd64", "arm64"}
	for _, arch := range append(Arches(), "", "any", "riscv64") {
		if got := ValidArch(arch); got != (arch == "amd64" || arch == "arm64") {
			t.Fatalf("architecture %q admitted = %v", arch, got)
		}
	}
	got := Arches()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("architectures = %v", got)
	}
	got[0] = "mutated"
	if !reflect.DeepEqual(Arches(), want) {
		t.Fatal("caller mutated the architecture domain")
	}
}

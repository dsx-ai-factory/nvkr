package definition

import (
	"errors"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestEnvironmentImageTagAdmissionAndClone(t *testing.T) {
	for _, tc := range []struct {
		name, output, tag string
		valid             bool
	}{
		{"explicit", "cwe", "candidate", true},
		{"version", "cwe", "v0.1.1-rc.9", true},
		{"empty", "cwe", "", false},
		{"repository", "cwe", "registry.example/cwe:v1", false},
		{"reserved", "cwe", "sha256-deadbeef", false},
		{"expansion", "cwe", "${VERSION}", false},
		{"newline", "cwe", "v1\n", false},
		{"long", "cwe", strings.Repeat("a", 129), false},
		{"invalid output", "cwe/image", "v1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := Environment{APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "fixture"}, UtilityContainer: "busybox@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64", Publication: &Publication{RepositoryPrefix: "registry.example/team", Anonymous: true, ImageTags: map[string]string{tc.output: tc.tag}}}
			err := env.Validate()
			if !tc.valid {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
					t.Fatalf("admission error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			clone := env.Clone()
			clone.Publication.ImageTags[tc.output] = "changed"
			if env.Publication.ImageTags[tc.output] != tc.tag {
				t.Fatal("clone shares discovery tags")
			}
		})
	}
}

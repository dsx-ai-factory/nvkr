package catalog

import (
	"context"
	"reflect"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestPrebuiltTargetsResolveBeforeArtifactPinning(t *testing.T) {
	registry := `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  s: {repo: https://example.invalid/source}
components:
  zulu: {platforms: [linux/amd64, linux/arm64], source: s, outputType: oci-image, prebuilt: {image: 'example.invalid/zulu:{{.Version}}', evidence: {none: true}}}
  alpha: {platforms: [linux/amd64, linux/arm64], source: s, outputType: raw, prebuilt: {url: 'https://example.invalid/{{.Tag}}/alpha-{{.Arch}}', evidence: {none: true}}}
  excluded: {platforms: [linux/amd64, linux/arm64], fromSource: false, exclusionReason: fixture}
`
	lock := lockYAML("  s: {tag: v1.2.3, commit: " + strings.Repeat("a", 40) + ", treeHash: " + strings.Repeat("b", 40) + ", sourceDateEpoch: 1}\n")
	for _, tc := range []struct {
		name, registry string
		lock           []byte
		code           e.Code
	}{
		{name: "unresolved artifact digests", registry: registry, lock: lock},
		{name: "missing source pin", registry: registry, lock: lockYAML("  {}\n"), code: e.CodeUnpinnedSource},
		{name: "invalid URL template", registry: strings.Replace(registry, "{{.Tag}}", "{{.Unknown}}", 1), lock: lock, code: e.CodeInvalidCatalog},
		{name: "invalid image template", registry: strings.Replace(registry, "{{.Version}}", "{{.Unknown}}", 1), lock: lock, code: e.CodeInvalidCatalog},
		{name: "invalid registry", registry: "invalid: [", lock: lock, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := bytesProvider{RegistryFile: []byte(tc.registry), LockFile: tc.lock}
			got, err := ReadPrebuiltTargetsContext(context.Background(), provider)
			if tc.code != "" {
				if codeOf(err) != tc.code {
					t.Fatalf("error = %v, want %s", err, tc.code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []PrebuiltTarget{{Platforms: []string{"linux/amd64", "linux/arm64"}, Name: "alpha", Tag: "v1.2.3", URL: "https://example.invalid/v1.2.3/alpha-{{.Arch}}"}, {Platforms: []string{"linux/amd64", "linux/arm64"}, Name: "zulu", Tag: "v1.2.3", Image: "example.invalid/zulu:1.2.3"}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("targets = %+v, want %+v", got, want)
			}
			sources, err := ReadSourcesContext(context.Background(), provider)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(SortedSourceNames(sources), []string{"s"}) {
				t.Fatalf("sources = %+v", sources)
			}
		})
	}
}

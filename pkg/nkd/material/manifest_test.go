package material

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestManifestIsDeterministicAndDerivesRelativePaths(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	forward := []Observation{
		{Binding: "..", Component: "configuration", Kind: "raw", Digest: "sha256:" + strings.Repeat("c", 64)},
		{Binding: "runtime package/arm64", Component: "runtime", Kind: "host-package", Digest: digestB},
		{Binding: "agent-package", Component: "agent", Kind: "host-package", Digest: digestA},
	}
	reverse := []Observation{forward[2], forward[1], forward[0]}

	first, err := NewManifest(forward)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewManifest(reverse)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := first.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := second.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := second.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) || firstDigest != secondDigest {
		t.Fatalf("input order changed manifest identity:\n%s\n%s", firstBytes, secondBytes)
	}
	wantPaths := []string{
		"materials/%2E%2E",
		"materials/agent-package",
		"materials/runtime%20package%2Farm64",
	}
	gotPaths := []string{first.Materials[0].Path, first.Materials[1].Path, first.Materials[2].Path}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("derived paths = %v, want %v", gotPaths, wantPaths)
	}
	for _, path := range gotPaths {
		if strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
			t.Fatalf("manifest contains unsafe path %q", path)
		}
	}
	privateRoot := t.TempDir()
	for _, entry := range first.Materials {
		if staged := filepath.Join(privateRoot, filepath.FromSlash(entry.Path)); !strings.HasPrefix(staged, privateRoot+string(filepath.Separator)) {
			t.Fatalf("derived material path escaped private root: %q", staged)
		}
	}
	if bytes.Contains(firstBytes, []byte(privateRoot)) {
		t.Fatalf("private staging root leaked into canonical manifest: %s", firstBytes)
	}
}

func TestManifestValidationFailsClosed(t *testing.T) {
	valid, err := NewManifest([]Observation{
		{Binding: "agent", Component: "agent-component", Kind: "host-package", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Binding: "runtime", Component: "runtime-component", Kind: "raw", Digest: "sha256:" + strings.Repeat("b", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*Manifest)
		want   e.Code
	}{
		{name: "schema", mutate: func(manifest *Manifest) { manifest.SchemaVersion = "unknown" }, want: e.CodeInvalidCatalog},
		{name: "empty", mutate: func(manifest *Manifest) { manifest.Materials = nil }, want: e.CodeProvenanceMissing},
		{name: "binding metadata", mutate: func(manifest *Manifest) { manifest.Materials[0].Binding = " agent" }, want: e.CodeInvalidCatalog},
		{name: "component metadata", mutate: func(manifest *Manifest) { manifest.Materials[0].Component = "agent\ncomponent" }, want: e.CodeInvalidCatalog},
		{name: "kind metadata", mutate: func(manifest *Manifest) { manifest.Materials[0].Kind = "" }, want: e.CodeInvalidCatalog},
		{name: "digest", mutate: func(manifest *Manifest) { manifest.Materials[0].Digest = "sha256:not-exact" }, want: e.CodeUnverifiedRef},
		{name: "order", mutate: func(manifest *Manifest) {
			manifest.Materials[0], manifest.Materials[1] = manifest.Materials[1], manifest.Materials[0]
		}, want: e.CodeInvalidCatalog},
		{name: "duplicate binding", mutate: func(manifest *Manifest) {
			manifest.Materials[1].Binding = manifest.Materials[0].Binding
			manifest.Materials[1].Path = manifest.Materials[0].Path
		}, want: e.CodeInvalidCatalog},
		{name: "duplicate component", mutate: func(manifest *Manifest) { manifest.Materials[1].Component = manifest.Materials[0].Component }, want: e.CodeInvalidCatalog},
		{name: "substituted path", mutate: func(manifest *Manifest) { manifest.Materials[0].Path = "../../agent" }, want: e.CodeInvalidCatalog},
		{name: "absolute path", mutate: func(manifest *Manifest) { manifest.Materials[0].Path = "/private/random/agent" }, want: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := *valid
			candidate.Materials = append([]Entry(nil), valid.Materials...)
			test.mutate(&candidate)
			if got := errorCode(candidate.Validate()); got != test.want {
				t.Fatalf("Validate() = %v, want %s", candidate.Validate(), test.want)
			}
			if raw, err := candidate.Marshal(); raw != nil || errorCode(err) != test.want {
				t.Fatalf("invalid manifest serialized as %q with %v", raw, err)
			}
		})
	}
}

func TestNewManifestAcceptsEmptyAndRejectsAmbiguousObservations(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	empty, err := NewManifest(nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyBytes, err := empty.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(emptyBytes, []byte(`"materials":[]`)) {
		t.Fatalf("empty manifest is not encoded as one canonical array: %s", emptyBytes)
	}
	for _, test := range []struct {
		name         string
		observations []Observation
		want         e.Code
	}{
		{name: "duplicate binding", observations: []Observation{
			{Binding: "package", Component: "one", Kind: "raw", Digest: digest},
			{Binding: "package", Component: "two", Kind: "raw", Digest: digest},
		}, want: e.CodeInvalidCatalog},
		{name: "duplicate component", observations: []Observation{
			{Binding: "one", Component: "component", Kind: "raw", Digest: digest},
			{Binding: "two", Component: "component", Kind: "raw", Digest: digest},
		}, want: e.CodeInvalidCatalog},
		{name: "binding exceeds encoded path segment", observations: []Observation{
			{Binding: strings.Repeat("é", 100), Component: "component", Kind: "raw", Digest: digest},
		}, want: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest, err := NewManifest(test.observations)
			if manifest != nil || errorCode(err) != test.want {
				t.Fatalf("NewManifest() = %#v, %v, want %s", manifest, err, test.want)
			}
		})
	}
}

func errorCode(err error) e.Code {
	var coded *e.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

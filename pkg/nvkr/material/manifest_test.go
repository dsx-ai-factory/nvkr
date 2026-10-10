// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package material

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestManifestIsDeterministicAndDerivesRelativePaths(t *testing.T) {
	digestA := "sha256:" + strings.Repeat("a", 64)
	digestB := "sha256:" + strings.Repeat("b", 64)
	forward := []Observation{
		{Binding: ".", Component: "dot-configuration", Kind: "raw", Architecture: "arm64", Digest: "sha256:" + strings.Repeat("d", 64)},
		{Binding: "..", Component: "configuration", Kind: "raw", Architecture: "arm64", Digest: "sha256:" + strings.Repeat("c", 64)},
		{Binding: "runtime package/arm64", Component: "runtime", Kind: "host-package", Architecture: "arm64", Digest: digestB},
		{Binding: "agent-package", Component: "agent", Kind: "host-package", Architecture: "arm64", Digest: digestA},
	}
	reverse := []Observation{forward[3], forward[2], forward[1], forward[0]}

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
		"materials/%2E",
		"materials/%2E%2E",
		"materials/agent-package",
		"materials/runtime%20package%2Farm64",
	}
	gotPaths := []string{
		first.Materials[0].Path,
		first.Materials[1].Path,
		first.Materials[2].Path,
		first.Materials[3].Path,
	}
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

func TestProjectedManifestPreservesAuthenticatedFieldsAndIsDeterministic(t *testing.T) {
	observations := []Observation{
		{Binding: "installer-iso", Component: "ubuntu-installer-iso", Kind: "raw", Architecture: "arm64", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Binding: "image-inputs", Component: "nvke-image-inputs", Kind: "tarball", Architecture: "arm64", Digest: "sha256:" + strings.Repeat("b", 64)},
	}
	projection := &Projection{
		SchemaVersion: "nke.dev/os-image-materials/v1",
		Kinds: map[string]string{
			"installer-iso": "disk-image",
			"image-inputs":  "archive",
		},
	}
	forward, err := MarshalProjected(observations, projection)
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := MarshalProjected([]Observation{observations[1], observations[0]}, projection)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(forward, reverse) {
		t.Fatalf("projection depends on observation order:\n%s\n%s", forward, reverse)
	}
	var manifest Manifest
	if err := canonical.UnmarshalExact(forward, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != projection.SchemaVersion || len(manifest.Materials) != 2 {
		t.Fatalf("projected manifest = %#v", manifest)
	}
	if got := manifest.Materials[0]; got.Binding != "image-inputs" || got.Component != "nvke-image-inputs" ||
		got.Kind != "archive" || got.Architecture != "arm64" || got.Digest != observations[1].Digest ||
		got.Path != "materials/image-inputs" {
		t.Fatalf("projected image inputs = %#v", got)
	}
	if got := manifest.Materials[1]; got.Binding != "installer-iso" || got.Component != "ubuntu-installer-iso" ||
		got.Kind != "disk-image" || got.Architecture != "arm64" || got.Digest != observations[0].Digest ||
		got.Path != "materials/installer-iso" {
		t.Fatalf("projected installer ISO = %#v", got)
	}

	native, err := MarshalProjected(observations, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := NewManifest(observations)
	if err != nil {
		t.Fatal(err)
	}
	legacyBytes, err := legacy.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(native, legacyBytes) {
		t.Fatalf("nil projection changed native v2 bytes:\n%s\n%s", native, legacyBytes)
	}
}

func TestMaterialManifestProjectionValidationFailsClosed(t *testing.T) {
	bindings := []string{"image-inputs", "installer-iso"}
	valid := func() *Projection {
		return &Projection{SchemaVersion: "nke.dev/os-image-materials/v1", Kinds: map[string]string{
			"image-inputs": "archive", "installer-iso": "disk-image",
		}}
	}
	for _, test := range []struct {
		name   string
		mutate func(*Projection)
	}{
		{name: "missing schema", mutate: func(p *Projection) { p.SchemaVersion = "" }},
		{name: "schema whitespace", mutate: func(p *Projection) { p.SchemaVersion = "nvke.dev/os image/v1" }},
		{name: "nil kinds", mutate: func(p *Projection) { p.Kinds = nil }},
		{name: "missing binding", mutate: func(p *Projection) { delete(p.Kinds, "image-inputs") }},
		{name: "unknown binding", mutate: func(p *Projection) { p.Kinds["other"] = "raw" }},
		{name: "invalid binding", mutate: func(p *Projection) { p.Kinds[" bad"] = "raw" }},
		{name: "missing kind", mutate: func(p *Projection) { p.Kinds["image-inputs"] = "" }},
		{name: "kind whitespace", mutate: func(p *Projection) { p.Kinds["image-inputs"] = "input archive" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection := valid()
			test.mutate(projection)
			if got := errorCode(ValidateProjection(projection, bindings)); got != e.CodeInvalidCatalog {
				t.Fatalf("ValidateProjection() = %v, want %s", ValidateProjection(projection, bindings), e.CodeInvalidCatalog)
			}
		})
	}
	if got := errorCode(ValidateProjection(valid(), []string{"image-inputs", "image-inputs"})); got != e.CodeInvalidCatalog {
		t.Fatalf("duplicate binding validation = %v, want %s", ValidateProjection(valid(), []string{"image-inputs", "image-inputs"}), e.CodeInvalidCatalog)
	}
	if got := errorCode(ValidateProjection(valid(), []string{" bad", "installer-iso"})); got != e.CodeInvalidCatalog {
		t.Fatalf("invalid declared binding validation = %v, want %s", ValidateProjection(valid(), []string{" bad", "installer-iso"}), e.CodeInvalidCatalog)
	}
}

func TestProjectedManifestRejectsAmbiguousObservations(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	projection := &Projection{SchemaVersion: "example.dev/materials/v1", Kinds: map[string]string{
		"one": "archive", "two": "archive",
	}}
	for _, test := range []struct {
		name         string
		observations []Observation
	}{
		{name: "mixed architecture", observations: []Observation{
			{Binding: "one", Component: "one", Kind: "raw", Architecture: "amd64", Digest: digest},
			{Binding: "two", Component: "two", Kind: "raw", Architecture: "arm64", Digest: digest},
		}},
		{name: "duplicate component", observations: []Observation{
			{Binding: "one", Component: "component", Kind: "raw", Architecture: "arm64", Digest: digest},
			{Binding: "two", Component: "component", Kind: "raw", Architecture: "arm64", Digest: digest},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := MarshalProjected(test.observations, projection)
			if raw != nil || errorCode(err) != e.CodeInvalidCatalog {
				t.Fatalf("MarshalProjected() = %q, %v, want %s", raw, err, e.CodeInvalidCatalog)
			}
		})
	}
}

func TestManifestValidationFailsClosed(t *testing.T) {
	var absent *Manifest
	if got := errorCode(absent.Validate()); got != e.CodeInvalidCatalog {
		t.Fatalf("nil Manifest.Validate() code = %s, want %s", got, e.CodeInvalidCatalog)
	}

	valid, err := NewManifest([]Observation{
		{Binding: "agent", Component: "agent-component", Kind: "host-package", Architecture: "arm64", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Binding: "runtime", Component: "runtime-component", Kind: "raw", Architecture: "arm64", Digest: "sha256:" + strings.Repeat("b", 64)},
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
		{name: "nil materials", mutate: func(manifest *Manifest) { manifest.Materials = nil }, want: e.CodeInvalidCatalog},
		{name: "binding metadata", mutate: func(manifest *Manifest) { manifest.Materials[0].Binding = " agent" }, want: e.CodeInvalidCatalog},
		{name: "binding display control", mutate: func(manifest *Manifest) { manifest.Materials[0].Binding = "agent\u202e" }, want: e.CodeInvalidCatalog},
		{name: "component metadata", mutate: func(manifest *Manifest) { manifest.Materials[0].Component = "agent\ncomponent" }, want: e.CodeInvalidCatalog},
		{name: "component display control", mutate: func(manifest *Manifest) { manifest.Materials[0].Component = "agent\u202ecomponent" }, want: e.CodeInvalidCatalog},
		{name: "kind metadata", mutate: func(manifest *Manifest) { manifest.Materials[0].Kind = "" }, want: e.CodeInvalidCatalog},
		{name: "kind whitespace", mutate: func(manifest *Manifest) { manifest.Materials[0].Kind = "host package" }, want: e.CodeInvalidCatalog},
		{name: "missing architecture", mutate: func(manifest *Manifest) { manifest.Materials[0].Architecture = "" }, want: e.CodeInvalidCatalog},
		{name: "unsupported architecture", mutate: func(manifest *Manifest) { manifest.Materials[0].Architecture = "riscv64" }, want: e.CodeInvalidCatalog},
		{name: "mixed architecture", mutate: func(manifest *Manifest) { manifest.Materials[0].Architecture = "amd64" }, want: e.CodeInvalidCatalog},
		{name: "digest", mutate: func(manifest *Manifest) { manifest.Materials[0].Digest = "sha256:not-exact" }, want: e.CodeInvalidCatalog},
		{name: "order", mutate: func(manifest *Manifest) {
			manifest.Materials[0], manifest.Materials[1] = manifest.Materials[1], manifest.Materials[0]
		}, want: e.CodeInvalidCatalog},
		{name: "duplicate binding rejected before path collision", mutate: func(manifest *Manifest) {
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
			if digest, err := candidate.Digest(); digest != "" || errorCode(err) != test.want {
				t.Fatalf("invalid manifest acquired digest %q with %v", digest, err)
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
			{Binding: "package", Component: "one", Kind: "raw", Architecture: "arm64", Digest: digest},
			{Binding: "package", Component: "two", Kind: "raw", Architecture: "arm64", Digest: digest},
		}, want: e.CodeInvalidCatalog},
		{name: "duplicate component", observations: []Observation{
			{Binding: "one", Component: "component", Kind: "raw", Architecture: "arm64", Digest: digest},
			{Binding: "two", Component: "component", Kind: "raw", Architecture: "arm64", Digest: digest},
		}, want: e.CodeInvalidCatalog},
		{name: "binding exceeds encoded path segment", observations: []Observation{
			{Binding: strings.Repeat("é", 100), Component: "component", Kind: "raw", Architecture: "arm64", Digest: digest},
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

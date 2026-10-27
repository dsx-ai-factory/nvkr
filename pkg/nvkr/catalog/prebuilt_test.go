// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"reflect"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// a valid bare content digest (sha256:<64-hex>) for the no-compile lane.
var goodArtifactDigest = "sha256:" + strings.Repeat("a", 64)

// goodPrebuilt is a valid resolved prebuilt (no-compile) component: a binary URL with
// the per-arch token intact and a pinned amd64 digest.
func goodPrebuilt() Component {
	return Component{Platforms: []string{"linux/amd64"},
		OutputType: "host-package",
		Prebuilt: &Prebuilt{
			URL:      "https://dl.k8s.io/release/v1.32.0/bin/linux/{{.Arch}}/kubectl",
			Digests:  map[string]string{"amd64": goodArtifactDigest},
			Evidence: &ExternalEvidence{None: true},
		},
	}
}

func TestPrebuiltValidates(t *testing.T) {
	if err := base(goodPrebuilt()).Validate(); err != nil {
		t.Fatalf("valid prebuilt component must validate: %v", err)
	}
	// An image delivery (oci-image) is equally valid.
	img := Component{Platforms: []string{"linux/amd64"}, OutputType: "oci-image", Prebuilt: &Prebuilt{
		Image: "registry.k8s.io/pause:3.10", Digests: map[string]string{"amd64": goodArtifactDigest},
		Evidence: &ExternalEvidence{None: true},
	}}
	if err := base(img).Validate(); err != nil {
		t.Fatalf("valid prebuilt image must validate: %v", err)
	}
	tarball := goodPrebuilt()
	tarball.OutputType = "tarball"
	if err := base(tarball).Validate(); err != nil {
		t.Fatalf("valid digest-pinned tarball must validate: %v", err)
	}
}

// ADR-13 §2: a prebuilt component must declare external evidence explicitly — either
// none:true or a digest-pinned ref — never silently absent.
func TestPrebuiltExternalEvidence(t *testing.T) {
	noEv := goodPrebuilt()
	noEv.Prebuilt.Evidence = nil
	if e, ok := base(noEv).Validate().(*nvkrerr.Error); !ok || e.Code != nvkrerr.CodeProvenanceMissing {
		t.Fatalf("missing evidence must be provenance-missing, got %v", base(noEv).Validate())
	}
	unpinned := goodPrebuilt()
	unpinned.Prebuilt.Evidence = &ExternalEvidence{SBOMRef: "https://x/sbom.json"} // no digest
	if e, ok := base(unpinned).Validate().(*nvkrerr.Error); !ok || e.Code != nvkrerr.CodeUnpinnedSource {
		t.Fatalf("unpinned evidence ref must fail closed, got %v", base(unpinned).Validate())
	}
	pinned := goodPrebuilt()
	pinned.Prebuilt.Evidence = &ExternalEvidence{SBOMRef: "https://x/sbom.json", SBOMDigest: goodArtifactDigest}
	if err := base(pinned).Validate(); err != nil {
		t.Fatalf("digest-pinned evidence ref must validate: %v", err)
	}
}

func TestPrebuiltFailClosed(t *testing.T) {
	cases := []struct {
		name string
		mut  func(Component) Component
		want nvkrerr.Code
	}{
		{"both url and image", func(c Component) Component {
			c.Prebuilt.Image = "registry.k8s.io/x:1"
			return c
		}, nvkrerr.CodeInvalidCatalog},
		{"neither url nor image", func(c Component) Component {
			c.Prebuilt.URL = ""
			return c
		}, nvkrerr.CodeInvalidCatalog},
		{"no pinned digest", func(c Component) Component {
			c.Prebuilt.Digests = nil
			return c
		}, nvkrerr.CodeUnpinnedSource},
		{"digest is a tag, not sha256", func(c Component) Component {
			c.Prebuilt.Digests = map[string]string{"amd64": "v1.32.0"}
			return c
		}, nvkrerr.CodeUnpinnedSource},
		{"short digest", func(c Component) Component {
			c.Prebuilt.Digests = map[string]string{"amd64": "sha256:deadbeef"}
			return c
		}, nvkrerr.CodeUnpinnedSource},
		{"missing outputType", func(c Component) Component {
			c.OutputType = ""
			return c
		}, nvkrerr.CodeInvalidCatalog},
		{"outputType not valid for the lane", func(c Component) Component {
			c.OutputType = "disk-image"
			return c
		}, nvkrerr.CodeInvalidCatalog},
		{"image with a non-image outputType", func(c Component) Component {
			c.Prebuilt = &Prebuilt{Image: "registry.k8s.io/pause:3.10", Digests: map[string]string{"amd64": goodArtifactDigest}}
			c.OutputType = "host-package"
			return c
		}, nvkrerr.CodeInvalidCatalog},
		{"url delivering an oci-image", func(c Component) Component {
			c.OutputType = "oci-image"
			return c
		}, nvkrerr.CodeInvalidCatalog},
		{"unknown token in the ref", func(c Component) Component {
			c.Prebuilt.URL = "https://x/{{.Bogus}}/kubectl"
			return c
		}, nvkrerr.CodeInvalidCatalog},
		{"declares both fromSource and prebuilt", func(c Component) Component {
			c.FromSource = true
			return c
		}, nvkrerr.CodeInvalidCatalog},
		{"declares both prebuilt and an exclusionReason", func(c Component) Component {
			c.ExclusionReason = "shipped elsewhere"
			return c
		}, nvkrerr.CodeInvalidCatalog},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := base(tc.mut(goodPrebuilt())).Validate()
			if e, ok := err.(*nvkrerr.Error); !ok || e.Code != tc.want {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

// The {{.Arch}} token survives to the resolved ref (Compile resolves it later); it is
// the one per-arch literal a prebuilt ref may carry.
func TestPrebuiltArchTokenAccepted(t *testing.T) {
	c := goodPrebuilt()
	c.Prebuilt.Digests = map[string]string{"amd64": goodArtifactDigest, "arm64": goodArtifactDigest}
	if err := base(c).Validate(); err != nil {
		t.Fatalf("{{.Arch}} in a prebuilt ref must validate: %v", err)
	}
}

// applyComponentOverrides deep-merges valid overrides onto a component but fails closed
// on an override key that is not a Component field (a typo), so a layered overlay cannot
// silently drop an operator's intended override.

func TestLoadPrebuiltRendersRefAndMergesLock(t *testing.T) {
	reg := `apiVersion: nvkr.dev/catalog/v3
kind: SourceBuildRegistry
sources: {s: {repo: https://github.com/o/r}}
components:
  c:
    platforms: [linux/amd64, linux/arm64]
    source: s
    outputType: host-package
    prebuilt:
      url: https://dl.k8s.io/release/{{.Tag}}/bin/linux/{{.Arch}}/kubectl
      evidence: {none: true}
`
	lock := `apiVersion: nvkr.dev/lock/v1
kind: PinLock
pins:
  s: {tag: v1.32.0, commit: ` + goodCommit + `, treeHash: ` + goodTree + `, sourceDateEpoch: 123}
artifacts:
  c:
    tag: v1.32.0
    digests: {amd64: ` + goodArtifactDigest + `, arm64: ` + goodArtifactDigest + `}
`
	cat, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(reg), LockFile: []byte(lock)})
	if err != nil {
		t.Fatal(err)
	}
	c := cat.Components["c"]
	if c.Prebuilt == nil {
		t.Fatal("prebuilt not resolved")
	}
	wantURL := "https://dl.k8s.io/release/v1.32.0/bin/linux/{{.Arch}}/kubectl"
	if c.Prebuilt.URL != wantURL {
		t.Fatalf("ref not version-rendered with {{.Arch}} preserved: got %q want %q", c.Prebuilt.URL, wantURL)
	}
	if c.Prebuilt.Digests["amd64"] != goodArtifactDigest || c.Prebuilt.Digests["arm64"] != goodArtifactDigest {
		t.Fatalf("lock artifact digests not merged: %v", c.Prebuilt.Digests)
	}
	if c.Tag != "v1.32.0" || c.SourceDate != 123 {
		t.Fatalf("prebuilt did not inherit the source version pin: tag=%q epoch=%d", c.Tag, c.SourceDate)
	}
}

// Registry and lock must not carry duplicate semantic digest inputs. The lock is the
// single authoritative pin surface, so authored digests fail closed even if equal.
func TestLoadPrebuiltRejectsAuthoredDigestCollision(t *testing.T) {
	authored := "sha256:" + strings.Repeat("b", 64)
	locked := "sha256:" + strings.Repeat("c", 64)
	reg := `apiVersion: nvkr.dev/catalog/v3
kind: SourceBuildRegistry
sources: {s: {repo: https://github.com/o/r}}
components:
  c:
    platforms: [linux/amd64, linux/arm64]
    source: s
    outputType: raw
    prebuilt:
      url: https://x/{{.Arch}}/tool
      digests: {amd64: ` + authored + `}
      evidence: {none: true}
`
	lock := `apiVersion: nvkr.dev/lock/v1
kind: PinLock
pins:
  s: {tag: v1.0.0, commit: ` + goodCommit + `, treeHash: ` + goodTree + `, sourceDateEpoch: 1}
artifacts:
  c: {tag: v1.0.0, digests: {amd64: ` + locked + `}}
`
	_, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(reg), LockFile: []byte(lock)})
	if codeOf(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("duplicate registry/lock digest inputs must fail closed, got %v", err)
	}
}

// An unknown template token in a prebuilt ref fails closed at load (missingkey=error).
func TestLoadPrebuiltUnknownTokenFailsClosed(t *testing.T) {
	reg := `apiVersion: nvkr.dev/catalog/v3
kind: SourceBuildRegistry
sources: {s: {repo: https://github.com/o/r}}
components:
  c:
    platforms: [linux/amd64, linux/arm64]
    source: s
    outputType: raw
    prebuilt:
      url: https://x/{{.Nonsense}}/tool
      evidence: {none: true}
`
	lock := `apiVersion: nvkr.dev/lock/v1
kind: PinLock
pins:
  s: {tag: v1.0.0, commit: ` + goodCommit + `, treeHash: ` + goodTree + `, sourceDateEpoch: 1}
artifacts:
  c: {tag: v1.0.0, digests: {amd64: ` + goodArtifactDigest + `, arm64: ` + goodArtifactDigest + `}}
`
	_, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(reg), LockFile: []byte(lock)})
	if e, ok := err.(*nvkrerr.Error); !ok || e.Code != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("unknown ref token must fail closed, got %v", err)
	}
}

func TestFixedImageArtifactTagIndependence(t *testing.T) {
	for _, rendered := range []bool{false, true} {
		label := "fixed"
		if rendered {
			label = "source-rendered"
		}
		t.Run(label, func(t *testing.T) {
			var baseline *Catalog
			for _, tag := range []string{"v1.32.0", "v1.31.0"} {
				t.Run(tag, func(t *testing.T) {
					ref := "registry.example.test/tool:v1.32.0"
					if rendered {
						ref = "registry.example.test/tool:{{.Tag}}"
					}
					reg := `apiVersion: nvkr.dev/catalog/v3
kind: SourceBuildRegistry
sources: {s: {repo: https://github.com/o/r}}
components:
  c:
    platforms: [linux/amd64, linux/arm64]
    source: s
    outputType: oci-image
    prebuilt:
      image: ` + ref + `
      evidence: {none: true}
`
					lock := `apiVersion: nvkr.dev/lock/v1
kind: PinLock
pins:
  s: {tag: v1.32.0, commit: ` + goodCommit + `, treeHash: ` + goodTree + `, sourceDateEpoch: 123}
artifacts:
  c:
    tag: ` + tag + `
    imageParentDigest: ` + goodArtifactDigest + `
    digests: {amd64: ` + goodArtifactDigest + `, arm64: ` + goodArtifactDigest + `}
`
					got, err := LoadContext(t.Context(), bytesProvider{RegistryFile: []byte(reg), LockFile: []byte(lock)})
					if rendered && tag != "v1.32.0" {
						if codeOf(err) != nvkrerr.CodeInvalidCatalog {
							t.Fatalf("stale rendered acquisition: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if baseline == nil {
						baseline = got
						return
					}
					if !reflect.DeepEqual(got.Components, baseline.Components) {
						t.Fatal("descriptive tag changed resolved acquisition or parent/native bindings")
					}
					a, err := baseline.SnapshotDigest()
					if err != nil {
						t.Fatal(err)
					}
					b, err := got.SnapshotDigest()
					if err != nil || a != b {
						t.Fatalf("descriptive tag changed catalog identity: %s %s %v", a, b, err)
					}
				})
			}
		})
	}
}

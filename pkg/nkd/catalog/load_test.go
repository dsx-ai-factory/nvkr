package catalog

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// bytesProvider serves a catalog from in-memory bytes, for exercising Load's parse
// path (including strict-decode fail-closed behavior).
type bytesProvider map[string][]byte

func (b bytesProvider) ReadFile(name string) ([]byte, error) {
	v, ok := b[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return v, nil
}
func (bytesProvider) ReadDir(string) ([]string, error) { return nil, nil }
func (bytesProvider) Name() string                     { return "bytes" }
func (b bytesProvider) ReadFileContext(_ context.Context, name string) ([]byte, error) {
	return b.ReadFile(name)
}
func (b bytesProvider) ReadDirContext(_ context.Context, name string) ([]string, error) {
	return b.ReadDir(name)
}

const (
	goodCommit = "70d3cc986aa8221cd1dfb1121852688902d3bf53"
	goodTree   = "195abcc8a74b362b88fc76b0ee676a3354e4937e"
	goodImage  = "golang@sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72"
)

func base(c Component) *Catalog {
	return &Catalog{
		Components: map[string]Component{"x": c},
	}
}

func goodComponent() Component {
	return Component{Platforms: []string{"linux/amd64"},
		FromSource: true, Repo: "https://github.com/kubernetes/kubernetes",
		Commit: goodCommit, TreeHash: goodTree, ToolchainImage: goodImage,
		OutputType: "host-package", BuildCommand: []string{"make"},
		ArtifactPath: "_output/bin/kubectl", SourceDate: 1733939955,
	}
}

func TestKubernetesProducerFixtureValidates(t *testing.T) {
	if _, err := LoadContext(context.Background(), Dir("../../../testdata/catalogs/kubernetes-layered")); err != nil {
		t.Fatalf("Kubernetes producer fixture must load and validate: %v", err)
	}
}

func TestValidateFailClosed(t *testing.T) {
	cases := []struct {
		name string
		mut  func(Component) Component
		want nkderr.Code
	}{
		{"unpinned commit (floating ref)", func(c Component) Component { c.Commit = "main"; return c }, nkderr.CodeUnpinnedSource},
		{"short commit", func(c Component) Component { c.Commit = "70d3cc9"; return c }, nkderr.CodeUnpinnedSource},
		{"missing tree hash", func(c Component) Component { c.TreeHash = ""; return c }, nkderr.CodeUnpinnedSource},
		{"malformed tree hash", func(c Component) Component { c.TreeHash = "not-a-git-tree"; return c }, nkderr.CodeUnpinnedSource},
		{"toolchain by tag", func(c Component) Component { c.ToolchainImage = "golang:1.23.3"; return c }, nkderr.CodeTagToolchain},
		{"unknown native toolchain arch", func(c Component) Component {
			c.ToolchainImage = ""
			c.ToolchainImages = map[string]string{"riscv64": goodImage}
			return c
		}, nkderr.CodeInvalidCatalog},
		{"missing source date", func(c Component) Component { c.SourceDate = 0; return c }, nkderr.CodeUnpinnedSource},
		{"empty build command", func(c Component) Component { c.BuildCommand = nil; return c }, nkderr.CodeInvalidCatalog},
		{"missing outputType", func(c Component) Component { c.OutputType = ""; return c }, nkderr.CodeInvalidCatalog},
		{"unknown outputType", func(c Component) Component { c.OutputType = "floppy"; return c }, nkderr.CodeInvalidCatalog},
		// Only {{.Arch}} is a supported build-string token; any other fails closed so a
		// typo can't reach a build as a literal (mirrors the toolchain fail-closed rule).
		{"unknown token in buildCommand", func(c Component) Component {
			c.BuildCommand = []string{"make", "PLATFORM=linux/{{.Platform}}"}
			return c
		}, nkderr.CodeInvalidCatalog},
		{"unknown token in artifactPath", func(c Component) Component {
			c.ArtifactPath = "_output/bin/{{.Goarch}}/kubectl"
			return c
		}, nkderr.CodeInvalidCatalog},
		// ADR-11 §4 scheme allowlist, per field role.
		{"source repo disallowed scheme", func(c Component) Component {
			c.Repo = "ftp://example.com/kubernetes"
			return c
		}, nkderr.CodeInvalidCatalog},
		{"source repo http (not https)", func(c Component) Component {
			c.Repo = "http://github.com/kubernetes/kubernetes"
			return c
		}, nkderr.CodeInvalidCatalog},
		{"source repo bare local path", func(c Component) Component {
			c.Repo = "../mutable-checkout"
			return c
		}, nkderr.CodeInvalidCatalog},
		{"toolchain ref carries a url scheme", func(c Component) Component {
			c.ToolchainImage = "https://ghcr.io/x@sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72"
			return c
		}, nkderr.CodeInvalidCatalog},
		{"malformed toolchain repository", func(c Component) Component {
			c.ToolchainImage = "not valid!@sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72"
			return c
		}, nkderr.CodeInvalidCatalog},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := base(tc.mut(goodComponent())).Validate()
			if e, ok := err.(*nkderr.Error); !ok || e.Code != tc.want {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

// The {{.Arch}} token (Compile substitutes it with the target arch) is accepted in a
// build command and artifact path — it is the one per-arch literal a lane may carry.
// ADR-10: a nix builder must carry a fully-pinned flake; oci-hermetic must not.
func TestBuilderTypeValidation(t *testing.T) {
	goodFlake := &FlakePin{
		Ref:        "github:kubernetes/kubernetes?rev=" + goodCommit,
		LockDigest: "sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72",
		Output:     "packages.x86_64-linux.kubelet",
	}
	cases := []struct {
		name string
		mut  func(Component) Component
		want nkderr.Code
	}{
		{"unknown builder", func(c Component) Component { c.BuilderType = "bazel"; return c }, nkderr.CodeInvalidCatalog},
		{"nix without flake", func(c Component) Component { c.BuilderType = "nix"; return c }, nkderr.CodeUnpinnedSource},
		{"oci-hermetic with flake", func(c Component) Component { c.Flake = goodFlake; return c }, nkderr.CodeInvalidCatalog},
		{"nix with unpinned lock", func(c Component) Component {
			c.BuilderType = "nix"
			c.Flake = &FlakePin{Ref: goodFlake.Ref, LockDigest: "sha256:short", Output: goodFlake.Output}
			return c
		}, nkderr.CodeUnpinnedSource},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if e, ok := base(tc.mut(goodComponent())).Validate().(*nkderr.Error); !ok || e.Code != tc.want {
				t.Fatalf("want %s, got %v", tc.want, base(tc.mut(goodComponent())).Validate())
			}
		})
	}
	// a well-formed nix component validates
	good := goodComponent()
	good.BuilderType = "nix"
	good.Flake = goodFlake
	if err := base(good).Validate(); err != nil {
		t.Fatalf("well-formed nix component must validate: %v", err)
	}
}

func TestArchTokenAccepted(t *testing.T) {
	c := goodComponent()
	c.VendorCommand = []string{"fetch", "{{.Arch}}"}
	c.BuildCommand = []string{"make", "KUBE_BUILD_PLATFORMS=linux/{{.Arch}}"}
	c.ArtifactPath = "_output/local/bin/linux/{{.Arch}}/kubectl"
	if err := base(c).Validate(); err != nil {
		t.Fatalf("{{.Arch}} token must validate: %v", err)
	}
}

func TestVendorArchTokensAtSourceAndComponentBoundaries(t *testing.T) {
	for _, token := range []string{"{{.Arch}}", "{{.Ach}}", "{{ .Arch }}"} {
		for _, boundary := range []string{"source", "component"} {
			t.Run(boundary+"/"+token, func(t *testing.T) {
				var err error
				if boundary == "source" {
					err = validateRegistryDeclarations(&registryDoc{Sources: map[string]Source{
						"source": {Repo: "https://example.com/source", VendorCommand: []string{"fetch", token}},
					}})
				} else {
					component := goodComponent()
					component.VendorCommand = []string{"fetch", token}
					err = base(component).Validate()
				}
				want := nkderr.CodeInvalidCatalog
				if token == "{{.Arch}}" {
					want = ""
				}
				if codeOf(err) != want {
					t.Fatalf("vendor token %q: error=%v, want %s", token, err, want)
				}
				if err != nil && boundary == "source" &&
					(!strings.Contains(err.Error(), `catalog source "source"`) || strings.Contains(err.Error(), `component "source"`)) {
					t.Fatalf("source token error has the wrong subject: %v", err)
				}
			})
		}
	}
}

func TestExclusionMustBeDeclared(t *testing.T) {
	// Tier-4: not from source and no reason -> fail closed (NKD-FR-RC-026).
	err := base(Component{Platforms: []string{"linux/amd64"}, FromSource: false}).Validate()
	if e, ok := err.(*nkderr.Error); !ok || e.Code != nkderr.CodeInvalidCatalog {
		t.Fatalf("undeclared exclusion must fail: %v", err)
	}
	// with a reason it is accepted as a declared exception.
	if err := base(Component{Platforms: []string{"linux/amd64"}, FromSource: false, ExclusionReason: "proprietary CSI"}).Validate(); err != nil {
		t.Fatalf("declared exclusion should validate: %v", err)
	}
}

// Composition (base/overlays/mixins) is a retained-but-inert v1 hook: the fields
// are recognized (not stripped) but any populated value fails closed rather than
// being silently flattened (NKD-FR-RC-014/015, NKD-CON-017).

func TestUpdateReadersValidateSourceDeclarations(t *testing.T) {
	cases := []string{
		`{repo: ../mutable-checkout, toolchainImage: ` + goodImage + `}`,
		`{repo: https://github.com/o/r, toolchainBaseImage: golang, toolchainImage: ` + goodImage + `}`,
		`{repo: https://github.com/o/r, toolchainBaseImage: golang:latest}`,
		`{repo: https://github.com/o/r, toolchainImagesByArch: {riscv64: ` + goodImage + `}}`,
	}
	for _, source := range cases {
		doc := `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
sources: {s: ` + source + `}
components: {x: {platforms: [linux/amd64], fromSource: false, exclusionReason: test}}
`
		if _, err := ReadSourcesContext(context.Background(), bytesProvider{RegistryFile: []byte(doc)}); err == nil {
			t.Fatalf("update-only source reader accepted invalid declaration %s", source)
		}
	}
}

// A full operator catalog (registry + lock via a provider) resolves: the source and
// pin merge into the concrete component and the version-stamp templates render from
// the pin. This is the --data path and the churn-decoupling in one test.
func TestLoadResolvesSourceAndRendersStamps(t *testing.T) {
	reg := `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  s: {repo: https://github.com/o/r, toolchainImage: ` + goodImage + `}
components:
  c:
    platforms: [linux/amd64, linux/arm64]
    fromSource: true
    source: s
    outputType: host-package
    buildCommand: [make]
    artifactPath: bin/c
    versionStamp: {V: "{{.Tag}}", C: "{{.Commit}}", MM: "{{.Major}}.{{.Minor}}"}
`
	lock := `apiVersion: nkd.dev/lock/v1
kind: PinLock
pins:
  s: {tag: v1.32.0, commit: ` + goodCommit + `, treeHash: ` + goodTree + `, sourceDateEpoch: 123}
`
	cat, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(reg), LockFile: []byte(lock)})
	if err != nil {
		t.Fatal(err)
	}
	c := cat.Components["c"]
	if c.Commit != goodCommit || c.Repo != "https://github.com/o/r" || c.ToolchainImage != goodImage {
		t.Fatalf("source+pin not merged: %+v", c)
	}
	if c.VersionStamp["V"] != "v1.32.0" || c.VersionStamp["C"] != goodCommit || c.VersionStamp["MM"] != "1.32" {
		t.Fatalf("stamps not rendered from pin: %v", c.VersionStamp)
	}
}

func TestLoadResolvesAndAuthenticatesPatchBytes(t *testing.T) {
	patch := []byte("diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new\n")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(patch))
	reg := `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  s: {repo: https://github.com/o/r, toolchainImage: ` + goodImage + `}
components:
  c:
    platforms: [linux/amd64, linux/arm64]
    fromSource: true
    source: s
    outputType: host-package
    buildCommand: [make]
    artifactPath: bin/c
    patches:
      - {name: patches/fix.patch, sha256: ` + digest + `}
`
	lock := `apiVersion: nkd.dev/lock/v1
kind: PinLock
pins:
  s: {tag: v1.0.0, commit: ` + goodCommit + `, treeHash: ` + goodTree + `, sourceDateEpoch: 123}
`
	provider := bytesProvider{
		RegistryFile: []byte(reg), LockFile: []byte(lock), "patches/fix.patch": patch,
	}
	cat, err := LoadContext(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	got := cat.Components["c"].Patches
	if len(got) != 1 || got[0].Content != string(patch) || got[0].SHA256 != digest {
		t.Fatalf("patch bytes were not resolved into the catalog: %+v", got)
	}

	provider["patches/fix.patch"] = []byte("tampered")
	if _, err := LoadContext(context.Background(), provider); codeOf(err) != nkderr.CodeUnpinnedSource {
		t.Fatalf("tampered patch must fail at catalog load, got %v", err)
	}
}

func TestRegistryCannotAuthorResolvedPatchContentInline(t *testing.T) {
	doc := `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
components:
  x:
    platforms: [linux/amd64, linux/arm64]
    fromSource: false
    exclusionReason: test
    patches: [{name: p.patch, sha256: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa, content: forged}]
`
	if _, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(doc)}); codeOf(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("inline resolved patch content must be rejected by strict decoding, got %v", err)
	}
}

func codeOf(err error) nkderr.Code {
	if coded, ok := err.(*nkderr.Error); ok {
		return coded.Code
	}
	return ""
}

// A toolchain resolved into the lock (from .go-version) takes precedence over the
// source's explicit digest; with neither, the component is unpinned.
func TestToolchainFromPinPrecedence(t *testing.T) {
	const pinTC = "golang@sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72"
	reg := `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
sources: {s: {repo: https://github.com/o/r, toolchainBaseImage: golang}}
components:
  c: {platforms: [linux/amd64, linux/arm64], fromSource: true, source: s, outputType: host-package, buildCommand: [make], artifactPath: bin/c}
`
	lock := `apiVersion: nkd.dev/lock/v1
kind: PinLock
pins:
  s: {tag: v1.32.0, commit: ` + goodCommit + `, treeHash: ` + goodTree + `, sourceDateEpoch: 123, goVersion: "1.23.3", toolchainImage: ` + pinTC + `}
`
	cat, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(reg), LockFile: []byte(lock)})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Components["c"].ToolchainImage != pinTC {
		t.Fatalf("expected toolchain from the lock, got %q", cat.Components["c"].ToolchainImage)
	}
}

// A from-source component whose source has no lock pin fails closed as unpinned.
func TestLoadUnpinnedSourceFailsClosed(t *testing.T) {
	reg := `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
sources: {s: {repo: https://github.com/o/r, toolchainImage: ` + goodImage + `}}
components:
  c: {platforms: [linux/amd64, linux/arm64], fromSource: true, source: s, outputType: host-package, buildCommand: [make], artifactPath: bin/c}
`
	_, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(reg)}) // no lock provided
	if e, ok := err.(*nkderr.Error); !ok || e.Code != nkderr.CodeUnpinnedSource {
		t.Fatalf("missing pin must fail closed as unpinned, got %v", err)
	}
}

func TestVersionStampCannotOverrideReproVar(t *testing.T) {
	c := goodComponent()
	c.VersionStamp = map[string]string{"CGO_ENABLED": "1"} // would break hermeticity
	if e, ok := base(c).Validate().(*nkderr.Error); !ok || e.Code != nkderr.CodeInvalidCatalog {
		t.Fatalf("versionStamp overriding a pinned repro var must fail closed, got %v", base(c).Validate())
	}
	// a non-reserved stamp (e.g. CC, KUBE_GIT_*) is allowed.
	c.VersionStamp = map[string]string{"CC": "x86_64-linux-gnu-gcc", "KUBE_GIT_VERSION": "v1.32.0"}
	if err := base(c).Validate(); err != nil {
		t.Fatalf("legitimate versionStamp keys must validate: %v", err)
	}
}

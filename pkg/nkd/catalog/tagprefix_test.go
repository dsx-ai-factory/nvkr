package catalog

import (
	"context"
	"strings"
	"testing"
)

// A monorepo publishes each module under its own tag prefix (github.com/NVIDIA/nke tags
// its CLI as cli/v0.1.0-rc.2). These tests pin the whole prefix contract: the FULL tag is
// what git resolves and what the lock records, while every rendered version is
// prefix-free — and a source that declares no prefix keeps its exact previous behavior.

func TestQualifiedTagPrependsPrefixIdempotently(t *testing.T) {
	prefixed := Source{Repo: "https://github.com/NVIDIA/nke", TagPrefix: "cli/"}
	for _, given := range []string{"v0.1.0-rc.2", "cli/v0.1.0-rc.2"} {
		if got := prefixed.QualifiedTag(given); got != "cli/v0.1.0-rc.2" {
			t.Errorf("QualifiedTag(%q) = %q, want cli/v0.1.0-rc.2", given, got)
		}
	}
	root := Source{Repo: "https://github.com/kubernetes/kubernetes"}
	if got := root.QualifiedTag("v1.32.0"); got != "v1.32.0" {
		t.Errorf("root-tagged QualifiedTag = %q, want v1.32.0", got)
	}
}

func TestSelectTagForSourceOnlyConsidersPrefixedTags(t *testing.T) {
	tags := []string{
		"v0.9.0", "cli/v0.1.0", "cli/v0.1.4", "cli/v0.1.10", "cli/v0.1.11-rc.0",
		"cli/v0.2.0", "operator/v0.1.9",
	}
	src := Source{Repo: "https://github.com/NVIDIA/nke", TagPrefix: "cli/", Constraint: "0.1.x"}
	got, err := SelectTagForSource(tags, src)
	if err != nil {
		t.Fatal(err)
	}
	// Newest stable cli/ tag inside 0.1.x: 0.1.10 (10 > 4; rc excluded; cli/v0.2.0 out of
	// range; operator/ and the root v0.9.0 belong to other modules).
	if got != "cli/v0.1.10" {
		t.Fatalf("selected %q, want cli/v0.1.10", got)
	}
	if _, err := SelectTagForSource(tags, Source{TagPrefix: "cli/", Constraint: "9.9.x"}); err == nil {
		t.Error("no matching prefixed tag must fail closed")
	}
	if _, err := SelectTagForSource(tags, Source{TagPrefix: "docs/", Constraint: "0.1.x"}); err == nil ||
		!strings.Contains(err.Error(), "docs/") {
		t.Errorf("missing prefix family must name the prefix, got %v", err)
	}
}

// A source WITHOUT a prefix must never select a foreign module's tag, even though that
// tag's trailing segment parses as a newer semver.
func TestSelectTagIgnoresForeignPrefixedTags(t *testing.T) {
	tags := []string{"v1.32.0", "cli/v1.32.9", "sub/module/v1.32.8"}
	got, err := SelectTagForSource(tags, Source{Constraint: "1.32.x"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.32.0" {
		t.Fatalf("selected %q, want v1.32.0", got)
	}
}

func TestNormalizeAndSplitStripTagPrefix(t *testing.T) {
	if got := StripTagPrefix("cli/v0.1.0-rc.2"); got != "v0.1.0-rc.2" {
		t.Errorf("StripTagPrefix = %q", got)
	}
	if got := StripTagPrefix("v1.32.0"); got != "v1.32.0" {
		t.Errorf("root tag must be unchanged, got %q", got)
	}
	if got := NormalizeVersion("cli/v0.1.0-rc.2"); got != "0.1.0-rc.2" {
		t.Errorf("NormalizeVersion = %q, want 0.1.0-rc.2", got)
	}
	if got := NormalizeVersion("v1.32.0"); got != "1.32.0" {
		t.Errorf("unprefixed NormalizeVersion = %q, want 1.32.0", got)
	}
	maj, min, pat := splitVersion("cli/v0.1.0-rc.2")
	if maj != "0" || min != "1" || pat != "0" {
		t.Fatalf("prefixed split = %q.%q.%q, want 0.1.0", maj, min, pat)
	}
}

func TestRenderStampsPrefixAndDate(t *testing.T) {
	pin := Pin{
		Tag:             "cli/v0.1.0-rc.2",
		Commit:          "ed9fb09b04fd40bf331d25df2222206ffaca593e",
		TreeHash:        "309887e8cdb8ee24a39785b864390eda3afce5ec",
		SourceDateEpoch: 1787066575,
	}
	stamps, err := renderStamps(map[string]string{
		"NKECTL_VERSION": "{{.Version}}",
		"NKECTL_TAG":     "{{.Tag}}",
		"NKECTL_MODTAG":  "{{.ModuleTag}}",
		"NKECTL_COMMIT":  "{{slice .Commit 0 8}}",
		"NKECTL_DATE":    "{{.Date}}",
		"NKECTL_MAJOR":   "{{.Major}}",
		"NKECTL_MINOR":   "{{.Minor}}",
		"NKECTL_PATCH":   "{{.Patch}}",
	}, pin)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"NKECTL_VERSION": "0.1.0-rc.2",
		"NKECTL_TAG":     "cli/v0.1.0-rc.2",
		"NKECTL_MODTAG":  "v0.1.0-rc.2",
		"NKECTL_COMMIT":  "ed9fb09b",
		"NKECTL_DATE":    "2026-08-18T15:22:55Z",
		"NKECTL_MAJOR":   "0",
		"NKECTL_MINOR":   "1",
		"NKECTL_PATCH":   "0",
	}
	for k, v := range want {
		if stamps[k] != v {
			t.Errorf("stamp %s = %q, want %q", k, stamps[k], v)
		}
	}
}

// The pre-existing unprefixed stamp rendering must be byte-identical, including the new
// Date field being available without disturbing any existing token.
func TestRenderStampsUnprefixedUnchanged(t *testing.T) {
	pin := Pin{Tag: "v1.32.0", Commit: goodCommit, TreeHash: goodTree, SourceDateEpoch: 1733939955}
	stamps, err := renderStamps(map[string]string{
		"KUBE_GIT_VERSION":    "{{.Tag}}",
		"KUBE_GIT_COMMIT":     "{{.Commit}}",
		"KUBE_GIT_TREE_STATE": "clean",
		"KUBE_GIT_MAJOR":      "{{.Major}}",
		"KUBE_GIT_MINOR":      "{{.Minor}}",
	}, pin)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"KUBE_GIT_VERSION": "v1.32.0", "KUBE_GIT_COMMIT": goodCommit,
		"KUBE_GIT_TREE_STATE": "clean", "KUBE_GIT_MAJOR": "1", "KUBE_GIT_MINOR": "32",
	}
	for k, v := range want {
		if stamps[k] != v {
			t.Errorf("stamp %s = %q, want %q", k, stamps[k], v)
		}
	}
	if got := EpochDate(1733939955); got != "2024-12-11T17:59:15Z" {
		t.Errorf("EpochDate = %q", got)
	}
}

// A tagPrefix declaration must survive the strict registry decoder and reach the loaded
// catalog's source (it is authoring surface, not a machine-managed pin).
func TestRegistryAcceptsTagPrefix(t *testing.T) {
	registry := `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  nke:
    repo: https://github.com/NVIDIA/nke
    tagPrefix: cli/
    constraint: 0.1.x
    toolchainImage: ` + goodImage + `
components:
  nke-cli:
    platforms: [linux/amd64, linux/arm64]
    fromSource: true
    source: nke
    outputType: raw
    buildCommand: [go, build]
    artifactPath: bin/nkectl
    writeDir: bin
    versionStamp:
      NKECTL_VERSION: "{{.Version}}"
      NKECTL_DATE: "{{.Date}}"
`
	lock := `apiVersion: nkd.dev/lock/v1
kind: PinLock
pins:
  nke:
    tag: cli/v0.1.0-rc.2
    commit: ed9fb09b04fd40bf331d25df2222206ffaca593e
    treeHash: 309887e8cdb8ee24a39785b864390eda3afce5ec
    sourceDateEpoch: 1787066575
`
	cat, err := LoadContext(context.Background(), bytesProvider{RegistryFile: []byte(registry), LockFile: []byte(lock)})
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Sources["nke"].TagPrefix; got != "cli/" {
		t.Fatalf("loaded tagPrefix = %q, want cli/", got)
	}
	component := cat.Components["nke-cli"]
	if component.Tag != "cli/v0.1.0-rc.2" {
		t.Errorf("component tag = %q, want the full upstream tag", component.Tag)
	}
	if component.VersionStamp["NKECTL_VERSION"] != "0.1.0-rc.2" {
		t.Errorf("version stamp = %q, want the prefix-free version", component.VersionStamp["NKECTL_VERSION"])
	}
	if component.VersionStamp["NKECTL_DATE"] != "2026-08-18T15:22:55Z" {
		t.Errorf("date stamp = %q", component.VersionStamp["NKECTL_DATE"])
	}
}

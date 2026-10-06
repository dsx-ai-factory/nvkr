package catalog

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

type pinSetIntegrationFixture struct {
	repo     string
	dir      string
	run      func(...string) string
	registry *registryDoc
}

func newPinSetIntegrationFixture(t *testing.T, manifest string) *pinSetIntegrationFixture {
	t.Helper()
	if manifest == "" {
		manifest = "dependencies: [{name: cwe, version: 1.2.3}]\n"
	}
	repo, _, run := pinTreeFixture(t, map[string][]byte{"Chart.lock": []byte(manifest), "nested.lock": []byte("dependencies: [{name: agent, version: 3.4.5}]\n")})
	run("tag", "umbrella/v1.0.0")
	run("tag", "controller/v1.2.3")
	const upstream = "https://pin-set.example.invalid/repository"
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", upstream)
	fixture := &pinSetIntegrationFixture{
		repo: repo, dir: t.TempDir(), run: run,
		registry: &registryDoc{
			APIVersion: catalogAPIVersion, Kind: catalogKind,
			Sources: map[string]Source{
				"umbrella": {Repo: upstream, TagPrefix: "umbrella/v", Constraint: "1.x", PinSet: &PinSet{File: "Chart.lock", Members: map[string]PinSetMember{
					"a-cwe": {Source: "cwe", Path: "dependencies[name=cwe].version"},
				}}},
				"cwe": {Repo: upstream, TagPrefix: "controller/v", PinnedBy: "umbrella", ToolchainImage: goodImage},
			},
			Components: map[string]rawComponent{
				"controller": {Platforms: []string{"linux/amd64"}, Source: "cwe", FromSource: true, OutputType: "host-package", BuildCommand: []string{"make"}, ArtifactPath: "bin/controller"},
			},
		},
	}
	fixture.writeRegistry(t)
	return fixture
}

func (fixture *pinSetIntegrationFixture) writeRegistry(t *testing.T) {
	t.Helper()
	raw, err := yaml.Marshal(fixture.registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.dir, RegistryFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fixture *pinSetIntegrationFixture) commitManifest(t *testing.T, manifest, tag string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.repo, "Chart.lock"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.run("add", "Chart.lock")
	fixture.run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "new curated versions")
	fixture.run("tag", tag)
}

func (fixture *pinSetIntegrationFixture) pin(t *testing.T, tag string) SourcePinUpdate {
	t.Helper()
	update, err := UpdateSourcePins(t.Context(), SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Tag: tag})
	if err != nil {
		t.Fatal(err)
	}
	return update
}

func (fixture *pinSetIntegrationFixture) lock(t *testing.T) *PinLock {
	t.Helper()
	lock, err := LoadLockContext(t.Context(), fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	return lock
}

func TestPinSetIntegrationSourceRoundTrip(t *testing.T) {
	fixture := newPinSetIntegrationFixture(t, "")
	first := fixture.pin(t, "1.0.0")
	if first.Changed != 2 || len(first.Pins) != 2 || first.Pins[0].Name != "umbrella" || first.Pins[1].Name != "cwe" {
		t.Fatalf("first pin must report whole set in derivation order: %+v", first)
	}
	lock := fixture.lock(t)
	if lock.Pins["umbrella"].Tag != "umbrella/v1.0.0" || lock.Pins["cwe"].Tag != "controller/v1.2.3" || lock.Pins["cwe"].DerivedFrom != "umbrella@"+lock.Pins["umbrella"].Commit {
		t.Fatalf("unexpected initial pins: %+v", lock.Pins)
	}
	cat, err := LoadContext(t.Context(), Dir(fixture.dir))
	if err != nil || cat.Components["controller"].Commit != lock.Pins["cwe"].Commit {
		t.Fatalf("pinned source must reach loaded producer: %v, %v", cat, err)
	}
	before := readPinFixtureLock(t, fixture.dir)
	noOp := fixture.pin(t, "1.0.0")
	if noOp.Changed != 0 || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
		t.Fatal("re-pinning same complete set changed lock bytes")
	}
	fixture.commitManifest(t, "dependencies: [{name: cwe, version: 1.2.4}]\n", "umbrella/v1.1.0")
	fixture.run("tag", "controller/v1.2.4")
	request := SourcePinRequest{Directory: fixture.dir, Bump: true, All: true, Check: true}
	checked, err := UpdateSourcePins(t.Context(), request)
	assertCatalogContractCode(t, err, e.CodeDiff)
	if checked.Changed != 2 || len(checked.Pins) != 2 || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
		t.Fatalf("check must describe whole set without writing: %+v", checked)
	}
	request.Check = false
	written, err := UpdateSourcePins(t.Context(), request)
	if err != nil || !reflect.DeepEqual(written, checked) {
		t.Fatalf("check and write candidates differ: check=%+v write=%+v err=%v", checked, written, err)
	}
	lock = fixture.lock(t)
	if lock.Pins["umbrella"].Tag != "umbrella/v1.1.0" || lock.Pins["cwe"].Tag != "controller/v1.2.4" || lock.Pins["cwe"].DerivedFrom != "umbrella@"+lock.Pins["umbrella"].Commit {
		t.Fatalf("unexpected updated pins: %+v", lock.Pins)
	}
	if _, err := LoadContext(t.Context(), Dir(fixture.dir)); err != nil {
		t.Fatal(err)
	}
}

func TestPinSetIntegrationRejectsIndependentMemberUpdates(t *testing.T) {
	fixture := newPinSetIntegrationFixture(t, "")
	fixture.pin(t, "1.0.0")
	before := readPinFixtureLock(t, fixture.dir)
	for _, request := range []SourcePinRequest{
		{Sources: []string{"cwe"}, Tag: "1.2.4"},
		{Sources: []string{"cwe"}, Bump: true},
		{Sources: []string{"umbrella", "cwe"}, Bump: true},
	} {
		request.Directory = fixture.dir
		update, err := UpdateSourcePins(t.Context(), request)
		assertCatalogContractCode(t, err, e.CodeUnsupported)
		if !strings.Contains(err.Error(), "umbrella") || len(update.Pins) != 0 || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
			t.Fatalf("member must be rejected before resolution and without a write: %+v %v", update, err)
		}
	}
}

func TestPinSetIntegrationLoadRederivesInsteadOfTrustingProvenance(t *testing.T) {
	fixture := newPinSetIntegrationFixture(t, "")
	fixture.pin(t, "1.0.0")
	before := readPinFixtureLock(t, fixture.dir)
	for _, tc := range []struct {
		name string
		edit func(*PinLock)
	}{
		{"stale version with correct provenance", func(lock *PinLock) { pin := lock.Pins["cwe"]; pin.Tag = "controller/v1.2.4"; lock.Pins["cwe"] = pin }},
		{"wrong provenance with correct version", func(lock *PinLock) {
			pin := lock.Pins["cwe"]
			pin.DerivedFrom = "umbrella@" + strings.Repeat("f", 40)
			lock.Pins["cwe"] = pin
		}},
		{"missing provenance", func(lock *PinLock) { pin := lock.Pins["cwe"]; pin.DerivedFrom = ""; lock.Pins["cwe"] = pin }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lock, err := decodePinLock(before, "test fixture")
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(lock)
			raw, err := yaml.Marshal(lock)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.dir, LockFile), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = LoadContext(t.Context(), Dir(fixture.dir))
			assertCatalogContractCode(t, err, e.CodePinSetMismatch)
		})
	}
}

func TestPinSetIntegrationMembershipFailurePreservesLock(t *testing.T) {
	fixture := newPinSetIntegrationFixture(t, "")
	fixture.pin(t, "1.0.0")
	before := readPinFixtureLock(t, fixture.dir)
	for index, tc := range []struct{ name, manifest string }{
		{"unknown", "dependencies: [{name: cwe, version: 1.2.3}, {name: extra, version: 1.0.0}]\n"},
		{"duplicate", "dependencies: [{name: cwe, version: 1.2.3}, {name: cwe, version: 1.2.3}]\n"},
		{"missing", "dependencies: []\n"},
		{"range", "dependencies: [{name: cwe, version: '^1.2.3'}]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tag := fmt.Sprintf("umbrella/v1.%d.0", index+1)
			fixture.commitManifest(t, tc.manifest, tag)
			_, err := UpdateSourcePins(t.Context(), SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Tag: tag})
			assertCatalogContractCode(t, err, e.CodePinSetMismatch)
			if !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
				t.Fatal("failed membership validation changed committed lock")
			}
		})
	}
	// The checkout and latest upstream tag now disagree with the locked tree.
	// A valid old lock still loads from that exact original commit.
	if _, err := LoadContext(t.Context(), Dir(fixture.dir)); err != nil {
		t.Fatalf("load used a moving ref or working tree: %v", err)
	}
}

func TestPinSetIntegrationAcquiredMembersAndRollback(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, strings.TrimPrefix(pinFixtureDigest(strings.TrimSuffix(r.URL.Path, ".sha256")), "sha256:"))
	}))
	t.Cleanup(server.Close)
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })
	fixture := newPinSetIntegrationFixture(t, "dependencies: [{name: cwe, version: 1.2.3}, {name: tool, version: 2.0.0}, {name: late, version: 3.0.0}]\n")
	fixture.run("tag", "late/v3.0.0")
	fixture.registry.Sources["umbrella"].PinSet.Members["b-tool"] = PinSetMember{Artifact: "tool", Path: "dependencies[name=tool].version"}
	fixture.registry.Sources["umbrella"].PinSet.Members["z-late"] = PinSetMember{Source: "late", Path: "dependencies[name=late].version"}
	fixture.registry.Sources["late"] = Source{Repo: fixture.registry.Sources["cwe"].Repo, TagPrefix: "late/v", PinnedBy: "umbrella"}
	fixture.registry.Components["tool"] = rawComponent{
		Platforms: []string{"linux/amd64", "linux/arm64"}, PinnedBy: "umbrella", OutputType: "raw",
		Prebuilt: &Prebuilt{URL: server.URL + "/tool/{{.Version}}/{{.Arch}}", Evidence: &ExternalEvidence{None: true}},
	}
	fixture.writeRegistry(t)
	initial := fixture.pin(t, "1.0.0")
	if initial.Changed != 4 || len(initial.Artifacts) != 1 {
		t.Fatalf("source and acquired members must share initial update: %+v", initial)
	}
	lock := fixture.lock(t)
	want := ArtifactPin{Tag: "2.0.0", DerivedFrom: "umbrella@" + lock.Pins["umbrella"].Commit, Digests: map[string]string{
		"amd64": pinFixtureDigest("/tool/2.0.0/amd64"), "arm64": pinFixtureDigest("/tool/2.0.0/arm64"),
	}}
	if !reflect.DeepEqual(lock.Artifacts["tool"], want) {
		t.Fatalf("acquired pin = %+v; want %+v", lock.Artifacts["tool"], want)
	}
	cat, err := LoadContext(t.Context(), Dir(fixture.dir))
	if err != nil || cat.Components["tool"].Prebuilt.URL != server.URL+"/tool/2.0.0/{{.Arch}}" {
		t.Fatalf("derived artifact version did not reach producer: %v, %v", cat, err)
	}
	before := readPinFixtureLock(t, fixture.dir)
	for _, request := range []ArtifactPinRequest{{Components: []string{"tool"}}, {All: true}} {
		request.Directory = fixture.dir
		beforeRequests := requests.Load()
		_, err := UpdateArtifactPins(t.Context(), request)
		assertCatalogContractCode(t, err, e.CodeUnsupported)
		if requests.Load() != beforeRequests || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
			t.Fatal("independent artifact update performed resolution or wrote a lock")
		}
	}
	fixture.commitManifest(t, "dependencies: [{name: cwe, version: 1.2.3}, {name: tool, version: 2.1.0}, {name: late, version: 3.1.0}]\n", "umbrella/v1.1.0")
	beforeRequests := requests.Load()
	update, err := UpdateSourcePins(t.Context(), SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Tag: "1.1.0"})
	assertCatalogContractCode(t, err, e.CodeFetchFailed)
	if len(update.Artifacts) != 1 || requests.Load()-beforeRequests != 2 || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
		t.Fatalf("later failed source must roll back already resolved artifact members: %+v, requests=%d", update, requests.Load()-beforeRequests)
	}
	fixture.run("tag", "late/v3.1.0")
	request := SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Tag: "1.1.0", Check: true}
	checked, err := UpdateSourcePins(t.Context(), request)
	assertCatalogContractCode(t, err, e.CodeDiff)
	if !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
		t.Fatal("artifact-inclusive dry run changed lock")
	}
	request.Check = false
	written, err := UpdateSourcePins(t.Context(), request)
	if err != nil || !reflect.DeepEqual(checked, written) {
		t.Fatalf("artifact-inclusive check differs from write: check=%+v write=%+v err=%v", checked, written, err)
	}
	if _, err := LoadContext(t.Context(), Dir(fixture.dir)); err != nil {
		t.Fatal(err)
	}
}

func TestPinSetIntegrationExplicitNestedSets(t *testing.T) {
	fixture := newPinSetIntegrationFixture(t, "")
	fixture.run("tag", "agent/v3.4.5")
	source := fixture.registry.Sources["cwe"]
	source.PinSet = &PinSet{File: "nested.lock", Members: map[string]PinSetMember{"agent": {Source: "agent", Path: "dependencies[name=agent].version"}}}
	fixture.registry.Sources["cwe"] = source
	fixture.registry.Sources["agent"] = Source{Repo: source.Repo, TagPrefix: "agent/v", PinnedBy: "cwe"}
	fixture.writeRegistry(t)
	update := fixture.pin(t, "1.0.0")
	lock := fixture.lock(t)
	if update.Changed != 3 || lock.Pins["agent"].Tag != "agent/v3.4.5" || lock.Pins["agent"].DerivedFrom != "cwe@"+lock.Pins["cwe"].Commit {
		t.Fatalf("explicit nested set was not derived from immediate umbrella: %+v", lock.Pins)
	}
	if _, err := LoadContext(t.Context(), Dir(fixture.dir)); err != nil {
		t.Fatal(err)
	}
	// Closing the declared ownership cycle must fail at registry admission.
	agent := fixture.registry.Sources["agent"]
	agent.PinSet = &PinSet{File: "Chart.lock", Members: map[string]PinSetMember{"umbrella": {Source: "umbrella", Path: "dependencies[name=cwe].version"}}}
	fixture.registry.Sources["agent"] = agent
	umbrella := fixture.registry.Sources["umbrella"]
	umbrella.PinnedBy = "agent"
	fixture.registry.Sources["umbrella"] = umbrella
	fixture.writeRegistry(t)
	before := readPinFixtureLock(t, fixture.dir)
	_, err := UpdateSourcePins(t.Context(), SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Tag: "1.0.0"})
	assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
	if !strings.Contains(err.Error(), "cycle") || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
		t.Fatalf("cycle did not fail at admission: %v", err)
	}
}

func TestPinSetRejectsInvalidAdapterDeclarationsBeforeIO(t *testing.T) {
	const registry = `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  umbrella:
    repo: https://example.invalid/umbrella
    pinSet:
      file: versions.yaml
      format: values
      members:
        tool: {artifact: tool, path: tool.version}
components:
  tool:
    platforms: [linux/amd64]
    pinnedBy: umbrella
    outputType: raw
    prebuilt:
      url: https://example.invalid/tool/{{.Version}}
      evidence: {none: true}
`
	if _, err := decodeRegistry([]byte(registry), "valid generic fixture"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, old, replacement, diagnostic string
	}{
		{"unknown format", "format: values", "format: unknown", "unsupported format"},
		{"accounted items", "      members:", "      accounted: {config: {type: manifest}}\n      members:", "accounted items require nke-deploy-v1"},
		{"member chart", "artifact: tool,", "chart: {name: tool, repository: https://example.invalid/charts}, artifact: tool,", "chart identity requires nke-deploy-v1"},
		{"prebuilt chart", "    prebuilt:", "    prebuilt:\n      chart: {name: tool, repository: https://example.invalid/charts}", "requires outputType raw and no url/image"},
		{"resolved OCI archive", "    prebuilt:", "    prebuilt:\n      ociArchive: https://example.invalid/archive", "authors resolved ociArchive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Pure declaration admission has no upstream or lock I/O to perform.
			raw := strings.Replace(registry, tc.old, tc.replacement, 1)
			reg, err := decodeRegistry([]byte(raw), "unsupported adapter fixture")
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			if reg != nil || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("adapter schema did not fail at admission: %+v, %v", reg, err)
			}
		})
	}
}

func TestPinSetImageRuntimeSelection(t *testing.T) {
	for _, sourceOwned := range []bool{false, true} {
		for _, custom := range []bool{false, true} {
			t.Run(fmt.Sprintf("source=%t/custom=%t", sourceOwned, custom), func(t *testing.T) {
				fixture := newPinSetIntegrationFixture(t, "")
				image := rawComponent{Platforms: []string{"linux/amd64"}, OutputType: "oci-image",
					Prebuilt: &Prebuilt{Image: "example.invalid/tool:{{.Version}}", Evidence: &ExternalEvidence{None: true}}}
				version := "1.2.3"
				if sourceOwned {
					image.Source = "cwe"
				} else {
					delete(fixture.registry.Sources, "cwe")
					delete(fixture.registry.Components, "controller")
					fixture.registry.Sources["umbrella"].PinSet.Members["a-cwe"] = PinSetMember{Artifact: "image", Path: "dependencies[name=cwe].version"}
					image.PinnedBy = "umbrella"
				}
				fixture.registry.Components["image"] = image
				fixture.writeRegistry(t)
				digest := "sha256:" + strings.Repeat("a", 64)
				request := SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Tag: "1.0.0"}
				if custom {
					request.Runtime = "unused-legacy-runtime"
				}
				resolveArtifact := func(ctx context.Context, selected PrebuiltTarget, runtime string) (ArtifactPin, error) {
					if custom {
						t.Fatal("non-default legacy runtime reached acquisition")
					}
					if runtime != "docker" || selected.Image != "example.invalid/tool:"+version {
						t.Fatalf("maintenance projection = %q, %q", runtime, selected.Image)
					}
					return ArtifactPin{Tag: selected.Tag, Digests: map[string]string{"amd64": digest}}, nil
				}
				update, err := updateSourcePinsWithArtifacts(t.Context(), request, ListTags, ResolvePin, resolveArtifact)
				if custom {
					assertCatalogContractCode(t, err, e.CodeUsage)
					_, lockErr := os.Stat(filepath.Join(fixture.dir, LockFile))
					if !os.IsNotExist(lockErr) || update.Changed != 0 || len(update.Pins) != 0 || len(update.Artifacts) != 0 {
						t.Fatalf("non-default runtime produced a partial update: %+v, %v", update, lockErr)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := fixture.lock(t).Artifacts["image"].Digests["amd64"]; got != digest {
					t.Fatalf("image digest = %s; want %s", got, digest)
				}
			})
		}
	}
}

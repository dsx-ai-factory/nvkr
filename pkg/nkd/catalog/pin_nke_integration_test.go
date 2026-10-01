package catalog

import (
	"archive/tar"
	"bytes"
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

type nkePinIntegrationFixture struct {
	repo         string
	dir          string
	run          func(...string) string
	tree         *nkeFixtureTree
	repository   string
	packages     map[string][]byte
	requests     atomic.Int32
	failTemporal atomic.Bool
}

func newNKEPinIntegrationFixture(t *testing.T) *nkePinIntegrationFixture {
	t.Helper()
	fixture := &nkePinIntegrationFixture{dir: t.TempDir(), packages: map[string][]byte{}}
	var index strings.Builder
	index.WriteString("apiVersion: v1\nentries:\n")
	for _, chart := range []struct {
		name     string
		versions []string
	}{
		{"postgresql", []string{"18.5.15"}},
		{"spire", []string{"0.30.1", "0.30.2", "v0.30.3"}},
		{"spire-crds", []string{"0.6.1"}},
		{"temporal", []string{"0.74.0"}},
	} {
		fmt.Fprintf(&index, "  %s:\n", chart.name)
		for _, version := range chart.versions {
			location := chart.name + "-" + version + ".tgz"
			archive := helmFixtureArchiveRecords(t, []helmFixtureRecord{{
				header: tar.Header{Name: chart.name + "/Chart.yaml", Mode: 0o644},
				body:   fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", chart.name, version),
			}})
			fixture.packages["/charts/"+location] = archive
			fmt.Fprintf(&index, "  - name: %s\n    version: %s\n    urls: [%s]\n    digest: %s\n", chart.name, version, location, pinFixtureDigest(string(archive)))
		}
	}
	indexBytes := []byte(index.String())
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.requests.Add(1)
		if r.URL.Path == "/charts/index.yaml" {
			_, _ = w.Write(indexBytes)
			return
		}
		if fixture.failTemporal.Load() && r.URL.Path == "/charts/temporal-0.74.0.tgz" {
			http.Error(w, "fixture acquisition failure", http.StatusServiceUnavailable)
			return
		}
		if archive, exists := fixture.packages[r.URL.Path]; exists {
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })
	fixture.repository = server.URL + "/charts"
	reg, tree := nkePinFixture(t)
	fixture.tree = tree
	reg.APIVersion, reg.Kind = catalogAPIVersion, catalogKind
	for name, producer := range reg.Components {
		chart := producer.Prebuilt.Chart
		file := "controller/deploy/nke-sbom/" + chart.Name + "/release.yaml"
		// Preserve the checked-in upstream versions and shape, replacing only the
		// reviewed acquisition identity with the local TLS fixture repository.
		replaceNKEFixture(t, tree, file, "repository: "+chart.Repository, "repository: "+fixture.repository)
		chart.Repository = fixture.repository
		producer.Platforms = []string{"linux/amd64", "linux/arm64"}
		producer.OutputType = "raw"
		producer.Prebuilt.Evidence = &ExternalEvidence{None: true}
		reg.Components[name] = producer
	}
	fixture.repo, _, fixture.run = pinTreeFixture(t, tree.files)
	fixture.run("tag", "controller/v1.0.0")
	const upstream = "https://nke-pin-set.example.invalid/repository"
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+fixture.repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", upstream)
	source := reg.Sources["nke"]
	source.Repo, source.TagPrefix = upstream, "controller/v"
	reg.Sources["nke"] = source
	raw, err := yaml.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.dir, RegistryFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture *nkePinIntegrationFixture) commit(t *testing.T, tag string) {
	t.Helper()
	for name, raw := range fixture.tree.files {
		if err := os.WriteFile(filepath.Join(fixture.repo, filepath.FromSlash(name)), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.run("add", ".")
	fixture.run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "updated NKE deploy fixture")
	fixture.run("tag", tag)
}

func (fixture *nkePinIntegrationFixture) request(tag string) SourcePinRequest {
	return SourcePinRequest{Directory: fixture.dir, Sources: []string{"nke"}, Tag: tag}
}

func (fixture *nkePinIntegrationFixture) assertLock(t *testing.T, spireVersion string) {
	t.Helper()
	lock, err := LoadLockContext(t.Context(), fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Pins) != 1 || len(lock.Artifacts) != 4 {
		t.Fatalf("want one NKE pin and four acquired charts, got %d and %d", len(lock.Pins), len(lock.Artifacts))
	}
	beforeRequests := fixture.requests.Load()
	cat, err := LoadContext(t.Context(), Dir(fixture.dir))
	if err != nil {
		t.Fatal(err)
	}
	if fixture.requests.Load() != beforeRequests {
		t.Fatal("catalog load re-acquired charts instead of validating pinned source metadata")
	}
	for name, version := range map[string]string{"spire": spireVersion, "spire-crds": "0.6.1", "temporal": "0.74.0", "postgresql": "18.5.15"} {
		location := "/charts/" + name + "-" + version + ".tgz"
		digest := pinFixtureDigest(string(fixture.packages[location]))
		pin := lock.Artifacts[name+"-chart"]
		want := ArtifactPin{
			Tag: version, DerivedFrom: "nke@" + lock.Pins["nke"].Commit,
			URL:     fixture.repository + "/" + name + "-" + version + ".tgz",
			Chart:   &HelmChart{Name: name, Repository: fixture.repository},
			Digests: map[string]string{"amd64": digest, "arm64": digest},
		}
		if !reflect.DeepEqual(pin, want) {
			t.Fatalf("%s pin = %+v; want %+v", name, pin, want)
		}
		component := cat.Components[name+"-chart"]
		if component.Tag != version || component.Prebuilt.URL != want.URL || !reflect.DeepEqual(component.Prebuilt.Digests, want.Digests) {
			t.Fatalf("loaded chart producer does not match derived pin: %+v", component)
		}
	}
}

func TestNKEPinSetIntegrationRoundTrip(t *testing.T) {
	fixture := newNKEPinIntegrationFixture(t)
	update, err := UpdateSourcePins(t.Context(), fixture.request("1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if update.Changed != 5 || len(update.Pins) != 1 || len(update.Artifacts) != 4 {
		t.Fatalf("initial pin did not include complete NKE set: %+v", update)
	}
	fixture.assertLock(t, "0.30.1")
	before := readPinFixtureLock(t, fixture.dir)
	update, err = UpdateSourcePins(t.Context(), fixture.request("1.0.0"))
	if err != nil || update.Changed != 0 || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
		t.Fatalf("unchanged set did not preserve exact lock bytes: %+v, %v", update, err)
	}
	replaceNKEFixture(t, fixture.tree, "controller/deploy/nke-sbom/spire/release.yaml", "version: \"0.30.1\"", "version: \"0.30.2\"")
	fixture.commit(t, "controller/v1.1.0")
	request := fixture.request("1.1.0")
	request.Check = true
	checked, err := UpdateSourcePins(t.Context(), request)
	assertCatalogContractCode(t, err, e.CodeDiff)
	if checked.Changed != 5 || len(checked.Artifacts) != 4 || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
		t.Fatalf("check must describe entire newly curated set without writing: %+v", checked)
	}
	request.Check = false
	written, err := UpdateSourcePins(t.Context(), request)
	if err != nil || !reflect.DeepEqual(checked, written) {
		t.Fatalf("NKE check and write candidates differ: check=%+v write=%+v err=%v", checked, written, err)
	}
	fixture.assertLock(t, "0.30.2")
}

func TestNKEPinSetIntegrationFailuresPreserveLock(t *testing.T) {
	fixture := newNKEPinIntegrationFixture(t)
	if _, err := UpdateSourcePins(t.Context(), fixture.request("1.0.0")); err != nil {
		t.Fatal(err)
	}
	before := readPinFixtureLock(t, fixture.dir)
	originalDeploy := bytes.Clone(fixture.tree.files[nkeFixtureDeploy])
	fixture.tree.files[nkeFixtureDeploy] = append(fixture.tree.files[nkeFixtureDeploy], []byte("  - {name: new-chart, type: helm}\n")...)
	fixture.commit(t, "controller/v1.1.0")
	beforeRequests := fixture.requests.Load()
	_, err := UpdateSourcePins(t.Context(), fixture.request("1.1.0"))
	assertCatalogContractCode(t, err, e.CodePinSetMismatch)
	if fixture.requests.Load() != beforeRequests || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
		t.Fatal("unknown deploy member acquired charts or changed lock bytes")
	}
	fixture.tree.files[nkeFixtureDeploy] = originalDeploy
	replaceNKEFixture(t, fixture.tree, "controller/deploy/nke-sbom/spire/release.yaml", "version: \"0.30.1\"", "version: \"0.30.2\"")
	fixture.commit(t, "controller/v1.2.0")
	fixture.failTemporal.Store(true)
	update, err := UpdateSourcePins(t.Context(), fixture.request("1.2.0"))
	assertCatalogContractCode(t, err, e.CodeFetchFailed)
	if len(update.Artifacts) != 3 || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
		t.Fatalf("last acquisition failure did not preserve complete original set: %+v", update)
	}
	// The original pinned tree must still load after both failed re-pins even
	// while the newer deployment metadata is present in the upstream checkout.
	fixture.assertLock(t, "0.30.1")
}

func TestNKEFieldsRejectedByValuesPinSetFormat(t *testing.T) {
	for _, format := range []string{"", "values"} {
		for _, field := range []string{"accounted", "member chart"} {
			t.Run(format+"/"+field, func(t *testing.T) {
				fixture := newPinSetIntegrationFixture(t, "")
				fixture.pin(t, "1.0.0")
				before := readPinFixtureLock(t, fixture.dir)
				set := fixture.registry.Sources["umbrella"].PinSet
				set.Format = format
				if field == "accounted" {
					set.Accounted = map[string]PinSetItem{"configuration": {Type: "manifest"}}
				} else {
					member := set.Members["a-cwe"]
					member.Chart = &HelmChart{Name: "cwe", Repository: "https://charts.example.invalid"}
					set.Members["a-cwe"] = member
				}
				fixture.writeRegistry(t)
				update, err := UpdateSourcePins(t.Context(), SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Tag: "1.0.0"})
				assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
				if !strings.Contains(err.Error(), "nke-deploy-v1") || len(update.Pins) != 0 || !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
					t.Fatalf("values format accepted NKE-only fields: %+v, %v", update, err)
				}
			})
		}
	}
}

func TestNKEPinSetExampleRegistryValidates(t *testing.T) {
	const example = "../../../docs/examples/nke-pin-set/registry.yaml"
	raw, err := os.ReadFile(example)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := decodeRegistry(raw, example)
	if err != nil {
		t.Fatalf("documented NKE catalog must pass full declaration admission: %v", err)
	}
	set := reg.Sources["nke-controller"].PinSet
	if set == nil || set.Format != "nke-deploy-v1" || set.File != nkeFixtureDeploy {
		t.Fatal("documented example does not select the NKE deploy adapter")
	}
}

func TestNKEPinPreservesExactChartVersion(t *testing.T) {
	fixture := newNKEPinIntegrationFixture(t)
	replaceNKEFixture(t, fixture.tree, "controller/deploy/nke-sbom/spire/release.yaml", `version: "0.30.1"`, `version: "v0.30.3"`)
	fixture.commit(t, "controller/v1.1.0")
	if _, err := UpdateSourcePins(t.Context(), fixture.request("1.1.0")); err != nil {
		t.Fatal(err)
	}
	fixture.assertLock(t, "v0.30.3")
}

func TestSourceOwnedAcquiredChartPreservesDeclaredVersion(t *testing.T) {
	for _, tc := range []struct{ prefix, version string }{
		{"", "v1.2.3"}, {"controller/", "v1.2.3"}, {"controller/v", "1.2.3"},
	} {
		t.Run(tc.prefix+tc.version, func(t *testing.T) {
			fixture := newPinSetIntegrationFixture(t, fmt.Sprintf("dependencies: [{name: cwe, version: %q}]\n", tc.version))
			src := fixture.registry.Sources["cwe"]
			src.TagPrefix = tc.prefix
			fixture.registry.Sources["cwe"] = src
			tag := src.QualifiedTag(tc.version)
			fixture.run("tag", "-f", tag)
			payload := helmFixtureArchive(t, fmt.Sprintf("apiVersion: v2\nname: demo\nversion: %q\n", tc.version))
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/index.yaml":
					fmt.Fprintf(w, "apiVersion: v1\nentries:\n  demo:\n  - name: demo\n    version: %q\n    urls: [demo.tgz]\n", tc.version)
				case "/demo.tgz":
					_, _ = w.Write(payload)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			original := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			t.Cleanup(func() { http.DefaultTransport = original })
			fixture.registry.Components["controller"] = rawComponent{Source: "cwe", Platforms: []string{"linux/amd64"}, OutputType: "raw", Prebuilt: &Prebuilt{Chart: &HelmChart{Name: "demo", Repository: server.URL}, Evidence: &ExternalEvidence{None: true}}}
			fixture.writeRegistry(t)
			fixture.pin(t, "1.0.0")
			lock := fixture.lock(t)
			artifact := lock.Artifacts["controller"]
			if artifact.Tag != tag || artifact.Digests["amd64"] != pinFixtureDigest(string(payload)) || artifact.DerivedFrom != "umbrella@"+lock.Pins["umbrella"].Commit {
				t.Fatalf("source/chart identities diverged: %+v", artifact)
			}
			if _, err := LoadContext(t.Context(), Dir(fixture.dir)); err != nil {
				t.Fatal(err)
			}
			before := readPinFixtureLock(t, fixture.dir)
			if update := fixture.pin(t, "1.0.0"); update.Changed != 0 {
				t.Fatalf("repeat pin changed: %+v", update)
			}
			fixture.commitManifest(t, "dependencies: [{name: cwe, version: v1.2.4}]\n", "umbrella/v1.1.0")
			fixture.run("tag", src.QualifiedTag("v1.2.4"))
			_, err := UpdateSourcePins(t.Context(), SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Tag: "1.1.0"})
			assertCatalogContractCode(t, err, e.CodeInvalidCatalog)
			if !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
				t.Fatal("failed chart lookup changed the lock")
			}
		})
	}
}

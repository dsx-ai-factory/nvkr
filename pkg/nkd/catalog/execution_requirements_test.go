package catalog

import (
	"context"
	"strings"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

func TestBuildRequirementsLoadStrictlyAndAffectCatalogIdentity(t *testing.T) {
	registry := `apiVersion: nkd.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  source:
    repo: https://example.test/source
    toolchainImage: ` + goodImage + `
components:
  artifact:
    platforms: [linux/amd64, linux/arm64]
    fromSource: true
    source: source
    outputType: raw
    buildRequirements:
      resources:
        cpuMillis: 2000
        memoryMiB: 4096
        ephemeralStorageMiB: 8192
      allowedHostPlatforms:
        - linux/amd64
        - linux/arm64
      capabilities:
        - name: kvm
          policy: preferred
      timeoutSeconds: 7200
    buildCommand:
      - make
    writeDir: out
    artifactPath: out/artifact
`
	lock := `apiVersion: nkd.dev/lock/v1
kind: PinLock
pins:
  source:
    tag: v1.0.0
    commit: ` + goodCommit + `
    treeHash: ` + goodTree + `
    sourceDateEpoch: 1733939955
`
	load := func(t *testing.T, authored string) *Catalog {
		t.Helper()
		cat, err := LoadContext(context.Background(), bytesProvider{
			RegistryFile: []byte(authored), LockFile: []byte(lock),
		})
		if err != nil {
			t.Fatal(err)
		}
		return cat
	}

	cat := load(t, registry)
	got := cat.Components["artifact"].BuildRequirements
	want := &execution.Requirements{
		Resources: &execution.ResourceRequest{
			CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
		},
		AllowedHostPlatforms: []execution.HostPlatform{
			execution.HostPlatformLinuxAMD64, execution.HostPlatformLinuxARM64,
		},
		Capabilities: []execution.CapabilityRequirement{{
			Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
		}},
		TimeoutSeconds: 7200,
	}
	if !execution.Equal(got, want) {
		t.Fatalf("loaded build requirements = %#v, want %#v", got, want)
	}
	unknown := strings.Replace(registry, "cpuMillis: 2000", "cpuMillis: 2000\n        futureResource: 1", 1)
	if _, err := LoadContext(context.Background(), bytesProvider{
		RegistryFile: []byte(unknown), LockFile: []byte(lock),
	}); catalogErrorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), "futureResource") {
		t.Fatalf("unknown nested buildRequirements field was not rejected strictly: %v", err)
	}
	unknownCapabilityField := strings.Replace(registry, "name: kvm", "name: kvm\n          device: /dev/kvm", 1)
	if _, err := LoadContext(context.Background(), bytesProvider{
		RegistryFile: []byte(unknownCapabilityField), LockFile: []byte(lock),
	}); catalogErrorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), "device") {
		t.Fatalf("unknown nested capability field was not rejected strictly: %v", err)
	}

	before, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, old, replacement string
	}{
		{name: "resource", old: "memoryMiB: 4096", replacement: "memoryMiB: 8192"},
		{name: "host platform", old: "\n        - linux/arm64", replacement: ""},
		{name: "capability policy", old: "policy: preferred", replacement: "policy: required"},
		{name: "timeout", old: "timeoutSeconds: 7200", replacement: "timeoutSeconds: 3600"},
	} {
		t.Run(test.name+" changes snapshot identity", func(t *testing.T) {
			changed := load(t, strings.Replace(registry, test.old, test.replacement, 1))
			after, err := changed.SnapshotDigest()
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Fatalf("%s did not affect catalog snapshot identity", test.name)
			}
		})
	}
}

func TestBuildRequirementsValidation(t *testing.T) {
	request := &execution.Requirements{
		Resources: &execution.ResourceRequest{
			CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
		},
		AllowedHostPlatforms: []execution.HostPlatform{
			execution.HostPlatformLinuxAMD64, execution.HostPlatformLinuxARM64,
		},
		Capabilities: []execution.CapabilityRequirement{{
			Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
		}},
		TimeoutSeconds: 7200,
	}
	for _, test := range []struct {
		name string
		edit func(*Component)
	}{
		{name: "empty requirements", edit: func(component *Component) {
			component.BuildRequirements = &execution.Requirements{}
		}},
		{name: "empty resources", edit: func(component *Component) {
			component.BuildRequirements = &execution.Requirements{Resources: &execution.ResourceRequest{}}
		}},
		{name: "non-source component", edit: func(component *Component) {
			component.FromSource = false
			component.ExclusionReason = "not built"
			component.BuildRequirements = execution.Clone(request)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			component := goodComponent()
			test.edit(&component)
			if err := base(component).Validate(); catalogErrorCode(err) != nkderr.CodeInvalidCatalog {
				t.Fatalf("invalid build requirements returned %v", err)
			}
		})
	}

}

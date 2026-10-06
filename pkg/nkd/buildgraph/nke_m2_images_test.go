package buildgraph

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

func TestMaintainedM2EnvironmentUsesSupportedRuntimeControls(t *testing.T) {
	environment, err := definition.LoadEnvironment(t.Context(), "../../../definitions/nke-m2/environment-github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if environment.Runtime.Runtime != "podman" || environment.Runtime.BuildNetworkPolicy != "producer-declared" {
		t.Fatalf("candidate runtime controls changed: %+v", environment.Runtime)
	}
	if len(environment.Runtime.Variables) != 0 || len(environment.Runtime.Setup) != 0 {
		t.Fatal("candidate supplies an ambient storage/isolation workaround")
	}
	if len(environment.ExecutionClasses) != 0 {
		t.Fatal("unmeasured execution classes must remain absent from candidate inputs")
	}
	if len(environment.DiskInspectionProfiles) != 0 {
		t.Fatal("unmeasured native inspector profiles must remain absent from candidate inputs")
	}
}

func TestMaintainedM2DeploymentImageMembership(t *testing.T) {
	const root = "../../../definitions/nke-m2"
	const commit = "33fb71b2f4224b470ebc628a9165fc4b92870406"
	expected := map[string]string{
		"csi-node-driver-registrar-image": "registry.k8s.io/sig-storage/csi-node-driver-registrar:v2.15.0",
		"postgresql-image":                "registry-1.docker.io/bitnami/postgresql@sha256:b74b23f091dcc87041bcae557d840c36458af46b955279cb13aedec4c32d7f71",
		"spiffe-csi-image":                "ghcr.io/spiffe/spiffe-csi-driver:0.2.7",
		"spire-agent-image":               "ghcr.io/spiffe/spire-agent:1.15.3",
		"spire-bash-image":                "cgr.dev/chainguard/bash@sha256:90041f375e30f41aa7e0390075d8a69dc61900771d52fa98d63ee5d03d866a58",
		"spire-controller-manager-image":  "ghcr.io/spiffe/spire-controller-manager:0.7.0",
		"spire-oidc-image":                "ghcr.io/spiffe/oidc-discovery-provider:1.15.3",
		"spire-server-image":              "ghcr.io/spiffe/spire-server:1.15.3",
		"spire-tpm-server-image":          "ghcr.io/spiffe/spire-tpm-plugin-tpm-attestor-server:v1.9.0",
		"temporal-admin-tools-image":      "temporalio/admin-tools:1.30.3",
		"temporal-server-image":           "temporalio/server:1.30.3",
		"temporal-ui-image":               "temporalio/ui:2.48.1",
	}
	authored, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(authored.Outputs) != 34 {
		t.Fatalf("got %d outputs, want original ten plus 24 native image members", len(authored.Outputs))
	}
	provider := catalog.Dir(filepath.Join(root, "image-catalog")).(catalog.ContextDataProvider)
	cat, err := catalog.LoadContext(t.Context(), provider)
	var coded *e.Error
	if cat != nil || !errors.As(err, &coded) || coded.Code != e.CodeUnverifiedRef {
		t.Fatalf("authored indexes must fail at catalog admission: catalog=%+v error=%v", cat, err)
	}
	// This is a literal selection check, not an alternate resolved catalog. The
	// owner above rejects the entire catalog before any component can execute.
	raw, err := provider.ReadFileContext(t.Context(), catalog.RegistryFile)
	if err != nil {
		t.Fatal(err)
	}
	var declarations struct {
		Components map[string]struct {
			Source   string
			Prebuilt struct{ Image string }
		}
	}
	if err := yaml.Unmarshal(raw, &declarations); err != nil {
		t.Fatal(err)
	}
	if len(declarations.Components) != len(expected) {
		t.Fatalf("got %d image declarations, want %d", len(declarations.Components), len(expected))
	}
	raw, err = provider.ReadFileContext(t.Context(), catalog.LockFile)
	if err != nil {
		t.Fatal(err)
	}
	var lock catalog.PinLock
	if err := yaml.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Pins["nke-deployment-selection"].Commit != commit {
		t.Fatal("deployment selection source pin changed")
	}
	seen := map[string]map[string]bool{}
	for _, output := range authored.Outputs {
		if output.Catalog == nil || output.Catalog.Base != "./image-catalog" {
			continue
		}
		if output.Format != definition.OutputOCI || len(output.Components) != 1 {
			t.Fatalf("bad image output %+v", output)
		}
		component := output.Components[0]
		want, ok := expected[component]
		if !ok {
			t.Fatalf("undeclared deployment image %s", component)
		}
		selected := declarations.Components[component]
		if selected.Source != "nke-deployment-selection" || selected.Prebuilt.Image != want {
			t.Fatalf("wrong source/selection for %s", component)
		}
		if seen[component] == nil {
			seen[component] = map[string]bool{}
		}
		if seen[component][output.Platform] {
			t.Fatalf("duplicate native image member %s/%s", component, output.Platform)
		}
		seen[component][output.Platform] = true
	}
	for component := range expected {
		if !seen[component]["linux/amd64"] || !seen[component]["linux/arm64"] {
			t.Fatalf("missing native image member for %s", component)
		}
	}
}

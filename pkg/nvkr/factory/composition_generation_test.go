// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func compositeEnvironment(t *testing.T, mutate func(environment *authored.Environment)) authored.Environment {
	t.Helper()
	_, environment := fixtureInputs("snapshot-fixture", "tools", "")
	mutate(&environment)
	return environment
}

func namedDevelopment(t *testing.T, bundle, prefix string) func(environment *authored.Environment) {
	return func(environment *authored.Environment) {
		namespace, err := development.ParseNamespace(prefix)
		identity := "^https://github\\.com/example/nvkr/\\.github/workflows/nvkr-dev-" + bundle + "\\.yml@refs/heads/[^[:space:]]+$"
		if err == nil {
			identity = namespace.Identity()
		}
		environment.GitHubDevelopment, environment.GitHubBundle = true, bundle
		environment.Publication = &authored.Publication{Mode: "per-output-v1", RepositoryPrefix: prefix, CredentialRef: development.WriteCredentialRef}
		environment.Signing = &authored.Signing{Mode: SigningModeCosign, Identity: identity, Issuer: development.Issuer}
	}
}

// renderComposite compiles the composite fixture under environment and checks
// that its generated bundle regenerates byte-exactly beneath root.
func renderComposite(t *testing.T, root string, environment authored.Environment) (*Plan, *GeneratedBundle) {
	t.Helper()
	writeSnapshotFixtureDocument(t, root, snapshotEnvironment, environment)
	plan, _, err := CaptureCompileFiles(t.Context(), root, compositeFixturePath, snapshotEnvironment, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}
	return plan, writeCheckedBundle(t, root, plan)
}

// writeCheckedBundle writes plan's generated bundle beneath root and requires
// a fresh rendering to match it byte-exactly.
func writeCheckedBundle(t *testing.T, root string, plan *Plan) *GeneratedBundle {
	t.Helper()
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
		t.Fatal(err)
	}
	again, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneratedBundle(t.Context(), root, again); err != nil {
		t.Fatalf("bundle does not regenerate byte-exactly: %v", err)
	}
	return bundle
}

// signedComposite admits a two-member composite from memory and compiles it
// against the signed fixture catalog, as the signing contracts do.
func signedComposite(t *testing.T, plain bool, environment authored.Environment) (*Plan, error) {
	t.Helper()
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	base.Tools = map[string]catalog.ToolPin{"cosign": {Format: ToolFormatTarGzip, URL: "https://example.invalid/cosign.tar.gz", Platform: "linux/amd64",
		ArchiveDigest: testCosignArchiveDigest, ExecutablePath: "cosign", ExecutableDigest: testCosignExecutableDigest}}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	loader := func(context.Context, authored.Catalog) (*catalog.Stack, error) { return stack, nil }
	files := map[string][]byte{}
	composite := authored.CompositeDefinition{APIVersion: authored.CompositeAPIVersion, Kind: authored.CompositeKind, Metadata: authored.Metadata{Name: "product"}}
	var members []authored.Definition
	for _, member := range []struct{ name, component string }{{"alpha", "containerd"}, {"beta", "kubelet"}} {
		definition, _ := fixtureInputs(member.name, member.name+"-tools", "")
		definition.Catalog.Base, definition.CosignTool = "catalog", "cosign"
		definition.Outputs[0].Components = []string{member.component}
		files["defs/"+member.name+".json"] = mustCanonical(t, definition)
		digest, err := definition.Digest()
		if err != nil {
			t.Fatal(err)
		}
		composite.Members = append(composite.Members, authored.CompositeMember{Name: member.name, Definition: member.name + ".json", Digest: digest})
		members = append(members, definition)
	}
	if plain {
		return compileDocumentsWithLoader(t.Context(), "defs/alpha.json", members[0], "defs/environment.json", environment, "v0.0.0 (test)", loader)
	}
	read := func(_ context.Context, name string) ([]byte, error) {
		raw, ok := files[name]
		if !ok {
			return nil, e.New(e.CodeInvalidCatalog, "missing %s", name)
		}
		return raw, nil
	}
	composition, err := authored.AdmitComposite(t.Context(), "defs/composite.json", mustCanonical(t, composite), read)
	if err != nil {
		t.Fatal(err)
	}
	return compileComposition(t.Context(), composition, "defs/environment.json", environment, "v0.0.0 (test)", loader)
}

func TestCompositeProductionBundles(t *testing.T) {
	for _, tc := range []struct {
		name       string
		partitions bool
	}{{"flat", false}, {"partitioned", true}} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := compositeFactoryFixture(t, nil)
			plan, bundle := renderComposite(t, root, compositeEnvironment(t, func(environment *authored.Environment) {
				environment.GitHubBundle, environment.GitHubExecutionPartitions = "product", tc.partitions
			}))
			if plan.SchemaVersion != CompositePlanSchemaVersion || bundle.WorkflowPath != ".github/workflows/nvkr-run-product.yml" ||
				bundle.PlanPath != ".github/nvkr-run-product/plan.json" || bundle.ManifestPath != ".github/nvkr-run-product/manifest.json" {
				t.Fatalf("composite bundle layout = %s %s %s", bundle.WorkflowPath, bundle.PlanPath, bundle.ManifestPath)
			}
			if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
				t.Fatal(err)
			}
			reloaded, err := LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath))
			if err != nil || reloaded.Composition == nil || reloaded.DefinitionDigest != plan.DefinitionDigest {
				t.Fatalf("generated composite Plan does not reload with its composition record: %v", err)
			}

			// A member edit without a re-pin fails before generation can proceed.
			alpha, err := authored.LoadDefinition(t.Context(), filepath.Join(root, "config", "alpha.json"))
			if err != nil {
				t.Fatal(err)
			}
			alpha.Metadata.Description = "edited without a re-pin"
			writeSnapshotFixtureDocument(t, root, "config/alpha.json", alpha)
			_, _, err = CaptureCompileFiles(t.Context(), root, compositeFixturePath, snapshotEnvironment, "v0.0.0 (test)")
			assertFactoryErrorCode(t, err, e.CodeUnverifiedRef)
		})
	}
}

func TestCompositeDevelopmentBundles(t *testing.T) {
	root := t.TempDir()
	plan, err := signedComposite(t, false, compositeEnvironment(t, namedDevelopment(t, "product", "ghcr.io/example/nvkr/development-product")))
	if err != nil {
		t.Fatal(err)
	}
	bundle := writeCheckedBundle(t, root, plan)
	if bundle.WorkflowPath != ".github/workflows/nvkr-dev-product.yml" || bundle.PlanPath != ".github/nvkr-dev-product/plan.json" ||
		bundle.ManifestPath != ".github/nvkr-dev-product/manifest.json" {
		t.Fatalf("named development layout = %s %s %s", bundle.WorkflowPath, bundle.PlanPath, bundle.ManifestPath)
	}
	if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
		t.Fatal(err)
	}
	for name := range bundle.Files {
		if name == GitHubDevelopmentWorkflowPath || strings.HasPrefix(name, ".github/nvkr-dev/") {
			t.Fatalf("named development bundle writes the fixed development layout: %s", name)
		}
	}

	// The fixed development bundle of a plain Definition coexists beside it.
	fixed := githubDevelopmentPublicationTestPlan(t)
	fixedBundle := writeCheckedBundle(t, root, fixed)
	for _, check := range []struct {
		plan   *Plan
		bundle *GeneratedBundle
	}{{plan, bundle}, {fixed, fixedBundle}} {
		again, err := RenderGitHub(t.Context(), check.plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckGeneratedBundle(t.Context(), root, again); err != nil {
			t.Fatalf("development bundles overwrite each other: %v", err)
		}
	}

	// Signer identities are disjoint, and neither is a production signer.
	named := regexp.MustCompile(plan.Environment.Signing.Identity)
	for subject, want := range map[string]bool{
		"https://github.com/example/nvkr/.github/workflows/nvkr-dev-product.yml@refs/heads/feature": true,
		"https://github.com/example/nvkr/.github/workflows/nvkr-dev.yml@refs/heads/feature":         false,
		"https://github.com/example/nvkr/.github/workflows/nvkr-run-product.yml@refs/heads/main":    false,
	} {
		if named.MatchString(subject) != want {
			t.Fatalf("named development signer admits %q = %t", subject, !want)
		}
	}
}

func TestCompositeDevelopmentAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(environment *authored.Environment)
		plain  bool
	}{
		{name: "plain Definition with named development bundle", plain: true, mutate: namedDevelopment(t, "product", "ghcr.io/example/nvkr/development-product")},
		{name: "unsigned named development", mutate: func(environment *authored.Environment) {
			environment.GitHubDevelopment, environment.GitHubBundle = true, "product"
		}},
		{name: "fixed development namespace", mutate: namedDevelopment(t, "product", "ghcr.io/example/nvkr/development")},
		{name: "another bundle's namespace", mutate: namedDevelopment(t, "product", "ghcr.io/example/nvkr/development-other")},
		{name: "reserved bundle name", mutate: namedDevelopment(t, "qualify", "ghcr.io/example/nvkr/development-qualify")},
		{name: "named namespace without bundle", mutate: func(environment *authored.Environment) {
			namedDevelopment(t, "product", "ghcr.io/example/nvkr/development-product")(environment)
			environment.GitHubBundle = ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := signedComposite(t, tc.plain, compositeEnvironment(t, tc.mutate))
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if plan != nil {
				t.Fatal("refused development mode produced a Plan")
			}
		})
	}
}

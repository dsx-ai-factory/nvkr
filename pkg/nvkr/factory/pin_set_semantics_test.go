// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCompileRejectsMalformedPinSetSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*catalog.PinSet)
	}{
		{"path escape", func(set *catalog.PinSet) { set.File = "../invalid.yaml" }},
		{"unknown format", func(set *catalog.PinSet) { set.Format = "unsupported" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, boundary := range []string{"compile", "root snapshot", "output snapshot"} {
				t.Run(boundary, func(t *testing.T) {
					base, doc, env := pinSetSemanticInputs(t)
					stack, err := catalog.ApplyStack(base)
					if err != nil {
						t.Fatal(err)
					}
					if got := uncheckedPinSetSnapshotDigest(t, stack.Catalog); got != stack.Digest {
						t.Fatal("test hash does not match the catalog snapshot codec")
					}
					if boundary == "compile" {
						tc.mutate(stack.Catalog.Sources["pins"].PinSet)
						stack.Digest = uncheckedPinSetSnapshotDigest(t, stack.Catalog)
						plan, err := Compile(t.Context(), doc, env, stack, "test (deadbeef)")
						assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
						if plan != nil {
							t.Fatal("malformed pin set emitted a Plan")
						}
						return
					}
					doc.Outputs[0].Catalog = &definition.Catalog{Base: "output-catalog"}
					plan, err := compileDocumentsWithLoader(t.Context(), "examples/definition.yaml", doc,
						"examples/environment.yaml", env, "test (deadbeef)",
						func(context.Context, definition.Catalog) (*catalog.Stack, error) { return stack, nil })
					if err != nil {
						t.Fatal(err)
					}
					plan.DefinitionPath, plan.EnvironmentPath = "examples/definition.yaml", "examples/environment.yaml"
					snapshot, digest := plan.CatalogSnapshot, &plan.CatalogDigest
					if boundary == "output snapshot" {
						snapshot, digest = plan.Builds[0].CatalogSnapshot, &plan.Builds[0].CatalogDigest
					}
					tc.mutate(snapshot.Sources["pins"].PinSet)
					*digest = uncheckedPinSetSnapshotDigest(t, snapshot)
					assertFactoryErrorCode(t, plan.Validate(), e.CodeInvalidCatalog)
					bundle, err := RenderGitHub(t.Context(), plan)
					assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
					if bundle != nil {
						t.Fatal("malformed pin set emitted a credential-bearing bundle")
					}
				})
			}
		})
	}
}

func TestResolvedPinSetCompilationRetainsOverlaySupport(t *testing.T) {
	for _, overlay := range []bool{false, true} {
		t.Run(map[bool]string{false: "base", true: "overlay"}[overlay], func(t *testing.T) {
			base, doc, env := pinSetSemanticInputs(t)
			digest, err := base.SnapshotDigest()
			if err != nil {
				t.Fatal(err)
			}
			var overlays []catalog.Overlay
			if overlay {
				replacement := base.Components["image"]
				replacement.Tag = "2.0.0"
				overlays = []catalog.Overlay{{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
					Name: "supplier", RequiresCatalogDigest: digest,
					Components: map[string]catalog.ComponentReplacement{"image": {Component: &replacement}},
				}}
			}
			stack, err := catalog.ApplyStack(base, overlays...)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), doc, env, stack, "test (deadbeef)")
			if err != nil {
				t.Fatal(err)
			}
			plan.DefinitionPath, plan.EnvironmentPath = "examples/definition.yaml", "examples/environment.yaml"
			if _, err := RenderGitHub(t.Context(), plan); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func pinSetSemanticInputs(t *testing.T) (*catalog.Catalog, definition.Definition, definition.Environment) {
	t.Helper()
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	const repository = "https://github.com/example/private-pins"
	base.Sources = map[string]catalog.Source{"pins": {Repo: repository,
		PinSet: &catalog.PinSet{File: "versions.yaml", Members: map[string]catalog.PinSetMember{
			"image": {Artifact: "image", Path: "image.version"},
		}},
	}}
	base.Components = map[string]catalog.Component{"image": {
		Platforms: []string{"linux/amd64"}, Tag: "1.2.3", SourceDate: 1700000000, OutputType: "oci-image",
		Prebuilt: &catalog.Prebuilt{Image: "supplier.example/image:1.2.3",
			Digests:  map[string]string{"amd64": "sha256:" + strings.Repeat("a", 64)},
			Evidence: &catalog.ExternalEvidence{None: true}},
	}}
	doc, env := fixtureInputs("pin-set-semantics", "acquired", "ubuntu-24.04")
	doc.Outputs[0].Components, doc.Outputs[0].Format = []string{"image"}, definition.OutputOCI
	env.SourceAccess = map[string]definition.SourceAccess{repository: {Mirror: repository, CredentialRef: "github:PIN_SOURCE_TOKEN"}}
	return base, doc, env
}

// Model a caller recomputing the typed integrity digest over an invalid
// description. A matching digest must never replace semantic admission, and
// neither this fixture nor resolved compilation authenticates upstream pins.
func uncheckedPinSetSnapshotDigest(t *testing.T, snapshot *catalog.Catalog) string {
	t.Helper()
	value := map[string]any{
		"schemaVersion": "nvkr.dev/catalog-snapshot/v3", "apiVersion": snapshot.APIVersion,
		"kind": snapshot.Kind, "metadata": snapshot.Metadata, "sources": snapshot.Sources, "components": snapshot.Components,
	}
	if len(snapshot.Actions) != 0 {
		value["actions"] = snapshot.Actions
	}
	if len(snapshot.Tools) != 0 {
		value["tools"] = snapshot.Tools
	}
	digest, err := canonical.Digest(canonical.KindCatalogSnapshot, value)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

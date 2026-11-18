// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestCatalogNameMigrationPreservesBuildIdentity(t *testing.T) {
	root, def, _ := snapshotFixture(t)
	before, _, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "migration")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "catalog/registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var registry map[string]any
	if err := yaml.UnmarshalStrict(raw, &registry); err != nil {
		t.Fatal(err)
	}
	registry["metadata"].(map[string]any)["name"] = "family/catalog"
	writeSnapshotFixtureDocument(t, root, "catalog/registry.yaml", registry)
	_, _, err = CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "migration")
	assertFactoryErrorCode(t, err, e.CodeUnverifiedRef)
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, "catalog")))
	if err != nil {
		t.Fatal(err)
	}
	overlay, err := catalog.LoadOverlayContext(t.Context(), filepath.Join(root, "config/overlay.json"))
	if err != nil {
		t.Fatal(err)
	}
	overlay.RequiresCatalogDigest, err = base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotFixtureDocument(t, root, "config/overlay.json", overlay)
	def.Metadata.Name = "product-release"
	writeSnapshotFixtureDocument(t, root, snapshotDefinition, def)
	after, _, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "migration")
	if err != nil {
		t.Fatal(err)
	}
	oldDigest, err := before.Digest()
	if err != nil {
		t.Fatal(err)
	}
	newDigest, err := after.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if oldDigest == newDigest || before.DefinitionDigest == after.DefinitionDigest {
		t.Fatal("renamed metadata did not change Plan and Definition identities")
	}
	if len(before.Builds) != len(after.Builds) {
		t.Fatal("metadata changed build membership")
	}
	changed := false
	for i, build := range before.Builds {
		changed = changed || build.CatalogDigest != after.Builds[i].CatalogDigest
		if build.BuildDigest != after.Builds[i].BuildDigest || !reflect.DeepEqual(build.ResolvedComponents, after.Builds[i].ResolvedComponents) {
			t.Fatalf("metadata changed producer identity for %s", build.Name)
		}
	}
	if !changed {
		t.Fatal("renamed catalog retained snapshot identity")
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestAcquiredImageRejectsCaseFoldedSchemaMembers(t *testing.T) {
	for _, tc := range []struct{ name, object, field, alias string }{
		{"index", "index", "manifests", "Manifests"},
		{"manifest", "manifest", "config", "Config"},
		{"descriptor", "descriptor", "digest", "Digest"},
		{"config", "config", "os", "OS"},
		{"command", "command", "Entrypoint", "entrypoint"},
		{"rootfs", "rootfs", "diff_ids", "Diff_Ids"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := imageFixture(t)
			mutateImageJSON(t, root, tc.object, func(object map[string]any) { object[tc.alias] = object[tc.field] })
			_, err := InspectContainerImage(t.Context(), root, "linux/amd64")
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("case alias accepted: %v", err)
			}
		})
	}
}

func TestImageSchemaPreservesCaseDistinctFreeMapKeys(t *testing.T) {
	for _, object := range []string{"manifest", "command"} {
		t.Run(object, func(t *testing.T) {
			root := imageFixture(t)
			mutateImageJSON(t, root, object, func(fields map[string]any) {
				key := "annotations"
				if object == "command" {
					key = "Labels"
				}
				fields[key] = map[string]any{"Name": "upper", "name": "lower"}
			})
			if _, err := InspectContainerImage(t.Context(), root, "linux/amd64"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Rewrite only fixture-owned blobs, removing obsolete ones so inventory checks
// cannot disguise a schema-admission regression.
func mutateImageJSON(t *testing.T, root, object string, mutate func(map[string]any)) {
	t.Helper()
	index, manifest := fixtureManifest(t, root)
	writer := layoutWriter{root: root}
	oldManifest := index.Manifests[0].Digest
	if object == "index" {
		raw, err := os.ReadFile(filepath.Join(root, "index.json"))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		mutate(fields)
		raw, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "index.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	descriptor := index.Manifests[0]
	if object == "config" || object == "command" || object == "rootfs" {
		descriptor = manifest.Config
	}
	raw, err := readBlob(t.Context(), root, descriptor, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	selected := fields
	switch object {
	case "descriptor":
		selected = fields["config"].(map[string]any)
	case "command":
		selected = fields["config"].(map[string]any)
	case "rootfs":
		selected = fields["rootfs"].(map[string]any)
	}
	mutate(selected)
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := writer.addBlobBytes(raw, descriptor.MediaType)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Digest == manifest.Config.Digest {
		manifest.Config = updated
		updated, err = writer.addJSONBlob(manifest, manifest.MediaType)
		if err != nil {
			t.Fatal(err)
		}
	}
	index.Manifests[0] = updated
	raw, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	obsolete := []string{oldManifest}
	if descriptor.Digest != oldManifest {
		obsolete = append(obsolete, descriptor.Digest)
	}
	for _, digest := range obsolete {
		if err := os.Remove(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))); err != nil {
			t.Fatal(err)
		}
	}
}

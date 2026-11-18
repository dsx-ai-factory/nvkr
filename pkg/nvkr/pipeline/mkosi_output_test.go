// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/material"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
)

func TestMkosiOutputBindsOriginalEvidenceAndMeasurements(t *testing.T) {
	for _, scenario := range []string{"exact", "changed report", "missing report", "changed firmware", "changed recipe"} {
		t.Run(scenario, func(t *testing.T) {
			node, source, inputs, work := mkosiStageFixture(t)
			node.Mkosi.Measurements = []mkosi.FileMeasurement{{Path: "/usr/share/firmware/code.fd", Digest: FileDigest([]byte("pinned firmware"))}}
			operations, _, err := prepareMkosiWork(t.Context(), node, source, inputs, work, nil)
			if err != nil {
				t.Fatal(err)
			}
			report, err := json.Marshal(material.MkosiManifest{ManifestVersion: 1,
				Config:   material.MkosiConfig{Name: "runtime", Distribution: "ubuntu", Release: "noble", Architecture: "x86-64", OutputFormat: "directory"},
				Packages: []material.InstalledPackage{{Type: "deb", Name: "base-files", Version: "1.0", Architecture: "amd64"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mkosiWorkPath(work, operations[0].Manifest), report, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := retainMkosiReport(t.Context(), work, inputs, operations[0], node.Mkosi.Roots[0]); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"usr/share/firmware/code.fd": "pinned firmware"}
			for _, name := range []string{"recipe.json", "mkosi.conf", "runtime.manifest.json"} {
				raw, err := os.ReadFile(filepath.Join(inputs, "evidence/nvkr/mkosi", name))
				if err != nil {
					t.Fatal(err)
				}
				files["nvkr/mkosi/"+name] = string(raw)
			}
			switch scenario {
			case "changed report":
				files["nvkr/mkosi/runtime.manifest.json"] += "\n"
			case "missing report":
				delete(files, "nvkr/mkosi/runtime.manifest.json")
			case "changed firmware":
				files["usr/share/firmware/code.fd"] = "substitute"
			case "changed recipe":
				files["nvkr/mkosi/recipe.json"] = "{}"
			}
			_, result, _, _ := runtimeReceiptFixtureFiles(t, files)
			subject, size, err := inspectMkosiOutput(t.Context(), node, result.TransportPath, inputs, t.TempDir())
			if scenario == "exact" {
				if err != nil || subject != result.SHA256 || size != result.Size {
					t.Fatalf("exact result rejected: %s %d %v", subject, size, err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("packaged substitution accepted: %v", err)
			}
		})
	}
}

func TestObserveMkosiResultChecksPlannedOCIClosure(t *testing.T) {
	for _, scenario := range []string{"exact", "wrong size", "wrong engine", "corrupt layer", "redirected transport"} {
		t.Run(scenario, func(t *testing.T) {
			node, result, _, layer := runtimeReceiptFixture(t)
			payload := result.TransportPath
			switch scenario {
			case "wrong size":
				result.Size++
			case "wrong engine":
				result.EngineClass = "host-command"
			case "corrupt layer":
				if err := os.WriteFile(filepath.Join(payload, "blobs/sha256", strings.TrimPrefix(layer, "sha256:")), []byte("corrupt"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "redirected transport":
				if err := os.Rename(payload, payload+".moved"); err != nil {
					t.Fatal(err)
				}
				result.TransportPath = payload + ".moved"
			}
			path := filepath.Join(t.TempDir(), "result.json")
			writeObservedResult(t, path, result)
			observed, err := AuthenticateResultForConsumption(t.Context(), node, payload, path)
			if scenario == "exact" {
				if err != nil || observed.TransportPath != payload || observed.AbsPath != result.AbsPath || observed.Size != result.Size {
					t.Fatalf("exact OCI observation: %+v %v", observed, err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
				t.Fatalf("unsafe OCI observation: %v", err)
			}
		})
	}
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package factory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// The executable is a held, digest-pinned protocol fixture, not Syft. These
// always-on Linux contracts drive the real scanner handoff and post-scan source
// checks; they do not establish discovery of a real acquired image's packages.
func TestAcquiredImageScanRechecksOriginalAfterHeldScanner(t *testing.T) {
	const inventory = `{"bomFormat":"CycloneDX","specVersion":"1.5","components":[{"type":"library","name":"controlled-package","version":"1","purl":"pkg:generic/controlled-package@1"}]}`
	for _, condition := range []string{"valid", "result changes", "payload changes", "scanner fails"} {
		t.Run(condition, func(t *testing.T) {
			plan, _, _, artifacts, subject := acquiredImageSBOMFixture(t, "amd64", types.OCIManifestSchema1)
			body := "test -z \"${NVKR_SOURCE_TOKEN:-}\" || exit 19\ncase \"$2\" in oci-archive:*) ;; *) exit 20 ;; esac\n"
			switch condition {
			case "result changes":
				record := artifacts["result/image/image"]
				result, err := readProducerResult(t.Context(), record.Path)
				if err != nil {
					t.Fatal(err)
				}
				result.Qualification = pipeline.ArtifactUnqualified
				raw, err := canonical.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				path, err := filepath.Abs(record.Path)
				if err != nil {
					t.Fatal(err)
				}
				body += "printf '%s' " + shellQuote(string(raw)) + " > " + shellQuote(path) + "\n"
			case "payload changes":
				path, err := filepath.Abs(filepath.Join(artifacts["artifact/image/image"].Path, "index.json"))
				if err != nil {
					t.Fatal(err)
				}
				body += "printf changed > " + shellQuote(path) + "\n"
			case "scanner fails":
				body += "exit 23\n"
			}
			script := []byte("#!/bin/sh\n" + body + "printf '%s' " + shellQuote(inventory) + "\n")
			pin := pipeline.FileDigest(script)
			base := plan.CatalogSnapshot
			tool := base.Tools["syft"]
			tool.ArchiveDigest, tool.ExecutableDigest = pin, pin
			base.Tools["syft"] = tool
			stack, err := catalog.ApplyStack(base)
			if err != nil {
				t.Fatal(err)
			}
			plan, err = Compile(t.Context(), plan.Definition, plan.Environment, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			nodes, artifacts := planIndex(plan)
			scanner := sbom.Scanner{Path: artifacts["tool/syft"].Path, Digest: pin}
			if err := writeFactoryFile(t.Context(), scanner.Path, script, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("NVKR_SOURCE_TOKEN", "synthetic-value-must-not-reach-scanner")
			got, err := inspectAcquiredImageDependencies(t.Context(), plan, nodes["image/sbom"],
				artifacts["artifact/image/image"], artifacts, subject, scanner)
			if condition == "valid" {
				if err != nil || len(got.Components) != 1 || got.Components[0].Name != "controlled-package" ||
					got.Method != "syft-cyclonedx/v1" || got.ScannerDigest != pin {
					t.Fatalf("held scan did not return its exact inventory: %+v, %v", got, err)
				}
			} else {
				want := e.CodeVerifyFailed
				if condition == "scanner fails" {
					want = e.CodeProvenanceMissing
				}
				assertFactoryErrorCode(t, err, want)
				if len(got.Components) != 0 {
					t.Fatal("failed scan accepted partial inventory")
				}
			}
			if _, err := os.Stat(scanner.Path); err != nil {
				t.Fatalf("scan cleanup removed caller-owned tool: %v", err)
			}
		})
	}
}

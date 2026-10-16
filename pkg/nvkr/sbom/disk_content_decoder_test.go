// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// These are private decoder projections, not minted pipeline observations or
// proof that the sandbox/helper/receipt issuer executed successfully.
func TestBoundDiskContentDecoder(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, name := range []string{"valid", "subject", "size", "kind", "platform", "inspector", "codec", "guestfish", "scanner", "package-reader", "filesystem-bound", "image", "unknown-field", "duplicate-field", "trailing-document", "malformed", "empty", "unsorted-files", "duplicate-files", "unsafe-path", "missing-file-digest", "unknown-file-kind", "missing-class", "unsorted-properties"} {
			t.Run(architecture+"/"+name, func(t *testing.T) {
				fixture := contentFixture("disk-image", architecture, "b")
				report, binding, image := fixture.report, fixture.report, fixture.runtimeDigest
				want := e.CodeVerifyFailed
				switch name {
				case "valid":
					want = ""
				case "subject":
					report.Subject = "sha256:" + strings.Repeat("c", 64)
				case "size":
					report.Size++
				case "kind":
					report.Kind = "tarball"
				case "platform":
					report.Platform = "linux/other"
				case "inspector":
					report.Inspector = ""
				case "codec":
					report.Codec = ""
				case "guestfish":
					report.Guestfish = ""
				case "scanner":
					report.Scanner = ""
				case "package-reader":
					report.PackageReader = ""
				case "filesystem-bound":
					report.MaxFilesystemBytes++
				case "image":
					image = "not-a-digest"
				case "empty":
					report.Components, report.Files = nil, nil
					want = e.CodeProvenanceMissing
				case "unsorted-files":
					report.Files = append(report.Files, DiskFile{Path: "a", Kind: "regular", Digest: image, Size: 1})
					want = e.CodeEnvelopeDecode
				case "duplicate-files":
					report.Files = append(report.Files, report.Files[0])
					want = e.CodeEnvelopeDecode
				case "unsafe-path":
					report.Files[0].Path = "../escape"
					want = e.CodeEnvelopeDecode
				case "missing-file-digest":
					report.Files[0].Digest = ""
					want = e.CodeEnvelopeDecode
				case "unknown-file-kind":
					report.Files[0].Kind = "socket"
					want = e.CodeEngineCapability
				case "unsorted-properties":
					slices.Reverse(report.Components[0].Properties)
					want = e.CodeInvalidCatalog
				case "missing-class":
					report.Components[0].Properties = report.Components[0].Properties[1:]
					want = e.CodeProvenanceMissing
				}
				raw, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				switch name {
				case "unknown-field":
					raw = append(append([]byte{}, raw[:len(raw)-1]...), []byte(",\"unknown\":true}")...)
					want = e.CodeEnvelopeDecode
				case "duplicate-field":
					raw = append(append([]byte{}, raw[:len(raw)-1]...), []byte(",\"size\":4096}")...)
					want = e.CodeEnvelopeDecode
				case "trailing-document":
					raw = append(raw, []byte(" {}")...)
					want = e.CodeEnvelopeDecode
				case "malformed":
					raw = []byte("{")
					want = e.CodeEnvelopeDecode
				}
				inspection, err := decodeBoundDiskContentReport(raw, image, binding)
				if want != "" {
					assertDependencyCode(t, err, want)
					if inspection != nil {
						t.Fatal("rejected report returned content authority")
					}
					return
				}
				if err != nil || inspection == nil {
					t.Fatalf("valid decoder failed: %v", err)
				}
				components, files := inspection.Components(), inspection.Files()
				components[0].Properties[0].Value = "changed"
				files[0].Path = "changed"
				again, err := json.Marshal(inspection.report)
				if err != nil || !bytes.Equal(raw, again) {
					t.Fatal("detached descriptions mutated decoded observation")
				}
			})
		}
	}
}

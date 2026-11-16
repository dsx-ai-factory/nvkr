// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestDiskReconciliationRequiresSameProducedHelper(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, variant := range []string{"exact", "name", "bytes", "Build", "terminal receipt"} {
			t.Run(arch+"/"+variant, func(t *testing.T) {
				root := contentFixture("disk-image", arch, "b")
				inputs := []*DiskContentInspection{contentFixture("installer-iso", arch, "c"), contentFixture("apt-acquisition", arch, "d"), contentFixture("tarball", arch, "e")}
				pin := "sha256:" + strings.Repeat("a", 64)
				root.helperName = "inspector"
				root.helper = pipeline.ProducedExecutableBinding{BuildDigest: pin, Platform: "linux/" + arch, Component: "helper", Digest: pin, Size: 1024, TerminalDigest: pin}
				for _, input := range inputs {
					input.helperName, input.helper = root.helperName, root.helper
				}
				switch variant {
				case "name":
					inputs[0].helperName = "other"
				case "bytes":
					inputs[0].helper.Digest = "sha256:" + strings.Repeat("b", 64)
				case "Build":
					inputs[0].helper.BuildDigest = "sha256:" + strings.Repeat("b", 64)
				case "terminal receipt":
					inputs[0].helper.TerminalDigest = "sha256:" + strings.Repeat("b", 64)
				}
				bom, err := ReconcileDiskContents(t.Context(), pin, "node", root, inputs)
				if variant != "exact" {
					assertDependencyCode(t, err, e.CodeVerifyFailed)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				properties := map[string]string{}
				for _, property := range bom.Metadata.Props {
					properties[property.Name] = property.Value
				}
				if properties["nvkr:diskInspectionHelperBuildDigest"] != pin || properties["nvkr:diskInspectionHelperDigest"] != pin || properties["nvkr:diskInspectionHelperComponent"] != "helper" {
					t.Fatal("SBOM lost its produced helper identity")
				}
				if _, exists := properties["nvkr:diskInspectionHelperTerminalDigest"]; exists {
					t.Fatal("attempt-scoped receipt leaked into reproducible SBOM identity")
				}
			})
		}
	}
}

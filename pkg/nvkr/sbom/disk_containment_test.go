// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestReconcileContainedPackagesRetainsCarriersBeforePublicDeduplication(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, conflict := range []bool{false, true} {
			name := architecture + "/matching"
			if conflict {
				name = architecture + "/conflicting other property"
			}
			t.Run(name, func(t *testing.T) {
				root := contentFixture("disk-image", architecture, "b")
				inputs := []*DiskContentInspection{contentFixture("installer-iso", architecture, "c"), contentFixture("apt-acquisition", architecture, "d"), contentFixture("tarball", architecture, "e")}
				installed := cloneComponent(root.report.Components[0])
				installed.Properties = append(installed.Properties, Property{Name: "cataloger", Value: "one"})
				root.report.Components = []Component{installed}
				pin := "sha256:" + strings.Repeat("a", 64)
				carriers := []string{"var/cache/second.deb@" + pin, "var/cache/first.deb@" + pin, "var/cache/second.deb@" + pin}
				for _, carrier := range carriers {
					contained := cloneComponent(installed)
					contained.Properties[0].Value = diskClassDebianArchive
					contained.Properties = append(contained.Properties, Property{Name: "nvkr:containedIn", Value: carrier})
					root.report.Components = append(root.report.Components, contained)
				}
				if conflict {
					root.report.Components[2].Properties[3].Value = "two"
				}
				for index := range root.report.Components {
					slices.SortFunc(root.report.Components[index].Properties, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
				}
				bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "disk", root, inputs)
				if conflict {
					assertDependencyCode(t, err, e.CodeVerifyFailed)
					if bom != nil {
						t.Fatal("conflicting non-containment metadata returned a document")
					}
					return
				}
				if err != nil || len(bom.Components) != 2 {
					t.Fatalf("installed/contained overlap failed public deduplication: %v", err)
				}
				wanted, err := canonical.Marshal([]string{carriers[1], carriers[0]})
				if err != nil {
					t.Fatal(err)
				}
				found, rootObserved := false, false
				for _, component := range bom.Components {
					for _, property := range component.Properties {
						if property.Name == "nvkr:containedIn" {
							found = property.Value == string(wanted)
						}
						if property.Name == "nvkr:observedAtSubjectRoot" {
							rootObserved = property.Value == "true"
						}
					}
				}
				if !found || !rootObserved {
					t.Fatal("final document lost a carrier or the separate subject-root observation")
				}
			})
		}
	}
}

func TestRootAndContainedImagePackageWithSamePURLRemainDescriptivelyDistinct(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			root := contentFixture("disk-image", arch, "b")
			inputs := []*DiskContentInspection{contentFixture("installer-iso", arch, "c"), contentFixture("apt-acquisition", arch, "d"), contentFixture("oci-image", arch, "e")}
			contained := cloneComponent(root.report.Components[0])
			contained.Properties[0].Value = diskClassContainer
			contained.Properties = append(contained.Properties, Property{Name: "nvkr:containedIn", Value: "images/save.tar@" + inputs[2].report.Subject})
			slices.SortFunc(contained.Properties, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
			root.report.Components = append(root.report.Components, contained)
			inputs[2].report.Components[0].Properties[0].Value = diskClassContainer
			projected, err := diskDocumentComponents(root.report.Components)
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(projected[0].Properties, func(p Property) bool { return p.Name == "nvkr:containedIn" }) ||
				!slices.Contains(projected[0].Properties, Property{Name: "nvkr:observedAtSubjectRoot", Value: "true"}) ||
				slices.ContainsFunc(projected[1].Properties, func(p Property) bool { return p.Name == "nvkr:observedAtSubjectRoot" }) {
				t.Fatal("containment was copied to a root observation or root observation to a contained package")
			}
			bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "disk", root, inputs)
			if err != nil {
				t.Fatal(err)
			}
			for _, component := range bom.Components {
				if component.Type != "file" && (!slices.Contains(component.Properties, Property{Name: "nvkr:observedAtSubjectRoot", Value: "true"}) ||
					!slices.ContainsFunc(component.Properties, func(p Property) bool { return p.Name == "nvkr:containedIn" })) {
					t.Fatal("public deduplication erased the host/image overlap")
				}
			}
		})
	}
}

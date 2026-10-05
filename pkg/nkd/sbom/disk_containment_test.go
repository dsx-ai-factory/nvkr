package sbom

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestContainedArchiveKeepsActualRootPathsAndImmediateCarrier(t *testing.T) {
	for _, kind := range []string{"debian-archive", "container", "filesystem"} {
		t.Run(kind, func(t *testing.T) {
			pin := "sha256:" + strings.Repeat("a", 64)
			actual := []DiskFile{{Path: "packages/cache.deb/usr/bin/tool", Kind: "regular", Digest: pin, Size: 7},
				{Path: "var/cache/cache.deb", Kind: "regular", Digest: pin, Size: 42}}
			root := &FilesystemInspection{files: append([]DiskFile(nil), actual...)}
			nested := &FilesystemInspection{components: withDiskContentClass([]Component{{Type: "library", Name: "contained-package", Version: "1"}}, kind),
				files: []DiskFile{{Path: "usr/bin/tool", Kind: "regular", Digest: pin, Size: 7}}}
			appendContainedArchive(root, nested, "var/cache/cache.deb", pin)
			appendContainedArchive(root, nested, "var/cache/second.deb", pin)
			if !reflect.DeepEqual(root.Files(), actual) || len(root.Components()) != 2 {
				t.Fatal("contained discovery changed actual root file paths or lost repeated carriers")
			}
			for index, component := range root.Components() {
				carrier := "var/cache/cache.deb"
				if index == 1 {
					carrier = "var/cache/second.deb"
				}
				if len(component.Properties) != 2 || component.Properties[0] != (Property{Name: "nkd:containedIn", Value: carrier + "@" + pin}) {
					t.Fatalf("immediate exact carrier absent: %+v", component)
				}
				if class, err := diskComponentClass(component); err != nil || class != kind {
					t.Fatalf("containment changed observation class: %q %v", class, err)
				}
			}
			if len(nested.components[0].Properties) != 1 || nested.files[0].Path != "usr/bin/tool" {
				t.Fatal("root projection mutated the nested observation")
			}
			inspection := contentFixture("tarball", "amd64", "b")
			inspection.report.Components, inspection.report.Files = root.Components(), root.Files()
			document, err := DiskContentDocument("sha256:"+strings.Repeat("f", 64), "material", inspection)
			if err != nil {
				t.Fatalf("repeated carriers failed public projection: %v", err)
			}
			wanted, err := canonical.Marshal([]string{"var/cache/cache.deb@" + pin, "var/cache/second.deb@" + pin})
			if err != nil {
				t.Fatal(err)
			}
			for _, component := range document.Components {
				if component.Type == "file" {
					continue
				}
				if len(component.Properties) != 1 || component.Properties[0] != (Property{Name: "nkd:containedIn", Value: string(wanted)}) {
					t.Fatalf("public inventory lost immediate carriers: %+v", component)
				}
			}
			// An already observed inner carrier is not overwritten with an
			// invented chain through the newly inspected outer archive.
			deeper := &FilesystemInspection{components: root.Components()[:1]}
			outer := &FilesystemInspection{}
			appendContainedArchive(outer, deeper, "outer.tar.gz", "sha256:"+strings.Repeat("b", 64))
			if !reflect.DeepEqual(outer.Components(), deeper.Components()) {
				t.Fatal("nested carrier identity was overwritten")
			}
		})
	}
}

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
				carriers := []string{"var/cache/first.deb@" + pin, "var/cache/second.deb@" + pin}
				for _, carrier := range carriers {
					contained := cloneComponent(installed)
					contained.Properties[0].Value = diskClassDebianArchive
					contained.Properties = append(contained.Properties, Property{Name: "nkd:containedIn", Value: carrier})
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
				wanted, err := canonical.Marshal(carriers)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, component := range bom.Components {
					for _, property := range component.Properties {
						if property.Name == "nkd:containedIn" {
							found = property.Value == string(wanted)
						}
					}
				}
				if !found {
					t.Fatal("final document lost one of the exact immediate carriers")
				}
			})
		}
	}
}

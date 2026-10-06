package sbom

import (
	"bytes"
	"os"
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

func TestInstallerMemberMergeRetainsImmediateSquashFSCarrier(t *testing.T) {
	raw := helmTestArchive(t, map[string]string{"casper/base.squashfs": "controlled base", "casper/additional.squashfs": "controlled additional"})
	archive, err := os.CreateTemp(t.TempDir(), "installer")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if _, err := archive.Write(raw); err != nil {
		t.Fatal(err)
	}
	count := 0
	inspection, err := inspectInstallerMembersWith(t.Context(), archive, func(member inspectionArchive) (*FilesystemInspection, error) {
		count++
		var payload bytes.Buffer
		if _, err := member.WriteToContext(t.Context(), &payload); err != nil {
			return nil, err
		}
		return &FilesystemInspection{components: withDiskContentClass([]Component{{Type: "library", Name: payload.String(), Version: "1"}}, diskClassFilesystem),
			files: []DiskFile{{Path: "usr/bin/tool", Kind: "regular", Digest: member.Digest(), Size: member.Size()}}}, nil
	})
	if err != nil || count != 2 || len(inspection.components) != 2 || len(inspection.files) != 0 {
		t.Fatalf("installer merge lost components or invented installed paths: %v", err)
	}
	want := map[string]string{"controlled base": "casper/base.squashfs@" + rawDigest([]byte("controlled base")), "controlled additional": "casper/additional.squashfs@" + rawDigest([]byte("controlled additional"))}
	for _, component := range inspection.components {
		if !slices.Contains(component.Properties, Property{Name: "nkd:containedIn", Value: want[component.Name]}) {
			t.Fatalf("installer member lost exact carrier: %+v", component)
		}
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
				carriers := []string{"var/cache/second.deb@" + pin, "var/cache/first.deb@" + pin, "var/cache/second.deb@" + pin}
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
				wanted, err := canonical.Marshal([]string{carriers[1], carriers[0]})
				if err != nil {
					t.Fatal(err)
				}
				found, rootObserved := false, false
				for _, component := range bom.Components {
					for _, property := range component.Properties {
						if property.Name == "nkd:containedIn" {
							found = property.Value == string(wanted)
						}
						if property.Name == "nkd:observedAtSubjectRoot" {
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
			contained.Properties = append(contained.Properties, Property{Name: "nkd:containedIn", Value: "images/save.tar@" + inputs[2].report.Subject})
			slices.SortFunc(contained.Properties, func(a, b Property) int { return strings.Compare(a.Name, b.Name) })
			root.report.Components = append(root.report.Components, contained)
			inputs[2].report.Components[0].Properties[0].Value = diskClassContainer
			projected, err := diskDocumentComponents(root.report.Components)
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(projected[0].Properties, func(p Property) bool { return p.Name == "nkd:containedIn" }) ||
				!slices.Contains(projected[0].Properties, Property{Name: "nkd:observedAtSubjectRoot", Value: "true"}) ||
				slices.ContainsFunc(projected[1].Properties, func(p Property) bool { return p.Name == "nkd:observedAtSubjectRoot" }) {
				t.Fatal("containment was copied to a root observation or root observation to a contained package")
			}
			bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "disk", root, inputs)
			if err != nil {
				t.Fatal(err)
			}
			for _, component := range bom.Components {
				if component.Type != "file" && (!slices.Contains(component.Properties, Property{Name: "nkd:observedAtSubjectRoot", Value: "true"}) ||
					!slices.ContainsFunc(component.Properties, func(p Property) bool { return p.Name == "nkd:containedIn" })) {
					t.Fatal("public deduplication erased the host/image overlap")
				}
			}
		})
	}
}

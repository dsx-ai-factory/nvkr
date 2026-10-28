// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func contentFixture(kind, architecture, payload string) *DiskContentInspection {
	pin := "sha256:" + strings.Repeat("a", 64)
	class := diskClassFilesystem
	switch kind {
	case "oci-image":
		class = diskClassContainer
	case "tarball", "apt-acquisition", "host-package":
		class = diskClassDebianArchive
	}
	return &DiskContentInspection{runtimeDigest: pin, report: diskContentReport{Subject: "sha256:" + strings.Repeat(payload, 64), Size: 4096,
		Kind: kind, Platform: "linux/" + architecture, Inspector: pin, Codec: pin, Guestfish: pin, Scanner: pin, PackageReader: pin, MaxFilesystemBytes: 8192,
		Components: []Component{{Type: "library", Name: "base-package", Version: "2:1.0-1ubuntu4", PURL: "pkg:deb/ubuntu/base-package@2%3A1.0-1ubuntu4?arch=" + architecture,
			Properties: []Property{{Name: diskContentClassProperty, Value: class}, {Name: "nvkr:packageArchitecture", Value: architecture}, {Name: "nvkr:packageFormat", Value: "deb"}}}},
		Files: []DiskFile{{Path: "usr/bin/tool", Kind: "regular", Digest: pin, Size: 12}}}}
}

// Literal keys pin the helper report vocabulary independently of the writer.
func TestNVKRDiskReportPropertyABI(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			carrier := "var/cache/input.deb@sha256:" + strings.Repeat("a", 64)
			component := Component{Type: "library", Name: "observed", Version: "1", Properties: []Property{
				{Name: "nvkr:containedIn", Value: carrier},
				{Name: "nvkr:diskContentClass", Value: "debian-archive"},
				{Name: "nvkr:packageArchitecture", Value: architecture},
				{Name: "nvkr:packageFormat", Value: "deb"},
			}}
			key, err := diskObservedComponentKey(component, "linux/"+architecture)
			if err != nil || key != (diskObservedPackageKey{diskPackageKey{"observed", "1", "deb", architecture}, "debian-archive"}) {
				t.Fatalf("pinned inspector report rejected: %+v %v", key, err)
			}
			projected, err := diskDocumentComponents([]Component{component})
			if err != nil {
				t.Fatal(err)
			}
			want, err := canonical.Marshal([]string{carrier})
			if err != nil {
				t.Fatal(err)
			}
			contained := false
			for _, property := range projected[0].Properties {
				if property.Name == "nvkr:containedIn" {
					contained = property.Value == string(want)
				}
				if property.Name == "nvkr:observedAtSubjectRoot" {
					t.Fatal("pinned inspector's nested component became a subject-root observation")
				}
			}
			if !contained {
				t.Fatal("pinned inspector's immediate carrier was lost")
			}
			for _, test := range []struct {
				property string
				code     e.Code
			}{{"diskContentClass", e.CodeProvenanceMissing}, {"packageFormat", e.CodeProvenanceMissing}, {"packageArchitecture", e.CodeVerifyFailed}} {
				t.Run(test.property, func(t *testing.T) {
					changed := cloneComponent(component)
					for index := range changed.Properties {
						if changed.Properties[index].Name == "nvkr:"+test.property {
							changed.Properties[index].Name = "unsupported:" + test.property
						}
					}
					_, err := diskObservedComponentKey(changed, "linux/"+architecture)
					assertDependencyCode(t, err, test.code)
				})
			}
		})
	}
}

func TestReconcileDiskContentsExactSubjectsBothArchitectures(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			root := contentFixture("disk-image", architecture, "b")
			inputs := []*DiskContentInspection{contentFixture("installer-iso", architecture, "c"), contentFixture("apt-acquisition", architecture, "d"), contentFixture("tarball", architecture, "e")}
			bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
			if err != nil {
				t.Fatal(err)
			}
			if len(bom.Components) != 2 || len(bom.Dependencies) != 1 || bom.Metadata.Component.Hashes[0].Content != strings.Repeat("b", 64) {
				t.Fatalf("exact package/file coverage absent: %+v", bom)
			}
			wantSubjects := "sha256:" + strings.Repeat("c", 64) + ",sha256:" + strings.Repeat("d", 64) + ",sha256:" + strings.Repeat("e", 64)
			for _, component := range bom.Components {
				if component.Type == "file" && (len(component.Properties) != 1 || component.Properties[0] != (Property{Name: "nvkr:materialSubjects", Value: wantSubjects})) {
					t.Fatalf("exact material correspondence lost: %+v", component.Properties)
				}
				for _, property := range component.Properties {
					if property.Name == diskContentClassProperty {
						t.Fatal("private observation class leaked as package provenance")
					}
				}
			}
			raw, err := bom.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(raw, []byte("disk-reconciled/v1")) || !bytes.Contains(raw, []byte("2:1.0-1ubuntu4")) {
				t.Fatalf("exact version/method absent: %s", raw)
			}
			again, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
			if err != nil {
				t.Fatal(err)
			}
			second, _ := again.Marshal()
			if !bytes.Equal(raw, second) {
				t.Fatal("same observations produced non-deterministic bytes")
			}
		})
	}
}

func TestReconcileDiskContentsDoesNotEquateUnknownVersions(t *testing.T) {
	for _, test := range []struct {
		name, format, rootVersion, materialVersion string
		code                                       e.Code
	}{
		{"same unknown version is not correspondence", "binary", "", "", e.CodeProvenanceMissing},
		{"unknown is not known", "binary", "", "1.0", e.CodeProvenanceMissing},
		{"known is not unknown", "binary", "1.0", "", e.CodeProvenanceMissing},
		{"Debian still requires exact version", "deb", "", "", e.CodeProvenanceMissing},
		{"versioned root cannot use unversioned Debian material", "deb", "1.0", "", e.CodeProvenanceMissing},
		{"same known version", "binary", "1.0", "1.0", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := contentFixture("disk-image", "arm64", "b")
			inputs := []*DiskContentInspection{contentFixture("installer-iso", "arm64", "c"), contentFixture("apt-acquisition", "arm64", "d"), contentFixture("tarball", "arm64", "e")}
			component := Component{Type: "library", Name: "observed", Version: test.materialVersion,
				Properties: []Property{{Name: diskContentClassProperty, Value: diskClassFilesystem}, {Name: "nvkr:packageArchitecture", Value: "arm64"}, {Name: "nvkr:packageFormat", Value: test.format}}}
			for _, input := range inputs {
				input.report.Components = []Component{cloneComponent(component)}
			}
			component.Version = test.rootVersion
			root.report.Components = []Component{component}
			bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
			if test.code != "" {
				assertDependencyCode(t, err, test.code)
				if test.format == "deb" && test.rootVersion != "" && !strings.Contains(err.Error(), "disk package lacks an exact semantic identity") {
					t.Fatalf("material semantic identity guard was not reached: %v", err)
				}
				if bom != nil {
					t.Fatal("failed correspondence returned a document")
				}
				return
			}
			if err != nil || bom == nil || len(bom.Components) != 2 {
				t.Fatalf("unknown-version component disappeared: %v", err)
			}
			for _, observed := range bom.Components {
				if observed.Name == "observed" && observed.Version != test.rootVersion {
					t.Fatal("observed version changed")
				}
			}
		})
	}
}

func TestDiskContentDocumentPreservesUnknownVersionsWithoutCorrespondence(t *testing.T) {
	inspection := contentFixture("tarball", "arm64", "b")
	inspection.report.Components[0].Version = ""
	inspection.report.Components[0].Properties[2].Value = "binary"
	document, err := DiskContentDocument("sha256:"+strings.Repeat("f", 64), "material", inspection)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range document.Components {
		if component.Name == inspection.report.Components[0].Name {
			if component.Version != "" {
				t.Fatal("descriptive inventory invented an unknown version")
			}
			return
		}
	}
	t.Fatal("unknown-version component disappeared from descriptive inventory")
}

func TestNestedPayloadFileDigestsDoNotInventRootMaterialCorrespondence(t *testing.T) {
	root := contentFixture("disk-image", "amd64", "b")
	inputs := []*DiskContentInspection{contentFixture("installer-iso", "amd64", "c"), contentFixture("apt-acquisition", "amd64", "d"), contentFixture("tarball", "amd64", "e")}
	root.report.Files[0].Digest = "sha256:" + strings.Repeat("9", 64)
	for _, input := range inputs {
		input.report.Files[0].Digest = "sha256:" + strings.Repeat("8", 64) // Actual outer carrier, not its payload file.
	}
	bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range bom.Components {
		if component.Type == "file" {
			if component.Name != root.report.Files[0].Path || len(component.Hashes) != 1 || component.Hashes[0].Content != strings.Repeat("9", 64) || len(component.Properties) != 0 {
				t.Fatalf("nested payload scope was invented: %+v", component)
			}
			return
		}
	}
	t.Fatal("actual observed root file disappeared")
}

func TestReconcileHostPackagesCannotUseContainerPackageDatabases(t *testing.T) {
	for _, test := range []struct {
		name, stagedKind, stagedClass, rootClass string
		code                                     e.Code
	}{
		{"direct OCI database cannot justify host", "oci-image", "container", "filesystem", e.CodeProvenanceMissing},
		{"embedded OCI database cannot justify host", "tarball", "container", "filesystem", e.CodeProvenanceMissing},
		{"aggregate staged scan is not held package metadata", "tarball", "filesystem", "filesystem", e.CodeProvenanceMissing},
		{"held Debian archive can justify host", "tarball", "debian-archive", "filesystem", ""},
		{"container package retains container correspondence", "oci-image", "container", "container", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := contentFixture("disk-image", "arm64", "b")
			inputs := []*DiskContentInspection{contentFixture("installer-iso", "arm64", "c"), contentFixture("apt-acquisition", "arm64", "d"), contentFixture(test.stagedKind, "arm64", "e")}
			root.report.Components[0].Properties[0].Value = test.rootClass
			inputs[0].report.Components = nil
			inputs[1].report.Components = nil
			inputs[2].report.Components[0].Properties[0].Value = test.stagedClass
			bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
			if test.code != "" {
				assertDependencyCode(t, err, test.code)
				if bom != nil {
					t.Fatal("container database authorized host correspondence")
				}
				return
			}
			if err != nil || bom == nil {
				t.Fatalf("matching observed class rejected: %v", err)
			}
		})
	}
}

func TestDiskPackageContentClassMatrixBothArchitectures(t *testing.T) {
	classes := []string{diskClassFilesystem, diskClassContainer, diskClassDebianArchive}
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, kind := range []string{"installer-iso", "installer-content", "apt-acquisition", "tarball", "host-package", "oci-image"} {
			for _, rootClass := range classes {
				for _, materialClass := range classes {
					t.Run(strings.Join([]string{architecture, kind, rootClass, materialClass}, "/"), func(t *testing.T) {
						root := contentFixture("disk-image", architecture, "b")
						root.report.Components[0].Properties[0].Value = rootClass
						inputs := []*DiskContentInspection{contentFixture("installer-iso", architecture, "c"), contentFixture("apt-acquisition", architecture, "d"), contentFixture("tarball", architecture, "e")}
						for _, input := range inputs {
							input.report.Components = nil
						}
						source := inputs[2]
						switch kind {
						case "installer-iso":
							source = inputs[0]
						case "apt-acquisition":
							source = inputs[1]
						default:
							source.report.Kind = kind
						}
						component := cloneComponent(root.report.Components[0])
						component.Properties[0].Value = materialClass
						source.report.Components = []Component{component}
						want := e.CodeProvenanceMissing
						if kind == "oci-image" && materialClass != diskClassContainer {
							want = e.CodeVerifyFailed
						} else if (rootClass == diskClassContainer && materialClass == diskClassContainer) ||
							(rootClass == diskClassDebianArchive && materialClass == diskClassDebianArchive) ||
							(rootClass == diskClassFilesystem && (materialClass == diskClassDebianArchive || (kind == "installer-iso" && materialClass == diskClassFilesystem))) {
							want = ""
						}
						bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
						if sbomCode(err) != want || (bom != nil) != (want == "") {
							t.Fatalf("class correspondence result=%v document=%v, want %s", err, bom != nil, want)
						}
					})
				}
			}
		}
	}
}

func TestDiskClassesAreOwnerIssuedNotScannerProperties(t *testing.T) {
	raw := []byte(`{"schema":{"version":"16.1.10"},"artifacts":[{"name":"sample","version":"1","type":"deb","metadata":{"architecture":"arm64"},"properties":[{"name":"nvkr:diskContentClass","value":"debian-archive"}]}]}`)
	inventory, err := filesystemDependenciesFromJSON(raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := diskComponentClass(inventory.Components[0]); sbomCode(err) != e.CodeProvenanceMissing {
		t.Fatalf("scanner authored the owner's class: %v", err)
	}
	for _, class := range []string{diskClassFilesystem, diskClassContainer} {
		components := withDiskContentClass(inventory.Components, class)
		if observed, err := diskComponentClass(components[0]); err != nil || observed != class {
			t.Fatalf("scanner namespace replaced owner %s with %s: %v", class, observed, err)
		}
	}
}

func TestDiskContentClassesRejectMissingAmbiguousAndSubstitutedValues(t *testing.T) {
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{"missing", e.CodeProvenanceMissing}, {"duplicate canonical property", e.CodeInvalidCatalog},
		{"unknown", e.CodeEnvelopeDecode}, {"archive for non-Debian", e.CodeEnvelopeDecode},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := contentFixture("disk-image", "arm64", "b")
			inputs := []*DiskContentInspection{contentFixture("installer-iso", "arm64", "c"), contentFixture("apt-acquisition", "arm64", "d"), contentFixture("tarball", "arm64", "e")}
			component := &root.report.Components[0]
			switch test.name {
			case "missing":
				component.Properties = component.Properties[1:]
			case "duplicate canonical property":
				component.Properties = append([]Property{component.Properties[0]}, component.Properties...)
			case "unknown":
				component.Properties[0].Value = "scanner-claimed-origin"
			case "archive for non-Debian":
				component.Properties[0].Value = diskClassDebianArchive
				component.Properties[2].Value = "binary"
			}
			bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
			assertDependencyCode(t, err, test.code)
			if bom != nil {
				t.Fatal("invalid observation class returned a document")
			}
		})
	}
}

func TestDiskComponentClassRejectsAmbiguousOwnerValues(t *testing.T) {
	for _, values := range [][]string{{diskClassFilesystem, diskClassFilesystem}, {diskClassFilesystem, diskClassContainer}} {
		component := Component{Properties: []Property{{Name: diskContentClassProperty, Value: values[0]}, {Name: diskContentClassProperty, Value: values[1]}}}
		class, err := diskComponentClass(component)
		assertDependencyCode(t, err, e.CodeEnvelopeDecode)
		if class != "" || !strings.Contains(err.Error(), "ambiguous observation classes") {
			t.Fatalf("ambiguous owner values returned class %q or wrong error: %v", class, err)
		}
	}
}

func TestDiskClassCoveragePrecedesPublicComponentDeduplication(t *testing.T) {
	for _, containerMaterial := range []bool{false, true} {
		t.Run(strconv.FormatBool(containerMaterial), func(t *testing.T) {
			root := contentFixture("disk-image", "arm64", "b")
			container := cloneComponent(root.report.Components[0])
			container.Properties[0].Value = diskClassContainer
			root.report.Components = append(root.report.Components, container)
			inputs := []*DiskContentInspection{contentFixture("installer-iso", "arm64", "c"), contentFixture("apt-acquisition", "arm64", "d"), contentFixture("tarball", "arm64", "e")}
			if containerMaterial {
				inputs[2].report.Kind = "oci-image"
				inputs[2].report.Components = []Component{container}
			}
			bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
			if !containerMaterial {
				assertDependencyCode(t, err, e.CodeProvenanceMissing)
				if bom != nil {
					t.Fatal("host tuple bypassed container coverage")
				}
				return
			}
			if err != nil || bom == nil || len(bom.Components) != 2 {
				t.Fatalf("independently covered overlap cannot retain standard package refs: %v", err)
			}
		})
	}
}

func TestDiskContentDocumentsRetainExactFileInventory(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, kind := range []string{"disk-image", "installer-iso", "apt-acquisition", "tarball", "host-package", "oci-image"} {
			for _, filesOnly := range []bool{false, true} {
				t.Run(architecture+"/"+kind+map[bool]string{true: "/loose-only", false: "/packages-and-files"}[filesOnly], func(t *testing.T) {
					inspection := contentFixture(kind, architecture, "b")
					if filesOnly {
						inspection.report.Components = nil
					}
					bom, err := DiskContentDocument("sha256:"+strings.Repeat("f", 64), "material", inspection)
					if err != nil {
						t.Fatal(err)
					}
					wantCount := 2
					if filesOnly {
						wantCount = 1
					}
					if len(bom.Components) != wantCount || len(bom.Dependencies) != 1 || len(bom.Dependencies[0].DependsOn) != wantCount {
						t.Fatalf("incomplete material coverage: %+v", bom)
					}
					method := ""
					for _, property := range bom.Metadata.Props {
						if property.Name == "nvkr:dependencyInspection" {
							method = property.Value
						}
					}
					if method != "disk-content/v1" {
						t.Fatalf("mixed content inventory falsely claims method %q", method)
					}
					found := false
					for _, component := range bom.Components {
						if component.Type == "file" && component.Name == "usr/bin/tool" && len(component.Hashes) == 1 && component.Hashes[0].Content == strings.Repeat("a", 64) {
							found = true
						}
					}
					if !found || bom.Metadata.Component.Hashes[0].Content != strings.Repeat("b", 64) {
						t.Fatal("file/source subject hash not retained")
					}
					if _, err := bom.Marshal(); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestReconcileDiskContentsRejectsMissingAndSubstitutedEvidence(t *testing.T) {
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{"nil root", e.CodeProvenanceMissing}, {"unissued runtime", e.CodeProvenanceMissing},
		{"missing base", e.CodeProvenanceMissing}, {"missing capture", e.CodeProvenanceMissing}, {"missing staged", e.CodeProvenanceMissing},
		{"wrong architecture", e.CodeVerifyFailed}, {"different scanner", e.CodeVerifyFailed},
		{"sibling runtime", e.CodeVerifyFailed}, {"different platform", e.CodeVerifyFailed},
		{"different guestfish", e.CodeVerifyFailed}, {"different package reader", e.CodeVerifyFailed},
		{"different codec", e.CodeVerifyFailed}, {"different inspector", e.CodeVerifyFailed},
		{"different filesystem bound", e.CodeVerifyFailed}, {"wrong input architecture", e.CodeVerifyFailed}, {"nil input", e.CodeVerifyFailed},
		{"unmatched package", e.CodeProvenanceMissing}, {"version shortening", e.CodeProvenanceMissing},
		{"unsupported input", e.CodeEngineCapability}, {"duplicate subject", e.CodeVerifyFailed}, {"duplicate root packages", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := contentFixture("disk-image", "arm64", "b")
			inputs := []*DiskContentInspection{contentFixture("installer-iso", "arm64", "c"), contentFixture("apt-acquisition", "arm64", "d"), contentFixture("tarball", "arm64", "e")}
			switch test.name {
			case "nil root":
				root = nil
			case "unissued runtime":
				root.runtimeDigest = ""
			case "missing base":
				inputs = inputs[1:]
			case "missing capture":
				inputs = append(inputs[:1], inputs[2])
			case "missing staged":
				inputs = inputs[:2]
			case "wrong architecture":
				root.report.Components[0].Properties[1].Value = "amd64"
			case "different scanner":
				inputs[0].report.Scanner = "sha256:" + strings.Repeat("f", 64)
			case "sibling runtime":
				inputs[0].runtimeDigest = "sha256:" + strings.Repeat("f", 64)
			case "different platform":
				inputs[0].report.Platform = "linux/amd64"
			case "different guestfish":
				inputs[0].report.Guestfish = "sha256:" + strings.Repeat("f", 64)
			case "different package reader":
				inputs[0].report.PackageReader = "sha256:" + strings.Repeat("f", 64)
			case "different codec":
				inputs[0].report.Codec = "sha256:" + strings.Repeat("f", 64)
			case "different inspector":
				inputs[0].report.Inspector = "sha256:" + strings.Repeat("f", 64)
			case "different filesystem bound":
				inputs[0].report.MaxFilesystemBytes--
			case "wrong input architecture":
				inputs[0].report.Components[0].Properties[1].Value = "amd64"
			case "nil input":
				inputs[0] = nil
			case "unmatched package":
				root.report.Components[0].Name = "unaccounted"
			case "version shortening":
				root.report.Components[0].Version = "1.0"
			case "unsupported input":
				inputs[0].report.Kind = "raw-guessed"
			case "duplicate subject":
				inputs[1].report.Subject = inputs[0].report.Subject
			case "duplicate root packages":
				root.report.Components = append(root.report.Components, cloneComponent(root.report.Components[0]))
			}
			bom, err := ReconcileDiskContents(t.Context(), "sha256:"+strings.Repeat("f", 64), "node", root, inputs)
			if test.code == "" {
				if err != nil || bom == nil || len(bom.Components) != 2 {
					t.Fatalf("same-class duplicate did not deduplicate deterministically: %v", err)
				}
				return
			}
			assertDependencyCode(t, err, test.code)
			if bom != nil {
				t.Fatalf("bad evidence returned a document: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReconcileDiskContents(ctx, "", "", nil, nil); sbomCode(err) != e.CodeTimeout {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestFilesystemNativeMetadataAndFileProjection(t *testing.T) {
	pin := "sha256:" + strings.Repeat("a", 64)
	for _, schema := range []string{"16.1.10", "16.1.9", "unknown"} {
		t.Run(schema, func(t *testing.T) {
			raw := []byte(`{"schema":{"version":"` + schema + `"},"artifacts":[{"name":"sample","version":"2:1.0-1ubuntu4","type":"deb","purl":"pkg:deb/ubuntu/sample@1?arch=arm64","metadata":{"architecture":"arm64"}}]}`)
			inventory, err := filesystemDependenciesFromJSON(raw, pin)
			if schema != "16.1.10" {
				assertDependencyCode(t, err, e.CodeProvenanceMissing)
				return
			}
			if err != nil || inventory.Components[0].Version != "2:1.0-1ubuntu4" || inventory.Components[0].Properties[0].Value != "arm64" {
				t.Fatalf("native metadata changed: %+v %v", inventory, err)
			}
		})
	}
	for _, name := range []string{"valid", "traversal", "duplicate", "fifo"} {
		t.Run(name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			filename := "usr/bin/loose"
			if name == "traversal" {
				filename = "../loose"
			}
			header := &tar.Header{Name: filename, Typeflag: tar.TypeReg, Mode: 0o644, Size: 3}
			if name == "fifo" {
				header.Typeflag = tar.TypeFifo
				header.Size = 0
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Size != 0 {
				_, _ = writer.Write([]byte("bin"))
			}
			if name == "duplicate" {
				_ = writer.WriteHeader(header)
				_, _ = writer.Write([]byte("bin"))
			}
			_ = writer.Close()
			file, err := os.CreateTemp(t.TempDir(), "export")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			_, _ = io.Copy(file, &archive)
			files, err := filesystemFiles(t.Context(), file, 8192)
			if name == "traversal" || name == "duplicate" {
				assertDependencyCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil || len(files) != 1 {
				t.Fatalf("file observation failed: %v", err)
			}
			if name == "valid" && !canonical.IsDigest(files[0].Digest) {
				t.Fatal("loose file has no exact content hash")
			}
		})
	}
}

func TestBoundedScannerTransportStopsOverflowAndCancellation(t *testing.T) {
	for _, name := range []string{"within", "overflow", "canceled"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			if name == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			var output bytes.Buffer
			size := 8
			if name == "overflow" {
				size = 64
			}
			err := writeBoundedArchive(ctx, &output, 16, func(destination io.Writer) error {
				_, err := io.Copy(destination, strings.NewReader(strings.Repeat("x", size)))
				return err
			})
			if name == "within" {
				if err != nil || output.Len() != 8 {
					t.Fatalf("valid stream failed: %v", err)
				}
				return
			}
			want := e.CodeEngineCapability
			if name == "canceled" {
				want = e.CodeTimeout
			}
			if sbomCode(err) != want {
				t.Fatalf("wrong bounded stream error: %v", err)
			}
			if output.Len() > 17 {
				t.Fatal("writer exceeded active-stream bound")
			}
		})
	}
}

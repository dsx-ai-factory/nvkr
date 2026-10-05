package sbom

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func compressedMaterialFixture(t *testing.T, members map[string]string) *generatedInspectionFile {
	t.Helper()
	var raw bytes.Buffer
	archive := tar.NewWriter(&raw)
	for _, name := range slices.Sorted(maps.Keys(members)) {
		data := members[name]
		if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(archive, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return compressedMaterialBytes(t, raw.Bytes())
}

func compressedMaterialBytes(t *testing.T, archive []byte) *generatedInspectionFile {
	t.Helper()
	var raw bytes.Buffer
	compressed := gzip.NewWriter(&raw)
	if _, err := compressed.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	subject, err := createGeneratedInspectionFile(t.Context(), 1<<20, func(destination io.Writer) error {
		_, err := destination.Write(raw.Bytes())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = subject.Close() })
	return subject
}

func compressedDockerFixture(t *testing.T) *generatedInspectionFile {
	return compressedDockerPlatformFixture(t, "amd64")
}

func compressedDockerPlatformFixture(t *testing.T, architecture string) *generatedInspectionFile {
	t.Helper()
	configuration, err := empty.Image.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	configuration.OS, configuration.Architecture = "linux", architecture
	image, err := mutate.ConfigFile(empty.Image, configuration)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := tarball.Write(name.MustParseReference("nkd.invalid/material:fixture"), image, &archive); err != nil {
		t.Fatal(err)
	}
	return compressedMaterialBytes(t, archive.Bytes())
}

func TestCompressedOrdinaryMaterialIsNotAnImage(t *testing.T) {
	for _, test := range []struct {
		name    string
		members map[string]string
	}{
		{"documentation", map[string]string{"examples/README.txt": "ordinary data"}},
		{"nested manifest is ordinary data", map[string]string{"source/manifest.json": "not an image manifest"}},
		{"empty tar", map[string]string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			subject := compressedMaterialFixture(t, test.members)
			for _, depth := range []int{0, 1, 2} {
				inspection, err := inspectCompressedImages(t.Context(), subject, "linux/amd64", Scanner{}, 1<<20, depth)
				if err != nil || inspection != nil {
					t.Fatalf("ordinary archive at depth %d gained an image claim or failed: inspection=%v error=%v", depth, inspection, err)
				}
			}
		})
	}
}

// Empty Debian data filesystems still need the scanner and authoritative package
// metadata. Reaching scanner admission is not proof of a native dpkg/Syft run.
func TestDebianPayloadEmptyFilesystemReachesScannerAdmission(t *testing.T) {
	for _, directoriesOnly := range []bool{false, true} {
		t.Run(strconv.FormatBool(directoriesOnly), func(t *testing.T) {
			subject, err := createGeneratedInspectionFile(t.Context(), 1<<20, func(destination io.Writer) error {
				archive := tar.NewWriter(destination)
				if directoriesOnly {
					if err := archive.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
						return err
					}
				}
				return archive.Close()
			})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			inspection, err := inspectMaterialArchiveContents(t.Context(), subject, "linux/amd64",
				Scanner{Path: "/missing/selected-syft", Digest: "sha256:" + strings.Repeat("f", 64)}, "", 1<<20, 2, true)
			assertDependencyCode(t, err, e.CodeUnverifiedRef)
			if inspection != nil {
				t.Fatal("scanner admission failure returned material inventory")
			}
			// The default route, including disk roots, still rejects empty files.
			inspection, err = inspectMaterialArchiveDepth(t.Context(), subject, "linux/amd64", Scanner{}, "", 1<<20, 2)
			assertDependencyCode(t, err, e.CodeProvenanceMissing)
			if inspection != nil {
				t.Fatal("generic empty filesystem returned an inventory")
			}
		})
	}
}

// This uses the existing dpkg owner on Linux, with a controlled empty scanner
// response. It does not prove native Syft cataloging or a full NKE disk join.
func TestControlOnlyDebianMaterialRetainsObservedMetadata(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held package-reader execution requires Linux")
	}
	toolBytes, err := os.ReadFile("/usr/bin/dpkg-deb")
	if os.IsNotExist(err) {
		t.Skip("existing package reader is unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	root := filepath.Join(workspace, "package")
	control := filepath.Join(root, "DEBIAN")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := "Package: nkd-test-metapackage\nVersion: 1:2.3-4\nArchitecture: all\nMaintainer: NKD Test <test@example.invalid>\nDescription: controlled empty payload fixture\n"
	if err := os.WriteFile(filepath.Join(control, "control"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(workspace, "package.deb")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/dpkg-deb", "--build", root, filename)
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "SOURCE_DATE_EPOCH=0"}
	if diagnostic, err := command.CombinedOutput(); err != nil {
		t.Fatalf("existing dpkg builder failed: %v: %s", err, diagnostic)
	}
	subject, err := heldfile.OpenContext(ctx, filename, heldfile.Options{Label: "control-only-debian-test", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer subject.Close()
	scannerPath := filepath.Join(workspace, "scanner")
	script := []byte("#!/bin/sh\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[]}'\n")
	if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, depth := range []int{0, 1} {
		t.Run(strconv.Itoa(depth), func(t *testing.T) {
			inspection, err := inspectDebianMaterialDepth(ctx, subject, "linux/"+runtime.GOARCH,
				Scanner{Path: scannerPath, Digest: rawDigest(script)}, rawDigest(toolBytes), 1<<20, depth)
			if err != nil || inspection == nil {
				t.Fatalf("valid metapackage rejected: %v", err)
			}
			components := inspection.Components()
			want := diskPackageKey{"nkd-test-metapackage", "1:2.3-4", "deb", "all"}
			if len(inspection.Files()) != 0 || len(components) != 1 || components[0].Name != want.name || components[0].Version != want.version || inspection.packageMetadataByDigest[subject.Digest()] != want {
				t.Fatalf("metapackage lost exact metadata or invented files: %+v", inspection)
			}
			if inspection.subject != subject.Digest() || inspection.size != subject.Size() {
				t.Fatal("metapackage subject changed to its empty data filesystem")
			}
			class, err := diskComponentClass(components[0])
			if err != nil || class != diskClassDebianArchive {
				t.Fatalf("actual package-reader operation issued class %q: %v", class, err)
			}
		})
	}
}

// These invoke the actual held scanner operations with a controlled response.
// They exercise owner class assignment, not native Syft discovery or the full
// helper's ELF/tool admission and original-owner receipt handshake.
func TestMaterialScanOperationsIssueTheirOwnClasses(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held scanner execution requires Linux")
	}
	scannerPath := filepath.Join(t.TempDir(), "scanner")
	script := []byte("#!/bin/sh\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[{\"name\":\"observed\",\"version\":\"1\",\"type\":\"deb\",\"metadata\":{\"architecture\":\"all\"},\"properties\":[{\"name\":\"nkd:diskContentClass\",\"value\":\"debian-archive\"}]}]}'\n")
	if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	scanner := Scanner{Path: scannerPath, Digest: rawDigest(script)}
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, operation := range []string{"filesystem", "embedded-container", "direct-container"} {
			t.Run(architecture+"/"+operation, func(t *testing.T) {
				var components []Component
				var err error
				want := diskClassContainer
				switch operation {
				case "filesystem":
					want = diskClassFilesystem
					subject, createErr := createGeneratedInspectionFile(t.Context(), 1<<20, func(destination io.Writer) error {
						archive := tar.NewWriter(destination)
						if err := archive.WriteHeader(&tar.Header{Name: "payload", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}); err != nil {
							return err
						}
						if _, err := io.WriteString(archive, "x"); err != nil {
							return err
						}
						return archive.Close()
					})
					if createErr != nil {
						t.Fatal(createErr)
					}
					defer subject.Close()
					var inspection *FilesystemInspection
					inspection, err = inspectMaterialArchive(t.Context(), subject, "linux/"+architecture, scanner, "", 1<<20)
					if inspection != nil {
						components = inspection.Components()
					}
				case "embedded-container":
					var inspection *FilesystemInspection
					inspection, err = inspectCompressedImages(t.Context(), compressedDockerPlatformFixture(t, architecture), "linux/"+architecture, scanner, 1<<20, 0)
					if inspection != nil {
						components = inspection.Components()
					}
				case "direct-container":
					// The controlled scanner owns the native JSON projection;
					// these subject bytes prove routing, not valid OCI discovery.
					subject, createErr := heldfile.FromBytesContext(t.Context(), []byte("controlled OCI subject"), heldfile.Options{Label: "class-operation-test", MaxBytes: 1 << 20})
					if createErr != nil {
						t.Fatal(createErr)
					}
					defer subject.Close()
					components, err = inspectContainerMaterial(t.Context(), subject, scanner)
				}
				if err != nil || len(components) != 1 {
					t.Fatalf("actual scan components=%+v error=%v", components, err)
				}
				class, err := diskComponentClass(components[0])
				if err != nil || class != want {
					t.Fatalf("operation issued class %q, want %q: %v", class, want, err)
				}
			})
		}
	}
}

func TestCompressedImageMaterialErrorsAreNotOrdinaryFallback(t *testing.T) {
	for _, test := range []struct {
		name    string
		members map[string]string
		code    e.Code
	}{
		{"malformed Docker manifest", map[string]string{"manifest.json": "broken"}, e.CodeVerifyFailed},
		{"empty Docker manifest", map[string]string{"manifest.json": "[]"}, e.CodeEngineCapability},
		{"excessive image manifest count", map[string]string{"manifest.json": "[" + strings.Repeat(`{"Config":"config.json","RepoTags":["nkd.invalid/material:fixture"],"Layers":[]},`, 256) + `{"Config":"config.json","RepoTags":["nkd.invalid/material:fixture"],"Layers":[]}]`}, e.CodeEngineCapability},
		{"missing Docker tag", map[string]string{"manifest.json": `[{"Config":"config.json","Layers":[]}]`}, e.CodeEngineCapability},
		{"missing Docker layer", map[string]string{"manifest.json": `[{"Config":"config.json","RepoTags":["nkd.invalid/material:fixture"],"Layers":["missing.tar"]}]`, "config.json": `{"os":"linux","architecture":"amd64"}`}, e.CodeVerifyFailed},
		{"OCI layout not silently ignored", map[string]string{"oci-layout": `{"imageLayoutVersion":"1.0.0"}`, "index.json": `{"schemaVersion":2,"manifests":[]}`}, e.CodeEngineCapability},
		{"mixed Docker and OCI cannot omit independent index", map[string]string{"manifest.json": `[{"Config":"config.json","RepoTags":["nkd.invalid/material:fixture"],"Layers":[]}]`, "config.json": `{"os":"linux","architecture":"amd64"}`, "oci-layout": `{"imageLayoutVersion":"1.0.0"}`, "index.json": `{"schemaVersion":2,"manifests":[]}`}, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection, err := inspectCompressedImages(t.Context(), compressedMaterialFixture(t, test.members), "linux/amd64", Scanner{}, 1<<20, 0)
			assertDependencyCode(t, err, test.code)
			if inspection != nil {
				t.Fatal("malformed or unsupported image returned inventory")
			}
		})
	}
}

func TestCompressedDockerMaterialKeepsDecoderAndDepthAdmission(t *testing.T) {
	for _, test := range []struct {
		name, platform string
		depth          int
		code           e.Code
	}{
		{"depth zero reaches pinned scanner", "linux/amd64", 0, e.CodeUnverifiedRef},
		{"depth one reaches pinned scanner", "linux/amd64", 1, e.CodeUnverifiedRef},
		{"wrong image platform", "linux/arm64", 0, e.CodeVerifyFailed},
		{"image at depth limit", "linux/amd64", 2, e.CodeEngineCapability},
		{"image beyond depth limit", "linux/amd64", 3, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection, err := inspectCompressedImages(t.Context(), compressedDockerFixture(t), test.platform,
				Scanner{Path: "/missing/selected-syft", Digest: "sha256:" + strings.Repeat("f", 64)}, 1<<20, test.depth)
			assertDependencyCode(t, err, test.code)
			if inspection != nil {
				t.Fatal("failed image scan returned inventory")
			}
		})
	}
}

func TestCompressedMaterialBoundsAndTarDecoding(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  []byte
		max  int64
		code e.Code
	}{
		{"malformed compressed tar", []byte("not a tar archive"), 1 << 20, e.CodeVerifyFailed},
		{"gzip expansion exceeds bound", bytes.Repeat([]byte{'a'}, 1<<20), 1024, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection, err := inspectCompressedImages(t.Context(), compressedMaterialBytes(t, test.raw), "linux/amd64", Scanner{}, test.max, 0)
			assertDependencyCode(t, err, test.code)
			if inspection != nil {
				t.Fatal("invalid tar returned inventory")
			}
		})
	}
}

func TestCompressedImageMarkersRejectAmbiguity(t *testing.T) {
	for _, test := range []struct {
		name     string
		typeflag byte
		paths    []string
	}{
		{"duplicate Docker marker", tar.TypeReg, []string{"manifest.json", "./manifest.json"}},
		{"duplicate OCI marker", tar.TypeReg, []string{"oci-layout", "./oci-layout"}},
		{"linked Docker marker", tar.TypeSymlink, []string{"manifest.json"}},
		{"linked OCI marker", tar.TypeSymlink, []string{"oci-layout"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var raw bytes.Buffer
			archive := tar.NewWriter(&raw)
			for _, path := range test.paths {
				if err := archive.WriteHeader(&tar.Header{Name: path, Typeflag: test.typeflag, Mode: 0o644, Linkname: "other"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			inspection, err := inspectCompressedImages(t.Context(), compressedMaterialBytes(t, raw.Bytes()), "linux/amd64", Scanner{}, 1<<20, 0)
			assertDependencyCode(t, err, e.CodeVerifyFailed)
			if inspection != nil {
				t.Fatal("ambiguous archive gained image inventory")
			}
		})
	}
}

func TestCompressedDockerTagsCannotSelectAnotherEntry(t *testing.T) {
	for _, test := range []struct {
		name string
		tags [][]string
		code e.Code
	}{
		{"cross-entry first tag", [][]string{{"nkd.invalid/material:fixture"}, {"nkd.invalid/material:fixture"}}, e.CodeVerifyFailed},
		{"cross-entry non-first tag", [][]string{{"nkd.invalid/material:first", "nkd.invalid/material:shared"}, {"nkd.invalid/material:second", "nkd.invalid/material:shared"}}, e.CodeVerifyFailed},
		{"normalized registry alias", [][]string{{"docker.io/library/material:fixture"}, {"index.docker.io/library/material:fixture"}}, e.CodeVerifyFailed},
		{"invalid non-first tag", [][]string{{"nkd.invalid/material:fixture", "not a tag"}}, e.CodeVerifyFailed},
		{"same-entry aliases are not ambiguous", [][]string{{"docker.io/library/material:fixture", "index.docker.io/library/material:fixture"}}, e.CodeUnverifiedRef},
		{"distinct entries reach scanner", [][]string{{"nkd.invalid/material:first"}, {"nkd.invalid/material:second"}}, e.CodeUnverifiedRef},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := make(tarball.Manifest, len(test.tags))
			members := make(map[string]string)
			for index, tags := range test.tags {
				config := strconv.Itoa(index) + ".json"
				manifest[index] = tarball.Descriptor{Config: config, RepoTags: tags, Layers: []string{}}
				members[config] = `{"architecture":"amd64","os":"linux","config":{"Labels":{"fixture":"` + strconv.Itoa(index) + `"}},"rootfs":{"type":"layers","diff_ids":[]}}`
			}
			raw, err := canonical.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			members["manifest.json"] = string(raw)
			inspection, err := inspectCompressedImages(t.Context(), compressedMaterialFixture(t, members), "linux/amd64",
				Scanner{Path: "/missing/selected-syft", Digest: "sha256:" + strings.Repeat("f", 64)}, 1<<20, 0)
			assertDependencyCode(t, err, test.code)
			if inspection != nil {
				t.Fatal("failed image selection returned inventory")
			}
		})
	}
}

// The controlled scanner proves parent hash retention and routing, not a real
// Syft inventory, Debian export or native full-disk evidence join.
func TestOrdinaryCompressedMaterialRetainsParentFileHash(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held scanner execution requires Linux")
	}
	scannerPath := filepath.Join(t.TempDir(), "scanner")
	script := []byte("#!/bin/sh\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[]}'\n")
	if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	scanner := Scanner{Path: scannerPath, Digest: rawDigest(script)}
	for _, suffix := range []string{".tgz", ".tar.gz"} {
		for _, depth := range []int{0, 2} {
			t.Run(suffix+"/"+strconv.Itoa(depth), func(t *testing.T) {
				member := compressedMaterialFixture(t, map[string]string{"docs/README": "ordinary source"})
				parent, err := createGeneratedInspectionFile(t.Context(), 1<<20, func(destination io.Writer) error {
					archive := tar.NewWriter(destination)
					if err := archive.WriteHeader(&tar.Header{Name: "source" + suffix, Typeflag: tar.TypeReg, Mode: 0o644, Size: member.Size()}); err != nil {
						return err
					}
					if _, err := member.WriteToContext(t.Context(), archive); err != nil {
						return err
					}
					return archive.Close()
				})
				if err != nil {
					t.Fatal(err)
				}
				defer parent.Close()
				inspection, err := inspectMaterialArchiveDepth(t.Context(), parent, "linux/amd64", scanner, "", 1<<20, depth)
				if err != nil || inspection == nil {
					t.Fatalf("parent inspection=%v error=%v", inspection, err)
				}
				files := inspection.Files()
				if len(files) != 1 || files[0].Path != "source"+suffix || files[0].Digest != member.Digest() || files[0].Size != member.Size() {
					t.Fatalf("compressed file hash lost: %+v", files)
				}
			})
		}
	}
}

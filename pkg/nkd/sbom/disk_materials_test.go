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
	"sync"
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

func TestOrdinaryArchiveProbeDoesNotChargeImageBudgets(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"gzip manpage", bytes.Repeat([]byte("ordinary documentation\n"), 150000)},
		{"large ordinary layer tar", helmTestArchive(t, map[string]string{"usr/share/doc/text": strings.Repeat("x", 3<<20)})},
		{"corrupt tar candidate", []byte("not a tar")},
		{"empty tar candidate", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			subject := compressedMaterialBytes(t, test.raw)
			budget := &materialInspectionBudget{remaining: 2 << 20, members: maxDiskInstalledPackages, images: 256}
			inspection, err := inspectNestedImagesBudget(t.Context(), subject, "linux/amd64", Scanner{}, 2<<20, 2, budget)
			if err != nil || inspection != nil || budget.remaining != 2<<20 || budget.members != maxDiskInstalledPackages || budget.images != 256 {
				t.Fatalf("ordinary data charged image inventory: %+v %v", budget, err)
			}
		})
	}
	for _, raw := range [][]byte{nil, []byte("corrupt")} {
		indicated, err := probeImageArchive(t.Context(), bytes.NewReader(raw))
		if err != nil || indicated {
			t.Fatalf("empty/corrupt candidate selected an image: %v", err)
		}
	}
}

type cancelProbeReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (reader *cancelProbeReader) Read(raw []byte) (int, error) {
	n, err := reader.reader.Read(raw)
	reader.cancel()
	return n, err
}

func TestImageArchiveProbeCancellationDuringOrdinaryExpansion(t *testing.T) {
	// The tar header advertises a highly compressible large ordinary member.
	// Cancel once compressed bytes enter bufio: discarding already-buffered
	// expanded content must still observe cancellation, not finish the bomb.
	var raw bytes.Buffer
	compressed := gzip.NewWriter(&raw)
	archive := tar.NewWriter(compressed)
	if err := archive.WriteHeader(&tar.Header{Name: "ordinary", Typeflag: tar.TypeReg, Size: 8 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(archive, strings.NewReader(strings.Repeat("0", 8<<20)), 8<<20); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	indicated, err := probeImageArchive(ctx, &cancelProbeReader{reader: bytes.NewReader(raw.Bytes()), cancel: cancel})
	assertDependencyCode(t, err, e.CodeTimeout)
	if indicated {
		t.Fatal("canceled format probe selected an image")
	}
}

// Only one underlying read is released after cancellation. The next read waits
// for cleanup, rather than manufacturing EOF or independently observing ctx.
// This tests cancellation between reads, not interruption of an arbitrary Read.
type gatedArchiveProbeReader struct {
	started   chan struct{}
	resume    chan struct{}
	readAgain chan struct{}
	stop      chan struct{}
	reads     int
}

func (reader *gatedArchiveProbeReader) Read(raw []byte) (int, error) {
	reader.reads++
	if reader.reads == 1 {
		close(reader.started)
		<-reader.resume
		// Leave both the large tar payload and gzip extra field incomplete.
		n := min(len(raw), 512)
		clear(raw[:n])
		return n, nil
	}
	close(reader.readAgain)
	<-reader.stop
	return 0, io.EOF
}

func archiveProbeHeaderPrefix(t *testing.T, name string, compressed bool) []byte {
	t.Helper()
	var raw bytes.Buffer
	archive := tar.NewWriter(&raw)
	if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	// Deliberately omit payload and EOF: the probe must select a root marker
	// from the standard-library header without materializing its huge payload.
	if !compressed {
		return raw.Bytes()
	}
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	if _, err := writer.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	prefix := bytes.Clone(encoded.Bytes())
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return prefix
}

func TestImageArchiveProbeCancellationBeforeEOF(t *testing.T) {
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	writer.Extra = make([]byte, 65535)
	if _, err := writer.Write([]byte("ordinary")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		prefix []byte
	}{
		{"ordinary tar payload", archiveProbeHeaderPrefix(t, "ordinary", false)},
		// Keep the gzip fixed header and extra-field length; its extra field
		// is supplied incrementally by the gated source, before tar decoding.
		{"gzip extra header", bytes.Clone(encoded.Bytes()[:12])},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			reader := &gatedArchiveProbeReader{
				started: make(chan struct{}), resume: make(chan struct{}),
				readAgain: make(chan struct{}), stop: make(chan struct{}),
			}
			resume := sync.OnceFunc(func() { close(reader.resume) })
			stop := sync.OnceFunc(func() { close(reader.stop) })
			finished := make(chan struct{})
			result := make(chan error, 1)
			t.Cleanup(func() {
				cancel()
				resume()
				stop()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("format probe did not join after releasing its source")
				}
			})
			go func() {
				defer close(finished)
				indicated, err := probeImageArchive(ctx, io.MultiReader(bytes.NewReader(test.prefix), reader))
				if indicated {
					result <- e.New(e.CodeVerifyFailed, "canceled ordinary stream selected an image")
					return
				}
				result <- err
			}()
			select {
			case <-reader.started:
			case <-time.After(5 * time.Second):
				t.Fatal("probe did not reach the gated source")
			}
			cancel()
			resume()
			select {
			case err := <-result:
				assertDependencyCode(t, err, e.CodeTimeout)
			case <-reader.readAgain:
				t.Fatal("canceled probe demanded more bytes instead of returning before EOF")
			case <-time.After(5 * time.Second):
				t.Fatal("canceled probe did not return")
			}
		})
	}
}

type unreadArchiveProbePayload struct{ reads int }

func (reader *unreadArchiveProbePayload) Read([]byte) (int, error) {
	reader.reads++
	return 0, io.ErrUnexpectedEOF
}

func TestImageArchiveProbeSelectsHeaderWithoutMaterializingPayload(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(strconv.FormatBool(compressed), func(t *testing.T) {
			payload := &unreadArchiveProbePayload{}
			prefix := archiveProbeHeaderPrefix(t, "manifest.json", compressed)
			indicated, err := probeImageArchive(t.Context(), io.MultiReader(bytes.NewReader(prefix), payload))
			if err != nil || !indicated || payload.reads != 0 {
				t.Fatalf("probe materialized a 1-TiB advertised payload: image=%v error=%v payload reads=%d", indicated, err, payload.reads)
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
func debianMaterialOperationFixture(t *testing.T, withPayload bool) (*heldfile.File, Scanner, string) {
	t.Helper()
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
	if withPayload {
		if err := os.MkdirAll(filepath.Join(root, "usr", "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "usr", "bin", "tool"), []byte("contained file"), 0o755); err != nil {
			t.Fatal(err)
		}
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
	t.Cleanup(func() { _ = subject.Close() })
	scannerPath := filepath.Join(workspace, "scanner")
	script := []byte("#!/bin/sh\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[]}'\n")
	if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	return subject, Scanner{Path: scannerPath, Digest: rawDigest(script)}, rawDigest(toolBytes)
}

func TestControlOnlyDebianMaterialRetainsObservedMetadata(t *testing.T) {
	subject, scanner, packageReader := debianMaterialOperationFixture(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, depth := range []int{0, 1} {
		t.Run(strconv.Itoa(depth), func(t *testing.T) {
			inspection, err := inspectDebianMaterialDepth(ctx, subject, "linux/"+runtime.GOARCH,
				scanner, packageReader, 1<<20, depth)
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

// This reaches the actual recursive archive and dpkg reader on Linux with a
// controlled scanner, not a native Syft or guestfish discovery run.
func TestCachedDebianArchivesDoNotInventInstalledPaths(t *testing.T) {
	packageFile, scanner, packageReader := debianMaterialOperationFixture(t, true)
	var raw bytes.Buffer
	if _, err := packageFile.WriteToContext(t.Context(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			members := map[string]string{"var/cache/apt/archives/first.deb": raw.String(), "var/cache/apt/archives/second.deb": raw.String(),
				"packages/var/cache/apt/archives/first.deb/usr/bin/tool": "actual guest path"}
			subject, err := createGeneratedInspectionFile(t.Context(), 1<<20, func(destination io.Writer) error {
				archive := tar.NewWriter(destination)
				for _, name := range slices.Sorted(maps.Keys(members)) {
					data := members[name]
					if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}); err != nil {
						return err
					}
					if _, err := io.WriteString(archive, data); err != nil {
						return err
					}
				}
				return archive.Close()
			})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			inspection, err := inspectMaterialArchive(t.Context(), subject, "linux/"+architecture, scanner, packageReader, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			if len(inspection.Files()) != len(members) || len(inspection.Components()) != 2 {
				t.Fatalf("nested files leaked into root inventory: %+v", inspection)
			}
			for _, file := range inspection.Files() {
				if data, exists := members[file.Path]; !exists || file.Digest != rawDigest([]byte(data)) {
					t.Fatalf("invented or altered guest file: %+v", file)
				}
			}
			carriers := map[string]bool{}
			for _, component := range inspection.Components() {
				if class, err := diskComponentClass(component); err != nil || class != diskClassDebianArchive {
					t.Fatalf("cached archive changed class: %q %v", class, err)
				}
				for _, property := range component.Properties {
					if property.Name == "nkd:containedIn" {
						carriers[property.Value] = true
					}
				}
			}
			for _, name := range []string{"var/cache/apt/archives/first.deb", "var/cache/apt/archives/second.deb"} {
				if !carriers[name+"@"+packageFile.Digest()] {
					t.Fatalf("exact cached carrier absent: %v", carriers)
				}
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
					// The real OCI owner validates this closure; the controlled
					// scanner models package discovery, not native Syft evidence.
					archive := nestedOCIFixture(t, []string{architecture}, false, nil)
					subject, createErr := heldfile.FromReaderContext(t.Context(), io.NewSectionReader(archive, 0, archive.Size()), heldfile.Options{Label: "class-operation-test", MaxBytes: 1 << 20})
					if createErr != nil {
						t.Fatal(createErr)
					}
					defer subject.Close()
					components, err = inspectContainerMaterial(t.Context(), subject, "linux/"+architecture, scanner, 1<<20)
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
		{"OCI layout not silently ignored", map[string]string{"oci-layout": `{"imageLayoutVersion":"1.0.0"}`, "index.json": `{"schemaVersion":2,"manifests":[]}`}, e.CodeVerifyFailed},
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
		{"selected image expansion exceeds bound", helmTestArchive(t, map[string]string{"manifest.json": "[]", "padding": strings.Repeat("a", 1<<20)}), 1024, e.CodeEngineCapability},
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

func TestOrdinaryGzipStreamDoesNotClaimAnImage(t *testing.T) {
	for _, raw := range [][]byte{[]byte("compressed manual page"), bytes.Repeat([]byte{'a'}, 2048)} {
		inspection, err := inspectCompressedImages(t.Context(), compressedMaterialBytes(t, raw), "linux/amd64", Scanner{}, 1<<20, 0)
		if err != nil || inspection != nil {
			t.Fatalf("ordinary gzip stream gained an image claim or failed: %v", err)
		}
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

func TestRootArchiveImageMergeKeepsExactCarrierAndBudget(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held scanner execution requires Linux")
	}
	script := []byte("#!/bin/sh\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[{\"name\":\"observed\",\"version\":\"1\",\"type\":\"binary\"}]}'\n")
	scannerPath := filepath.Join(t.TempDir(), "scanner")
	if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"docker", "oci"} {
		t.Run(format, func(t *testing.T) {
			member := compressedDockerFixture(t)
			if format == "oci" {
				member = nestedOCIFixture(t, []string{"amd64"}, false, nil)
			}
			parent, err := createGeneratedInspectionFile(t.Context(), 1<<20, func(destination io.Writer) error {
				archive := tar.NewWriter(destination)
				if err := archive.WriteHeader(&tar.Header{Name: "cache/images.tar", Typeflag: tar.TypeReg, Mode: 0o644, Size: member.Size()}); err != nil {
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
			budget := &materialInspectionBudget{remaining: 1 << 20}
			inspection, err := inspectMaterialArchiveBudget(t.Context(), parent, "linux/amd64", Scanner{Path: scannerPath, Digest: rawDigest(script)}, "", 1<<20, 0, false, budget)
			if err != nil || len(inspection.components) != 2 || len(inspection.files) != 1 || budget.remaining >= 1<<20 || budget.images != 1 || budget.members == 0 {
				t.Fatalf("root archive merge or shared budget missing: %+v %v", budget, err)
			}
			contained := inspection.components[1]
			if !slices.Contains(contained.Properties, Property{Name: "nkd:containedIn", Value: "cache/images.tar@" + member.Digest()}) ||
				inspection.files[0].Path != "cache/images.tar" || inspection.files[0].Digest != member.Digest() {
				t.Fatal("contained image lost immediate carrier or invented nested file paths")
			}
		})
	}
}

func TestRootOrdinaryCandidatesDoNotExhaustNestedImageBudgets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held scanner execution requires Linux")
	}
	script := []byte("#!/bin/sh\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[]}'\n")
	scannerPath := filepath.Join(t.TempDir(), "scanner")
	if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	member := compressedMaterialBytes(t, []byte("ordinary manpage"))
	parent, err := createGeneratedInspectionFile(t.Context(), 64<<20, func(destination io.Writer) error {
		archive := tar.NewWriter(destination)
		for index := range maxDiskInstalledPackages + 1 {
			if err := archive.WriteHeader(&tar.Header{Name: "usr/share/man/page" + strconv.Itoa(index) + ".gz", Typeflag: tar.TypeReg, Mode: 0o644, Size: member.Size()}); err != nil {
				return err
			}
			if _, err := member.WriteToContext(t.Context(), archive); err != nil {
				return err
			}
		}
		for _, name := range []string{"empty.tar", "corrupt.tar"} {
			data := ""
			if name == "corrupt.tar" {
				data = "not a tar"
			}
			if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}); err != nil {
				return err
			}
			if _, err := io.WriteString(archive, data); err != nil {
				return err
			}
		}
		return archive.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	budget := &materialInspectionBudget{remaining: 2 << 20}
	inspection, err := inspectMaterialArchiveBudget(t.Context(), parent, "linux/amd64", Scanner{Path: scannerPath, Digest: rawDigest(script)}, "", 64<<20, 0, false, budget)
	if err != nil || len(inspection.files) != maxDiskInstalledPackages+3 || budget.remaining != 2<<20 || budget.members != 0 || budget.images != 0 {
		t.Fatalf("ordinary root files were charged as nested images: %+v %v", budget, err)
	}
}

func TestRootSiblingArchivesAndDebianExportsShareExpansionBudget(t *testing.T) {
	packageFile, scanner, packageReader := debianMaterialOperationFixture(t, true)
	for _, kind := range []string{"oci", "deb"} {
		t.Run(kind, func(t *testing.T) {
			var raw bytes.Buffer
			if kind == "deb" {
				if _, err := packageFile.WriteToContext(t.Context(), &raw); err != nil {
					t.Fatal(err)
				}
			} else {
				archive := nestedOCIFixture(t, []string{runtime.GOARCH}, false, nil)
				if _, err := archive.WriteToContext(t.Context(), &raw); err != nil {
					t.Fatal(err)
				}
			}
			members := helmTestArchive(t, map[string]string{"first." + kind: raw.String(), "second." + kind: raw.String()})
			parent, err := createGeneratedInspectionFile(t.Context(), 1<<20, func(destination io.Writer) error { _, err := destination.Write(members); return err })
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			// Each first export fits; the second cannot replenish the operation.
			remaining := int64(raw.Len()) + int64(raw.Len()/2)
			if kind == "deb" {
				sample := &materialInspectionBudget{remaining: 1 << 20}
				if _, err := inspectDebianMaterialBudget(t.Context(), packageFile, "linux/"+runtime.GOARCH, scanner, packageReader, 1<<20, 1, sample); err != nil {
					t.Fatal(err)
				}
				used := (1 << 20) - sample.remaining
				if used <= 0 {
					t.Fatal("actual Debian export did not charge its byte budget")
				}
				remaining = used + used/2
			}
			budget := &materialInspectionBudget{remaining: remaining}
			inspection, err := inspectMaterialArchiveBudget(t.Context(), parent, "linux/"+runtime.GOARCH, scanner, packageReader, 1<<20, 0, false, budget)
			assertDependencyCode(t, err, e.CodeEngineCapability)
			if inspection != nil || budget.members < 2 || budget.remaining >= remaining {
				t.Fatalf("sibling operations replenished or did not exercise shared expansion: %+v %v", budget, err)
			}
		})
	}
}

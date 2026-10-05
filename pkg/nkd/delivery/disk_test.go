package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const testDiskType = "application/vnd.example.disk.v1"

func TestDiskArtifactTypeNameBounds(t *testing.T) {
	for _, tc := range []struct {
		name              string
		topLevel, subtype int
		valid             bool
	}{
		{"both at bound", 127, 127, true},
		{"top-level over bound", 128, 1, false},
		{"subtype over bound", 1, 128, false},
		{"both over bound", 128, 128, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifactType := strings.Repeat("t", tc.topLevel) + "/" + strings.Repeat("s", tc.subtype)
			if validDiskArtifactType(artifactType) != tc.valid {
				t.Fatalf("unexpected admission for %d/%d-byte names", tc.topLevel, tc.subtype)
			}
			if tc.valid {
				payload := []byte("opaque")
				path := filepath.Join(t.TempDir(), "source")
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				_, err := BuildDiskArtifact(t.Context(), filepath.Join(t.TempDir(), "output"), DiskArtifactRequest{
					Path: path, Digest: digestBytes(payload), Size: int64(len(payload)), Platform: "linux/amd64",
					Format: "qcow2", Encryption: "none", ArtifactType: artifactType})
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestBuildDiskArtifactRejectsOversizedTypeBeforeIO(t *testing.T) {
	artifactType := "t/" + strings.Repeat("a", 8<<20)
	if normalized, parameters, err := mime.ParseMediaType(artifactType); err != nil || normalized != artifactType || len(parameters) != 0 {
		t.Fatal("fixture is not a valid unparameterized media type")
	}
	for _, missingSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "tiny payload", true: "missing source"}[missingSource], func(t *testing.T) {
			parent := t.TempDir()
			path := filepath.Join(t.TempDir(), "source")
			payload := []byte("opaque")
			if !missingSource {
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := BuildDiskArtifact(t.Context(), filepath.Join(parent, "output"), DiskArtifactRequest{
				Path: path, Digest: digestBytes(payload), Size: int64(len(payload)), Platform: "linux/amd64",
				Format: "qcow2", Encryption: "none", ArtifactType: artifactType})
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if !strings.Contains(err.Error(), "requires exact bounded payload and artifact type") {
				t.Fatalf("oversized type reached source/output I/O: %v", err)
			}
			if !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("failure returned identity: %+v", result)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failure leaked output or staging: %v, %v", entries, err)
			}
		})
	}
}

// These are opaque envelope bytes, not proof of QCOW2 or filesystem validity.
func diskFixture(t *testing.T, platform string) (string, []byte, string) {
	t.Helper()
	payload := bytes.Repeat([]byte("opaque disk envelope\x00"), 20000)
	path := filepath.Join(t.TempDir(), "source.qcow2")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "layout")
	_, err := BuildDiskArtifact(t.Context(), root, DiskArtifactRequest{Path: path, Digest: digestBytes(payload), Size: int64(len(payload)),
		Platform: platform, Format: "qcow2", Encryption: "none", ArtifactType: testDiskType})
	if err != nil {
		t.Fatal(err)
	}
	index, _, err := readIndex(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	return root, payload, index.Manifests[0].Digest
}

func TestDiskArtifactExactPlatformTransport(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			root, payload, manifestPin := diskFixture(t, platform)
			index, indexRaw, err := readIndex(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := readBlob(t.Context(), root, index.Manifests[0], 8<<20)
			if err != nil {
				t.Fatal(err)
			}
			var manifest ocispec.Manifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.ArtifactType != testDiskType || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != MediaQCOW2 ||
				manifest.Layers[0].Digest.String() != digestBytes(payload) || manifest.Layers[0].Size != int64(len(payload)) ||
				manifest.Config.MediaType != ocispec.MediaTypeImageConfig {
				t.Fatalf("manifest = %+v", manifest)
			}
			configRaw, err := os.ReadFile(diskBlobPath(root, manifest.Config.Digest.String()))
			if err != nil {
				t.Fatal(err)
			}
			wantConfig, err := json.Marshal(ocispec.Platform{OS: "linux", Architecture: strings.TrimPrefix(platform, "linux/")})
			if err != nil || !bytes.Equal(configRaw, wantConfig) {
				t.Fatalf("config = %s, want %s: %v", configRaw, wantConfig, err)
			}
			artifact, err := OpenDiskArtifact(t.Context(), root, manifestPin, platform, int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = artifact.Close() })
			want := DiskArtifactInspection{ManifestDigest: manifestPin, PayloadDigest: digestBytes(payload), Size: int64(len(payload)),
				Platform: platform, ArtifactType: testDiskType, Encryption: "none"}
			if got := artifact.Inspection(); got != want {
				t.Fatalf("inspection = %+v, want %+v", got, want)
			}
			var received bytes.Buffer
			bounded := &diskBoundedWriter{writer: &received}
			n, err := artifact.WriteToContext(t.Context(), bounded)
			if err != nil || n != int64(len(payload)) || !bytes.Equal(received.Bytes(), payload) || bounded.largest > 128<<10 {
				t.Fatalf("stream = %d bytes, largest chunk %d: %v", n, bounded.largest, err)
			}
			// Source filename and output location do not enter the transport identity.
			otherSource := filepath.Join(t.TempDir(), "renamed")
			if err := os.WriteFile(otherSource, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			otherRoot := filepath.Join(t.TempDir(), "other-layout")
			result, err := BuildDiskArtifact(t.Context(), otherRoot, DiskArtifactRequest{Path: otherSource, Digest: want.PayloadDigest, Size: want.Size,
				Platform: platform, Format: "qcow2", Encryption: "none", ArtifactType: testDiskType})
			if err != nil || result.RootDigest != digestBytes(indexRaw) || result.SemanticDigest != want.PayloadDigest {
				t.Fatalf("repeat result = %+v: %v", result, err)
			}
			otherIndex, _, err := readIndex(t.Context(), otherRoot)
			if err != nil || otherIndex.Manifests[0].Digest != manifestPin {
				t.Fatalf("repeat manifest = %+v: %v", otherIndex, err)
			}
		})
	}
}

func TestBuildDiskArtifactFailsWithoutPartialOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*DiskArtifactRequest)
		code e.Code
	}{
		{"unsupported platform", func(r *DiskArtifactRequest) { r.Platform = "linux/s390x"; r.Path = "missing" }, e.CodeEngineCapability},
		{"raw format", func(r *DiskArtifactRequest) { r.Format = "raw"; r.Path = "missing" }, e.CodeEngineCapability},
		{"encrypted", func(r *DiskArtifactRequest) { r.Encryption = "luks"; r.Path = "missing" }, e.CodeEngineCapability},
		{"missing type", func(r *DiskArtifactRequest) { r.ArtifactType = "" }, e.CodeVerifyFailed},
		{"disposition not media type", func(r *DiskArtifactRequest) { r.ArtifactType = "attachment" }, e.CodeVerifyFailed},
		{"invalid media type", func(r *DiskArtifactRequest) { r.ArtifactType = "application/a(b)" }, e.CodeVerifyFailed},
		{"Unicode media type", func(r *DiskArtifactRequest) { r.ArtifactType = "application/dísk" }, e.CodeVerifyFailed},
		{"colon media type", func(r *DiskArtifactRequest) { r.ArtifactType = "application/disk:v1" }, e.CodeVerifyFailed},
		{"media type parameters", func(r *DiskArtifactRequest) { r.ArtifactType = "application/vnd.example.disk;version=1" }, e.CodeVerifyFailed},
		{"malformed digest", func(r *DiskArtifactRequest) { r.Digest = "not-a-pin" }, e.CodeVerifyFailed},
		{"zero size", func(r *DiskArtifactRequest) { r.Size = 0 }, e.CodeVerifyFailed},
		{"oversize", func(r *DiskArtifactRequest) { r.Size = maxDeliveryFileBytes + 1 }, e.CodeVerifyFailed},
		{"wrong size", func(r *DiskArtifactRequest) { r.Size++ }, e.CodeVerifyFailed},
		{"wrong digest", func(r *DiskArtifactRequest) { r.Digest = digestBytes([]byte("different")) }, e.CodeVerifyFailed},
		{"missing source", func(r *DiskArtifactRequest) { r.Path = "missing" }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			path := filepath.Join(t.TempDir(), "source")
			payload := []byte("opaque")
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			request := DiskArtifactRequest{Path: path, Digest: digestBytes(payload), Size: int64(len(payload)), Platform: "linux/amd64",
				Format: "qcow2", Encryption: "none", ArtifactType: testDiskType}
			tc.edit(&request)
			result, err := BuildDiskArtifact(t.Context(), filepath.Join(parent, "output"), request)
			assertRegistryCode(t, err, tc.code)
			if !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("failure returned identity: %+v", result)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failure leaked output or staging: %v, %v", entries, err)
			}
		})
	}
}

func TestBuildDiskArtifactPreservesExistingOutputAndRejectsSymlink(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "prior output", true: "source symlink"}[symlink], func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "source")
			if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "output")
			code := e.CodeImmutableViolation
			if symlink {
				link := filepath.Join(root, "link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path, code = link, e.CodeVerifyFailed
			} else if err := os.WriteFile(out, []byte("prior output"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := BuildDiskArtifact(t.Context(), out, DiskArtifactRequest{Path: path, Digest: digestBytes([]byte("payload")), Size: 7,
				Platform: "linux/amd64", Format: "qcow2", Encryption: "none", ArtifactType: testDiskType})
			assertRegistryCode(t, err, code)
			stages, err := filepath.Glob(filepath.Join(root, ".nkd-delivery-*"))
			if err != nil || len(stages) != 0 {
				t.Fatalf("leaked stage: %v, %v", stages, err)
			}
			if !symlink {
				prior, err := os.ReadFile(out)
				if err != nil || string(prior) != "prior output" {
					t.Fatalf("prior output changed: %q, %v", prior, err)
				}
			}
		})
	}
}

func TestOpenDiskArtifactRejectsSubstitutedContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ociManifest, *[]byte)
		code   e.Code
	}{
		{"schema", func(m *ociManifest, _ *[]byte) { m.SchemaVersion = 1 }, e.CodeVerifyFailed},
		{"manifest media", func(m *ociManifest, _ *[]byte) { m.MediaType = MediaOCIIndex }, e.CodeVerifyFailed},
		{"missing type", func(m *ociManifest, _ *[]byte) { m.ArtifactType = "" }, e.CodeVerifyFailed},
		{"disposition not media type", func(m *ociManifest, _ *[]byte) { m.ArtifactType = "attachment" }, e.CodeVerifyFailed},
		{"invalid media type", func(m *ociManifest, _ *[]byte) { m.ArtifactType = "application/a(b)" }, e.CodeVerifyFailed},
		{"Unicode media type", func(m *ociManifest, _ *[]byte) { m.ArtifactType = "application/dísk" }, e.CodeVerifyFailed},
		{"colon media type", func(m *ociManifest, _ *[]byte) { m.ArtifactType = "application/disk:v1" }, e.CodeVerifyFailed},
		{"oversized type name", func(m *ociManifest, _ *[]byte) { m.ArtifactType = strings.Repeat("t", 128) + "/s" }, e.CodeVerifyFailed},
		{"oversized subtype name", func(m *ociManifest, _ *[]byte) { m.ArtifactType = "t/" + strings.Repeat("s", 128) }, e.CodeVerifyFailed},
		{"config media", func(m *ociManifest, _ *[]byte) { m.Config.MediaType = MediaOctetStream }, e.CodeVerifyFailed},
		{"subject", func(m *ociManifest, _ *[]byte) { m.Subject = &m.Config }, e.CodeVerifyFailed},
		{"missing layer", func(m *ociManifest, _ *[]byte) { m.Layers = nil }, e.CodeVerifyFailed},
		{"extra layer", func(m *ociManifest, _ *[]byte) { m.Layers = append(m.Layers, m.Layers[0]) }, e.CodeVerifyFailed},
		{"tar layer", func(m *ociManifest, _ *[]byte) { m.Layers[0].MediaType = ocispec.MediaTypeImageLayer }, e.CodeVerifyFailed},
		{"empty layer", func(m *ociManifest, _ *[]byte) { m.Layers[0].Size = 0 }, e.CodeVerifyFailed},
		{"layer size", func(m *ociManifest, _ *[]byte) { m.Layers[0].Size-- }, e.CodeVerifyFailed},
		{"malformed layer pin", func(m *ociManifest, _ *[]byte) { m.Layers[0].Digest = "not-a-pin" }, e.CodeVerifyFailed},
		{"raw declaration", func(m *ociManifest, _ *[]byte) { m.Annotations[diskFormatAnnotation] = "raw" }, e.CodeEngineCapability},
		{"encrypted declaration", func(m *ociManifest, _ *[]byte) { m.Annotations[diskEncryptionAnnotation] = "luks" }, e.CodeEngineCapability},
		{"wrong platform", func(_ *ociManifest, b *[]byte) { *b = []byte(`{"architecture":"arm64","os":"linux"}`) }, e.CodeVerifyFailed},
		{"variant", func(_ *ociManifest, b *[]byte) { *b = []byte(`{"architecture":"amd64","os":"linux","variant":"v3"}`) }, e.CodeVerifyFailed},
		{"runnable image config", func(_ *ociManifest, b *[]byte) { *b = []byte(`{"architecture":"amd64","os":"linux","rootfs":{}}`) }, e.CodeVerifyFailed},
		{"trailing config", func(_ *ociManifest, b *[]byte) { *b = []byte(`{"architecture":"amd64","os":"linux"} {}`) }, e.CodeVerifyFailed},
		{"duplicate platform", func(_ *ociManifest, b *[]byte) {
			*b = []byte(`{"architecture":"arm64","architecture":"amd64","os":"linux"}`)
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, payload, pin := diskFixture(t, "linux/amd64")
			pin = rewriteDiskFixture(t, root, pin, tc.change)
			artifact, err := OpenDiskArtifact(t.Context(), root, pin, "linux/amd64", int64(len(payload)))
			assertRegistryCode(t, err, tc.code)
			if artifact != nil {
				t.Fatal("failed verification returned a payload capability")
			}
		})
	}
}

func TestOpenDiskArtifactORASShapedWrapperRetainsUnknownEncryption(t *testing.T) {
	root, payload, pin := diskFixture(t, "linux/arm64")
	pin = rewriteDiskFixture(t, root, pin, func(m *ociManifest, _ *[]byte) { m.Annotations = nil })
	artifact, err := OpenDiskArtifact(t.Context(), root, pin, "linux/arm64", int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifact.Close() })
	if artifact.Inspection().Encryption != "" {
		t.Fatal("absent producer declaration was promoted to unencrypted proof")
	}
}

func TestOpenDiskArtifactRejectsAmbiguousOrExternalWrapper(t *testing.T) {
	for _, kind := range []string{"index", "manifest", "external config", "external payload", "embedded payload"} {
		t.Run(kind, func(t *testing.T) {
			root, payload, pin := diskFixture(t, "linux/amd64")
			index, raw, err := readIndex(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "index" {
				raw, err = readBlob(t.Context(), root, index.Manifests[0], 8<<20)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "index", "manifest":
				raw = bytes.Replace(raw, []byte(`"schemaVersion":2`), []byte(`"schemaVersion":1,"schemaVersion":2`), 1)
			case "external config":
				raw = bytes.Replace(raw, []byte(`"config":{`), []byte(`"config":{"urls":["https://external.invalid/config"],`), 1)
			case "external payload":
				raw = bytes.Replace(raw, []byte(`"layers":[{`), []byte(`"layers":[{"urls":["https://external.invalid/disk"],`), 1)
			case "embedded payload":
				raw = bytes.Replace(raw, []byte(`"layers":[{`), []byte(`"layers":[{"data":"ZGlzaw==",`), 1)
			}
			if kind == "index" {
				if err := os.WriteFile(filepath.Join(root, "index.json"), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writer := layoutWriter{root: root}
				newDescriptor, err := writer.addBlobBytes(raw, MediaOCIManifest)
				if err != nil {
					t.Fatal(err)
				}
				index.Manifests = []Descriptor{newDescriptor}
				if _, err := writer.writeIndex(index); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(diskBlobPath(root, pin)); err != nil {
					t.Fatal(err)
				}
				pin = newDescriptor.Digest
			}
			artifact, err := OpenDiskArtifact(t.Context(), root, pin, "linux/amd64", int64(len(payload)))
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if artifact != nil {
				t.Fatal("ambiguous wrapper returned a payload capability")
			}
		})
	}
}

// Opt-in, local-layout interoperability only. This neither contacts a registry
// nor proves the config of any published NKE artifact.
func TestDiskArtifactLocalORASInterop(t *testing.T) {
	tool := os.Getenv("NKD_TEST_ORAS")
	if tool == "" {
		t.Skip("set NKD_TEST_ORAS to an ORAS executable for local layout interoperability")
	}
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			root, payload, pin := diskFixture(t, platform)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, tool, "manifest", "fetch", "--oci-layout", root+"@"+pin)
			raw, err := cmd.Output()
			if err != nil {
				t.Fatalf("ORAS reading constructed manifest: %v", err)
			}
			if digestBytes(raw) != pin {
				t.Fatal("ORAS fetched different manifest bytes")
			}
			cmd = exec.CommandContext(ctx, tool, "blob", "fetch", "--oci-layout", "--output", "-", root+"@"+digestBytes(payload))
			got, err := cmd.Output()
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("ORAS reading raw payload: %v", err)
			}
			producerDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(producerDir, "disk.qcow2"), payload, 0o600); err != nil {
				t.Fatal(err)
			}
			producerRoot := filepath.Join(producerDir, "oras-layout")
			cmd = exec.CommandContext(ctx, tool, "push", "--oci-layout", "--artifact-platform", platform,
				"--artifact-type", testDiskType, producerRoot+":test", "disk.qcow2:"+MediaQCOW2)
			cmd.Dir = producerDir
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("ORAS constructing local platform artifact: %v: %s", err, output)
			}
			index, _, err := readIndex(t.Context(), producerRoot)
			if err != nil || len(index.Manifests) != 1 {
				t.Fatalf("ORAS index: %+v: %v", index, err)
			}
			artifact, err := OpenDiskArtifact(t.Context(), producerRoot, index.Manifests[0].Digest, platform, int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = artifact.Close() })
			if got := artifact.Inspection(); got.PayloadDigest != digestBytes(payload) || got.Encryption != "" || got.Platform != platform {
				t.Fatalf("ORAS inspection: %+v", got)
			}
		})
	}
}

func TestOpenDiskArtifactRejectsLayoutTamperingAndBudget(t *testing.T) {
	for _, kind := range []string{"payload", "extra blob", "symlink", "wrong pin", "budget", "zero budget", "platform"} {
		t.Run(kind, func(t *testing.T) {
			root, payload, pin := diskFixture(t, "linux/amd64")
			path := diskBlobPath(root, digestBytes(payload))
			maxBytes := int64(len(payload))
			platform := "linux/amd64"
			switch kind {
			case "payload":
				payload[0] ^= 1
				if err := os.WriteFile(path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
			case "extra blob":
				if err := os.WriteFile(diskBlobPath(root, digestBytes([]byte("extra"))), []byte("extra"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				moved := filepath.Join(t.TempDir(), "moved")
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, path); err != nil {
					t.Fatal(err)
				}
			case "wrong pin":
				pin = digestBytes([]byte("other"))
			case "budget":
				maxBytes--
			case "zero budget":
				maxBytes = 0
			case "platform":
				platform = "linux/arm64"
			}
			artifact, err := OpenDiskArtifact(t.Context(), root, pin, platform, maxBytes)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if artifact != nil {
				t.Fatal("failed verification returned a payload capability")
			}
		})
	}
}

func TestDiskArtifactHeldInodeAndRecheckedStream(t *testing.T) {
	for _, kind := range []string{"path replacement", "inode mutation", "inode growth", "inode shrink"} {
		t.Run(kind, func(t *testing.T) {
			root, payload, pin := diskFixture(t, "linux/amd64")
			artifact, err := OpenDiskArtifact(t.Context(), root, pin, "linux/amd64", int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = artifact.Close() })
			path := diskBlobPath(root, digestBytes(payload))
			changed := bytes.Clone(payload)
			switch kind {
			case "path replacement":
				if err := os.Rename(path, filepath.Join(t.TempDir(), "held")); err != nil {
					t.Fatal(err)
				}
				changed[0] ^= 1
			case "inode mutation":
				changed[0] ^= 1
			case "inode growth":
				changed = append(changed, '!')
			case "inode shrink":
				changed = changed[:len(changed)-1]
			}
			if err := os.WriteFile(path, changed, 0o600); err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			n, err := artifact.WriteToContext(t.Context(), &got)
			if kind == "path replacement" {
				if err != nil || n != int64(len(payload)) || !bytes.Equal(got.Bytes(), payload) {
					t.Fatalf("path redirected held stream: %d, %v", n, err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestDiskArtifactMutationDuringStreamRemainsBounded(t *testing.T) {
	for _, kind := range []string{"growth", "shrink", "changed unread bytes"} {
		t.Run(kind, func(t *testing.T) {
			root, payload, pin := diskFixture(t, "linux/amd64")
			artifact, err := OpenDiskArtifact(t.Context(), root, pin, "linux/amd64", int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = artifact.Close() })
			mutable, err := os.OpenFile(diskBlobPath(root, digestBytes(payload)), os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = mutable.Close() }()
			writer := &diskMutatingWriter{mutate: func() error {
				switch kind {
				case "growth":
					return mutable.Truncate(int64(len(payload) + 1))
				case "shrink":
					return mutable.Truncate(128 << 10)
				default:
					_, err := mutable.WriteAt([]byte("changed"), 200000)
					return err
				}
			}}
			n, err := artifact.WriteToContext(t.Context(), writer)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if n > int64(len(payload)) {
				t.Fatalf("stream exceeded declared payload budget: %d > %d", n, len(payload))
			}
		})
	}
}

func TestDiskArtifactCancellationAndClose(t *testing.T) {
	root, payload, pin := diskFixture(t, "linux/amd64")
	artifact, err := OpenDiskArtifact(t.Context(), root, pin, "linux/amd64", int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifact.Close() })
	copyOfArtifact := *artifact
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = artifact.WriteToContext(canceled, io.Discard)
	assertRegistryCode(t, err, e.CodeTimeout)
	_, err = OpenDiskArtifact(canceled, root, pin, "linux/amd64", int64(len(payload)))
	assertRegistryCode(t, err, e.CodeTimeout)
	_, err = BuildDiskArtifact(canceled, filepath.Join(t.TempDir(), "out"), DiskArtifactRequest{})
	assertRegistryCode(t, err, e.CodeTimeout)
	_, err = artifact.WriteToContext(t.Context(), nil)
	assertRegistryCode(t, err, e.CodeUsage)
	var absentContext context.Context
	_, err = artifact.WriteToContext(absentContext, io.Discard)
	assertRegistryCode(t, err, e.CodeUsage)

	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	writer := &diskCancelWriter{cancel: stop}
	n, err := artifact.WriteToContext(ctx, writer)
	assertRegistryCode(t, err, e.CodeTimeout)
	if n != 128<<10 || n >= int64(len(payload)) {
		t.Fatalf("cancellation did not stop between bounded chunks: %d", n)
	}

	blocked := &diskBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	streamDone := make(chan error, 1)
	go func() { _, err := artifact.WriteToContext(t.Context(), blocked); streamDone <- err }()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		close(blocked.release)
		t.Fatal("stream did not enter destination")
	}
	waitCtx, stopWait := context.WithCancel(t.Context())
	waitDone := make(chan error, 1)
	go func() { _, err := artifact.WriteToContext(waitCtx, io.Discard); waitDone <- err }()
	stopWait()
	select {
	case err := <-waitDone:
		assertRegistryCode(t, err, e.CodeTimeout)
	case <-time.After(5 * time.Second):
		close(blocked.release)
		t.Fatal("waiting stream ignored cancellation")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- artifact.Close() }()
	close(blocked.release)
	for _, done := range []<-chan error{streamDone, closeDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("released stream or close did not finish")
		}
	}
	_, err = artifact.WriteToContext(t.Context(), io.Discard)
	assertRegistryCode(t, err, e.CodeGateUnmet)
	_, err = copyOfArtifact.WriteToContext(t.Context(), io.Discard)
	assertRegistryCode(t, err, e.CodeGateUnmet)
	if err := copyOfArtifact.Close(); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Close(); err != nil {
		t.Fatal(err)
	}
	var zero DiskArtifact
	_, err = zero.WriteToContext(t.Context(), io.Discard)
	assertRegistryCode(t, err, e.CodeGateUnmet)
	if err := zero.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDiskArtifactFailedDestinationDoesNotReturnSuccess(t *testing.T) {
	root, payload, pin := diskFixture(t, "linux/amd64")
	artifact, err := OpenDiskArtifact(t.Context(), root, pin, "linux/amd64", int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifact.Close() })
	_, err = artifact.WriteToContext(t.Context(), diskFailingWriter{})
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	n, err := artifact.WriteToContext(t.Context(), io.Discard)
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("failed stream poisoned subsequent read: %d, %v", n, err)
	}
}

func diskBlobPath(root, pin string) string {
	return filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(pin, "sha256:"))
}

func rewriteDiskFixture(t *testing.T, root, oldPin string, change func(*ociManifest, *[]byte)) string {
	t.Helper()
	index, _, err := readIndex(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := readBlob(t.Context(), root, index.Manifests[0], 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	var manifest ociManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	config, err := readBlob(t.Context(), root, manifest.Config, 8<<20)
	if err != nil {
		t.Fatal(err)
	}
	oldConfig := manifest.Config.Digest
	change(&manifest, &config)
	writer := layoutWriter{root: root}
	newConfig, err := writer.addBlobBytes(config, manifest.Config.MediaType)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Config = newConfig
	descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.ArtifactType = manifest.ArtifactType
	index.Manifests = []Descriptor{descriptor}
	if _, err := writer.writeIndex(index); err != nil {
		t.Fatal(err)
	}
	if descriptor.Digest != oldPin {
		if err := os.Remove(diskBlobPath(root, oldPin)); err != nil {
			t.Fatal(err)
		}
	}
	if newConfig.Digest != oldConfig {
		if err := os.Remove(diskBlobPath(root, oldConfig)); err != nil {
			t.Fatal(err)
		}
	}
	return descriptor.Digest
}

type diskBoundedWriter struct {
	writer  io.Writer
	largest int
}

func (writer *diskBoundedWriter) Write(raw []byte) (int, error) {
	writer.largest = max(writer.largest, len(raw))
	return writer.writer.Write(raw)
}

type diskCancelWriter struct{ cancel context.CancelFunc }

func (writer *diskCancelWriter) Write(raw []byte) (int, error) {
	writer.cancel()
	return len(raw), nil
}

type diskBlockingWriter struct {
	entered, release chan struct{}
	once             sync.Once
}

func (writer *diskBlockingWriter) Write(raw []byte) (int, error) {
	writer.once.Do(func() { close(writer.entered) })
	<-writer.release
	return len(raw), nil
}

type diskFailingWriter struct{}

func (diskFailingWriter) Write([]byte) (int, error) { return 0, errors.New("destination failed") }

type diskMutatingWriter struct{ mutate func() error }

func (writer *diskMutatingWriter) Write(raw []byte) (int, error) {
	if writer.mutate != nil {
		err := writer.mutate()
		writer.mutate = nil
		if err != nil {
			return 0, err
		}
	}
	return len(raw), nil
}

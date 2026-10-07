// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/google/go-containerregistry/pkg/registry"
)

// These documents are synthetic contract fixtures, not acquired disk evidence.
// The diagnostic constructor cannot authenticate scanner or live-run authority.
func diskPublicationRequest(t *testing.T, arch string) (PublicationRequest, string) {
	t.Helper()
	layout, payload, manifest := diskFixture(t, "linux/"+arch)
	manifest = rewriteDiskFixture(t, layout, manifest, func(wrapper *ociManifest, _ *[]byte) {
		wrapper.ArtifactType = MediaDiskImage
	})
	index, _, err := readIndex(t.Context(), layout)
	if err != nil {
		t.Fatal(err)
	}
	_, indexRaw, err := readIndex(t.Context(), layout)
	if err != nil || index.Manifests[0].Digest != manifest {
		t.Fatalf("disk index: %v", err)
	}
	build := digestBytes([]byte("exact full build"))
	rawPin := digestBytes(payload)
	disk := &DiskOutput{PayloadDigest: rawPin, Producer: "build-image", Component: "image", Format: "qcow2", Encryption: "none",
		Size: int64(len(payload)), PlanDigest: digestBytes([]byte("full plan")), Outcome: "succeeded"}
	output := Output{Name: "node-image", Kind: "disk-image", Digest: manifest, BuildDigest: build, Components: []string{"agent", "image"}, Disk: disk}
	content, err := ContentDigest([]Output{output})
	if err != nil {
		t.Fatal(err)
	}
	agentPin := digestBytes([]byte("exact staged agent"))
	request := PublicationRequest{Release: &ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, ContentSubject: content}, Objects: []ObjectInput{
		{Name: output.Name, Kind: "disk-image", Path: layout, Layout: true, ArtifactType: MediaDiskImage, SemanticDigest: rawPin,
			BlobDigest: digestBytes(indexRaw), OS: "linux", Arch: arch, Output: &OutputBinding{Repository: "node-image", Format: "disk-image",
				BuildDigest: build, Components: append([]string(nil), output.Components...), Disk: disk,
				BuildSubjects: []BuildSubject{{Component: "agent", Digest: agentPin}, {Component: "image", Digest: rawPin}}}},
	}}
	for _, subject := range request.Objects[0].Output.BuildSubjects {
		bom, err := sbom.NewSubject(build, subject.Component, subject.Digest)
		if err != nil {
			t.Fatal(err)
		}
		bom, err = sbom.ForSubject(bom, subject.Digest, "build-output", subject.Digest)
		if err != nil {
			t.Fatal(err)
		}
		if subject.Component == "image" {
			bom.Metadata.Component.Type = "operating-system"
			bom.Metadata.Component.BOMRef = "artifact:" + strings.TrimPrefix(rawPin, "sha256:")
			dependency := "pkg:generic/fixture-installed-package@1"
			bom.Components = []sbom.Component{{Type: "library", Name: "fixture-installed-package", Version: "1", PURL: dependency, BOMRef: dependency}}
			bom.Dependencies = []sbom.Dependency{{Ref: bom.Metadata.Component.BOMRef, DependsOn: []string{dependency}}}
			bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "nkd:dependencyInspection", Value: "disk-reconciled/v1"})
			for _, key := range []string{"nkd:dependencyScannerDigest", "nkd:diskInspectionRuntimeDigest", "nkd:diskInspectorDigest", "nkd:diskCodecDigest"} {
				bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: key, Value: build})
			}
			sort.Slice(bom.Metadata.Props, func(i, j int) bool { return bom.Metadata.Props[i].Name < bom.Metadata.Props[j].Name })
		} else {
			bom, err = sbom.WithDependencies(bom, []sbom.Component{{Type: "library", Name: "fixture-staged-package", Version: "1"}}, "disk-content/v1", build)
			if err != nil {
				t.Fatal(err)
			}
		}
		semantic, err := bom.Digest()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := bom.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		request.Objects = append(request.Objects, ObjectInput{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom",
			ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: raw,
			Evidence: &EvidenceBinding{Outputs: []string{output.Name}, Component: subject.Component, Stage: "build-output", SubjectKind: "file", SubjectDigest: subject.Digest}})
	}
	return request, manifest
}

// Synthetic disk fixtures exercise the real owner and capacity policy using
// controlled observed headroom, not a claim about the test host's TMPDIR.
func openDiskPublicationFixture(ctx context.Context, root, pin string) (*Publication, error) {
	return openPublication(ctx, root, pin, func(ctx context.Context, directory *os.File, required uint64) error {
		return admitPublicationDiskScratchWith(ctx, directory, required,
			func(context.Context, *os.File) (uint64, error) { return required, nil })
	})
}

func TestDiskPublicationPreservesManifestAndPayloadSubjects(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			request, manifest := diskPublicationRequest(t, arch)
			root := filepath.Join(t.TempDir(), "prepared")
			prepared, err := BuildPublicationRoot(t.Context(), root, request)
			if err != nil {
				t.Fatal(err)
			}
			var attested []string
			final := filepath.Join(t.TempDir(), "attested")
			result, err := AttestEvidence(t.Context(), root, final, func(_ context.Context, subject string, document []byte) ([]byte, error) {
				attested = append(attested, subject)
				return diskFixtureBundle(t, subject, document), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if prepared.RootDigest == result.RootDigest || len(attested) != 2 {
				t.Fatal("attested closure did not replace both evidence preparations")
			}
			for _, subject := range attested {
				if subject == manifest {
					t.Fatal("SBOM attested the wrapper instead of raw build payload")
				}
			}
			publication, err := openDiskPublicationFixture(t.Context(), final, result.RootDigest)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = publication.Close() }()
			if err := publication.RequireAttestedEvidence(); err != nil {
				t.Fatal(err)
			}
			outputs, content, err := publication.Artifacts("registry.example.test/release@" + result.RootDigest)
			if err != nil || content != request.Release.ContentSubject || len(outputs) != 1 || outputs[0].Digest != manifest ||
				outputs[0].Disk.PayloadDigest != request.Objects[0].Output.Disk.PayloadDigest {
				t.Fatalf("output subject correspondence: %#v %v", outputs, err)
			}
			if len(publication.diskFiles) != 1 {
				t.Fatal("disk payload was not retained in disk-backed storage")
			}
			description, err := publication.Description("registry.example.test/release@" + result.RootDigest)
			if err != nil || len(description.Outputs) != 1 || description.Outputs[0].OS != "linux" || description.Outputs[0].Arch != arch ||
				description.Outputs[0].Disk == nil || description.Outputs[0].Disk.PayloadDigest != outputs[0].Disk.PayloadDigest {
				t.Fatalf("disk navigation lost its checked platform or payload subject: %+v, %v", description, err)
			}
			description.Outputs[0].Disk.PayloadDigest = manifest
			description, err = publication.Description("registry.example.test/release@" + result.RootDigest)
			if err != nil || description.Outputs[0].Disk.PayloadDigest != outputs[0].Disk.PayloadDigest {
				t.Fatal("detached disk navigation mutated its held owner")
			}
			rawPin := outputs[0].Disk.PayloadDigest
			for _, held := range publication.files {
				if held.Digest() == rawPin {
					t.Fatal("whole disk was duplicated into memfd storage")
				}
			}
			outputs[0].Disk.PayloadDigest = manifest
			outputs[0].Components[0] = "caller-change"
			if err := os.WriteFile(diskBlobPath(final, rawPin), []byte("source replaced"), 0o600); err != nil {
				t.Fatal(err)
			}
			outputs, _, err = publication.Artifacts("registry.example.test/release@" + result.RootDigest)
			if err != nil || outputs[0].Disk.PayloadDigest != rawPin || outputs[0].Components[0] != "agent" {
				t.Fatal("caller mutation or source replacement changed the held projection")
			}
			var recovered bytes.Buffer
			bounded := &diskBoundedWriter{writer: &recovered}
			written, err := publication.WriteDiskPayload(t.Context(), "node-image", bounded)
			if err != nil || written != outputs[0].Disk.Size || digestBytes(recovered.Bytes()) != rawPin || bounded.largest > 128<<10 {
				t.Fatalf("held disk stream: size=%d chunk=%d err=%v", written, bounded.largest, err)
			}
			file := publication.diskFiles[0]
			if err := publication.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := file.Stat(); err == nil {
				t.Fatal("Close retained disk-backed descriptor")
			}
			_, _, err = publication.Artifacts("registry.example.test/release@" + result.RootDigest)
			assertRegistryCode(t, err, e.CodeGateUnmet)
			_, err = publication.WriteDiskPayload(t.Context(), "node-image", io.Discard)
			assertRegistryCode(t, err, e.CodeGateUnmet)
		})
	}
}

func TestDiskPublicationLargeInventoryAttestationReadback(t *testing.T) {
	request, _ := diskPublicationRequest(t, "amd64")
	changeDiskFixtureBOM(&request, func(bom *sbom.BOM) {
		bom.Metadata.Component.Properties = []sbom.Property{{Name: "fixture", Value: strings.Repeat("x", sbom.MaxDocumentBytes-4096)}}
	})
	prepared := filepath.Join(t.TempDir(), "prepared")
	if _, err := BuildPublicationRoot(t.Context(), prepared, request); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(t.TempDir(), "attested")
	expanded := false
	result, err := AttestEvidence(t.Context(), prepared, final, func(_ context.Context, subject string, document []byte) ([]byte, error) {
		bundle := diskFixtureBundle(t, subject, document)
		if len(bundle) > sbom.MaxDocumentBytes {
			expanded = true
		}
		return bundle, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !expanded {
		t.Fatal("fixture did not exercise an expanded signed bundle")
	}
	publication, err := openDiskPublicationFixture(t.Context(), final, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = publication.Close() }()
	if err := publication.RequireAttestedEvidence(); err != nil {
		t.Fatal(err)
	}
	_, document, err := EvidenceDocument(t.Context(), final, request.Objects[2].Name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(document, request.Objects[2].Bytes) {
		t.Fatal("large inventory changed during attestation and readback")
	}
}

func TestPublicationDiskStreamAdmissionCancellationAndClose(t *testing.T) {
	request, _ := diskPublicationRequest(t, "amd64")
	root := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := openDiskPublicationFixture(t.Context(), root, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = publication.Close() }()
	for _, tc := range []struct {
		name        string
		output      string
		destination io.Writer
		ctx         context.Context
		code        e.Code
	}{
		{"unknown", "missing", io.Discard, t.Context(), e.CodeVerifyFailed},
		{"nil writer", "node-image", nil, t.Context(), e.CodeUsage},
		{"nil context", "node-image", io.Discard, nil, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := publication.WriteDiskPayload(tc.ctx, tc.output, tc.destination)
			assertRegistryCode(t, err, tc.code)
			if n != 0 {
				t.Fatal("rejected stream wrote bytes")
			}
		})
	}
	ordinaryRoot, ordinaryPin := releasePublicationFixture(t)
	ordinary, err := OpenPublication(t.Context(), ordinaryRoot, ordinaryPin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ordinary.Close() }()
	_, err = ordinary.WriteDiskPayload(t.Context(), "tools", io.Discard)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	var absent *Publication
	_, err = absent.WriteDiskPayload(t.Context(), "node-image", io.Discard)
	assertRegistryCode(t, err, e.CodeGateUnmet)
	ctx, cancel := context.WithCancel(t.Context())
	writer := &diskCancelWriter{cancel: cancel}
	n, err := publication.WriteDiskPayload(ctx, "node-image", writer)
	assertRegistryCode(t, err, e.CodeTimeout)
	if n != 128<<10 {
		t.Fatalf("unbounded cancellation write: %d", n)
	}
	_, err = publication.WriteDiskPayload(t.Context(), "node-image", diskFailingWriter{})
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	blocked := &diskBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	streamDone := make(chan error, 1)
	go func() { _, err := publication.WriteDiskPayload(t.Context(), "node-image", blocked); streamDone <- err }()
	select {
	case <-blocked.entered:
	case <-time.After(5 * time.Second):
		close(blocked.release)
		<-streamDone
		t.Fatal("stream did not enter destination")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- publication.Close() }()
	select {
	case err := <-closeDone:
		close(blocked.release)
		<-streamDone
		t.Fatalf("Close interrupted active stream: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(blocked.release)
	if err := <-streamDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestPublicationDiskStreamRechecksSubstitutedSnapshot(t *testing.T) {
	request, _ := diskPublicationRequest(t, "amd64")
	root := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := openDiskPublicationFixture(t.Context(), root, result.RootDigest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = publication.Close() }()
	expected := request.Objects[0].Output.Disk
	path := filepath.Join(t.TempDir(), "substituted")
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, int(expected.Size)), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	publication.diskPayloads[expected.PayloadDigest] = file
	n, err := publication.WriteDiskPayload(t.Context(), "node-image", io.Discard)
	assertRegistryCode(t, err, e.CodeVerifyFailed)
	if n != expected.Size {
		t.Fatal("same-size substitution was not checked against complete hash")
	}
}

func TestPublicationAdmitsDiskMetadataAndInventoryBeforePayloadCapture(t *testing.T) {
	for _, tc := range []string{"stripped output", "unreferenced large blob"} {
		t.Run(tc, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "amd64")
			root := filepath.Join(t.TempDir(), "publication")
			result, err := BuildPublicationRoot(t.Context(), root, request)
			if err != nil {
				t.Fatal(err)
			}
			pin := result.RootDigest
			want := "reachable"
			if tc == "stripped output" {
				inspection, err := Inspect(t.Context(), root)
				if err != nil {
					t.Fatal(err)
				}
				for i := range inspection.Root.Objects {
					if inspection.Root.Objects[i].Output != nil {
						inspection.Root.Objects[i].Output = nil
					}
				}
				// Deliberately bypass the validated constructor to forge hostile wire
				// metadata. The missing disk blob proves metadata admission wins
				// before even attempting its source descriptor, let alone a memfd.
				raw, err := canonical.Marshal(inspection.Root)
				if err != nil {
					t.Fatal(err)
				}
				writer := layoutWriter{root: root}
				config, err := writer.addBlobBytes(raw, MediaPublicationRootConfig)
				if err != nil {
					t.Fatal(err)
				}
				descriptor, err := writer.addJSONBlob(ociManifest{SchemaVersion: 2, MediaType: MediaOCIManifest, ArtifactType: MediaPublicationRoot, Config: config, Layers: []Descriptor{}}, MediaOCIManifest)
				if err != nil {
					t.Fatal(err)
				}
				descriptor.ArtifactType = MediaPublicationRoot
				pin, err = writer.writeIndex(ociIndex{SchemaVersion: 2, MediaType: MediaOCIIndex, ArtifactType: MediaPublicationRoot, Manifests: []Descriptor{descriptor}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(diskBlobPath(root, request.Objects[0].Output.Disk.PayloadDigest)); err != nil {
					t.Fatal(err)
				}
				want = "release object has no output or evidence binding"
			} else {
				orphan := bytes.Repeat([]byte{'o'}, 9<<20)
				if err := os.WriteFile(diskBlobPath(root, digestBytes(orphan)), orphan, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			disks, err := publicationDiskPayloads(t.Context(), root, pin)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if disks != nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("admission did not precede capture: %v", err)
			}
			publication, err := openDiskPublicationFixture(t.Context(), root, pin)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if publication != nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("publication reached payload capture: %v", err)
			}
		})
	}
}

// Fixture DSSE shape tests byte/subject plumbing only, not signature authenticity.
func diskFixtureBundle(t *testing.T, subject string, document []byte) []byte {
	t.Helper()
	statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": sbom.CycloneDXPredicateType,
		"subject": []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(subject, "sha256:")}}}, "predicate": json.RawMessage(document)})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"mediaType": sbom.BundleMediaType, "verificationMaterial": map[string]any{}, "dsseEnvelope": map[string]any{
		"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "Zml4dHVyZQ=="}}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDiskPublicationRejectsMissingOrSubstitutedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*PublicationRequest, string)
		code   e.Code
	}{
		{"missing disk binding", func(r *PublicationRequest, _ string) { r.Objects[0].Output.Disk = nil }, e.CodeVerifyFailed},
		{"missing raw pin", func(r *PublicationRequest, _ string) { r.Objects[0].Output.Disk.PayloadDigest = "" }, e.CodeVerifyFailed},
		{"manifest as raw pin", func(r *PublicationRequest, manifest string) { r.Objects[0].Output.Disk.PayloadDigest = manifest }, e.CodeVerifyFailed},
		{"manifest as root subject", func(r *PublicationRequest, manifest string) { r.Objects[0].Output.BuildSubjects[1].Digest = manifest }, e.CodeVerifyFailed},
		{"missing staged slot", func(r *PublicationRequest, _ string) { r.Objects = append(r.Objects[:1], r.Objects[2:]...) }, e.CodeProvenanceMissing},
		{"missing root slot", func(r *PublicationRequest, _ string) { r.Objects = r.Objects[:2] }, e.CodeProvenanceMissing},
		{"wrong platform", func(r *PublicationRequest, _ string) { r.Objects[0].Arch = "arm64" }, e.CodeVerifyFailed},
		{"wrong size", func(r *PublicationRequest, _ string) { r.Objects[0].Output.Disk.Size++ }, e.CodeVerifyFailed},
		{"encrypted wrapper", func(r *PublicationRequest, _ string) { r.Objects[0].Output.Disk.Encryption = "luks2" }, e.CodeVerifyFailed},
		{"metadata-only inventory", func(r *PublicationRequest, _ string) {
			changeDiskFixtureBOM(r, func(b *sbom.BOM) {
				b.Components, b.Dependencies = nil, nil
				for i := range b.Metadata.Props {
					if b.Metadata.Props[i].Name == "nkd:dependencyInspection" {
						b.Metadata.Props = append(b.Metadata.Props[:i], b.Metadata.Props[i+1:]...)
						break
					}
				}
			})
		}, e.CodeProvenanceMissing},
		{"missing scanner pin", func(r *PublicationRequest, _ string) {
			changeDiskFixtureBOM(r, func(b *sbom.BOM) {
				for i := range b.Metadata.Props {
					if b.Metadata.Props[i].Name == "nkd:dependencyScannerDigest" {
						b.Metadata.Props = append(b.Metadata.Props[:i], b.Metadata.Props[i+1:]...)
						break
					}
				}
			})
		}, e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, manifest := diskPublicationRequest(t, "amd64")
			tc.change(&request, manifest)
			out := filepath.Join(t.TempDir(), "rejected")
			_, err := BuildPublicationRoot(t.Context(), out, request)
			assertRegistryCode(t, err, tc.code)
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("rejected request published output: %v", err)
			}
		})
	}
}

func changeDiskFixtureBOM(request *PublicationRequest, change func(*sbom.BOM)) {
	changeDiskFixtureComponentBOM(request, "image", change)
}

func changeDiskFixtureComponentBOM(request *PublicationRequest, component string, change func(*sbom.BOM)) {
	var input *ObjectInput
	for index := range request.Objects {
		candidate := &request.Objects[index]
		if candidate.Evidence != nil && candidate.Evidence.Component == component {
			input = candidate
			break
		}
	}
	if input == nil {
		panic("fixture has no component evidence: " + component)
	}
	var bom sbom.BOM
	if err := canonical.UnmarshalExact([]byte(strings.TrimSuffix(string(input.Bytes), "\n")), &bom); err != nil {
		panic(err)
	}
	change(&bom)
	var err error
	// Negative fixtures deliberately bypass BOM.Validate so invalid inventory
	// bytes reach the consumer's owning validation gate instead of failing here.
	input.Bytes, err = canonical.Marshal(bom)
	if err != nil {
		panic(err)
	}
	input.SemanticDigest, err = canonical.Digest(canonical.KindSBOM, bom)
	if err != nil {
		panic(err)
	}
	input.Name = "sbom-" + strings.TrimPrefix(input.SemanticDigest, "sha256:")
}

func TestDiskPublicationRequiresEveryInspectionIdentity(t *testing.T) {
	for _, key := range []string{"nkd:dependencyScannerDigest", "nkd:diskInspectionRuntimeDigest", "nkd:diskInspectorDigest", "nkd:diskCodecDigest"} {
		t.Run(key, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, "amd64")
			changeDiskFixtureBOM(&request, func(bom *sbom.BOM) {
				for index, property := range bom.Metadata.Props {
					if property.Name == key {
						bom.Metadata.Props = append(bom.Metadata.Props[:index], bom.Metadata.Props[index+1:]...)
						return
					}
				}
				t.Fatalf("fixture lacks %s", key)
			})
			out := filepath.Join(t.TempDir(), "rejected")
			_, err := BuildPublicationRoot(t.Context(), out, request)
			assertRegistryCode(t, err, e.CodeProvenanceMissing)
			if !strings.Contains(err.Error(), "disk inventory lacks an exact "+key) {
				t.Fatalf("did not reach exact inspection identity gate: %v", err)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("rejected request published output: %v", err)
			}
		})
	}
}

func TestDiskRegistryRoundTrip(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		for _, private := range []bool{false, true} {
			t.Run(arch+map[bool]string{false: "/anonymous", true: "/explicit-private"}[private], func(t *testing.T) {
				handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if private {
						user, password, ok := r.BasicAuth()
						if !ok || user != "fixture" || password != "fixture-password" {
							w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
					}
					handler.ServeHTTP(w, r)
				}))
				defer server.Close()
				request, manifest := diskPublicationRequest(t, arch)
				root := filepath.Join(t.TempDir(), "prepared")
				result, err := BuildPublicationRoot(t.Context(), root, request)
				if err != nil {
					t.Fatal(err)
				}
				options := RegistryOptions{PlainHTTP: true, Anonymous: !private}
				if private {
					options.Username, options.Password = "fixture", "fixture-password"
				}
				reference, err := Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/release", options)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasSuffix(reference, "@"+result.RootDigest) {
					t.Fatal("registry changed root pin")
				}
				pulled := filepath.Join(t.TempDir(), "pulled")
				inspection, err := Pull(t.Context(), reference, pulled, options)
				if err != nil {
					t.Fatal(err)
				}
				publication, err := openDiskPublicationFixture(t.Context(), pulled, inspection.RootDigest)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = publication.Close() }()
				outputs, _, err := publication.Artifacts(reference)
				if err != nil || len(outputs) != 1 || outputs[0].Digest != manifest || outputs[0].Disk.PayloadDigest != request.Objects[0].Output.Disk.PayloadDigest {
					t.Fatalf("registry disk correspondence: %#v %v", outputs, err)
				}
				assertRegistryCode(t, publication.RequireAttestedEvidence(), e.CodeProvenanceMissing)
			})
		}
	}
}

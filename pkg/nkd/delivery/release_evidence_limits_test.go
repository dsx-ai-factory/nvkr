package delivery

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func TestReleaseEvidenceIndependentDocumentAndBundleBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		bundle bool
		size   int64
		valid  bool
	}{
		{name: "document one byte over inventory ceiling", size: sbom.MaxDocumentBytes + 1},
		{name: "bundle one byte over transport ceiling", bundle: true, size: sbom.MaxBundleBytes + 1},
		{name: "bundle at transport ceiling exceeds document ceiling", bundle: true, size: sbom.MaxBundleBytes, valid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, _ := releasePublicationFixture(t)
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			index := -1
			for i := range inspection.Root.Objects {
				if inspection.Root.Objects[i].Evidence != nil {
					index = i
					break
				}
			}
			if index < 0 {
				t.Fatal("fixture lacks bound release evidence")
			}
			object := &inspection.Root.Objects[index]
			manifest, err := flatManifest(t.Context(), root, *object)
			if err != nil {
				t.Fatal(err)
			}
			writer := layoutWriter{root: root, plain: true}
			if test.bundle {
				object.ArtifactType = sbom.BundleMediaType
			}
			if test.valid {
				_, document, err := EvidenceDocument(t.Context(), root, object.Name)
				if err != nil {
					t.Fatal(err)
				}
				statement, err := json.Marshal(map[string]any{
					"_type": "https://in-toto.io/Statement/v1", "predicateType": sbom.CycloneDXPredicateType,
					"subject":   []any{map[string]any{"digest": map[string]string{"sha256": strings.TrimPrefix(object.Evidence.SubjectDigest, "sha256:")}}},
					"predicate": json.RawMessage(document),
				})
				if err != nil {
					t.Fatal(err)
				}
				bundle, err := json.Marshal(map[string]any{
					"mediaType": sbom.BundleMediaType, "verificationMaterial": map[string]any{},
					"dsseEnvelope": map[string]any{"payloadType": "application/vnd.in-toto+json",
						"payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "Zml4dHVyZQ=="}}},
				})
				if err != nil {
					t.Fatal(err)
				}
				// Transport whitespace reaches the exact outer byte ceiling while
				// retaining the original small, exact canonical CycloneDX document.
				bundle = append(bundle, []byte(strings.Repeat(" ", int(test.size)-len(bundle)))...)
				descriptor, blob, err := writer.addBytesObject(t.Context(), ObjectInput{
					Name: object.Name, Kind: object.Kind, ArtifactType: object.ArtifactType,
					LayerMediaType: object.ArtifactType, SemanticDigest: object.SemanticDigest,
					Bytes: bundle, subject: manifest.Subject,
				})
				if err != nil {
					t.Fatal(err)
				}
				object.DescriptorDigest, object.Size, object.BlobDigest = descriptor.Digest, descriptor.Size, blob
			} else {
				// Rehash the manifest after editing only its descriptor. The size
				// admission must precede payload opening/decoding, not fall through
				// to a stale-byte or malformed-document error.
				manifest.ArtifactType = object.ArtifactType
				manifest.Layers[0].MediaType = object.ArtifactType
				manifest.Layers[0].Size = test.size
				descriptor, err := writer.addJSONBlob(manifest, MediaOCIManifest)
				if err != nil {
					t.Fatal(err)
				}
				object.DescriptorDigest, object.Size = descriptor.Digest, descriptor.Size
			}
			err = verifyReleaseEvidence(t.Context(), root, inspection.Root)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if !strings.Contains(err.Error(), "descriptor is malformed or exceeds its byte bound") {
				t.Fatalf("oversize evidence reached a later guard: %v", err)
			}
		})
	}
}

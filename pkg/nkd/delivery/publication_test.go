package delivery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func releasePublicationFixture(t *testing.T) (string, string) {
	t.Helper()
	return releasePublicationPlatformFixture(t, "", "")
}

func releasePublicationPlatformFixture(t *testing.T, osName, arch string) (string, string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "cli")
	payload := []byte("exact executable")
	if err := os.WriteFile(source, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "bundle")
	bundle, err := BuildBinaryBundle(t.Context(), bundlePath, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tools", Files: []BinaryFileInput{{Name: "cli", Path: source, InstallPath: "/usr/local/bin/cli", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := PayloadDescriptor(t.Context(), bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	build := digestBytes([]byte("build"))
	output := Output{Name: "tools", Kind: "executable", Digest: descriptor.Digest, BuildDigest: build, Components: []string{"cli"}}
	content, err := ContentDigest([]Output{output})
	if err != nil {
		t.Fatal(err)
	}
	bom, err := sbom.NewSubject(build, "cli", digestBytes(payload))
	if err != nil {
		t.Fatal(err)
	}
	bom, err = sbom.ForSubject(bom, digestBytes(payload), "build-output", digestBytes(payload))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := bom.Digest()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "publication")
	result, err := BuildPublicationRoot(t.Context(), root, PublicationRequest{Release: &ReleaseMetadata{DefinitionDigest: build, EnvironmentDigest: build, ContentSubject: content}, Objects: []ObjectInput{
		{Name: "tools", Kind: "binary-bundle", Path: bundlePath, Layout: true, ArtifactType: MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, BlobDigest: bundle.RootDigest, OS: osName, Arch: arch, Output: &OutputBinding{Repository: "tools", Format: "executable", BuildDigest: build, Components: []string{"cli"}, BuildSubjects: []BuildSubject{{Component: "cli", Digest: digestBytes(payload)}}}},
		{Name: "sbom-" + strings.TrimPrefix(semantic, "sha256:"), Kind: "sbom", ArtifactType: "application/vnd.cyclonedx+json", LayerMediaType: "application/vnd.cyclonedx+json", SemanticDigest: semantic, Bytes: raw, Evidence: &EvidenceBinding{Outputs: []string{"tools"}, Component: "cli", Stage: "build-output", SubjectKind: "file", SubjectDigest: digestBytes(payload)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return root, result.RootDigest
}

func TestPublicationOwnsHeldClosureLifetime(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	held := append([]*heldfile.File(nil), publication.files...)
	if len(held) < 2 {
		t.Fatal("publication has no held closure")
	}
	reference := "registry.example.test/release@" + digest
	if _, _, err := publication.Artifacts(reference); err != nil {
		t.Fatal(err)
	}
	assertRegistryCode(t, publication.RequireAttestedEvidence(), e.CodeProvenanceMissing)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 20 {
				_, _, err := publication.Artifacts(reference)
				if err != nil {
					assertRegistryCode(t, err, e.CodeGateUnmet)
				}
			}
		})
	}
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	for _, file := range held {
		if file.OSFile() != nil {
			t.Fatal("Close leaked anonymous bytes")
		}
	}
	_, _, err = publication.Artifacts(reference)
	assertRegistryCode(t, err, e.CodeGateUnmet)
}

func TestPublicationRejectsCorruptionAndUnreachableBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string, string)
	}{
		{"replaced index", func(t *testing.T, root, _ string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("other"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"replaced blob", func(t *testing.T, root, _ string) {
			t.Helper()
			entries, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", entries[0].Name()), []byte("other"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unreachable blob", func(t *testing.T, root, _ string) {
			t.Helper()
			payload := []byte("extra")
			if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digestBytes(payload), "sha256:")), payload, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink blob", func(t *testing.T, root, _ string) {
			t.Helper()
			if err := os.Symlink(filepath.Join(root, "index.json"), filepath.Join(root, "blobs", "sha256", strings.Repeat("f", 64))); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, digest := releasePublicationFixture(t)
			tc.mutate(t, root, digest)
			publication, err := OpenPublication(t.Context(), root, digest)
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if publication != nil {
				t.Fatal("rejected closure returned capability")
			}
		})
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := OpenPublication(canceled, "missing", digestBytes(nil))
	assertRegistryCode(t, err, e.CodeTimeout)
	_, err = OpenPublication(t.Context(), "missing", "latest")
	assertRegistryCode(t, err, e.CodeVerifyFailed)
}

package delivery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
)

func TestBinaryBundlePublicationRootAndExtraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	first := filepath.Join(root, "kubectl")
	second := filepath.Join(root, "nkdctl")
	if err := os.WriteFile(first, []byte("kubectl-exact\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("nkdctl-exact\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "bundle")
	bundle, err := BuildBinaryBundle(ctx, bundlePath, BinaryBundleRequest{OS: "linux", Arch: "amd64",
		Purpose: "operator-tools", Epoch: 1_748_736_000, Files: []BinaryFileInput{
			{Name: "nkdctl", Path: second, InstallPath: "/usr/local/bin/nkdctl", Mode: 0o755},
			{Name: "kubectl", Path: first, InstallPath: "/usr/local/bin/kubectl", Mode: 0o755},
		}})
	if err != nil {
		t.Fatal(err)
	}
	definition := filepath.Join(root, "cluster-definition.json")
	definitionRaw := []byte(`{"schemaVersion":"example/v1","release":"exact"}`)
	if err := os.WriteFile(definition, definitionRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	publicationPath := filepath.Join(root, "publication")
	publication, err := BuildPublicationRoot(ctx, publicationPath, PublicationRequest{Objects: []ObjectInput{
		{Name: "cluster-definition", Kind: "cluster-definition", Path: definition,
			ArtifactType: MediaClusterDefinition, LayerMediaType: MediaClusterDefinition, SemanticDigest: digestBytes(definitionRaw)},
		{Name: "operator-tools-linux-amd64", Kind: "binary-bundle", Path: bundlePath,
			ArtifactType: MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest,
			OS: "linux", Arch: "amd64", Layout: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(ctx, publicationPath)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.RootDigest != publication.RootDigest || inspection.Root.Qualification != QualificationDiagnostic || len(inspection.Root.Objects) != 2 {
		t.Fatalf("unexpected publication inspection: %#v", inspection)
	}
	extracted := filepath.Join(root, "extracted")
	config, err := ExtractBinaryBundle(ctx, publicationPath, "operator-tools-linux-amd64", extracted)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Entries) != 2 {
		t.Fatalf("got %d bundle entries", len(config.Entries))
	}
	for name, want := range map[string]string{"kubectl": "kubectl-exact\n", "nkdctl": "nkdctl-exact\n"} {
		got, err := os.ReadFile(filepath.Join(extracted, name))
		if err != nil || string(got) != want {
			t.Fatalf("extracted %s = %q, %v", name, got, err)
		}
	}
	extractedDefinition := filepath.Join(root, "consumer", "cluster-definition.json")
	object, err := ExtractObject(ctx, publicationPath, "cluster-definition", extractedDefinition)
	if err != nil {
		t.Fatal(err)
	}
	gotDefinition, err := os.ReadFile(extractedDefinition)
	if err != nil || string(gotDefinition) != string(definitionRaw) || object.SemanticDigest != digestBytes(definitionRaw) {
		t.Fatalf("extracted definition = %q, object=%#v, err=%v", gotDefinition, object, err)
	}
	if _, err := ExtractObject(ctx, publicationPath, "cluster-definition", extractedDefinition); err == nil {
		t.Fatal("object extraction overwrote an existing destination")
	}
}

func TestPublicationInspectionRejectsTamperedNestedBlob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	binary := filepath.Join(root, "tool")
	if err := os.WriteFile(binary, []byte("exact"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "bundle")
	bundle, err := BuildBinaryBundle(ctx, bundlePath, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tools", Files: []BinaryFileInput{{Name: "tool", Path: binary, InstallPath: "/usr/bin/tool", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	publicationPath := filepath.Join(root, "publication")
	_, err = BuildPublicationRoot(ctx, publicationPath, PublicationRequest{Objects: []ObjectInput{{Name: "tools", Kind: "binary-bundle", Path: bundlePath, ArtifactType: MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, Layout: true}}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(publicationPath, "blobs", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	var target string
	for _, entry := range entries {
		raw, readErr := os.ReadFile(filepath.Join(publicationPath, "blobs", "sha256", entry.Name()))
		if readErr == nil && strings.Contains(string(raw), "binary-bundle/v1") {
			target = entry.Name()
			break
		}
	}
	if target == "" {
		t.Fatal("did not find binary bundle config blob")
	}
	if err := os.WriteFile(filepath.Join(publicationPath, "blobs", "sha256", target), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, publicationPath); err == nil {
		t.Fatal("tampered recursive blob passed inspection")
	}
}

func TestPublicationInspectionRejectsUnreachableBlob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	binary := filepath.Join(root, "tool")
	if err := os.WriteFile(binary, []byte("exact"), 0o755); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "bundle")
	bundle, err := BuildBinaryBundle(ctx, bundlePath, BinaryBundleRequest{OS: "linux", Arch: "amd64", Purpose: "tools", Files: []BinaryFileInput{{Name: "tool", Path: binary, InstallPath: "/usr/bin/tool", Mode: 0o755}}})
	if err != nil {
		t.Fatal(err)
	}
	publicationPath := filepath.Join(root, "publication")
	_, err = BuildPublicationRoot(ctx, publicationPath, PublicationRequest{Objects: []ObjectInput{{Name: "tools", Kind: "binary-bundle", Path: bundlePath, ArtifactType: MediaBinaryBundle, SemanticDigest: bundle.SemanticDigest, Layout: true}}})
	if err != nil {
		t.Fatal(err)
	}
	extra := strings.Repeat("0", 64)
	if err := os.WriteFile(filepath.Join(publicationPath, "blobs", "sha256", extra), []byte("smuggled"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, publicationPath); err == nil {
		t.Fatal("unreachable OCI blob passed exact-closure inspection")
	}
}

func TestAPTRepositoryProjectsExactDebianPackage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	binary := filepath.Join(root, "crictl")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	deb := filepath.Join(root, "crictl_1.34.0_amd64.deb")
	if _, err := assemble.Deb(binary, "crictl", "1.34.0", 1_748_736_000, "amd64", deb); err != nil { //nolint:staticcheck // creates an explicitly unqualified APT-projection fixture
		t.Fatal(err)
	}
	out := filepath.Join(root, "apt")
	result, err := BuildAPTRepository(ctx, out, APTRequest{Distribution: "stable", Component: "main", Epoch: 1_748_736_000, Packages: []DebianPackageInput{{Path: deb}}})
	if err != nil {
		t.Fatal(err)
	}
	if !isDigest(result.Digest) || len(result.Repository.Packages) != 1 || result.Repository.Packages[0].Name != "crictl" {
		t.Fatalf("unexpected APT result: %#v", result)
	}
	for _, path := range []string{
		"dists/stable/Release", "dists/stable/main/binary-amd64/Packages",
		"dists/stable/main/binary-amd64/Packages.gz", "nkd-apt-repository.json",
		result.Repository.Packages[0].Filename,
	} {
		if info, err := os.Stat(filepath.Join(out, filepath.FromSlash(path))); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("missing APT file %s: %v", path, err)
		}
	}
	packages, err := os.ReadFile(filepath.Join(out, "dists/stable/main/binary-amd64/Packages"))
	if err != nil || !strings.Contains(string(packages), "SHA256: "+strings.TrimPrefix(result.Repository.Packages[0].SHA256, "sha256:")) {
		t.Fatalf("Packages did not bind exact deb: %v\n%s", err, packages)
	}
}

func TestQualifiedPublicationAdmissionRequiresOpaqueAuthorityBeforeOutput(t *testing.T) {
	t.Parallel()
	out := filepath.Join(t.TempDir(), "qualified")
	if _, err := BuildQualifiedPublicationRoot(context.Background(), out, QualifiedPublicationRequest{}); err == nil {
		t.Fatal("zero caller input minted a qualified publication root")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("qualified admission touched output before authority: %v", err)
	}
}

func TestRegistryOptionsRejectPartialOrConflictingCredentialsBeforeLayoutIO(t *testing.T) {
	t.Parallel()
	for name, options := range map[string]RegistryOptions{
		"password-only":  {Password: "secret"},
		"anonymous-auth": {Anonymous: true, Username: "user", Password: "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Push(context.Background(), filepath.Join(t.TempDir(), "missing"),
				"registry.example/nkd:test", options); err == nil ||
				(!strings.Contains(err.Error(), "credential") && !strings.Contains(err.Error(), "authentication")) {
				t.Fatalf("unsafe registry options did not fail at admission: %v", err)
			}
		})
	}
}

package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

type swappingOCIVerifier struct {
	mu          sync.Mutex
	original    string
	valid       string
	acceptFirst bool
	calls       []string
}

func (*swappingOCIVerifier) Acquire(context.Context, string, string) (string, int64, error) {
	return "", 0, fmt.Errorf("not implemented")
}

func (verifier *swappingOCIVerifier) Verify(ctx context.Context, digestRef, layoutPath string) error {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	verifier.calls = append(verifier.calls, layoutPath)
	if verifier.acceptFirst && len(verifier.calls) == 1 {
		return nil
	}

	parked := verifier.original + ".parked"
	if err := os.Rename(verifier.original, parked); err != nil {
		return err
	}
	if err := os.Rename(verifier.valid, verifier.original); err != nil {
		_ = os.Rename(parked, verifier.original)
		return err
	}
	defer func() {
		_ = os.Rename(verifier.original, verifier.valid)
		_ = os.Rename(parked, verifier.original)
	}()

	_, want, err := parseDigestReference(digestRef)
	if err != nil {
		return err
	}
	return verifyOCILayout(ctx, layoutPath, want)
}

func (verifier *swappingOCIVerifier) observedPaths() []string {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	return append([]string(nil), verifier.calls...)
}

func TestTerminalReceiptMintVerifiesDescriptorDerivedOCISnapshot(t *testing.T) {
	root := t.TempDir()
	malicious := filepath.Join(root, "transport.oci")
	valid := filepath.Join(root, "valid.oci")
	writeInvalidRegularOCITree(t, malicious)
	digest := writeMinimalOCILayout(t, valid)
	node, artifact := terminalPullFixture(digest, malicious)
	verifier := &swappingOCIVerifier{original: malicious, valid: valid}

	_, err := captureTerminalArtifact(context.Background(), node, artifact, RunOptions{OCI: verifier})
	if err == nil {
		t.Fatalf("temporarily substituting a valid layout at the producer path authorized malicious bytes: %v", err)
	}
	paths := verifier.observedPaths()
	if len(paths) != 0 {
		t.Fatalf("terminal authority delegated to a pathname-based transport verifier: %v", paths)
	}
	if _, err := os.Stat(valid); err != nil {
		t.Fatalf("untrusted verifier was able to swap the valid fixture into place: %v", err)
	}
}

func TestTerminalReceiptRequireConsumesSamePrivateOCISnapshot(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "transport.oci")
	digest := writeMinimalOCILayout(t, live)
	node, artifact := terminalPullFixture(digest, live)
	report := &Report{
		BuildDigest: "sha256:" + repeatHex("a"),
		Artifacts:   []ArtifactResult{artifact},
	}
	receipt, err := issueTerminalReceipt(context.Background(), report,
		map[string]ir.Node{node.ID: node}, map[string]bool{node.ID: true}, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.artifacts[0].tree == nil || receipt.artifacts[0].tree.digest == "" {
		t.Fatal("terminal receipt did not retain an immutable logical OCI tree")
	}
	if err := os.Rename(live, live+".valid"); err != nil {
		t.Fatal(err)
	}
	writeInvalidRegularOCITree(t, live)
	if err := receipt.require(context.Background(), report); err != nil {
		t.Fatalf("require reopened the now-malicious producer pathname instead of immutable captured facts: %v", err)
	}
}

func terminalPullFixture(digest, transportPath string) (ir.Node, ArtifactResult) {
	node := ir.Node{
		ID: "pull-image", Kind: ir.KindPull, Component: "image", OutputType: "oci-image",
		Artifact: "registry.example.test/image", Digest: digest,
	}
	artifact := ArtifactResult{
		Component: node.Component, OutputType: node.OutputType,
		Qualification: ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire,
		SHA256: digest, Ref: node.Artifact + "@" + digest, TransportPath: transportPath,
	}
	return node, artifact
}

func writeInvalidRegularOCITree(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte(`{"schemaVersion":2,"manifests":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "attacker"), []byte("malicious"), 0o644); err != nil {
		t.Fatal(err)
	}
}
